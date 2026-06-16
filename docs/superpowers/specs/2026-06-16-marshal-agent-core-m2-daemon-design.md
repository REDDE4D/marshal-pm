# Marshal — Agent Core M2 (daemonization + control CLI) — Design

**Date:** 2026-06-16
**Status:** Approved (design); ready for implementation planning
**Parent:** [Agent-core design](2026-06-16-marshal-agent-core-design.md)
**Milestone:** M2 of the agent core (M1 ✅ done; M3–M4 remain)
**Project:** Marshal (`marshal` CLI / `marshald` daemon)

## 1. Scope

Turn the M1 foreground supervisor into a **daemon + thin control CLI**. `marshald` becomes
a long-lived process that owns and supervises all managed apps; `marshal` becomes a thin
**gRPC client** over a **Unix domain socket** that drives it.

This is a deliberately **lifecycle-only** milestone — the clean cut between M2 and M3:

**In scope (M2):**
- gRPC daemon over a Unix socket; CLI client with daemon auto-spawn.
- Full process lifecycle: `start`, `stop`, `restart`, `delete`, `list`/`ls`, `describe`.
- Daemon lifecycle: `daemon` (foreground), `kill`, auto-spawn on demand.
- Persistence: `save` / `resurrect` (`dump.json`) + auto-resurrect on daemon startup.
- The M1 `run` command stays as-is (foreground supervisor; no daemon).

**Deferred to M3 (explicitly NOT in M2):**
- Log capture (rotated files + in-memory ring buffer) and the `marshal logs` command.
- Live metrics (CPU% / RSS via gopsutil). `list`/`describe` show state/pid/uptime/
  restarts only.

**Deferred to later milestones / sub-projects:**
- Boot startup install (`startup`/`unstartup`) — M4.
- Upstream gRPC stream to a central server — sub-project #3 (the contract is shaped here
  so it is reusable, but no cross-host wire is implemented).

**Platform:** Linux + macOS. Windows deferred (process groups, sockets, detach differ).

## 2. Architecture

```
  marshal (CLI)  ──gRPC over UDS──▶  marshald (daemon)
   thin client                        owns manager.Manager + all processes
                                      $XDG_DATA_HOME/marshal/marshald.sock
                                      (default ~/.marshal/marshald.sock)
```

- **`marshald`** holds one long-lived `manager.Manager` (the M1 supervisor core, extended
  for runtime add/remove). It runs a gRPC server bound to a Unix socket under the state
  directory.
- **`marshal` CLI** dials the socket for every command. On *connection refused / socket
  missing*, it **auto-spawns** the daemon (see §5), waits for readiness, and retries.
- **`marshal daemon`** runs the server in the foreground — this is what the detached spawn
  invokes and what the future M4 systemd/launchd unit will call.

### State directory

`~/.marshal/` by default, `$XDG_DATA_HOME/marshal/` when `$XDG_DATA_HOME` is set. Created
on first use. Contents in M2:
- `marshald.sock` — the gRPC Unix socket.
- `marshald.log` — daemon stdout/stderr when auto-spawned (detached).
- `dump.json` — saved app definitions (§6).

## 3. gRPC contract (the reusable artifact)

`proto/marshal/v1/daemon.proto` defines service `Daemon`, compiled with `protoc` +
`protoc-gen-go` / `protoc-gen-go-grpc` into `internal/pb` (generated code is committed; a
`go generate` directive / `make proto` regenerates it).

M2 implements only the lifecycle subset. The proto **defines** the dormant fields/RPCs so
M3 adds them without a contract break.

| RPC | Request → Response | M2 status |
|-----|--------------------|-----------|
| `Start` | `StartRequest{ apps: []AppSpec }` → `ProcList` | implemented |
| `Stop` | `Selector` → `ProcList` | implemented |
| `Restart` | `Selector` → `ProcList` | implemented |
| `Delete` | `Selector` → `ProcList` | implemented |
| `List` | `Empty` → `ProcList` | implemented |
| `Describe` | `Selector` → `ProcInfo` | implemented |
| `Save` | `Empty` → `Ack` | implemented |
| `Resurrect` | `Empty` → `ProcList` | implemented |
| `Kill` | `Empty` → `Ack` | implemented |
| `Logs` (server-streaming) | `LogRequest` → stream `LogLine` | **defined, not implemented** (M3) |

### Message shapes

- **`AppSpec`** mirrors `config.App`: `name`, `cmd`, `args`, `cwd`, `instances`, `env`,
  `restart` (always|on-failure|no), `max_restarts`, `kill_timeout`.
- **`Selector`** = `{ target: string }` resolving to a `name`, numeric `id`, or `all`.
- **`ProcInfo`** mirrors a supervisor instance snapshot: `id`, `name`, `instance_id`,
  `state`, `pid`, `uptime`, `restarts`, plus **`cpu`/`mem` defined but unset in M2**.
- **`ProcList`** = `{ procs: []ProcInfo }`. **`Ack`** = `{ ok, message }`.

