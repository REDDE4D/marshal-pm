# Handoff — 2026-06-17 — M5 (metric history) complete

## TL;DR
Marshal **Milestone M5 (metric history)** — the first milestone of **sub-project #2 (metrics &
log pipeline)** — is fully implemented, tested (race-clean), reviewed task-by-task **and** with
a final whole-branch review (verdict: *ready to merge*, no Critical/Important findings), and
smoke-tested with the real binary. Marshal now persists per-instance CPU%/RSS to a local
**pure-Go SQLite** DB and surfaces history via `marshal metrics <name>` (sparklines + min/avg/max)
and a last-hour sparkline in `describe`. Built subagent-driven on branch **`m5-metric-history`**
(8 commits, **NOT yet merged** — finish with the finishing-a-development-branch flow: no git
remote, so a local `--no-ff` merge to `main`, like M1–M4). Next milestone is **M6 — log history
& retention**.

## Current state

- Branch: `m5-metric-history`, cut from `main` at `3f045d0` (the M5 plan commit). 8 commits
  `9cc703d`..`e984ab7`. Working tree clean (the built `./marshal` binary is gitignored via `/marshal`).
- `main` holds the M5 **design** (`74de1b6`) and **plan** (`3f045d0`) docs only.
- Full gate green: `go build ./...` ✓, `go vet ./...` ✓, `gofmt -l .` lists nothing,
  `go test ./... -race -count=1` ✓ (all packages).
- Commits on the branch:
  - `9cc703d` feat(metricstore): pure-Go SQLite sample store with bucket queries + prune
  - `e180976` fix(metricstore): path-safe DSN, round MemAvg, check test errors
  - `16a2d99` feat(proto): add MetricsHistory RPC + messages
  - `39ceebb` feat(metrics): add per-tick sample callback (SetOnTick)
  - `ea5eb05` feat(daemon): persist samples, prune by age, serve MetricsHistory
  - `d5c5ef5` feat(cli): add sparkline + summarize rendering helpers
  - `5cd90eb` feat(cli): add 'marshal metrics' history command
  - `e984ab7` feat(cli): add last-hour sparkline to describe

## What exists now (and works)

