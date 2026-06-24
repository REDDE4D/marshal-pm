# M-E · Restart history (rollups) — Handoff

**Date:** 2026-06-24
**Branch:** `mE-restart-history` (off `dev` @ `646c975`). Reviewed + live-demo-verified;
**ready to merge → `dev` (`--no-ff`)**. `main` unchanged (still v0.2.0).

To resume: read this file. Spec: `docs/superpowers/specs/2026-06-24-mE-restart-history-design.md`;
plan: `docs/superpowers/plans/2026-06-24-mE-restart-history.md`; program roadmap:
`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`; SDD ledger (git-ignored):
`.superpowers/sdd/progress.md`.

## What M-E does

Per-process **restart rollups** on each process row, from real restart events:

| Field               | Meaning                                                        |
|---------------------|---------------------------------------------------------------|
| `restarts_24h`      | count of restart events in the trailing 24h                   |
| `last_restart_unix` | unix seconds of the most recent retained restart (0 = none)   |

Point-in-time, like M-D/M-C. The supervisor emits a real-timestamped restart event via an
injected hook on each genuine restart; a new agent-side SQLite **event store** records them
(7-day retention); the daemon computes the rollups per snapshot and ships them on `ProcInfo`.
Velocity is UI-derived. **No** event shipping to the server, **no** server store, **no**
`/api/restarts` ledger endpoint (deferred).

## What changed (8 commits, `a56d31e..cc724ec`; plus spec c2c474b + plan a56d31e)

- `internal/eventstore/store.go` (new) — SQLite `(ts, label)`; `Record`, `Rollups(sinceMs)`
  (one grouped query → per-label `{Count24h, LastMs}`), `Prune`. Mirrors metricstore. (e433bba)
- `internal/supervisor/instance.go` — `Option`/`WithOnRestart`, variadic `NewInstance`; hook
  fires once per genuine restart (after `restarts++`, outside the lock, restart-path only). (4f4d851)
- `internal/manager/manager.go` — `RestartSink` interface + `WithRestartSink`; `startInstance`
  wires each instance's restart (with its `name#idx` label) to the sink. (583b7dd)
- `proto/marshal/v1/daemon.proto` — `ProcInfo.restarts24h = 17`, `last_restart_unix = 18`
  (field spelled `restarts24h` → clean `GetRestarts24H`; JSON tag `restarts_24h`); `internal/pb`
  regenerated. (4d87c4e)
- `internal/store/store.go` + `internal/daemon/{server,convert,fleet}.go` — `RestartsDBPath()`;
  open the event store before `manager.New`, wire `WithRestartSink`, prune to 7 days, and merge
  a single per-snapshot `Rollups(now-24h)` into each `ProcInfo` (millis→seconds). (c43c31e)
- `internal/dashboard/fleet.go` — `procView.Restarts24h`/`LastRestartUnix` (`restarts_24h`,
  `last_restart_unix,omitempty`) mapped from the getters. (43f8fc8)
- `web/src/api.ts` + `web/src/ProcessCard.tsx` — `Proc` fields + `ago()` + `restartStats`
  (`<n>/24h · last <relative>`), online-gated; bundle rebuilt. (9901531)
- `CHANGELOG.md` `[Unreleased]` + a gate-caught gofmt realign of `manager.go`. (cc724ec)

## Quality gates

- TDD throughout (subagent-driven: fresh implementer + spec/quality reviewer per task). All
  per-task reviews clean. Final whole-branch review (opus): **READY TO MERGE**, no
  Critical/Important.
- `go test ./... -race -count=1` green (incl. `eventstore`); plain suite ALL PASS; `go vet`
  clean; `gofmt -l .` empty; `make build` ok (`v0.1.0-96`).
- **Live demo PASS:** real agent with a crashing `flapper` app; `/api/fleet` showed
  `restarts_24h` climbing (30 → 49) and `last_restart_unix` recent ("~2s ago"). In-browser the
  card rendered `49/24h · last 2s ago` alongside the M-D/M-C surfaces. Scratch torn down by data
  dir; standing launchd daemon (PID 899) preserved; no orphans.

## Deferred / notes (not bugs)

- **Open follow-up (spun off as a background task):** daemon startup error paths leak the
  metric/restart SQLite handles (e.g. if `net.Listen`/`metricstore.Open` fails mid-`Run`,
  `estore`/`mdb` aren't closed before the error return). Pre-existing `mdb` pattern; M-E adds
  `estore` to it. Fix = `defer estore.Close()`/`mdb.Close()` after each Open (and drop the
  explicit happy-path closes). Low impact (rare startup failures, process exiting). Touches
  shutdown ordering — read the full `Run` defers before editing.
- **Minor (left as-is):** the manager restart hook `_ = sink.Record(...)` swallows a failed
  SQLite write silently → a degraded store would under-count rollups invisibly. Optional debug
  log; deferred to avoid hot-path noise.
- **Retention vs last-restart:** a process that last restarted >7 days ago shows
  `last_restart_unix = 0` (pruned). The lifetime `restarts` counter (existing) is unaffected.
- **Daemon restart:** the event store persists on disk, so the 24h window survives a daemon
  restart; the in-memory cumulative `restarts` counter still resets as before (out of scope).
- UI is **minimal transitional** surfacing. **M-A** delivers the real treatment.
- Recommendation: the `24h` window constant is duplicated in `convert.go`/`fleet.go` and `7d`
  is inline in `server.go` — a shared const would prevent drift (cosmetic).

## Next step

Merge `mE-restart-history` → `dev` (`--no-ff`), delete the branch. Remaining roadmap milestones:
**M-G** (control additions: graceful reload, per-agent restart-all, log download — small),
**M-F** (errors/exceptions subsystem — largest; reuses this event-store pattern), then **M-A**
(the full redesign, backed by all the B–F data). Recommend **M-G** next (small, unblocks the
mocked control buttons), then **M-F**, then **M-A**.

## Build / run / test

```bash
make proto   # regenerate internal/pb from proto/marshal/v1
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```
