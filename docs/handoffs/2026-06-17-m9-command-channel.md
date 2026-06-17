# M9 Fleet Command Channel — Handoff

**Date:** 2026-06-17
**Branch:** `m9-command-channel` (not yet merged)
**Base / merge-base(main):** `f6c8bfa` (main, post-M8b merge)
**Gate:** green — `gofmt -l .` silent, `go vet ./...` clean, `go build ./...` clean, `go test ./... -race -count=1` passes (17 packages).

---

## Current state

M9 is complete (pending final whole-branch review + merge). The fleet is now
**controllable**, not just observable. An operator runs a command against the central
server; the server routes it down the agent's existing `Fleet.Connect` stream; the agent
executes it via its normal manager logic and reports the result back up; the CLI prints the
outcome. This is the control-plane half of the architecture spec (§5).

New operator commands:

```
marshal fleet start   <agent> <marshal.yaml>   # deploy + start new app(s) on one agent
marshal fleet stop    <agent> <name|id|all>
marshal fleet restart <agent> <name|id|all>
marshal fleet delete  <agent> <name|id|all>
```

- Commands ride the existing bidirectional `Connect` stream — no new connection, no inbound
  port on hosts. Synchronous: the CLI's unary `FleetControl` RPC blocks until the agent's
  result returns, correlated by a per-session request id.
- `start` ships a full `AppSpec` down (the agent runs the same admit/launch path as local
  `marshal start`). `stop/restart/delete` are selector ops resolved **on the agent**.
- **Auto-save:** the agent persists its `dump.json` after a successful `start` or `delete`
  (the ops that change the persisted spec set), so a remote deploy survives a daemon
  restart / reboot. `stop`/`restart` leave the spec set unchanged and do not save.

## Architecture (what was added)

- **Proto** (`proto/marshal/v1/fleet.proto`): `ControlOp` (oneof stop/restart/delete =
  `Selector`, start = `StartRequest`), `ControlResult{ok,error,procs}`,
  `Command{request_id,op}` (down), `CommandResult{request_id,result}` (up);
  `ServerMessage.command=2` (filled the reserved M9 slot), `AgentMessage.result=5`; unary
  `FleetControl(FleetControlRequest) returns (FleetControlResponse)`.
- **Server command broker** (`internal/server/broker.go`): one `session` per connected
  agent — a mutex-serialized downstream send path plus a `pending` map keyed by a monotonic
  request id. `dispatch` sends a command and waits for the reply or ctx; `deliver` routes an
  up-stream result; `failAll` fails all pending on disconnect. `errDisconnected` →
  `codes.Unavailable`.
- **Server wiring** (`internal/server/server.go`): `Connect` registers a session at Hello
  (HelloAck now goes through `session.sendMsg`), handles the `CommandResult` case, and tears
  the session down on stream exit. `FleetControl` looks up the session (absent →
  `Unavailable`), dispatches, and maps errors.
- **Agent client** (`internal/fleet/client.go`): `WithCommands(fn CommandFunc)` +
  `type CommandFunc func(*pb.Command) *pb.ControlResult`. A receiver goroutine runs for the
  stream's life; all sends (snapshots/metrics/logs + command replies) are serialized behind
  one `sendMu`; stream errors surface via a buffered `recvErr` channel into the ticker
  select. nil `commands` ⇒ received commands are ignored (forward-compatible).
- **Daemon executor** (`internal/daemon/command.go`): `handleFleetCommand` dispatches by op,
  reusing the manager ops and the extracted `doStart` (shared with the `Daemon.Start` RPC),
  and auto-saves on start/delete. Wired via `fleet.WithCommands(srv.handleFleetCommand)`.
- **CLI** (`cmd/marshal/fleet.go`): the four verbs, a shared `fleetControl` dial/print
  helper, `--server` (`resolveServer`) and `--timeout` (start 30s, others 10s).

## What changed this session (commits oldest → newest)

