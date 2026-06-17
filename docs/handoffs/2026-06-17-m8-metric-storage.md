# M8 Metric Storage — Handoff

**Date:** 2026-06-17  
**Branch:** `m8-metric-storage`  
**Commit range:** `32e9c4a..HEAD` (12 commits + 1 pending for pruning + handoff)  
**Gate:** green (gofmt silent, vet/build clean, all tests pass with `-race -count=1`)

---

## Current state

M8 is complete. The central server now:

- Persists per-agent metrics to SQLite (one DB per agent under `<data-dir>/agents/<name>/metrics.db`)
- Derives a high-water-mark from `MaxTs()` and sends it back to the agent on Hello, enabling backfill of any gaps
- Ingests `MetricBatch` gRPC messages from agents and stores them oldest-first for crash-safe commits
- Serves `FleetMetricsHistory` RPC: bucketed CPU/mem query for any agent + app selector
- Exposes `marshal fleet metrics <agent> <name>` CLI with sparkline output (mirrors local `marshal metrics`)
- Prunes stored samples older than 7 days (10-minute ticker in `ServeDir`)

The daemon side:
- Samples all supervised instances every 5 s via `metrics.Sampler`
- Stores samples locally in `~/.local/share/marshal/metrics.db` (7-day retention)
- Pushes new samples (since watermark) to the central server on each connect, then streams in real-time
- Live cpu/mem surfaces in `fleet ps` proc snapshots (zero until first sample tick)

## What changed this session (M8 commits, oldest → newest)

| Commit | Change |
|--------|--------|
| `d7728f3` | `metricstore`: add `SamplesSince`, `MaxTs`, `Labels` accessors |
| `a2face7` | `metricstore`: share `MergeBuckets` and `AutoBucketMs` with daemon |
| `e129aff` | Proto: add `MetricBatch`, `HelloAck` watermark, `FleetMetricsHistory` RPC |
| `2399abb` | Server: per-agent lazily-opened metric stores (`stores` type) |
| `c10d137` | Server: ack metric watermark, store `MetricBatch`, reject empty name |
| `610d5e7` | Server: `FleetMetricsHistory` query handler |
| `04f826b` | Server: `--data-dir` flag, `ServeDir` wiring |
| `b8c1cc5` | Fleet client: watermark backfill, silence canceled-shutdown log |
| `eba7f43` | Daemon: ship local metrics to fleet, live cpu/mem in snapshots, unknown-name default |
| `af38d25` | Daemon: remove dead `procInfos` helper |
| `ecb044c` | CLI: `marshal fleet metrics` with sparkline |
| `1539f41` | Tests: e2e metric ingest + reconnect backfill |
| *(this commit)* | Server: `pruneAll` + 7-day retention goroutine in `ServeDir`; M8 handoff |
| *(spec-gap fix)* | CLI: `fleet ps` now renders live CPU/MEM columns (`fix(cli): render live CPU/MEM columns in fleet ps`) |

### Key decisions

