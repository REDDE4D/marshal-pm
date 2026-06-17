# Marshal Central Server — M7 (agent↔server connection + live fleet state) — Design

**Date:** 2026-06-17
**Status:** Approved (design); plan to follow.
**Sub-project:** #3 — Central server / fleet aggregation (first milestone, M7).
**Depends on:** sub-project #1 (agent/supervisor core, M1–M4) and #2 (metrics & log pipeline, M5–M6).

## 1. Context

Marshal is a free, self-hosted process manager being built bottom-up toward a fleet
manager (architecture spec:
`docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`).
Sub-projects #1 and #2 are complete: a single host runs an agent/daemon that supervises
processes, captures stdout/stderr, samples metrics, and serves a local CLI.

Sub-project #3 adds the **central server** — a control + data plane that aggregates many
agents. It is too large for one milestone, so it is decomposed into:

- **M7 (this design)** — agent↔server connection + live in-memory fleet state.
- **M8** — metric/log records streamed up + server-side storage (SQLite metrics, log files;
  the natural home for the deferred Approach-B timestamped log records).
- **M9** — downward control plane (`restart`/`stop`/`start <app> on <host>` with acks).
- **M10** — auth & TLS hardening (bootstrap token, per-agent identity, TLS everywhere).

Sub-project #4 (web dashboard) builds on top of #3.

## 2. Goal & scope

**Goal:** one Marshal binary can run as a central server; an agent configured with a
server address dials it over an agent-initiated gRPC stream and pushes its process-state
snapshots up; the server keeps live fleet state in memory; `marshal fleet ps` reads that
aggregated state.

**In scope (M7):**
- A `marshal server` subcommand (single binary, role chosen by subcommand).
- A `server:` block in `marshal.yaml` that makes the daemon dial the server.
- An agent-initiated, long-lived, bidirectional gRPC stream (`FleetService.Connect`),
  with **only the upstream direction implemented**.
- In-memory fleet registry on the server (no persistence).
- `marshal fleet ps` CLI to read aggregated state.

**Out of scope (deferred to later milestones):**
- Metric/log streaming and SQLite/file storage (M8).
- Downward control commands (M9).
- TLS, bootstrap tokens, agent identity/authorization (M10) — **M7 transport is plaintext
  and unauthenticated, trust-all.**
