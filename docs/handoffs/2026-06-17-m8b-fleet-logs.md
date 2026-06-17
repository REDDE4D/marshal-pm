# M8b Fleet Log Storage — Handoff

**Date:** 2026-06-17
**Branch:** `m8b-fleet-logs` (not yet merged)
**Base:** `cd0b0c3` (main, post-M8 merge)
**Gate:** green — `gofmt -l .` silent, `go vet ./...` clean, `go build ./...` clean, `go test ./... -race -count=1` passes (16 packages).

---

## Current state

M8b is complete (pending final review + merge). The central server now stores **logs** as
well as metrics, and `marshal fleet logs <agent> <app>` queries that stored history. This
is the exact log analog of M8's metric storage.

- The daemon ships captured stdout/stderr lines to the central server over the existing
  `Fleet.Connect` stream.
- The server persists per-agent log lines to SQLite (`<data-dir>/agents/<name>/logs.db`,
  sitting next to `metrics.db`).
- The server derives a log high-water-mark from `MaxTs()` and returns it in
  `HelloAck.last_log_ts_ms`, enabling reconnect backfill of gaps.
- `marshal fleet logs <agent> <app|label>` serves backfill (last-N lines) with
  `-n/--lines`, `--stdout`, `--stderr` filters.
- Stored logs are pruned at 7-day retention by the existing `ServeDir` 10-minute ticker
  (now prunes both metric and log stores).

## Key decision: pull-based shipping (refinement vs the spec)

The approved spec (`docs/superpowers/specs/2026-06-17-m8b-fleet-logs-design.md`, §5)
described the daemon shipping mechanism as "subscribe to the Sink's live channel + a
Registry tap/observer." **The implementation instead uses a pull-based
`LogsFunc(sinceTsMs)` that reads the in-memory log ring each 2s push tick** — the exact
seam the metrics path already uses (`MetricsFunc`). This was reviewed and accepted before
implementation. It meets every approved constraint (backfill bounded by the ~1000-line
ring, no new local store, no write amplification) with far less surface area: no `Sink`
changes, no tap goroutines, no extra lock ordering. The only behavioral difference is that
a line becomes queryable on the server up to one push interval (~2s) later, which is
immaterial because `fleet logs` is backfill-only (no live follow). The spec carries a
banner noting this; the plan documents it in full.

## What changed this session (commits oldest → newest)

| Commit | Change |
|--------|--------|
| `3520ce0` | `logstore`: SQLite per-agent log record store (`Open`/`Append`/`Tail`/`MaxTs`/`Labels`/`Prune` + `MergeTail`, `StreamFilter`) |
| `ab8ce07` | Proto: `LogShipLine`, `LogBatch`, `HelloAck.last_log_ts_ms`, `AgentMessage.logs=4`, `FleetLogsHistory` RPC; regenerated pb |
| `12938a1` | `logs.Registry.RingSince(sinceMs)` + `LabeledLine` — merge ring lines across all sinks (incl. future ones) since ts |
| `4c81df9` | Fleet client: `LogsFunc`/`WithLogs`, separate log watermark seeded from `HelloAck`, `pushLogs` (mirrors `pushMetrics`) |
| `b1c0556` | Daemon: `logsSince(reg)` adapter; fleet client built with `WithLogs(logsSince(reg))` |
| `90b5fbf` | Server: `logStores` (lazy per-agent log stores, mirror of `stores`) |
| `43b8126` | Server: `Connect` `LogBatch` branch, `HelloAck` log watermark, `NewServer`/`Serve`/`ServeDir` arity + log pruning |
| `90307a1` | Server: `FleetLogsHistory` handler (selector resolve, per-label `Tail`, `MergeTail`, stream filter) |
| `7cbbcd4` | CLI: `marshal fleet logs <agent> <name|label>` |
| `1d64c62` | Tests: e2e log ingest + reconnect backfill |
| *(handoff commit)* | M8b handoff |
| `75b404b` | Tests: assert exact backfill lines (no-resend proof); doc: same-ms edge is steady-state |

(Plus earlier on the branch: `cf291a5` spec, `43af8aa` plan, `8d16d7d` spec refinement note.)

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

# Fleet log commands
./marshal fleet logs dev-1 <appname> --server localhost:9000
./marshal fleet logs dev-1 <appname> -n 50 --stderr --server localhost:9000
```

## Smoke test proof (2026-06-17)

`XDG_DATA_HOME=/tmp/m8bsmoke`, server on `:9000`, app yaml with `server:{address:
localhost:9000, name: dev-1}` and a `ticker` app (`while true; do echo tick; sleep 1; done`).

`./marshal fleet ps --server localhost:9000`:
```
AGENT  STATUS  ID  NAME    INST  STATE   PID    CPU   MEM    UPTIME  RESTARTS
dev-1  online  1   ticker  0     online  11676  0.1%  3.1MB  14s     0
```

`./marshal fleet logs dev-1 ticker --server localhost:9000` → 14 `ticker#0 | tick` lines.
`./marshal fleet logs dev-1 ticker -n 3` → exactly 3 lines.
`./marshal fleet logs dev-1 ticker --stderr` → empty (ticker writes only stdout — filter works).
On disk: `/tmp/m8bsmoke/marshal-server/agents/dev-1/logs.db` (alongside `metrics.db`).

## Deferred / known issues

1. **Reconnect backfill bounded by the ring (~1000 lines)**, not the 7-day rotated files.
   A long disconnect on a busy app loses lines beyond the ring. By design for M8b.
2. **Same-ms watermark edge (steady-state for logs):** the ring filter is `ts > sinceMs`, so a burst of lines sharing one millisecond that straddles a 2s push-tick boundary can drop the trailing same-ms line even on a live connection (not just on reconnect). Logs have a larger collision surface than metrics (which sample at controlled ~1s intervals). This is the accepted watermark semantics; a future tightening would key on `(ts, label, text)` or a per-tick sequence instead of `ts` alone.
3. **No live `--follow` over the fleet** — backfill-only this milestone.
4. **Selector is name / label-prefix only** server-side (no numeric-ID resolution).
5. **High-volume shipping** copies each sink's whole ring per 2s tick (`RingSince`); fine
   for modest fleets, revisit if it becomes hot.
6. **`splitLabel` duplicated** in package `server` and package `daemon` (independent
   packages); extracting a shared `internal/labelfmt` is reasonable future work.
8. **Server binds all interfaces, unauthenticated** (TLS/auth planned for M10).

## Concrete next step

1. **Merge `m8b-fleet-logs` to `main`** via the `finishing-a-development-branch` skill.
2. **Next milestone** (candidates): live `fleet logs --follow` (server fan-out of live
   lines); or M9 (server → agent command channel); or M10 (auth/TLS).