| Commit | Change |
|--------|--------|
| `ea3e519` | docs: M9 design spec |
| `c7a0fdb` | docs: M9 implementation plan; tighten spec persistence wording |
| `c06c38b` | proto: M9 command surface (Command/ControlResult/FleetControl); regenerated pb |
| `c497048` | server: per-agent command broker (sessions, dispatch, correlation) |
| `cf96cf5` | server: route commands via broker in Connect + FleetControl RPC |
| `4bde8d5` | fleet: agent receives and executes down-stream commands (WithCommands) |
| `1c53df7` | daemon: execute fleet commands via manager, auto-save on start/delete |
| `db49e5d` | cli: marshal fleet start/stop/restart/delete |
| `60ac4d3` | test(server): e2e fleet command round-trip over a real stream |
| *(this)* | docs: M9 handoff |

## Build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .          # must print nothing
go vet ./...

# Run the server
./marshal server --listen :9000   # data: $XDG_DATA_HOME/marshal-server

# Start an agent (app.yaml has server:{address: localhost:9000, name: dev-1})
./marshal start /path/to/app.yaml

# Fleet command commands
./marshal fleet restart dev-1 ticker --server localhost:9000
./marshal fleet start   dev-1 /path/to/other.yaml --server localhost:9000
./marshal fleet stop    dev-1 all --server localhost:9000
./marshal fleet delete  dev-1 beeper --server localhost:9000
```

## Smoke test proof (2026-06-17)

`XDG_DATA_HOME=/tmp/m9smoke`, server on `:9000`, `app.yaml` = `ticker` app
(`sh -c 'while true; do echo tick; sleep 1; done'`) + `server:{address: localhost:9000,
name: dev-1}`; `other.yaml` = a `beeper` app.

- `fleet ps` → `dev-1 online`, `ticker online` PID 32012.
- `fleet restart dev-1 ticker` → PID 32012 → 32174, uptime reset to 0s (real bounce).
- `fleet start dev-1 other.yaml` → `beeper online` PID 32257; **`dump.json` now lists BOTH
  ticker and beeper** (auto-save on start).
- `fleet delete dev-1 beeper` → **`dump.json` lists only ticker** (auto-save on delete).
- `fleet restart dev-1 ghost` → `error: no app matching "ghost"`, exit code 1 (agent error
  string propagates; non-zero exit).

(The `fleet ps` issued ~1s after delete still showed beeper, because the agent's state
snapshot streams up on a ~2s tick — `dump.json` is the authoritative proof of the mutation.
This snapshot lag is expected, not a bug.)

## Deferred / known issues

1. **Auth / server-side authorization of commands** — M10 (bootstrap token + TLS). The
   server is still unauthenticated and binds all interfaces; anyone who can reach `:9000`
   can issue fleet commands. This is the most important next step.
2. **Best-effort timeout semantics:** a `FleetControl` that hits its deadline may still have
   executed on the agent (the command was already sent). Documented, not reconciled.
3. **No command audit log** on the server.
4. **Partial-failure reporting for `all`-selector ops** is the manager's aggregate result;
   per-instance failure detail beyond the `error` string is future work.
5. **No live `fleet logs --follow`** — unchanged from M8b.
6. **Snapshot lag:** post-command `fleet ps` reflects the agent's state only after its next
   ~2s snapshot push. Acceptable for now; a server-side optimistic update on command success
   could tighten it.

### Minor code-quality follow-ups surfaced in review (non-blocking, for triage)

- `cmd/marshal/fleet.go`: `fmt.Errorf("%s", res.GetError())` → `%w`/`errors.New` is more
  idiomatic (vet is clean on 1.26); cobra prints usage on this error (consider
  `SilenceUsage`).
- No dedicated unit test for `fleetStartCmd`'s YAML-load + `ControlOp_Start` path (covered
  by the smoke test, not a Go test).
- `internal/daemon/server.go`: `Start` uses `strings.Contains(err.Error(), "already")` to
  re-map the `AlreadyExists` gRPC code — pre-existing string coupling made explicit; a
  manager sentinel error + `errors.Is` would be cleaner.
- `internal/server/broker.go`: `pendingLen()` is a test-only helper living in the production
  file; could move to `broker_test.go`.

## Concrete next step

1. **Final whole-branch review**, then **merge `m9-command-channel` to `main`** via the
   `finishing-a-development-branch` skill.
2. **Next milestone:** M10 (auth/TLS — bootstrap token + per-agent identity + TLS
   everywhere; gate command authorization server-side). This closes the biggest gap now that
   commands can mutate remote hosts. Alternatively the web dashboard sub-project, which can
   now show live state, history, logs, AND drive controls.
