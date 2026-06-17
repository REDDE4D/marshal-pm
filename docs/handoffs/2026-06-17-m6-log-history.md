# Handoff — 2026-06-17 — M6 (log history & retention) complete

## TL;DR
Marshal **Milestone M6 (log history & retention)** — the second milestone of **sub-project #2
(metrics & log pipeline)** — is fully implemented, tested (race-clean), reviewed task-by-task
**and** with a final whole-branch review (one Important finding, **fixed and re-reviewed clean**),
and smoke-tested with the real binary. `marshal logs` now reads **deep history from the rotated
files on disk** (beyond the 1000-line in-memory ring, surviving daemon restarts), retains/compresses
old segments by age via lumberjack, supports **per-stream `--stdout`/`--stderr`**, and ships two
carried bug fixes. Built subagent-driven on branch **`m6-log-history`** (11 commits,
`179db3d`..`21d3df6`, **NOT yet merged** — finish with the finishing-a-development-branch flow:
no git remote, so a local `--no-ff` merge to `main`, like M1–M5). Next milestone begins
**sub-project #3 — central server / fleet aggregation**.

## Current state

- Branch: `m6-log-history`, cut from `main` at `48b2fb6` (the M6 plan commit). 11 commits. Working
  tree clean (the built `./marshal` binary is gitignored via `/marshal`).
- `main` holds the M6 **design** (`7bd6ac4`) and **plan** (`48b2fb6`) docs only.
- Full gate green: `gofmt -l .` lists nothing, `go vet ./...` ✓, `go build ./...` ✓,
  `go test ./... -race -count=1` ✓ (all packages).
- Commits on the branch:
  - `179db3d` feat(logs): add Policy with age/compress retention on sinks
  - `b1a4f86` feat(logs): select retention policy per app in registry
  - `97b1d49` fix(logs): cap newline-less partial line at 64 KiB
  - `cc6a0ae` feat(logs): deep on-disk backfill across rotated/gz segments
  - `68b608a` fix(logs): atomic ring snapshot + subscribe to close follow race
  - `1d500c3` feat(config): add per-app logs retention block
  - `26f8614` feat(proto): add LogStream selector + AppSpec.logs retention
  - `0cb6dee` feat(daemon): route log backfill by stream + file tier; atomic follow; per-app policy
  - `6b0f062` feat(cli): send per-app log retention; add --stdout/--stderr to logs
  - `43c5d2f` style(logs): gofmt comment alignment in sink consts
  - `21d3df6` fix(daemon): read disk files when ring is cold for merged backfill *(final-review fix)*

## What exists now (and works)

The daemon already captured each process's stdout/stderr to lumberjack-rotated files
(`<label>.out.log` / `.err.log`) plus an in-memory ring. M6 adds:

1. **Deep on-disk backfill.** `internal/logs/filetail.go` reads the last N lines of one stream
   newest→oldest across the active file + rotated `.log` + compressed `.log.gz` segments
   (transparently gunzipping), tolerating absent/concurrently-rotated files. `Sink.FileBackfill`
   exposes it.
2. **Retention + gzip.** `logs.Policy{MaxSizeMB,MaxBackups,MaxAgeDays,Compress}` is passed into the
   lumberjack loggers (`MaxAge`/`Compress`). Default is **10 MB × 5 backups, 14-day age, compress
   on** (was: 10 MB × 5, no age, no compress). Lumberjack does the age-delete + gzip itself — no
   extra prune goroutine.
3. **Per-app override.** `marshal.yaml` apps may carry a `logs:` block; it travels CLI→daemon via a
   new `AppSpec.logs` proto field and is registered into the registry (by app name) **before** the
   sink is lazily created, on both the `Run` startup path and the `Start` RPC path.
4. **Per-stream view.** `logs --stdout` / `--stderr` (mutually exclusive) read exactly one stream
   from disk.