The daemon samples per-instance CPU%/RSS every 5s (M3's `metrics.Sampler`) and now **also writes
each tick to SQLite**. A new `MetricsHistory` gRPC RPC serves server-side time-bucketed
aggregates; the CLI renders them. Retention prunes samples older than 7 days.

### Smoke proof (macOS host, this session)
```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m5smoke
./marshal start app.yaml        # a `tick` app echoing in a loop
sleep ~22s                      # several 5s sampler ticks
./marshal metrics tick --since 5m
#   tick — last 5m0s, 4 buckets
#   CPU  ▁█▇█  min 0.0%  avg 0.3%  max 0.4%
#   MEM  ▁▁▁▁  min 3.1MB avg 3.1MB max 3.1MB
./marshal describe tick         # proc table + "CPU (1h) ▁" / "MEM (1h) ▁"
ls $XDG_DATA_HOME/marshal/metrics.db   # created
./marshal kill
```

### Architecture (M5 additions)
```
  metrics.Sampler ──5s tick──▶ daemon.Run SetOnTick callback ──Append(now, []Sample)──▶ metricstore (SQLite: <stateDir>/metrics.db)
                                  │                                                            ▲
  marshal metrics ──gRPC────▶ marshald.MetricsHistory ──Query(label) per instance ─merge──────┘  (SQL bucket aggregation)
       describe   ──gRPC────────────┘                    (sums instances per bucket; max-of-maxes)
  daemon prune goroutine ──10min──▶ metricstore.Prune(now - retention)   (retention default 168h)
```

### Packages / files (new in M5)
- `internal/metricstore/` — **new leaf package** (imports only stdlib + `modernc.org/sqlite`):
  `store.go` (`Sample`, `Bucket`, `QueryReq`, `Open`/`Append`/`Query`/`Prune`/`Close`), `store_test.go`.
  Single table `samples(ts, label, cpu, mem)` + index `(label, ts)`; query-time bucketing via
  `(ts/bucket)*bucket` GROUP BY; `SetMaxOpenConns(1)` serializes access; `busy_timeout=5000` pragma.
- `internal/daemon/metrics.go` — `MetricsHistory` handler (resolves selector→labels via
  `mgr.Describe`, auto-picks bucket ≈ window/60 with a 1000ms floor, queries each label) +
  `mergeBuckets` (sum per-instance avgs per bucket, max-of-maxes, oldest-first). `metrics_test.go`.
- `cmd/marshal/sparkline.go` — pure `sparkline([]float64) string` (`▁▂▃▄▅▆▇█`, scaled to data
  min..max) + `summarize() (min,avg,max)`. `sparkline_test.go`.
- `cmd/marshal/metrics.go` — `metricsCmd()` + pure `printMetrics(io.Writer, ...)`. `metrics_test.go`.

### Changed packages / files
- `proto/marshal/v1/daemon.proto` — added `MetricsHistory` RPC + `MetricsHistoryRequest`
  (`selector`, `since_ms` = **lookback duration ms**, `bucket_ms`, 0 = auto), `MetricBucket`,
  `MetricsHistoryResponse`. Regenerated `internal/pb/daemon{,_grpc}.pb.go` via `go generate ./internal/pb`.
- `internal/metrics/sampler.go` — added optional `onTick func(map[string]Sample)` + `SetOnTick`,
  fired each tick **outside** the mutex (so a callback can't deadlock the sampler).
- `internal/daemon/server.go` — `Server.mdb *metricstore.Store`; `WithRetention(d)` Option
  (default `168h`); `Run` opens the store, wires `SetOnTick`→`Append`, starts the prune goroutine,
  closes `mdb` on shutdown.
- `internal/store/store.go` — added `MetricsDBPath()` → `<base>/metrics.db`.
- `cmd/marshal/main.go` — registered `metricsCmd()`.
- `cmd/marshal/control.go` — `describeCmd()` now also fetches last-hour history (best-effort) and
  prints `CPU (1h)` / `MEM (1h)` sparklines below the table.
- `go.mod`/`go.sum` — added `modernc.org/sqlite` (pure Go; the only new dependency).

## Key decisions / non-obvious things

- **Pure-Go SQLite (`modernc.org/sqlite`), never cgo.** Preserves the single static binary and
  clean cross-compile. Driver registers as `"sqlite"`; opened with a **raw path** (not a `file:`
  URI) plus an explicit `PRAGMA busy_timeout=5000`.
- **Raw 5s samples + age-out + query-time bucketing** (no tiered rollups — deferred until a real
  host proves they're needed). Bucketing is `(ts/bucket)*bucket` floored, aggregated in SQL.
- **Downtime = gaps, not zeros.** The sampler only emits rows for *online* instances, so an offline
  instance simply produces no rows → missing buckets, rendered as gaps. No status column.
- **`since_ms` is a lookback *duration*** (ms), not an absolute timestamp; the server computes
  `now - since_ms`. Single host, so no clock-sync concern.
- **Multi-instance apps:** the handler queries each instance label and `mergeBuckets` **sums** the
  per-instance averages per bucket (whole-app total) and takes the max of maxes.
- **`SetOnTick` callback fires outside the sampler mutex** — confirmed by review; prevents deadlock
  if a callback re-enters the sampler.

## Deferred / known issues (all non-blocking; from the final whole-branch review)

- **Shutdown does not await the sampler/prune goroutines before `mdb.Close()`.** Benign:
  `database/sql` is goroutine-safe; a late `Append`/`Prune` after close returns `sql.ErrConnDone`,
  which is swallowed. No panic/corruption. Could add a done-channel join if clean shutdown matters.
- **`TestMetricsHistoryReturnsBuckets` asserts only `len>0`,** not bucket values (those are covered
  by `mergeBuckets` and `metricstore` aggregation tests). Strengthen if revisited.
- **Memory averages pass through `float64`,** truncating above 2^53 bytes (~8 PB) — never reachable.
- **DSN not escaped for a literal `?` in the path.** `stateDir` is always XDG/home-derived and
  never contains `?`, so theoretical only. (The `e180976` "path-safe DSN" message slightly overstates;
  the change — raw path + explicit pragma — is a real improvement over the prior always-appended query.)
- **`marshal metrics all` / `describe all`** would blend every app's series into one sparkline
  (`all` resolves to all instances). Spec scopes these to `<name|id>`; an extension of the documented
  label-blend caveat. Accept for v1 or reject `all` in those two commands.
- Carried from earlier milestones (now **M6**): deep log backfill across rotated segments,
  retention-by-age + compression, per-stream `logs` view, max-line cap in `logs.Sink`,
  backfill→subscribe race in `logs -f`.

## How to build / run / test
```bash
go build -o marshal ./cmd/marshal
./marshal start app.yaml
./marshal metrics <name|id> [--since 6h] [--bucket 1m] [--cpu|--mem]
./marshal describe <name|id>        # table + last-hour sparkline
go test ./... -race -count=1        # all green
go vet ./... && gofmt -l .          # clean
go generate ./internal/pb           # regenerate proto (needs protoc on PATH)
```

## Architecture context (bigger picture)

Marshal → fleet manager, 4 sub-projects (spec
`docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`):
1. **Agent / supervisor core** — M1–M4 ✅ (sub-project #1 complete).
2. **Metrics & log pipeline** — **M5 ✅ (this handoff)**; **M6 (log history & retention) ← next**.
3. Central server / fleet aggregation. 4. Web dashboard.

M5 design: `docs/superpowers/specs/2026-06-17-marshal-agent-core-m5-metric-history-design.md`;
plan: `docs/superpowers/plans/2026-06-17-marshal-agent-core-m5-metric-history.md`.

## Next step

Merge `m5-metric-history` to `main` (local `--no-ff`, via the finishing-a-development-branch flow).
Then **start M6 — log history & retention**: brainstorm scope from the M5 spec's deferred list
(deep cross-segment backfill, retention-by-age + compression, per-stream `logs` view) and fold in
the two carried log fixes (max-line cap in `logs.Sink`; backfill→subscribe race in `logs -f`);
write its design + plan.