These types are deliberately close to `config.App` + supervisor state so the same wire
types serialize for the sub-project-#3 cross-host link.

## 4. Package layout

New packages, each with one clear responsibility:

- **`internal/pb`** — generated gRPC/protobuf code (do-not-edit).
- **`internal/store`** — state-dir layout (resolve base from `$XDG_DATA_HOME`/`$HOME`) +
  `dump.json` read/write. Pure; tested with an injected base path in a temp dir.
- **`internal/daemon`** — the gRPC server: implements the `Daemon` service, owns the
  `manager.Manager`, maps RPCs → manager operations, handles socket bind/listen/cleanup
  and graceful shutdown.
- **`internal/client`** — gRPC client + auto-spawn (dial, detect-not-running, fork-exec
  the daemon, wait-for-ready). Used by every CLI command.
- **`cmd/marshal`** — cobra commands, thin wrappers over `internal/client`.

### Change to M1 `internal/manager`

Today the manager fans a fixed `config.Config` into instances and supervises them. M2 adds
a small **runtime-mutation API**:
- `Add(app config.App) []ProcInfo` — fan into N instances, supervise, assign an id.
- `Stop(sel) / Restart(sel) / Delete(sel)` — selector-resolved (`name` | numeric `id` |
  `all`).
- `List() / Describe(sel)` — snapshots (`Snapshot()` already exists from M1).

**App identity:** the daemon assigns a stable **numeric id** per app (PM2-style,
monotonic). Selectors resolve by `name`, `id`, or `all`. `start` of a name that already
exists is an error (`AlreadyExists`) — use `restart` to cycle it.

This is the one M1 package that grows; the mutation API stays small and is unit-tested
independently of gRPC.

## 5. Daemon lifecycle & auto-spawn

- **Auto-spawn:** when the CLI's dial fails (socket missing or connection refused), it
  `fork-exec`s its own binary as `marshal daemon`, detached: new session (`Setsid`), std
  streams redirected to `marshald.log`. It then polls the socket until the server answers
  (bounded timeout, e.g. ~3s); on timeout it surfaces the tail of `marshald.log`.
- **Stale socket:** on startup the daemon attempts to remove a leftover socket file if no
  live daemon answers it, then binds fresh. A dial failure on the CLI side always means
  "spawn needed."
- **`marshal kill`:** RPC `Kill` → daemon gracefully stops all apps (SIGTERM →
  `kill_timeout` → SIGKILL, reusing M1 logic), removes the socket, and exits.
- **Signals:** `marshald` handles SIGINT/SIGTERM as a graceful shutdown of all apps (same
  path as `Kill`), reusing the M1 shutdown discipline.

## 6. Persistence — save / resurrect

- **`marshal save`** → daemon serializes the current app **definitions** (the `config.App`
  specs, not live PIDs) to `dump.json`.
- **`marshal resurrect`** → daemon reads `dump.json` and starts every app.
- **Auto-resurrect:** on daemon startup, if `dump.json` exists, load and start it (so a
  boot-time daemon brings the saved fleet back). Missing/empty dump → start idle.

## 7. Error handling

- gRPC status codes map to clean CLI messages:
  - `NotFound` → `marshal: no app matching "foo"`.
  - `AlreadyExists` → `marshal: app "foo" already exists (use restart)`.
  - `Unavailable` after a failed auto-spawn → print the `marshald.log` tail.
- Daemon RPC handlers validate `AppSpec` via the existing `config` validation before
  admitting an app.

## 8. Testing

- **Unit:**
  - `store` — round-trip `dump.json` and state-dir resolution in a temp dir / temp
    `$XDG_DATA_HOME`.
  - `manager` — mutation API: add/stop/restart/delete, selector resolution (name/id/all),
    duplicate-name rejection, id assignment.
  - `daemon` — RPC handlers against an in-process manager (no real sockets needed for the
    mapping logic).
- **Integration (TDD, real processes):** start the daemon on a temp socket inside a temp
  `$XDG_DATA_HOME`, drive it through the **real gRPC client** with short-lived test
  binaries. Assert: `start`→online, `list` reflects state, `restart` bumps restart count,
  `stop` is graceful, `delete` removes, and `save` + a fresh daemon + auto-resurrect
  restores the fleet.
  - Reuse the M1 e2e discipline: poll for readiness tokens, **never** a fixed
    `time.Sleep`; mutex-guarded output buffers.
- **Race / lint:** whole suite under `go test ./... -race -count=1`; `go vet ./...` and
  `gofmt -l .` clean before finishing, per project convention.

## 9. Suggested implementation order (for the plan)

1. `proto` + `internal/pb` codegen (toolchain + generated stubs committed).
2. `internal/store` (state dir + dump.json) — pure, TDD first.
3. `internal/manager` runtime-mutation API — TDD.
4. `internal/daemon` gRPC service wiring the manager + socket lifecycle.
5. `internal/client` (dial + auto-spawn).
6. `cmd/marshal` commands (thin wrappers); keep M1 `run` intact.
7. Integration suite over the real socket; race/lint pass; handoff doc.

These refine in the implementation plan.
