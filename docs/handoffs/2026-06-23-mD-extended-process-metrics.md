# M-D · Extended per-process metrics — Handoff

**Date:** 2026-06-23
**Branch:** `mD-extended-process-metrics` (off `dev` @ `b564252`). Reviewed + live-demo-verified;
**ready to merge → `dev` (`--no-ff`)**. `main` unchanged (still v0.2.0).

To resume: read this file. Spec: `docs/superpowers/specs/2026-06-23-mD-extended-process-metrics-design.md`;
plan: `docs/superpowers/plans/2026-06-23-mD-extended-process-metrics.md`; program roadmap:
`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`; SDD ledger (git-ignored):
`.superpowers/sdd/progress.md`.

## What M-D does

Adds four **point-in-time** fields to every process row and threads them source → wire → dashboard:

| Field         | Source                          | Notes                                                    |
|---------------|---------------------------------|----------------------------------------------------------|
| `threads`     | gopsutil `NumThreads`           | group-summed like cpu/mem; available on darwin + linux   |
| `open_fds`    | gopsutil `NumFDs`               | group-summed; **`-1` = unavailable** (darwin) → UI `—`   |
| `exit_code`   | supervisor `p.Wait()`           | `-1` when signaled or spawn failure                      |
| `exit_reason` | supervisor `p.Wait()`           | `"exit status 1"`, `"signal: killed"`, …; `""`=never exited |

Two sources converge on `ProcInfo` (reused by `AgentState.procs`, so it flows to both the CLI
and the fleet path). Threads/FDs are sampler gauges; exit code/reason are captured from the
supervisor on **every** exit path (natural, spawn-failure, and operator-stop), **persist across
restarts**, and blank until the first exit. Point-in-time only — no time-series, no new endpoints.

## What changed (9 commits, `991a2e8..5e6f74f`; plus spec 8791b45 + plan 991a2e8)

- `internal/metrics/sampler.go` — `Sample` gains `Threads`/`Fds`; `sample()` sums them per group;
  `Fds=-1` when no pid yields an FD count (darwin sentinel). (commit 9a39b13)
- `internal/supervisor/instance.go` — `Snapshot` gains `ExitCode`/`ExitReason`; `deriveExit` +
  `recordExit`; `handleExit` takes `error`; `stop()` records the operator-stop exit. (1c0c841)
- `proto/marshal/v1/daemon.proto` — `ProcInfo` fields 13–16; `internal/pb` regenerated via
  `make proto`. (35f6133)
- `internal/daemon/convert.go` + `fleet.go` — `snapshotToProc(s, sm metrics.Sample)`; both callers
  (`procList`, `fleetSnapshot`) default `Fds:-1` before the sample lookup. (5885f94)
- `internal/dashboard/fleet.go` — `procView` JSON gains the four fields. (d7307dc)
- `web/src/api.ts` + `web/src/ProcessCard.tsx` — `Proc` type + minimal render (`N thr · — fds`
  when online; `last exit: …` line); embedded bundle rebuilt (`make ui`). (9abb7a5)
- `CHANGELOG.md` `[Unreleased]` + two gate-surfaced cleanups (stale `fleetSnapshot` comment;
  gofmt). (f7c0393)
- Test: assert operator-stop records an exit reason (closes the only untested exit path). (5e6f74f)

## Quality gates

- TDD throughout (subagent-driven: fresh implementer + spec/quality reviewer per task). Per-task
  reviews all clean. Final whole-branch review (opus): **READY TO MERGE**, no Critical/Important.
- `go test ./... -race -count=1` green (24 pkgs); `go vet` clean; `gofmt -l .` empty; `make build`
  ok (`v0.1.0-73-g5e6f74f`).
- **Live demo PASS:** real agent enrolled to a real server (scratch `/tmp/marshal-mD-demo`, ports
  :9000/:9001). `/api/fleet` returned real data — api (online): `threads=1, open_fds=-1,
  exit_code=0, exit_reason absent`; crasher (errored, exit 7): `exit_code=7, exit_reason="exit
  status 7"`. In-browser (Playwright): api card `… · 1 thr · — fds`, crasher card `last exit: exit
  status 7`. Scratch torn down by data dir; standing launchd daemon (PID 899) preserved; no orphans.

## Deferred / notes (not bugs)

- **macOS FDs unavailable** → `-1` → UI `—`. Real on Linux agents. Expected, by design.
- **Operator stop shows `-1 · signal: terminated`** (a SIGTERM is an exit like any other, per the
  "every exit, persists" decision). Flagged for **M-A**: the redesign should distinguish a clean
  operator stop from a genuine crash so the `-1`/signal reason isn't styled as an error.
- Minor (no fix): the "never exited" test asserts the zero-value default, slightly less than its
  name implies; behavior is correct by construction.
- UI is **minimal transitional** surfacing (same posture as M-B). **M-A** delivers the real visual
  treatment of these fields.

## Next step

Merge `mD-extended-process-metrics` → `dev` (`--no-ff`), delete the branch. Per the roadmap the next
milestone is **M-C (host system metrics: host CPU%/load, total/free memory, network I/O)** —
design → plan → subagent-driven execution, same as M-B/M-D. (M-E restart history and M-F errors
subsystem also remain; M-A redesign ships last.)

## Build / run / test

```bash
make proto   # regenerate internal/pb from proto/marshal/v1
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```
