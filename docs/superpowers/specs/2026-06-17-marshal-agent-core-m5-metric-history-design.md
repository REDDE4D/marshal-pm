# Marshal Agent-Core — M5: Metric History (design)

Status: approved (2026-06-17). Milestone **M5** — the first milestone of **sub-project #2
(metrics & log pipeline)**. Builds on M3 (live metrics via `metrics.Sampler`). Delivers the
headline "PM2 Plus insights" replacement: per-instance CPU%/RSS **history**, persisted
locally and queryable from the CLI on a single host (no central server or dashboard yet).

Related docs:
- Fleet architecture: `docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`
- Agent-core design: `docs/superpowers/specs/2026-06-16-marshal-agent-core-design.md`
- M3 (logs + live metrics): `docs/superpowers/specs/2026-06-16-marshal-agent-core-m3-logs-metrics-design.md`

## 1. Goal

Today `metrics.Sampler` keeps only the **latest** CPU%/RSS reading per instance in memory.
M5 persists those readings as a time-series and surfaces the history through the CLI, so a
single-host user can answer "what has this process's CPU/memory been doing over the last N
hours" without any server or dashboard.

## 2. Scope

**In:**
- Persist per-instance CPU%/RSS samples to a local **SQLite** DB via the **pure-Go**
  `modernc.org/sqlite` driver (cgo-free — preserves the single static binary).
- Write path fed by the existing `metrics.Sampler` (every 5s tick).
- Retention by age: prune samples older than a configurable window (default 7 days).
- `MetricsHistory` gRPC RPC with **server-side SQL aggregation** into time buckets.
- `marshal metrics <name|id>` command (`--since`, `--bucket`, `--cpu`/`--mem`): ASCII
  sparkline + min/avg/max summary.
- A compact last-hour CPU+MEM sparkline added to `describe`.

**Out (deferred to M6 / later):**
- Tiered rollups / downsampling (raw → 1-min → 1-hour). Raw + age-out only for now.
- Stored status / restart-count / uptime series (event-like; already shown live).
- Log compression; deep log backfill across rotated segments; per-stream `logs` view; the
  two carried log fixes (max-line cap; backfill→subscribe race) — all **M6**.
