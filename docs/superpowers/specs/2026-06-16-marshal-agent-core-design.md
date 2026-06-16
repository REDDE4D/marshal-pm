# Marshal — Agent / Supervisor Core (Sub-Project #1) — Design

**Date:** 2026-06-16
**Status:** Approved (design); ready for implementation planning
**Parent:** [Fleet architecture](2026-06-16-fleet-process-manager-architecture-design.md)
**Project:** Marshal (`marshal` CLI / `marshald` daemon)

## 1. Scope

A standalone, single-host process supervisor: the `marshald` daemon plus the `marshal`
CLI. It is **fully usable with no central server** — a working PM2-core replacement on one
host. It *defines* the gRPC contract for the future server link but does **not** implement
the upstream stream (that is sub-project #3).

**Platform:** Linux + macOS for v1. **Windows is deferred** — named pipes, service
installation, and process signals differ enough to be handled as separate work.

### Out of scope for #1
- Upstream gRPC stream to the central server (contract defined here, not implemented).
- Built-in TCP load balancer across instances (fork mode only; apps self-bind via
  `SO_REUSEPORT`).
- Metric **history**/storage and log aggregation (sub-project #2 — only live, in-memory
  metrics here).
- Windows support.
- Network auth (the local Unix socket is gated by filesystem permissions).

## 2. Process Model & Supervision

### States
`starting → online → stopping → stopped`, plus `restarting` and `errored`.

### Restart policy (defaults, overridable per app)
- `autorestart` **on** by default.
- **Exponential backoff:** start 100ms, double each unstable restart, capped at 15s.
- **min-uptime guard:** 1s. A process that exits faster than min-uptime counts as an
  "unstable" restart.
- **max-restarts:** after 16 consecutive unstable restarts the process is marked
  `errored` and no longer restarted.
- **Policy modes:** `always` | `on-failure` | `no`.
  - `always`: restart on any exit.
  - `on-failure`: restart only on non-zero exit / signal-kill.
  - `no`: never auto-restart.

### Graceful stop
SIGTERM → wait `kill_timeout` (default 5s) → SIGKILL.

### Fork mode (multiple instances)
`instances: N` runs N independent copies of the app as separate OS processes. Each
receives its index via env var `MARSHAL_INSTANCE_ID` (0..N-1) so the app can choose to
`SO_REUSEPORT`-bind a shared port itself. No Marshal-side load balancing in v1.

## 3. Daemon ↔ CLI Architecture

- `marshald` is a long-lived daemon that owns and supervises all managed processes.
- `marshal` CLI is a thin **gRPC client** over a **Unix domain socket** at
  `~/.marshal/marshald.sock`. The CLI auto-spawns the daemon if it is not running.
- A single gRPC service, `Daemon`, with methods:
  `Start, Stop, Restart, Delete, List, Describe, Logs (server-streaming), Save,
  Resurrect`.
- The same RPC stack (gRPC) is reused for the future agent↔server link, so message types
  for process state/metrics/logs are designed once.

## 4. App Definition

### Imperative (CLI)
```
marshal start ./server --name api -i 4 --cwd /srv/api --env KEY=val --restart on-failure
```

### Declarative (`marshal.yaml`)
```yaml
apps:
  - name: api
    cmd: ./server
    args: ["--port", "8080"]
    cwd: /srv/api
    instances: 4            # fork mode; each gets MARSHAL_INSTANCE_ID
    env: { NODE_ENV: production }
    restart: on-failure     # always | on-failure | no
    max_restarts: 16
    kill_timeout: 5s
```
`marshal start marshal.yaml` launches every app in the file.

**Field reference (per app):** `name` (required, unique), `cmd` (required), `args`,
`cwd`, `instances` (default 1), `env`, `restart` (default `always`), `max_restarts`
(default 16), `kill_timeout` (default 5s).

## 5. Persistence & Resurrection

- State directory `~/.marshal/` (honoring `$XDG_DATA_HOME` when set).
- `marshal save` writes the current app definitions to `~/.marshal/dump.json`.
- `marshal resurrect` restores apps from the dump.
- On startup, the daemon auto-resurrects from `dump.json` if present.

## 6. Boot Startup

- `marshal startup` detects the init system and generates + installs:
  - a **systemd unit** on Linux, or
  - a **launchd plist** on macOS,
  that launches `marshald` on boot (which then resurrects saved apps).
- `marshal unstartup` removes the installed unit/plist.

## 7. Logs

- Per process+instance, stdout and stderr are captured to rotated files:
  `~/.marshal/logs/<name>-<instance>.out.log` and `.err.log`.
- Rotation: rotate at 10MB, keep 5 files.
- An in-memory **ring buffer** (~1000 lines per process) backs instant
  `marshal logs <name> -f` (follow) without re-reading files.

## 8. Metrics (live only)

- Sampled every **5s** via `gopsutil`: per-process CPU %, RSS memory, uptime, restart
  count, pid, status.
- Held in memory and surfaced through `marshal list` and `marshal describe`.
- History/storage is sub-project #2.

## 9. CLI Surface

| Command | Purpose |
|---------|---------|
| `marshal start <file\|marshal.yaml> [flags]` | Start app(s). |
| `marshal stop <name\|id\|all>` | Graceful stop. |
| `marshal restart <name\|id\|all>` | Restart. |
| `marshal delete <name\|id\|all>` | Stop and remove from management. |
| `marshal list` / `ls` | Status table (state, pid, cpu, mem, uptime, restarts). |
| `marshal describe <name\|id>` | Full detail for one app/instance. |
| `marshal logs <name\|id> [--lines N] [-f]` | Tail logs. |
| `marshal save` | Dump app list. |
| `marshal resurrect` | Restore app list. |
| `marshal startup` / `unstartup` | Install/remove boot service. |
| `marshal kill` | Stop the daemon (and its processes). |
| `marshal daemon` | Run the daemon in the foreground (debug/used by service). |

## 10. Testing

- **Unit:** table-driven tests for the restart/backoff state machine (min-uptime,
  max-restarts, backoff cap, policy modes) and for `marshal.yaml` parsing/validation.
- **Integration:** spawn real short-lived test binaries through the daemon over the
  socket; assert supervision (online), restart-on-crash, backoff escalation,
  errored-after-max-restarts, graceful stop, and log capture/tail.

## 11. Suggested Module Boundaries (for the plan)

- `proc` — process spawn/signal/wait, instance fork.
- `supervisor` — per-app state machine, restart policy, backoff.
- `config` — `marshal.yaml` + CLI flag parsing/validation.
- `store` — dump/resurrect (`dump.json`), state dir layout.
- `logs` — capture, rotation, ring buffer, streaming.
- `metrics` — gopsutil sampling.
- `daemon` — gRPC server over UDS, wires the above.
- `cli` — gRPC client + command surface.
- `startup` — systemd/launchd generation & install.

These are a starting point; the implementation plan refines them.
