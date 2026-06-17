# Handoff — 2026-06-17 — M7 (central-server connection + live fleet state) complete

## TL;DR
Marshal **Milestone M7** — the first milestone of **sub-project #3 (central server / fleet
aggregation)** — is fully implemented, tested (race-clean), reviewed task-by-task **and** with a
final whole-branch review (verdict **Ready to merge: Yes**, one trivial pre-merge fix applied),
and smoke-tested with the real binary. Marshal now runs as a central server (`marshal server`);
a daemon with a `server:` block in its `marshal.yaml` dials it over an agent-initiated gRPC
stream, pushes process-state snapshots up every 2s; the server keeps live fleet state in memory;
`marshal fleet ps` reads it. Transport is **plaintext/unauthenticated by design** (TLS+auth are
M10). Built subagent-driven on branch **`m7-fleet-connection`** (11 commits,
`ef5891b`..`93867ab`, **NOT yet merged** — finish with the finishing-a-development-branch flow:
no git remote, so a local `--no-ff` merge to `main`, like M1–M6). Next milestone: **M8 — metric
& log records up + server-side storage (SQLite + log files)**.

## Current state

- Branch: `m7-fleet-connection`, cut from `main` at `220e3f5` (the M7 plan commit). 11 commits.
  Working tree clean (the built `./marshal` binary is gitignored via `/marshal`).
- `main` holds the M7 **design** (`fc888c4`, refined `1b2e92e`) and **plan** (`220e3f5`) docs only.
- Full gate green: `gofmt -l .` lists nothing, `go vet ./...` ✓, `go build ./...` ✓,
  `go test ./... -race -count=1` ✓ (all packages).
- Commits on the branch:
  - `ef5891b` feat(proto): add Fleet service (Connect stream + ListFleet)
  - `bb11e8e` feat(config): add optional server: block (address + name)
  - `2d8c789` feat(store): persist central-server config to fleet.json
  - `de3c2d0` feat(server): in-memory fleet registry with offline detection
  - `93ef57d` feat(server): Fleet gRPC service (Connect stream + ListFleet)
  - `ad9ef9d` feat(fleet): agent client with periodic snapshot push and reconnect
  - `2ebb1a3` feat(daemon): start fleet client when a server config is persisted
  - `b5a7831` feat(cli): add marshal server subcommand
  - `2f84f3d` feat(cli): add marshal fleet ps
  - `3efde58` feat(cli): persist server: block on start so the daemon connects
  - `93867ab` fix(store): write fleet.json 0600 for store-private consistency *(final-review fix)*

## What exists now (and works)

1. **`marshal server`** (`internal/server`, `cmd/marshal/server.go`). Single binary, role chosen by
   subcommand. `--listen` (default `:9000`). In-memory only — no persistence in M7. Serves both
   the agent stream (`Connect`) and the CLI read (`ListFleet`) on one gRPC server; `GracefulStop`
   on SIGINT/SIGTERM.
2. **Fleet registry** (`internal/server/registry.go`). `agentName → {procs, streamOpen, lastSeen}`,
   mutex-guarded, clock-injectable. `connected = streamOpen && (now − lastSeen ≤ offlineAfter)`,
   default `offlineAfter` 10s; last snapshot retained when offline.
3. **Fleet gRPC service** (`internal/server/server.go`). `Connect` reads `Hello` (→ `Open` + ack)
   and `StateSnapshot` (→ `Update`); on stream EOF/error → `Close`. `ListFleet` returns `reg.List()`.
   Stream is bidirectional but only the upstream direction is used; `ServerMessage` downstream is
   **reserved for M9**.
4. **Agent fleet client** (`internal/fleet/client.go`). One goroutine: dial (insecure) → `Hello` →
   immediate snapshot → push every 2s (the push doubles as the liveness heartbeat). Reconnect with
   exponential backoff 1s→30s, reset on a successfully established stream. **Never blocks or crashes
   supervision; a server outage only logs and backs off.**
5. **Daemon wiring** (`internal/daemon/fleet.go` + `server.go` `Run`). If `store.LoadServer()`
   returns a config, `Run` starts the client (scoped to `serveCtx`) with a `manager.List()` →
   `[]*pb.ProcInfo` adapter (`procInfos`, reusing the existing `snapshotToProc`; cpu/mem zero in M7).
   Agent name defaults to `os.Hostname()`. No config → no client (standalone unchanged).
6. **Config delivery** (`config.ServerConfig`, `store.SaveServer/LoadServer`,
   `cmd/marshal/control.go`). `marshal start <yaml>` persists the `server:` block to `fleet.json`
   in the store dir **before** connecting, so the auto-spawned daemon reads it on startup. The
   `Daemon` proto service is **unchanged** — no new RPC.
7. **`marshal fleet ps`** (`cmd/marshal/fleet.go`). Dials `--server` (default `$MARSHAL_SERVER`,
   then `localhost:9000`), calls `ListFleet`, renders a table grouped by agent with online/offline
   status (offline shows age).

### Key design decisions (recap from the spec)