5. **Bug fix — partial-line cap.** A newline-less line is force-flushed in ≤64 KiB chunks so memory
   stays bounded.
6. **Bug fix — follow race.** `Sink.SubscribeWithRing` snapshots the ring and registers the
   subscriber under one lock, so `logs -f` neither drops nor duplicates a line at the backfill→live
   boundary.

### Backfill routing (the heart of Approach A — see `internal/daemon/logs.go` `backfillLines`)
- **Per-stream** (`--stdout`/`--stderr`): always read from files — exact (a single stream is
  inherently chronological), deep, restart-durable.
- **Merged** (default): use the ring when it already satisfies N (exact timestamp interleave).
  Otherwise read the files for both streams and serve them **only if they hold more than the ring**
  (else keep the ring for exact ordering). `n == 0` ("all") always consults the files.
  Cross-stream / cross-instance order in the file path is **best-effort** — disk lines carry no
  timestamp (the accepted Approach-A limitation).

### Smoke proof (macOS host, this session)
```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m6smoke
./marshal start /tmp/m6app.yaml   # app prints 1500 out + 1500 err lines, then sleeps
./marshal kill                    # empties the in-memory ring
./marshal start /tmp/m6app.yaml   # cold ring after restart
./marshal logs noisy -n 1200      # 1200 lines served FROM DISK (ring is only 1000)
./marshal logs noisy -n 5 --stderr   # err-1496..err-1500 only
./marshal logs noisy -n 5 --stdout   # out-1496..out-1500 only
```

### Packages / files
New:
- `internal/logs/filetail.go` (+ `filetail_test.go`) — `fileBackfill`, `readSegmentLines` (gzip-aware).
- `internal/daemon/logpolicy.go` — `logPolicy(app, default)` applies non-nil per-app pointer overrides.
- `internal/daemon/convert_test.go`, `cmd/marshal/control_test.go`, `internal/config/config_test.go`.

Changed:
- `internal/logs/sink.go` — `Policy`, `DefaultPolicy`, policy-aware constructor (lumberjack
  `MaxAge`/`Compress`), 64 KiB partial-line cap, `FileBackfill`, `SubscribeWithRing`.
- `internal/logs/registry.go` — default + per-app policy map; `SetDefaultPolicy`/`SetPolicy`;
  policy selection in `For`.
- `internal/config/config.go` — `App.Logs *LogRetention` (pointer fields → correct default fallback).
- `internal/daemon/logs.go` — `streamMatch`, `backfillLines` (ring-vs-file routing), `trimTail`,
  `sortByTs`, rewritten `Logs` handler (stream filter + atomic follow via `SubscribeWithRing`).
- `internal/daemon/server.go` — `WithLogRetention` Option, `runOptions.logRetention`,
  `Server.logPolicyDefault`, policy push in `Run` + `Start`.
- `internal/daemon/convert.go` — read `AppSpec.logs` into `config.App.Logs`.
- `proto/marshal/v1/daemon.proto` (+ regenerated `internal/pb/daemon.pb.go`) — `LogStream` enum,
  `LogRequest.stream`, `LogRetention` message, `AppSpec.logs`.
- `cmd/marshal/control.go` — `appToSpec` sends retention; `--stdout`/`--stderr` flags + `streamFromFlags`.

## Key decisions / non-obvious things

