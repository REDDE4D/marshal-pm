# Marshal

A free, self-hosted process manager — an alternative to PM2 (and the paywalled
PM2 Plus insights) that supervises any kind of OS process.

## Status

**Milestone M1: foreground supervisor.** Run apps defined in a `marshal.yaml`,
with restart policies, exponential backoff, and N fork-mode instances per app.
Each process runs in its own process group, so stopping an app cleanly tears
down its whole child tree.

Planned next (see `docs/superpowers/specs/` and `docs/superpowers/plans/`):

- **M2** — daemon mode + the `marshal` CLI control surface (start/stop/list/…) over a Unix-socket gRPC link, plus dump/resurrect.
- **M3** — log capture to rotated files and live metrics (CPU/memory) via sampling.
- **M4** — boot startup integration (systemd / launchd).
- Later — a central fleet server (many hosts) and a web dashboard for full insights.

## Build

```bash
go build -o marshal ./cmd/marshal
```

## Usage

```yaml
# marshal.yaml
apps:
  - name: api
    cmd: ./server
    args: ["--port", "8080"]
    instances: 2          # fork mode; each instance gets MARSHAL_INSTANCE_ID
    restart: on-failure   # always | on-failure | no
    max_restarts: 16
    kill_timeout: 5s
```

```bash
marshal run marshal.yaml   # supervise in the foreground; Ctrl-C to stop
```

On `Ctrl-C` (or `SIGTERM`) Marshal sends each app `SIGTERM`, waits up to
`kill_timeout`, then `SIGKILL`s any survivors — signaling the whole process
group so child processes are not orphaned.

## License

MIT (or Apache-2.0) — to be finalized. The goal: everything PM2 paywalls is free here.