**Watermark-pull design:** rather than the agent tracking what it sent, the server derives the watermark from `MaxTs()` on its local SQLite and sends it back as `HelloAck.LastMetricTsMs`. The agent then queries `SamplesSince(watermark)` and pushes the diff. This is crash-safe: if the server loses data, it simply sends a lower watermark and gets a full replay (bounded by the agent's 7-day local retention).

**Per-agent SQLite:** each agent gets its own `metrics.db` to avoid cross-agent contention and make per-agent deletion cheap.

**Derived high-water-mark:** `MaxTs()` reads `SELECT max(ts) FROM metric_row`; no extra bookkeeping column needed.

**Shared `MergeBuckets`/`AutoBucketMs`:** moved to `metricstore` so both the local `marshal metrics` and server-side `FleetMetricsHistory` share the same bucketing logic without import cycles.

**Folded-in M7 fixes:** empty-name guard (server rejects `Hello` with empty agent name), canceled-stream log suppression in the fleet client.

---

## Build / run / test

```bash
# Build
go build -o marshal ./cmd/marshal

# Run all tests (including race check)
go test ./... -race -count=1

# Lint / format
gofmt -l .          # must print nothing
go vet ./...

# Run the server (data defaults to $XDG_DATA_HOME/marshal-server or ~/.local/share/marshal-server)
./marshal server --listen :9000

# Start an agent connecting to the server
./marshal start /path/to/app.yaml   # app.yaml must have server: {address: localhost:9000, name: dev-1}

# Fleet commands
./marshal fleet ps --server localhost:9000
./marshal fleet metrics dev-1 <appname> --server localhost:9000
```

---

## Smoke test proof (2026-06-17)

Setup: binary built from HEAD, `XDG_DATA_HOME=/tmp/m8smoke`, app yaml:

```yaml
server:
  address: localhost:9000
  name: dev-1

apps:
  - name: ticker
    cmd: /bin/sh
    args: ["-c", "while true; do echo tick; sleep 1; done"]
    restart: always
```

**Server start:**
```
marshal server: listening on [::]:9000, data /tmp/m8smoke/marshal-server
```

**`./marshal start /tmp/m8app.yaml`:**
```
ID  NAME    INST  STATE     PID  CPU  MEM  UPTIME  RESTARTS
1   ticker  0     starting  0    -    -    -       0
```

**`./marshal list` (local, after ~40s):**
```
ID  NAME    INST  STATE   PID    CPU   MEM    UPTIME  RESTARTS
1   ticker  0     online  92679  0.1%  3.1MB  40s     0
```

**`./marshal fleet ps --server localhost:9000`:**
```
AGENT  STATUS  ID  NAME    INST  STATE   PID    CPU   MEM    UPTIME  RESTARTS
dev-1  online  1   ticker  0     online  93226  0.1%  3.1MB  24s     0
```

**`./marshal fleet metrics dev-1 ticker --server localhost:9000`:**
```
dev-1/ticker — last 1h0m0s, 2 buckets
CPU  ▁█  min 0.1%  avg 0.1%  max 0.1%
MEM  ▁▁  min 3.1MB  avg 3.1MB  max 3.1MB
```

Sparkline present with CPU and MEM buckets. Metrics stored in `/tmp/m8smoke/marshal-server/agents/dev-1/metrics.db`.

---

## Deferred / known issues

1. **Backfill bounded by local retention:** if the local daemon has pruned data (7-day window), the server cannot backfill beyond that point. This is by design but means gaps accumulate if an agent is offline > 7 days.

2. **Server binds all interfaces, unauthenticated:** `./marshal server` listens on `0.0.0.0` with no TLS or auth. Planned for M10.

3. **Selector resolution is name/label-prefix only:** `FleetMetricsHistory` uses name or label-prefix matching (`sel == l || strings.HasPrefix(l, sel+"#")`). Numeric ID resolution is not implemented on the server (only works locally where manager state is available).

4. **Minor: empty `HOME` relative data path:** if both `XDG_DATA_HOME` and `HOME` are unset, the server data path falls back to a relative `"marshal-server"`. Rare in practice but not hardened.

5. **Minor: `--data-dir` cobra help shows empty default:** the cobra flag for `--data-dir` shows an empty default string in `--help`; the actual default is computed at runtime. Cosmetic only.

---

## Concrete next step

1. **Merge `m8-metric-storage` to `main`** using the `finishing-a-development-branch` skill:
   ```
   /superpowers:finishing-a-development-branch
   ```

2. **M8b — logs up + server-side log files + Approach-B timestamped records:**
   - `marshal fleet logs <agent> <app>` — stream captured stdout/stderr from the central server
   - Server-side log files per agent/app (Approach B: timestamped record store, analogous to metricstore)
   - Wire daemon to ship new log lines to the server on connect + stream