- **Approach A (raw files, no per-line timestamps).** Files stay externally `tail`/`grep`-able;
  the cost is that merged history deeper than the live ring (or after a cold restart) can't perfectly
  interleave stdout/stderr — it's grouped by stream/instance, best-effort. Documented and accepted.
  Perfect interleave (timestamped log records) is deferred to the central-server work (sub-project #3).
- **Per-app override pointer fields.** Both `config.LogRetention` and the proto `LogRetention` use
  `optional`/pointer fields so an explicit `max_age_days: 0` (= "no age limit") and `compress: false`
  are distinguishable from "unset → use default". `logPolicy` applies only non-nil fields.
- **Per-app policy must be set before the sink is created** (sinks are created lazily and cached) —
  done before `mgr.Add` on both the `Run` and `Start` paths.
- **Final-review fix (`21d3df6`).** The plan's merged-routing gate used `anyRingSaturated`
  (ring-has-wrapped) to decide whether to read files. That was wrong for a **cold ring after a
  restart**: the ring isn't wrapped but disk still holds deep history, so `logs -n 1200` returned
  only the few live lines. Fixed to: use the ring only when it satisfies N; otherwise read files and
  prefer them when they hold more than the ring. Dead `anyRingSaturated`/`Sink.RingSaturated` removed.
  Regression test `TestMergedBackfillReadsFilesWhenRingCold`.

## Deferred / known issues (all non-blocking; from the final whole-branch review)

- **CRLF not stripped.** `readSegmentLines` strips only `\n`; a process emitting `\r\n` would leave a
  trailing `\r` on backfilled lines. Non-issue for `\n` output on macOS/Linux.
- **`SubscribeWithRing` closed-sink path untested** (structurally identical to the tested `Subscribe`).
- **`readSegmentLines` reads each segment fully into memory** (~10 MB at the current default rotation
  threshold, ×instances). Fine at defaults; revisit if `max_size_mb` is ever defaulted much higher.
- **Nested `TrimSuffix(TrimSuffix(...))` label recovery** in `Sink.FileBackfill` reads awkwardly;
  an `if stderr {…} else {…}` would be clearer (cosmetic).
- **Daemon tests that build `&Server{…}` directly** leave `logPolicyDefault` zero-valued; production
  always sets it via `Run`. Test-fidelity only.
- **`.gz` rotation not volume-triggered by the smoke** (1500 short lines ≈ 24 KB, far under 10 MB);
  the gzip read path is covered by `TestFileBackfillAcrossSegments`.
- **Live policy hot-reload on existing sinks** is out of scope: a changed per-app policy applies to
  sinks created after the change / next daemon restart, not retroactively.
- Carried from earlier milestones: deep-merge perfect cross-stream interleave (Approach B, → central
  server); `marshal logs all` blends every app's streams (documented label-blend caveat).

## How to build / run / test
```bash
go build -o marshal ./cmd/marshal
./marshal start app.yaml
./marshal logs <name|id> [-n N] [--stdout|--stderr] [-f]
go test ./... -race -count=1        # all green
go vet ./... && gofmt -l .          # clean
go generate ./internal/pb           # regenerate proto (protoc 35.0 on PATH)
```
Per-app retention in `marshal.yaml`:
```yaml
apps:
  - name: api
    cmd: ./api
    logs: { max_size_mb: 50, max_backups: 10, max_age_days: 30, compress: true }
```

## Architecture context (bigger picture)

Marshal → fleet manager, 4 sub-projects (spec
`docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`):
1. **Agent / supervisor core** — M1–M4 ✅ (sub-project #1 complete).
2. **Metrics & log pipeline** — M5 (metric history) ✅; **M6 (log history & retention) ✅ (this
   handoff)** → sub-project #2 complete.
3. **Central server / fleet aggregation ← next.** 4. Web dashboard.

M6 design: `docs/superpowers/specs/2026-06-17-marshal-agent-core-m6-log-history-design.md`;
plan: `docs/superpowers/plans/2026-06-17-marshal-agent-core-m6-log-history.md`.

## Next step

Merge `m6-log-history` to `main` (local `--no-ff`, via the finishing-a-development-branch flow).
Then **start sub-project #3 — the central server**: brainstorm scope from the fleet architecture
spec (agent↔server gRPC stream of metric batches + log records; server-side SQLite + log-file
storage; in-memory current-state). This is also the natural home for the deferred **timestamped
log records** (Approach B) that would give perfect cross-stream interleave in deep history.
