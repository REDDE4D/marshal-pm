# M-D Extended Per-Process Metrics — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add four point-in-time fields to every process row — threads, open FDs, last exit code, last exit reason — and thread them from source to the dashboard.

**Architecture:** Two sources converge on the existing `ProcInfo` wire message (which already flows agent → server registry → `/api/fleet` → SPA, and daemon → CLI). Threads/FDs are gopsutil gauges added to `metrics.Sampler`, group-summed like cpu/mem. Exit code/reason are captured from the supervisor's `p.Wait()` and persisted on the instance. No time-series, no new endpoints, no registry/fleet-proto changes (ProcInfo is reused by `AgentState.procs`).

**Tech Stack:** Go 1.26, gopsutil/v3, protobuf (`make proto`), React/TypeScript SPA (`make ui`), SQLite (unaffected here).

## Global Constraints

- **TDD:** failing test first, then implementation. `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (empty) before finishing.
- **Group-summed gauges:** threads/FDs summed across the process group via `groupPids`, exactly like cpu/mem.
- **FD sentinel:** `open_fds = -1` means *unavailable on this platform* (gopsutil `NumFDs` returns `ErrNotImplementedError` on darwin). Never report a misleading `0` for a live process. UI renders `—` when `< 0`.
- **Exit semantics:** record every exit of any kind (clean `0`, crash, signal, operator stop); persists across restarts; `exit_reason == ""` means *never exited yet*.
- **Proto field numbers are additive:** `ProcInfo` continues at 13–16. Regenerate `internal/pb` with `make proto` (never hand-edit `*.pb.go`).
- **Changelog:** add an `[Unreleased]` entry as part of the work.
- **Commit trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Branch:** `mD-extended-process-metrics` (already created off `dev`; the design spec is committed at `8791b45`).

---

## File Structure

- `internal/metrics/sampler.go` — `Sample` gains `Threads`/`Fds`; `sample()` collects them (Task 1).
- `internal/supervisor/instance.go` — `Snapshot` gains exit fields; `deriveExit`/`recordExit`; `handleExit` takes `error`; `stop()` records (Task 2).
- `proto/marshal/v1/daemon.proto` + `internal/pb/*` — `ProcInfo` fields 13–16 (Task 3).
- `internal/daemon/convert.go` + `internal/daemon/fleet.go` — `snapshotToProc` takes a `metrics.Sample`; callers populate the four fields (Task 4).
- `internal/dashboard/fleet.go` — `procView` JSON gains the four fields (Task 5).
- `web/src/api.ts` + `web/src/ProcessCard.tsx` + embedded bundle — type + minimal render (Task 6).
- `CHANGELOG.md` + whole-branch verification (Task 7).

---

### Task 1: Sampler — threads & open-FD gauges

**Files:**
- Modify: `internal/metrics/sampler.go` (`Sample` struct ~13-17; `sample()` group loop ~87-101)
- Test: `internal/metrics/sampler_test.go`

**Interfaces:**
- Produces: `metrics.Sample{Cpu float64; Mem uint64; Threads int32; Fds int32}`. `Threads` is the group sum (≥0). `Fds` is the group sum, or `-1` when no pid in the group yields an FD count (e.g. darwin).

- [ ] **Step 1: Write the failing test**

Add to `internal/metrics/sampler_test.go`:

```go
func TestSamplerRecordsThreadsAndFds(t *testing.T) {
	pid, stop := startGroup(t)
	defer stop()
	s := NewSampler(time.Hour)
	s.sample([]Instance{{Label: "a#0", Pid: pid, Online: true}})

	got, ok := s.Get("a#0")
	if !ok {
		t.Fatal("no sample for a#0")
	}
	if got.Threads < 1 {
		t.Fatalf("Threads = %d, want >= 1", got.Threads)
	}
	// Fds is -1 (unavailable, e.g. darwin) or a real positive count — never a
	// misleading 0 for a live process group.
	if got.Fds == 0 {
		t.Fatalf("Fds = 0, want -1 (unavailable) or > 0")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/metrics/ -run TestSamplerRecordsThreadsAndFds -v`
Expected: FAIL — `got.Threads` / `got.Fds` undefined (compile error).

- [ ] **Step 3: Add the fields to `Sample`**

In `internal/metrics/sampler.go`, replace the `Sample` struct:

```go
// Sample is one instance's latest reading.
type Sample struct {
	Cpu     float64 // percent, summed over the process group
	Mem     uint64  // RSS bytes, summed over the process group
	Threads int32   // thread count, summed over the process group
	Fds     int32   // open FD count, summed over the group; -1 if unavailable (e.g. darwin)
}
```

- [ ] **Step 4: Collect threads/FDs in `sample()`**

In `internal/metrics/sampler.go`, replace the per-instance group loop (the block from `var sum Sample` through `result[in.Label] = sum`) with:

```go
		var sum Sample
		fdsOK := false
		for _, pid := range groupPids(int32(in.Pid)) {
			live[pid] = true
			p := s.handle(pid)
			if p == nil {
				continue
			}
			if c, err := p.Percent(0); err == nil {
				sum.Cpu += c
			}
			if m, err := p.MemoryInfo(); err == nil && m != nil {
				sum.Mem += m.RSS
			}
			if t, err := p.NumThreads(); err == nil {
				sum.Threads += t
			}
			if fd, err := p.NumFDs(); err == nil {
				sum.Fds += fd
				fdsOK = true
			}
		}
		if !fdsOK {
			sum.Fds = -1 // unavailable on this platform (gopsutil NumFDs unsupported)
		}
		result[in.Label] = sum
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/metrics/ -race -count=1`
Expected: PASS (all sampler tests, including the new one).

- [ ] **Step 6: Commit**

```bash
git add internal/metrics/sampler.go internal/metrics/sampler_test.go
git commit -m "feat(metrics): sample per-group thread and open-FD counts

Threads always available; FDs summed when readable, -1 when the
platform (e.g. darwin) does not support NumFDs.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Supervisor — capture last exit code & reason

**Files:**
- Modify: `internal/supervisor/instance.go` (`Snapshot` ~22-28; `Instance` fields ~30-41; `Snapshot()` ~48-53; `Run` callers ~76-97; `handleExit` ~102-103; `stop` ~160-168)
- Test: `internal/supervisor/instance_test.go`

**Interfaces:**
- Consumes: `proc.Process.Wait() error` (returns `*exec.ExitError` on non-zero/signal exit).
- Produces: `supervisor.Snapshot` gains `ExitCode int32` and `ExitReason string`. `ExitReason == ""` ⇒ never exited. `ExitCode == -1` ⇒ signaled or spawn failure. Helper `deriveExit(error) (int32, string)`.

- [ ] **Step 1: Write the failing test (`deriveExit` mapping)**

Add to `internal/supervisor/instance_test.go` (add imports `"errors"` and `"os/exec"` to the file's import block):

```go
func TestDeriveExit(t *testing.T) {
	if c, r := deriveExit(nil); c != 0 || r != "exit status 0" {
		t.Fatalf("deriveExit(nil) = (%d, %q), want (0, \"exit status 0\")", c, r)
	}
	// Real non-zero exit yields *exec.ExitError.
	err := exec.Command("sh", "-c", "exit 3").Run()
	if c, r := deriveExit(err); c != 3 || r == "" {
		t.Fatalf("deriveExit(exit 3) = (%d, %q), want (3, non-empty)", c, r)
	}
	// Generic (non-ExitError) error, e.g. spawn failure.
	if c, r := deriveExit(errors.New("boom")); c != -1 || r != "boom" {
		t.Fatalf("deriveExit(boom) = (%d, %q), want (-1, \"boom\")", c, r)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/supervisor/ -run TestDeriveExit -v`
Expected: FAIL — `deriveExit` undefined.

- [ ] **Step 3: Add `deriveExit` + `recordExit` and instance fields**

In `internal/supervisor/instance.go`, add `"errors"` and `"os/exec"` to the import block. Add the exit fields to `Snapshot`:

```go
// Snapshot is a point-in-time view of an instance.
type Snapshot struct {
	State      State
	Pid        int
	Restarts   int
	StartedAt  time.Time
	ExitCode   int32  // last exit code; -1 if signaled or spawn failure
	ExitReason string // e.g. "exit status 1" / "signal: killed"; "" = never exited
}
```

Add the backing fields to `Instance` (after `startedAt time.Time`):

```go
	startedAt time.Time
	exitCode   int32  // last observed exit code
	exitReason string // last observed exit reason ("" until first exit)
```

Update `Snapshot()` to return them:

```go
func (i *Instance) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	return Snapshot{
		State: i.state, Pid: i.pid, Restarts: i.restarts, StartedAt: i.startedAt,
		ExitCode: i.exitCode, ExitReason: i.exitReason,
	}
}
```

Add the helpers (place them near `handleExit`):

```go
// deriveExit maps a Wait/Start error to a numeric code and human reason.
// nil -> clean exit 0; *exec.ExitError -> its code (-1 if signaled) and string;
// any other error (e.g. spawn failure) -> -1 and the error text.
func deriveExit(waitErr error) (int32, string) {
	if waitErr == nil {
		return 0, "exit status 0"
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return int32(ee.ExitCode()), ee.String()
	}
	return -1, waitErr.Error()
}

// recordExit stores the most recent exit under the lock. Persists across
// restarts; overwritten only by the next exit.
func (i *Instance) recordExit(waitErr error) {
	code, reason := deriveExit(waitErr)
	i.mu.Lock()
	i.exitCode = code
	i.exitReason = reason
	i.mu.Unlock()
}
```

- [ ] **Step 4: Run the mapping test to verify it passes**

Run: `go test ./internal/supervisor/ -run TestDeriveExit -v`
Expected: PASS.

- [ ] **Step 5: Write the failing integration test (Snapshot reflects last exit)**

Add to `internal/supervisor/instance_test.go`:

```go
func TestInstanceRecordsExitCode(t *testing.T) {
	// Never exited yet -> blank reason.
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 7"}}, testPolicy(config.RestartNo))
	if got := i.Snapshot().ExitReason; got != "" {
		t.Fatalf("ExitReason before run = %q, want empty", got)
	}
	_, wait := runInstance(i)
	wait() // RestartNo + failure -> instance stops itself (errored)
	snap := i.Snapshot()
	if snap.ExitCode != 7 || snap.ExitReason != "exit status 7" {
		t.Fatalf("after exit 7: code=%d reason=%q, want 7 / \"exit status 7\"", snap.ExitCode, snap.ExitReason)
	}
}

func TestInstanceRecordsCleanExit(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 0"}}, testPolicy(config.RestartNo))
	_, wait := runInstance(i)
	wait()
	snap := i.Snapshot()
	if snap.ExitCode != 0 || snap.ExitReason != "exit status 0" {
		t.Fatalf("after clean exit: code=%d reason=%q, want 0 / \"exit status 0\"", snap.ExitCode, snap.ExitReason)
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/supervisor/ -run 'TestInstanceRecordsExitCode|TestInstanceRecordsCleanExit' -v`
Expected: FAIL — exit is never recorded yet (reason stays `""`).

- [ ] **Step 7: Thread `waitErr` into `handleExit` and record on every exit path**

In `internal/supervisor/instance.go`, change the two `handleExit` call sites in `Run`:

The spawn-failure branch:

```go
		p, err := proc.Start(i.spec)
		if err != nil {
			// Spawn failure: treat like an immediate crash for restart accounting.
			if !i.handleExit(ctx, started, err) {
				return
			}
			continue
		}
```

The natural-exit branch:

```go
		case waitErr := <-exited:
			if !i.handleExit(ctx, started, waitErr) {
				return
			}
		}
```

Change `handleExit`'s signature and derive `failed` internally, recording first:

```go
// handleExit runs after a process terminates (or fails to spawn) and decides whether to restart. It returns false to terminate Run.
func (i *Instance) handleExit(ctx context.Context, started time.Time, waitErr error) bool {
	i.recordExit(waitErr)
	failed := waitErr != nil
	if ctx.Err() != nil {
		i.set(StateStopped, 0, time.Time{})
		return false
	}
```

(The remainder of `handleExit` — the `switch i.policy.Mode`, stability accounting, etc. — is unchanged; it already uses the local `failed`.)

Record operator-stop exits in `stop()` (the `ctx.Done` path does not call `handleExit`):

```go
func (i *Instance) stop(p *proc.Process, exited <-chan error) {
	i.set(StateStopping, 0, time.Time{})
	_ = p.Signal(syscall.SIGTERM)
	select {
	case waitErr := <-exited:
		i.recordExit(waitErr)
	case <-time.After(i.policy.KillTimeout):
		_ = p.Kill()
		i.recordExit(<-exited)
	}
}
```

- [ ] **Step 8: Run the supervisor suite to verify all pass**

Run: `go test ./internal/supervisor/ -race -count=1`
Expected: PASS (new exit tests plus all existing lifecycle tests).

- [ ] **Step 9: Commit**

```bash
git add internal/supervisor/instance.go internal/supervisor/instance_test.go
git commit -m "feat(supervisor): record last exit code and reason

Capture p.Wait()'s result on every exit path (natural, spawn-failure,
and operator stop) via deriveExit; expose ExitCode/ExitReason on
Snapshot. Persists across restarts; blank until first exit.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Proto — `ProcInfo` fields 13–16

**Files:**
- Modify: `proto/marshal/v1/daemon.proto` (`ProcInfo` message ~62-75)
- Regenerate: `internal/pb/daemon.pb.go` (and any dependents) via `make proto`

**Interfaces:**
- Produces: `pb.ProcInfo` getters `GetThreads() int32`, `GetOpenFds() int32`, `GetExitCode() int32`, `GetExitReason() string`; settable fields `Threads`, `OpenFds`, `ExitCode`, `ExitReason`.

- [ ] **Step 1: Add the fields to the proto**

In `proto/marshal/v1/daemon.proto`, inside `message ProcInfo`, after the `credential = 12;` line:

```proto
  int32  threads     = 13; // M-D: thread count, summed over the process group
  int32  open_fds    = 14; // M-D: open FD count; -1 = unavailable on this platform
  int32  exit_code   = 15; // M-D: last exit code; -1 if signaled / spawn failure
  string exit_reason = 16; // M-D: last exit reason; "" = never exited
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `make proto`
Expected: succeeds; `internal/pb/daemon.pb.go` now defines `OpenFds`, `Threads`, `ExitCode`, `ExitReason` on `ProcInfo`.

- [ ] **Step 3: Verify the build compiles and getters exist**

Run: `go build ./... && grep -c 'func (x \*ProcInfo) GetOpenFds\|func (x \*ProcInfo) GetExitReason\|func (x \*ProcInfo) GetThreads\|func (x \*ProcInfo) GetExitCode' internal/pb/daemon.pb.go`
Expected: build succeeds; grep prints `4`.

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/daemon.proto internal/pb/
git commit -m "feat(proto): add threads, open_fds, exit_code, exit_reason to ProcInfo

Additive fields 13-16; reused by AgentState.procs so they flow through
the fleet path too. Regenerated internal/pb via make proto.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Daemon plumbing — populate `ProcInfo` from sample + snapshot

**Files:**
- Modify: `internal/daemon/convert.go` (`snapshotToProc` ~67-86; `procList` ~88-101; imports ~3-10)
- Modify: `internal/daemon/fleet.go` (`fleetSnapshot` ~14-35)
- Test: `internal/daemon/fleet_test.go` (`TestSnapshotToProcCredential` call site at ~12-16; add a new test)

**Interfaces:**
- Consumes: `metrics.Sample` (Task 1), `manager.InstanceSnapshot` (embeds `supervisor.Snapshot` with `ExitCode`/`ExitReason`), `pb.ProcInfo` fields (Task 3).
- Produces: `snapshotToProc(s manager.InstanceSnapshot, sm metrics.Sample) *pb.ProcInfo` — sets cpu/mem/threads/fds from `sm` and exit code/reason from `s`.

- [ ] **Step 1: Update the failing test for the new signature + add coverage**

In `internal/daemon/fleet_test.go`, add `"marshal/internal/manager"` is already imported; add `"marshal/internal/metrics"` to the import block. Replace `TestSnapshotToProcCredential` and add a new test:

```go
func TestSnapshotToProcCredential(t *testing.T) {
	p := snapshotToProc(manager.InstanceSnapshot{
		Name:       "priv",
		Source:     "git",
		Credential: "gh-ci",
	}, metrics.Sample{})
	if p.GetCredential() != "gh-ci" {
		t.Fatalf("credential not stamped: %q", p.GetCredential())
	}
}

func TestSnapshotToProcExtendedMetrics(t *testing.T) {
	sn := manager.InstanceSnapshot{Name: "api"}
	sn.ExitCode = 2
	sn.ExitReason = "exit status 2"
	p := snapshotToProc(sn, metrics.Sample{Threads: 12, Fds: -1})
	if p.GetThreads() != 12 {
		t.Fatalf("threads = %d, want 12", p.GetThreads())
	}
	if p.GetOpenFds() != -1 {
		t.Fatalf("open_fds = %d, want -1", p.GetOpenFds())
	}
	if p.GetExitCode() != 2 || p.GetExitReason() != "exit status 2" {
		t.Fatalf("exit = (%d, %q), want (2, \"exit status 2\")", p.GetExitCode(), p.GetExitReason())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestSnapshotToProc' -v`
Expected: FAIL — `snapshotToProc` still takes `(s, cpu, mem)`; compile error.

- [ ] **Step 3: Change `snapshotToProc` to take a `metrics.Sample`**

In `internal/daemon/convert.go`, add `"marshal/internal/metrics"` to the import block. Replace `snapshotToProc`:

```go
// snapshotToProc converts a manager snapshot + metrics into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot, sm metrics.Sample) *pb.ProcInfo {
	var uptimeMs int64
	if s.State == supervisor.StateOnline && !s.StartedAt.IsZero() {
		uptimeMs = time.Since(s.StartedAt).Milliseconds()
	}
	return &pb.ProcInfo{
		Id:         int32(s.ID),
		Name:       s.Name,
		InstanceId: int32(s.InstanceID),
		State:      string(s.State),
		Pid:        int32(s.Pid),
		UptimeMs:   uptimeMs,
		Restarts:   int32(s.Restarts),
		Cpu:        sm.Cpu,
		Mem:        int64(sm.Mem),
		Source:     s.Source,
		Credential: s.Credential,
		Threads:    sm.Threads,
		OpenFds:    sm.Fds,
		ExitCode:   s.ExitCode,
		ExitReason: s.ExitReason,
	}
}
```

- [ ] **Step 4: Update `procList` to build a `metrics.Sample`**

In `internal/daemon/convert.go`, replace the entire `for _, s := range snaps { ... }` loop inside `procList` (the block that fetches cpu/mem and appends) with:

```go
	for _, s := range snaps {
		sm := metrics.Sample{Fds: -1} // default: unavailable until first sample
		if srv.metrics != nil {
			if v, ok := srv.metrics.Get(s.Label); ok {
				sm = v
			}
		}
		procs = append(procs, snapshotToProc(s, sm))
	}
```

- [ ] **Step 5: Update `fleetSnapshot` likewise**

In `internal/daemon/fleet.go`, replace the entire `for _, sn := range snaps { ... }` loop inside `fleetSnapshot`'s returned closure (the `var cpu`/`var mem` block through the `out = append(...)`) with:

```go
		for _, sn := range snaps {
			sm := metrics.Sample{Fds: -1} // default: unavailable until first sample
			if s.metrics != nil {
				if v, ok := s.metrics.Get(sn.Label); ok {
					sm = v
				}
			}
			out = append(out, snapshotToProc(sn, sm))
		}
```

Add `"marshal/internal/metrics"` to `internal/daemon/fleet.go`'s import block.

- [ ] **Step 6: Run the daemon suite to verify all pass**

Run: `go test ./internal/daemon/ -race -count=1`
Expected: PASS (updated + new tests, plus existing).

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/convert.go internal/daemon/fleet.go internal/daemon/fleet_test.go
git commit -m "feat(daemon): populate ProcInfo threads/fds/exit from sample + snapshot

snapshotToProc now takes a metrics.Sample; callers default Fds to -1
when no sample exists yet. Exit code/reason come from the snapshot.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Dashboard — expose the fields in `/api/fleet`

**Files:**
- Modify: `internal/dashboard/fleet.go` (`procView` struct ~11-22; `fleetView` mapping ~44-55)
- Test: `internal/dashboard/fleet_test.go` (`TestFleetView` ~12-50)

**Interfaces:**
- Consumes: `pb.ProcInfo` getters (Task 3).
- Produces: `procView` JSON gains `threads`, `open_fds`, `exit_code`, `exit_reason`.

- [ ] **Step 1: Extend the failing test**

In `internal/dashboard/fleet_test.go`, within `TestFleetView`, add the new fields to the first `ProcInfo` literal (the `ticker` proc), so it reads:

```go
		Procs: []*pb.ProcInfo{{
			Name: "ticker", State: "running", Pid: 99, UptimeMs: 1000, Restarts: 2, Cpu: 1.5, Mem: 2048,
			Source: "command", Threads: 8, OpenFds: -1, ExitCode: 1, ExitReason: "exit status 1",
		}, {
```

Then add assertions after the existing `p := v[0].Procs[0]` block (after the Source check):

```go
	if p.Threads != 8 || p.OpenFds != -1 {
		t.Fatalf("threads/fds = %d/%d, want 8/-1", p.Threads, p.OpenFds)
	}
	if p.ExitCode != 1 || p.ExitReason != "exit status 1" {
		t.Fatalf("exit = (%d, %q), want (1, \"exit status 1\")", p.ExitCode, p.ExitReason)
	}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run TestFleetView -v`
Expected: FAIL — `p.Threads` / `p.OpenFds` / `p.ExitCode` / `p.ExitReason` undefined.

- [ ] **Step 3: Add the fields to `procView`**

In `internal/dashboard/fleet.go`, add to the `procView` struct (after `Credential`):

```go
	Threads    int32  `json:"threads"`
	OpenFds    int32  `json:"open_fds"`              // -1 = unavailable on this platform
	ExitCode   int32  `json:"exit_code"`
	ExitReason string `json:"exit_reason,omitempty"` // "" = never exited
```

- [ ] **Step 4: Map them in `fleetView`**

In `internal/dashboard/fleet.go`, add to the `procView{...}` literal inside the `procs = append(...)` call (after `Credential: p.GetCredential(),`):

```go
				Threads:    p.GetThreads(),
				OpenFds:    p.GetOpenFds(),
				ExitCode:   p.GetExitCode(),
				ExitReason: p.GetExitReason(),
```

- [ ] **Step 5: Run the dashboard suite to verify all pass**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/fleet.go internal/dashboard/fleet_test.go
git commit -m "feat(dashboard): expose threads/fds/exit in /api/fleet

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Web — type + minimal render + rebuild bundle

**Files:**
- Modify: `web/src/api.ts` (`Proc` type ~1-12)
- Modify: `web/src/ProcessCard.tsx` (`meta` ~31-33; `.pcard-meta`/`.pcard-metrics` render ~87-95)
- Rebuild: embedded SPA bundle via `make ui`

**Interfaces:**
- Consumes: `/api/fleet` JSON fields `threads`, `open_fds`, `exit_code`, `exit_reason` (Task 5).
- Produces: process card shows `N thr` / `M fds` (`—` when `< 0`) and a `last exit: …` line when a reason is present. Transitional surfacing only; M-A delivers the real treatment.

- [ ] **Step 1: Extend the `Proc` type**

In `web/src/api.ts`, add to the `Proc` type (after `credential?: string;`):

```ts
  threads: number;
  open_fds: number; // -1 = unavailable on this platform
  exit_code: number;
  exit_reason?: string; // "" / absent = never exited
```

- [ ] **Step 2: Render threads/FDs + last-exit in `ProcessCard`**

In `web/src/ProcessCard.tsx`, add two derived values just after the existing `meta` const (~line 33):

```tsx
  const fds = proc.open_fds < 0 ? "—" : String(proc.open_fds);
  const stats = `${proc.threads} thr · ${fds} fds`;
```

Add a stats/last-exit line: replace the existing meta line

```tsx
      <div className="pcard-meta">{meta}</div>
```

with

```tsx
      <div className="pcard-meta">{meta}{state === "online" && ` · ${stats}`}</div>
      {proc.exit_reason && (
        <div className="pcard-meta pcard-exit">last exit: {proc.exit_reason}</div>
      )}
```

- [ ] **Step 3: Type-check the web sources**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no type errors.

- [ ] **Step 4: Rebuild the embedded bundle**

Run: `make ui`
Expected: succeeds; the embedded bundle under `internal/dashboard` (e.g. `web/dist` → embed) is regenerated.

- [ ] **Step 5: Verify the Go build still embeds cleanly**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 6: Commit**

```bash
git add web/src/api.ts web/src/ProcessCard.tsx web/dist internal/dashboard
git commit -m "feat(dashboard): show threads/fds and last exit on the process card

Minimal transitional surfacing (— for unavailable FDs); M-A delivers
the real visual treatment. Rebuilt the embedded bundle.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

(If `git add web/dist internal/dashboard` reports paths that don't exist, run `git status` and stage whatever `make ui` actually regenerated — the embedded bundle path is what matters.)

---

### Task 7: Changelog + whole-branch verification

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]` → `Added`)

- [ ] **Step 1: Add the changelog entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added` (create the subsection if absent):

```markdown
- **Extended per-process metrics (M-D):** thread count and open file-descriptor
  count (group-summed; FDs shown as `—` where the platform does not report them,
  e.g. macOS), plus the last exit code and reason for each process, surfaced on
  the process card and in `/api/fleet`.
```

- [ ] **Step 2: Run the full verification suite**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all tests PASS; `go vet` clean; `gofmt -l .` prints nothing.

- [ ] **Step 3: Build the binary**

Run: `make build`
Expected: succeeds; `./marshal --version` prints a git-derived version.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(mD): changelog entry for extended per-process metrics

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Final state check**

Run: `git log --oneline dev..HEAD && git status -s`
Expected: the M-D commits listed; working tree clean. Branch is ready for code review → live demo → handoff → merge to `dev` (`--no-ff`).

---

## Post-plan steps (outside task checkboxes)

1. **Code review** — requesting-code-review (whole branch) before merge.
2. **Live demo** — per CLAUDE.md: scratch `XDG_DATA_HOME`, standard ports :9000/:9001, set password + rotate enroll token while the server is down, start the server, enroll an agent (`marshal start`, not `run`), then confirm `/api/fleet` shows real `threads`, `exit_code`/`exit_reason`, and `open_fds` as `—` locally (darwin). Tear down by data dir; verify no orphans (`pgrep -fl marshal`).
3. **Handoff** — `docs/handoffs/2026-06-23-mD-extended-process-metrics.md`.
4. **Merge** `mD-extended-process-metrics` → `dev` (`--no-ff`); delete the branch.
```
