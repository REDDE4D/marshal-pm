# M-D · Extended per-process metrics — design spec

**Date:** 2026-06-23
**Milestone:** M-D (second data milestone of the dashboard program; see
`2026-06-23-dashboard-program-roadmap.md`). Small, additive.
**Branch (to create):** `mD-extended-process-metrics` off `dev`.

## Goal

Add four **point-in-time** fields to every process row and thread them from source → wire
→ dashboard:

| Field         | Type     | Source                         | Notes                                          |
|---------------|----------|--------------------------------|------------------------------------------------|
| `threads`     | `int32`  | gopsutil `NumThreads`          | group-summed; available on darwin + linux      |
| `open_fds`    | `int32`  | gopsutil `NumFDs`              | group-summed; **`-1` = unavailable** (darwin)  |
| `exit_code`   | `int32`  | supervisor `p.Wait()`          | `-1` when signaled or spawn failure            |
| `exit_reason` | `string` | supervisor `p.Wait()`          | `"exit status 1"`, `"signal: killed"`, …       |

No time-series, no charts, no new endpoints. These are current-value gauges plus a
last-exit record shown on the existing per-process surface.

## Decisions (locked in brainstorming)

1. **All four fields** — threads, open FDs, last exit code, **and** a human-readable exit
   reason alongside the numeric code.
2. **Point-in-time only.** Threads/FDs are NOT stored as time-series (no metricstore
   columns, no charts/sparklines). They live on `ProcInfo` only, exactly where cpu/mem
   already appear as current values. The mockup shows them as current values.
3. **Group-summed.** Threads and FDs are summed across the supervised process group, the
   same way cpu/mem are (`groupPids`).
4. **macOS FD gap is explicit.** gopsutil's `NumFDs` returns `ErrNotImplementedError` on
   darwin. When **no** pid in a group yields an FD count, report `open_fds = -1`
   ("unavailable") rather than a misleading `0`. The UI renders `—` for `< 0`. (Honors the
   program's "show only real data" rule.)
5. **Exit: record every exit, persists.** The most recent exit of any kind — clean `0`,
   crash, signal, or operator-initiated stop — is recorded. It **persists across
   restarts**: a process that crashed, restarted, and is now `Online` still shows its last
   exit. `exit_reason == ""` means *never exited yet* → UI shows `—`. No separate
   `has_exited` flag.

## Data path

Both sources converge on `ProcInfo` (the per-process wire message), which already powers
CLI `marshal list` and the dashboard `/api/fleet` process rows.

```
gopsutil ──► metrics.Sampler.Sample{Threads,Fds} ─┐
                                                   ├─► ProcInfo ─► /api/fleet ─► SPA
supervisor.p.Wait() ─► Snapshot{ExitCode,Reason} ─┘            └─► CLI marshal list
```

## Component changes

### 1. Sampler — `internal/metrics/sampler.go`

`Sample` gains:

```go
type Sample struct {
    Cpu     float64
    Mem     uint64
    Threads int32 // summed over the process group
    Fds     int32 // summed over the group; -1 when unavailable (e.g. darwin)
}
```

In `sample()`'s per-pid group loop, alongside the existing `Percent`/`MemoryInfo`:

- `p.NumThreads()` → add to a running `threads` sum on success.
- `p.NumFDs()` → add to a running `fds` sum on success, and set a group-local
  `fdsOK = true`. After the group loop, `result[label].Fds = fds` if `fdsOK` else `-1`.

Threads default to `0` (always available in practice). FDs default to `-1` so a group with
no readable FD counts is reported as unavailable, not zero.

### 2. Supervisor — `internal/supervisor/instance.go`

- `Snapshot` gains `ExitCode int32` and `ExitReason string`.
  - `Snapshot()` returns `i.exitCode`, `i.exitReason` under the existing mutex.
- New instance fields `i.exitCode int32`, `i.exitReason string` (default zero / `""`).
- `handleExit` signature changes from `(ctx, started, failed bool)` to
  `(ctx, started, waitErr error)`. Callers:
  - spawn-failure path passes the `proc.Start` error;
  - the `p.Wait()` path passes `waitErr` directly.
  - `failed` is now derived inside as `waitErr != nil`.
- Before deciding restart, `handleExit` records the exit under the mutex via a helper
  `deriveExit(waitErr) (int32, string)`:
  - `nil` → `(0, "exit status 0")`.
  - `*exec.ExitError ee` → `(int32(ee.ExitCode()), ee.String())` — `ExitCode()` is `-1`
    when the process was signaled; `String()` yields `"exit status N"` or
    `"signal: killed"`.
  - any other error → `(-1, err.Error())` (spawn failure, etc.).
- The recorded values persist; they are overwritten only by the next exit. No reset on
  (re)start.

### 3. Proto — `proto/marshal/v1/daemon.proto`

`ProcInfo` (currently fields 1–12) gains:

```proto
int32  threads     = 13;
int32  open_fds    = 14; // -1 = unavailable on this platform
int32  exit_code   = 15;
string exit_reason = 16; // "" = never exited
```

Regenerate `internal/pb` with `make proto`.

### 4. Plumbing

- `manager.InstanceSnapshot` embeds `supervisor.Snapshot` → exit fields flow with no
  change to the manager.
- `snapshotToProc` (`internal/daemon/convert.go`) gains `threads, fds int32` parameters
  and sets all four `ProcInfo` fields (exit from the snapshot).
- `fleetSnapshot` (`internal/daemon/fleet.go`) and `procList` (`internal/daemon/convert.go`)
  read `sample.Threads`/`sample.Fds` from `s.metrics.Get(label)` and pass them through.
  When no sample exists yet, threads `0` / fds `-1`.
- Dashboard `/api/fleet` (`internal/dashboard/fleet.go`): the per-process JSON view gains
  `threads`, `openFds`, `exitCode`, `exitReason`.
- Web (`web/src/api.ts` + the process-row component): extend the proc type; render
  threads, FDs (`—` when `< 0`), and `exit_code · exit_reason` (`—` when reason empty).
  **Minimal transitional surfacing**, same posture as M-B — M-A delivers the real visual
  treatment. Rebuild the embedded bundle with `make ui` and commit it.

## Testing (TDD per layer)

- **Sampler:** threads and FDs summed across a multi-pid group; a group whose FD reads all
  fail reports `Fds == -1`; a successful read reports the sum.
- **Supervisor:** `deriveExit` maps nil → `(0, "exit status 0")`, an `*exec.ExitError` →
  the right code/reason, and a generic error → `(-1, msg)`. `Snapshot` reflects the last
  exit; the value persists across a restart; `ExitReason == ""` before the first exit.
- **convert:** `snapshotToProc` populates all four fields.
- **dashboard:** `/api/fleet` JSON carries the four fields with correct values.

## Edge cases / non-goals

- **macOS FDs:** unavailable → `-1` → UI `—`. The live demo on darwin will show real
  threads and real exit code/reason, with FDs as `—`; FDs are real on Linux agents.
- **Operator stop:** the signal we send (e.g. SIGTERM) is recorded as an exit like any
  other (`"signal: terminated"`). That is intentional — "every exit, persists".
- **Non-goals:** no time-series storage or charts for threads/FDs; no historical exit log
  or restart-event store (that is **M-E**); no new API endpoints; no changes to the
  metricstore schema or `MetricSample`.

## Next step

Write the implementation plan (writing-plans), then build on branch
`mD-extended-process-metrics` off `dev`, TDD per layer, with a `CHANGELOG.md`
`[Unreleased]` entry, handoff, and a live demo per project conventions.