- **`marshal server` subcommand**, not a separate binary (honors the locked single-static-binary
  decision; one artifact, no version skew).
- **`ProcInfo` reused** across `daemon.proto` and `fleet.proto` (fleet.proto imports daemon.proto;
  same proto package `marshal.v1` / Go package `pb`). The DRY win.
- **Periodic 2s push doubles as heartbeat** — M7 has no on-change mechanism; process-state changes
  surface within ≤2s. (The spec's original "on-change + 15s heartbeat" was simplified to this during
  planning to avoid building a manager event bus.)
- **Config via persisted `fleet.json`**, not a flag or a new RPC. Consequence (accepted under the
  "no hot-reload" non-goal): adding/altering a `server:` block on an **already-running** daemon takes
  effect on its next restart, not live.
- Approach: in-memory server, plaintext transport. SQLite/log storage → M8; downward control → M9;
  TLS/token auth → M10.

### Smoke proof (macOS host, this session)
```bash
go build -o marshal ./cmd/marshal
./marshal server --listen :9000 &                 # central server (in-memory)
export XDG_DATA_HOME=/tmp/m7smoke
# /tmp/m7app.yaml: server{address: localhost:9000, name: dev-1} + a ticker app
./marshal start /tmp/m7app.yaml                   # auto-spawns daemon, which dials the server
./marshal fleet ps --server localhost:9000        # -> dev-1 / ticker / online, uptime 4s
./marshal kill                                    # stop the daemon (stream closes)
./marshal fleet ps --server localhost:9000        # -> dev-1 "offline 4s", last snapshot retained
```
Both transitions observed exactly as designed.

## Deferred / known issues (all non-blocking; from the final whole-branch review)

- **Empty agent name edge.** If `os.Hostname()` fails **and** no `server.name` is configured, the
  agent name is `""`; the server's `if name != ""` guard in `Connect` then silently drops that
  agent's snapshots (it appears connected via Hello but reports no procs), and two such agents would
  collide on the `""` key. Rare (hostname failure is exotic). **Fix in early M8:** reject empty names
  in `Registry.Open` or default to `"unknown"`.
- **Clean-shutdown log noise.** On ctx-cancel the fleet client logs
  `fleet: connection to <addr> ended: context canceled` on every normal daemon stop. **Fix:**
  `errors.Is(err, context.Canceled)` check before logging in `client.go` `Run`.
- **Server binds all interfaces, unauthenticated.** `--listen :9000` exposes `Connect`/`ListFleet`
  to anyone who can reach the port — by design for M7's plaintext scope. Call this out in operator
  docs until **M10** adds TLS + bootstrap tokens.
- **Minor/cosmetic:** `registry.go` `entry()` lacks a "must hold mu" comment (Open/Update
  auto-vivify, Close uses a raw lookup — both deliberate); `cmd/marshal/control.go` `startCmd` calls
  `store.New()` a second time (harmless; `withClient` makes its own); `printFleet` bare-"offline"
  path (`LastSeenUnix==0`) not directly unit-tested; reconnect test uses a hard-coded 60ms sleep
  (passed `-race`, generous 2s `waitFor`).
- Carried from earlier: deep-merge perfect cross-stream log interleave (Approach B) and timestamped
  log records are the natural home of **M8**.

## How to build / run / test
```bash
go build -o marshal ./cmd/marshal
./marshal server [--listen :9000]                  # central server
# In a marshal.yaml: add a top-level `server: { address: host:port, name: <agent> }` block.
./marshal start marshal.yaml                        # daemon connects to the server
./marshal fleet ps [--server host:port]             # default $MARSHAL_SERVER, then localhost:9000
go test ./... -race -count=1                        # all green
go vet ./... && gofmt -l .                          # clean
go generate ./internal/pb                           # regenerate proto (protoc 35.0 on PATH)
```

## Architecture context (bigger picture)

Marshal → fleet manager, 4 sub-projects (spec
`docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`):
1. **Agent / supervisor core** — M1–M4 ✅ (sub-project #1 complete).
2. **Metrics & log pipeline** — M5–M6 ✅ (sub-project #2 complete).
3. **Central server / fleet aggregation** — **M7 (connection + live state) ✅ (this handoff)**;
   M8 (metric/log records + storage), M9 (downward control), M10 (auth + TLS) remain.
4. **Web dashboard** — last.

M7 design: `docs/superpowers/specs/2026-06-17-marshal-central-server-m7-connection-design.md`;
plan: `docs/superpowers/plans/2026-06-17-marshal-central-server-m7-connection.md`.

## Next step

Merge `m7-fleet-connection` to `main` (local `--no-ff`, via the finishing-a-development-branch flow).
Then **start M8 — metric & log records up + server-side storage**: stream metric batches → SQLite on
the server, log records → server-side files (the natural home for the deferred **timestamped log
records / Approach B** that give perfect cross-stream interleave in deep history), with server-side
history queries. Fold in the deferred empty-agent-name fix and the clean-shutdown log fix early.
The downstream `ServerMessage` direction reserved in `fleet.proto` is where **M9**'s control plane
will land.