- A REST API and the dashboard (sub-project #4).
- Live policy/config hot-reload of the server connection.

## 3. Locked decisions (from this brainstorm)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Server packaging | **`marshal server` subcommand**, logic in new `internal/server` package | Honors the locked "single static binary" decision; one artifact, no agent/server version skew; shares proto/config/logging; mirrors the existing `internal/daemon` agent boundary. |
| Agent connection config | **`server:` block in `marshal.yaml`** (no flag override in M7) | The connection is durable agent config (daemon persists and auto-reconnects), not ephemeral. Absence of the block = standalone mode, honoring "server layer is purely additive, never required." |
| Agent name | **Defaults to OS hostname**, overridable via `server.name` | Minimal config is just `server: { address: host:port }`. |
| Stream shape | **Bidirectional from day one, only upstream implemented in M7** | Downstream `ServerMessage` is reserved so M9's control plane slots in without a proto break. |
| Server state | **In-memory only** (map keyed by agent name) | M7 is the connection slice; persistence is M8. Keeps the milestone thin and independently useful. |
| CLI read path | **`marshal fleet ps`** dedicated namespace, own `--server` flag | Fleet output differs from local `ps` (host column across agents); `fleet` is the natural home for M9's `fleet restart …`. Operator querying the server is often a different actor than the agent. |
| CLI↔server protocol | **gRPC `ListFleet` on the same server** | Avoids standing up a second protocol this early; the richer dashboard REST API is sub-project #4. |

## 4. The gRPC contract

A new `Fleet` service, separate from the existing local `Daemon` service, in a new
`proto/marshal/v1/fleet.proto`. It shares proto package `marshal.v1` and `go_package`
`marshal/internal/pb;pb` with `daemon.proto`, so it can **import `daemon.proto` and reuse
the existing `ProcInfo` message** rather than redefine the proc shape (the DRY win the
design calls for):

```proto
syntax = "proto3";
package marshal.v1;
option go_package = "marshal/internal/pb;pb";
import "marshal/v1/daemon.proto";   // reuse ProcInfo

service Fleet {
  // Agent-initiated, long-lived, bidirectional. M7 uses only the upstream direction.
  rpc Connect(stream AgentMessage) returns (stream ServerMessage);
  // Read path for the fleet CLI (and, later, the dashboard).
  rpc ListFleet(ListFleetRequest) returns (ListFleetResponse);
}

message AgentMessage {
  oneof msg {
    Hello hello = 1;             // sent once on connect: agent name + marshal version
    StateSnapshot snapshot = 2;  // full process-state snapshot (periodic; see §5)
  }
}

message ServerMessage {
  oneof msg {
    HelloAck hello_ack = 1;
    // reserved: command messages land here in M9
  }
}

message Hello { string agent_name = 1; string marshal_version = 2; }
message HelloAck {}

message StateSnapshot { repeated ProcInfo procs = 1; }   // ProcInfo from daemon.proto

message ListFleetRequest {}
message ListFleetResponse { repeated AgentState agents = 1; }
message AgentState {
  string agent_name = 1;
  bool connected = 2;
  int64 last_seen_unix = 3;
  repeated ProcInfo procs = 4;
}
```

Notes:
- The **full snapshot** (not deltas) is sent each time. Simple, idempotent, and self-healing
  across reconnects; the process list per host is small. The reused `ProcInfo` carries
  `cpu`/`mem` fields (from M3) which the agent leaves zero in M7 — `fleet ps` does not render
  them yet; metric streaming is M8.
- Proto regenerated by extending the existing `go generate ./internal/pb` directive to
  include `fleet.proto` (protoc on PATH).

## 5. Agent side (`internal/daemon` + new `internal/fleet` client)

**Config delivery.** The daemon is auto-spawned with no args and reads the store dir, never
`marshal.yaml`. So `marshal start <yaml>`, when the parsed config carries a `server:` block,
**persists it to `fleet.json` in the store dir** (via a new `store.SaveServer`/`LoadServer`).
The daemon loads it at startup and starts the fleet client. Consequence, accepted under the
"no hot-reload" non-goal: adding/altering a `server:` block while a daemon is **already
running** takes effect on the daemon's next restart, not live.

- On daemon startup, if `store.LoadServer()` returns a config, the daemon starts **one
  fleet-client goroutine** (`internal/fleet`). The client:
  1. Dials the server address (plaintext/insecure in M7).
  2. Sends `Hello { agent_name, marshal_version }`.
  3. Pushes a full `StateSnapshot` immediately, then **periodically every 2s** (configurable;
     fast value injected in tests). This periodic push **doubles as the liveness heartbeat** —
     M7 has no separate on-change mechanism, so process-state changes surface within ≤2s.
- **Reconnect:** exponential backoff with a cap (1s → 30s). The client never blocks or
  crashes the supervisor. **Server down ⇒ the agent keeps running locally, fully
  functional** (the standalone guarantee). Connection state is logged, never fatal.
- **No `fleet.json` ⇒ the client never starts.** Zero behavior change for existing
  standalone users.
- The fleet client reads current state through a small injected `SnapshotFunc func() []*pb.ProcInfo`
  (the daemon adapts `manager.List()`), so it stays decoupled from manager internals and is
  unit-testable against a fake.
- `marshal run` (foreground, no daemon) is **not** wired to the fleet in M7; the daemon is
  the fleet agent.

## 6. Server side (`internal/server`)

- `marshal server --listen :9000` (default port; `--listen` overrides). **In-memory only —
  no data dir, no persistence in M7.**
- Maintains a registry: `agentName → { lastSnapshot, streamOpen bool, lastSeen time }`,
  guarded by a mutex.
  - On `Hello`: mark `streamOpen = true`.
  - On `StateSnapshot`: replace that agent's snapshot, bump `lastSeen`.
  - On stream end: mark `streamOpen = false` (the last snapshot is retained so `fleet ps`
    can show stale state with a last-seen time).
  - `ListFleet` reports `connected = streamOpen && (now − lastSeen ≤ offlineAfter)`, with
    `offlineAfter` defaulting to **10s** (overridable for tests). This makes a clean
    disconnect show offline immediately and a wedged/silent stream show offline after 10s.
- Serves both `Connect` (the agent stream) and `ListFleet` (the CLI read) on the same gRPC
  server.

## 7. CLI (`cmd/marshal/fleet.go`)

- `marshal fleet ps [--server host:port]`:
  - Dials the server's `ListFleet`.
  - Prints a table grouped by agent: a **host/agent column** plus the usual proc columns
    (name, id, pid, status, uptime, restarts).
  - `--server` defaults to the `MARSHAL_SERVER` env var, then `localhost:9000`.
  - Disconnected-but-known agents render with an `offline`/`stale` marker and their
    last-seen time.

## 8. Error handling

- **Agent:** all fleet-client errors (dial failure, stream drop, send error) trigger
  backoff-and-retry; none affect local supervision. A misconfigured/unreachable server is a
  logged warning, not a startup failure.
- **Server:** a malformed/half-open agent stream is dropped and the agent marked
  disconnected; the server keeps serving other agents and `ListFleet`.
- **CLI:** if `--server` is unreachable, `fleet ps` prints a clear connection error and a
  non-zero exit code.

## 9. Testing (TDD, per project convention)

- **`internal/fleet` client:** snapshot-on-change + heartbeat cadence; reconnect/backoff
  against a fake server; standalone (no `server:` block) starts nothing.
- **`internal/server`:** registry update/replace keyed by name; disconnect on stream end and
  on heartbeat lapse; retention of last snapshot for offline agents; `ListFleet` output.
- **End-to-end (in-process):** spin up a server, connect a real agent over a
  bufconn/loopback stream, assert `fleet ps` reflects a started app, then an `offline`
  marker after disconnect.
- **`config`:** parsing/validation of the `server:` block; absence ⇒ standalone.
- Full gate before finishing: `go test ./... -race -count=1`, `go vet ./...`,
  `gofmt -l .` (clean), `go build ./...`.

## 10. New / changed surfaces (summary)

New:
- `cmd/marshal/server.go` — `marshal server` subcommand.
- `cmd/marshal/fleet.go` — `marshal fleet ps` subcommand.
- `internal/server/` — gRPC `Fleet` server, in-memory fleet registry.
- `internal/fleet/` — agent-side fleet client (dial, Hello, periodic snapshot push, reconnect).
- `proto/marshal/v1/fleet.proto` (+ regenerated `internal/pb`) — `Fleet` service and messages.

Changed:
- `internal/config/config.go` — `Server *ServerConfig` block (`address`, optional `name`).
- `internal/store/store.go` — `SaveServer`/`LoadServer` persisting `fleet.json`.
- `internal/daemon/server.go` — on `Run`, if `store.LoadServer()` is non-nil, start the fleet
  client wired to a `manager.List()` → `[]*pb.ProcInfo` adapter.
- `cmd/marshal/control.go` (or the start command file) — `marshal start` persists the parsed
  `server:` block via `store.SaveServer` before connecting to the daemon.

Unchanged: the `Daemon` proto service gets **no new RPC** — config reaches the daemon through
the persisted `fleet.json`, not over the socket.

## 11. Config example

```yaml
# marshal.yaml
server:
  address: server.internal:9000   # presence enables fleet mode
  name: web-1                      # optional; defaults to OS hostname
apps:
  - name: api
    cmd: ./api
```

Absence of `server:` ⇒ pure standalone agent, unchanged from M1–M6.