- Cross-host storage and the central server (#3).

**Baked-in design call — downtime is a gap, not a row.** The `Sampler` already records
samples only for *online* instances (it skips `!Online || Pid <= 0`). M5 keeps that: an
offline instance simply produces no rows, so downtime shows as **missing buckets** in the
series rather than zero values or an explicit status column. Truthful and simpler.

## 3. Architecture

New leaf package **`internal/metricstore`** — SQLite-backed, single responsibility (persist
+ query samples). It imports only `modernc.org/sqlite` and stdlib; it knows nothing about
gopsutil, gRPC, or the supervisor. The **daemon owns all wiring**, the same pattern M3 used
for `logs.Registry` and the `Sampler`.

```
  metrics.Sampler ──5s tick──▶ daemon ──Append(ts, batch)──▶ metricstore (SQLite: metrics.db)
                                  │                                ▲
  marshal metrics ──gRPC────▶ marshald.MetricsHistory ──Query──────┘  (SQL bucket aggregation)
       describe   ──gRPC────────────┘
```

- The `Sampler` stays pure (gopsutil only). The daemon already calls it each tick to refresh
  live state; M5 adds the persistence write at that same point, so the sampler is never
  coupled to storage.
- DB lives at `<stateDir>/metrics.db` — one shared DB for all instances, keyed by label.
  `stateDir` is the directory the daemon already resolves (the same one holding `dump.json`).

### `metricstore` surface

```go
type Sample struct { Label string; Cpu float64; Mem uint64 }

type Bucket struct {
    TsMs   int64
    CpuAvg float64; CpuMax float64
    MemAvg uint64;  MemMax uint64
}

type QueryReq struct { Label string; SinceMs int64; BucketMs int64 }

func Open(path string) (*Store, error)
func (s *Store) Append(tsMs int64, samples []Sample) error // one batched tx per tick
func (s *Store) Query(req QueryReq) ([]Bucket, error)
func (s *Store) Prune(beforeMs int64) (int64, error)       // returns rows deleted
func (s *Store) Close() error
```

## 4. Data model

One table, one covering index:

```sql
CREATE TABLE samples (
    ts    INTEGER NOT NULL,   -- unix millis
    label TEXT    NOT NULL,   -- instance label (same key as logs/ring)
    cpu   REAL    NOT NULL,   -- percent, process-group sum
    mem   INTEGER NOT NULL    -- RSS bytes, process-group sum
);
CREATE INDEX idx_samples_label_ts ON samples(label, ts);
```

- **Write:** one batched transaction per 5s tick, inserting a row per online instance.
- **Retention:** a periodic `Prune(now - retention)` (e.g. every few minutes, driven by the
  daemon) deletes aged-out rows. Default retention **168h (7d)**, configurable.
- **Query-time bucketing** (no stored aggregates):

```sql
SELECT (ts/:bucket)*:bucket AS b,
       avg(cpu), max(cpu), avg(mem), max(mem)
FROM samples
WHERE label = :label AND ts >= :since
GROUP BY b
ORDER BY b;
```

Gaps (downtime) appear naturally as missing buckets.

## 5. Query path — gRPC

New RPC on the existing daemon service (mirrors how M3 added `Logs`):

```proto
rpc MetricsHistory(MetricsHistoryRequest) returns (MetricsHistoryResponse);

message MetricsHistoryRequest {
  string selector  = 1;  // name or id, resolved like logs/describe
  int64  since_ms  = 2;  // window start (now - since)
  int64  bucket_ms = 3;  // bucket width; 0 = server picks from window
}
message MetricBucket {
  int64  ts_ms   = 1;
  double cpu_avg = 2; double cpu_max = 3;
  uint64 mem_avg = 4; uint64 mem_max = 5;
}
message MetricsHistoryResponse { repeated MetricBucket buckets = 1; }
```

The server resolves the selector → label (same resolution `logs`/`describe` use), runs the
bucket-aggregation SQL, and returns buckets. If `bucket_ms == 0` it picks a sane width from
the window (target ~60 buckets across the range).

## 6. CLI surface & rendering

- **`marshal metrics <name|id> [--since 6h] [--bucket 1m] [--cpu|--mem]`** — default renders
  both CPU and MEM as Unicode sparklines (`▁▂▃▄▅▆▇█`) with a `min / avg / max` summary line,
  echoing the resolved window and bucket. `--cpu` / `--mem` narrows to one metric.
- **`describe`** gains a compact **last-hour** CPU+MEM sparkline (one line each), via the same
  RPC with a fixed window — no new flags.
- Sparkline rendering is a small pure helper (`[]float64 → string`) in the CLI, unit-tested
  independently of any I/O.

## 7. Config

- New daemon-level setting: **metric retention window** (default `168h`), wired through the
  same config path M3's sampling interval uses.
- Sampling interval (5s, from M3) is reused as the write cadence — no new knob.
- History is automatic for every supervised instance; no per-app config.

## 8. Testing (TDD)

- `metricstore`: `Append`→`Query` round-trip; bucket aggregation math (avg/max, bucket
  boundaries); `Prune` age-out; empty / gap windows; reopen persistence (temp-file DB).
- Sparkline helper: pure table tests (flat, ramp, single point, all-equal, empty input).
- Daemon: `MetricsHistory` resolves the selector and returns buckets (in-memory/temp store
  wired to a synthetic sampler).
- Full gate before finishing: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .`.

## 9. Deferred / known issues

- Tiered rollups; stored status/restart/uptime series; log compression — later.
- **Label reuse across delete + re-add** of an app with the same name blends history. Accepted
  for v1 (PM2 behaves similarly); revisit if it bites.
- All M6 log items (deep backfill across rotated segments, retention-by-age, per-stream view,
  max-line cap in `logs.Sink`, backfill→subscribe race in `logs -f`).

## 10. Next step

Write the M5 implementation plan (`writing-plans`), then implement on a branch (TDD), gate
green, review, and merge `--no-ff` to `main` like M1–M4. After M5: **M6 — log history &
retention** (the deferred log items above).
