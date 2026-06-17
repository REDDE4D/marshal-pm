# M9 — Fleet Command Channel — Design

**Date:** 2026-06-17
**Status:** Approved
**Depends on:** M7 (agent↔server connection), M8/M8b (metric & log storage over the stream)

## 1. Purpose

Make the fleet *controllable*, not just observable. Today the central server aggregates
state, metrics, and logs flowing **up** the `Fleet.Connect` stream. M9 adds the **down**
direction: an operator runs `marshal fleet restart <agent> <app>` against the server, the
server routes the command to that agent over its existing stream, the agent executes it
and reports the result back up, and the CLI prints the outcome.

This realizes the control-plane half of the architecture spec
(`2026-06-16-fleet-process-manager-architecture-design.md` §5: "dashboard/CLI issues
`restart <app> on <host>` → server validates → pushes the command over that host's
existing stream → agent executes → streams result and new state back up").

## 2. Locked decisions

| Decision | Choice |
|----------|--------|
| Command set | **stop / restart / delete / start** (start = deploy a brand-new app from an `AppSpec` shipped down) |
| Result model | **Synchronous** — the CLI's unary RPC blocks until the agent reports the result, correlated by request id |
| Transport | **Multiplex on the existing `Fleet.Connect` stream** (no second stream) |
| Persistence | **Auto-save on the agent** after start/delete (the ops that change the persisted spec set) so remote deploys survive a daemon restart / reboot; stop/restart leave the spec set unchanged |
| Auth | **Deferred to M10** — server remains unauthenticated this milestone |

## 3. Approach

**Multiplex commands on the existing `Fleet.Connect` bidirectional stream, with a
per-agent command broker on the server.** A unary `FleetControl` RPC from the CLI is
correlated to a reply that travels back **up** the agent's stream.

**Rejected alternative — a second dedicated command-stream RPC.** It would duplicate the
reconnect/backoff/liveness logic the `Connect` stream already owns and break the
one-outbound-connection-per-agent invariant the architecture spec fixes. No upside.

## 4. Wire protocol (proto changes in `proto/marshal/v1/fleet.proto`)

Shared op/result shapes, reused by both the down-stream command and the CLI's unary RPC so
there is one source of truth for the command surface:

```proto
message ControlOp {
  oneof op {
    Selector     stop    = 1;
    Selector     restart = 2;
    Selector     delete  = 3;
    StartRequest start   = 4;   // reuses AppSpec from daemon.proto
  }
}

message ControlResult {
  bool              ok    = 1;
  string            error = 2;  // set when !ok
  repeated ProcInfo procs = 3;  // affected instances on success
}

message Command       { uint64 request_id = 1; ControlOp     op     = 2; } // server → agent
message CommandResult { uint64 request_id = 1; ControlResult result = 2; } // agent → server
```

Stream oneof additions:

- `ServerMessage` gains `Command command = 2;` (fills the existing
  `// reserved: command messages land here in M9` slot).
- `AgentMessage` gains `CommandResult result = 5;`.

CLI-facing unary RPC on the `Fleet` service:

```proto
message FleetControlRequest  { string agent_name = 1; ControlOp op = 2; }
message FleetControlResponse { ControlResult result = 1; }

rpc FleetControl(FleetControlRequest) returns (FleetControlResponse);
```

`request_id` correlates a down-command to its up-reply and is internal to the
server↔agent pair; the CLI never sees it. The pb is regenerated after editing the proto.

## 5. Server: the command broker

A new `broker` type in package `server`, sibling to `stores` / `logStores`, holding one
**session** per connected agent.

- A `session` owns:
  - a **mutex-serialized `Send`** over that agent's stream. gRPC forbids concurrent
    `Send` on one stream, and `HelloAck`, future acks, and commands all originate from
    different goroutines — every downstream send goes through `session.send`.
  - a `pending map[uint64]chan *pb.ControlResult` and a monotonic request-id counter
    (counter is session-local; ids only need to be unique within a session).
- `Connect` registers the session on `Hello` and tears it down on disconnect (EOF or
  error), **failing every pending request** so in-flight `FleetControl` calls return
  promptly with `Unavailable` instead of hanging until their own deadline.
- The `Connect` recv-loop gains a `CommandResult` case → delivers the result to the
  matching pending channel (drop silently if the id is unknown, e.g. already timed out).
- `FleetControl(ctx, req)`:
  1. Look up the session; **no session → `codes.Unavailable "agent %q not connected"`**.
  2. Allocate a request id, register a `chan *pb.ControlResult` in `pending`.
  3. `session.send` the `Command` down.
  4. Block on **the channel or `ctx.Done()`**:
     - channel → return the `ControlResult` in a `FleetControlResponse`.
     - `ctx` (CLI deadline/cancel) → remove the pending entry, return
       `codes.DeadlineExceeded`. The command may already have executed on the agent —
       semantics are best-effort and this is documented in CLI help.

`Registry` stays pure live-state (procs / last-seen / connected); the live-stream and
command concern lives entirely in `broker`. `Connect` wires both: `reg.Open/Update/Close`
**and** `broker.register/deliver/unregister`.

## 6. Agent: receiving and executing commands

`fleet.Client` gains `WithCommands(fn CommandFunc)` where
`type CommandFunc func(*pb.Command) *pb.ControlResult`.

Today `connectOnce` receives exactly once (the `HelloAck`) then only sends on the ticker.
M9 runs a **receiver goroutine** for the stream's lifetime alongside the existing ticker
(sender) goroutine:

- Client-side sends become **mutex-guarded** — the ticker (snapshots/metrics/logs) and the
  receiver (command replies) are two senders on one stream.
- The receiver reads `Command`s and executes them **sequentially** (commands are
  infrequent; sequential keeps per-agent ordering trivial and avoids manager-concurrency
  questions), sending a `CommandResult` back up after each.
- If `WithCommands` is unset, received `Command`s are ignored (forward-compatible).

Execution **reuses the daemon's existing logic** rather than reimplementing it. The bodies
of the `Daemon` gRPC handlers are extracted into `doStart/doStop/doRestart/doDelete` on
`daemon.Server`, and a new `handleFleetCommand(*pb.Command) *pb.ControlResult` dispatches
to them. Per the persistence decision `handleFleetCommand` calls `store.Save` after a
successful **start** or **delete** (the ops that change `mgr.Specs()`); stop/restart leave
the spec set unchanged so they need no save. The daemon wires
`fleet.WithCommands(srv.handleFleetCommand)` next to the existing `WithMetrics`/`WithLogs`.

Selector (`name` / `id` / `all`) is resolved **on the agent** by the existing manager, so
M8b's "no server-side id resolution" caveat does not apply to commands.

## 7. CLI (`cmd/marshal/fleet.go`)

```
marshal fleet start   <agent> <marshal.yaml>   # loads yaml locally, ships its AppSpecs
marshal fleet stop    <agent> <selector>
marshal fleet restart <agent> <selector>
marshal fleet delete  <agent> <selector>
```

Each builds a `ControlOp`, calls `FleetControl`, and on success prints the returned
`ProcList` with the existing proc table renderer; on `ok=false` prints the `error` string
and exits non-zero. Flags: `--server` (as the other fleet commands) and `--timeout`
(default 10s). `fleet start` loads and validates the yaml locally with `config.Load`
before shipping, mirroring local `marshal start`.

## 8. Testing (TDD — failing test first)

- **`broker` / `session` units:** register/lookup, deliver result to pending, fail all
  pending on disconnect, deadline path removes pending, concurrent `send` safety.
- **`FleetControl` (server) with a fake `Fleet_ConnectServer`:** command goes down,
  result routes back up to the caller; not-connected → `Unavailable`.
- **Agent command handler:** `Command` → executor → `CommandResult`; reuses the manager;
  asserts `store.Save` ran after a mutation.
- **e2e (real server + real daemon over the stream):** `fleet restart` bounces a real PID
  (restart count / new pid changes); `fleet start` deploys a new app and it appears in
  `fleet ps` and in the agent's dump; `fleet delete` removes it and persists.

## 9. Deferred / out of scope

1. **Auth / server-side authorization of commands** — M10 (TLS + bootstrap token). The
   server is still unauthenticated and binds all interfaces.
2. **Live `fleet logs --follow`** — unchanged from M8b.
3. **Web dashboard controls** — separate sub-project.
4. **Command audit log** on the server.
5. **Rich partial-failure reporting** for `all`-selector ops — M9 returns the manager's
   aggregate result; per-instance failure detail beyond the `error` string is future work.
6. **Best-effort timeout semantics:** a `FleetControl` that times out may still have
   executed on the agent. Documented, not reconciled, this milestone.
