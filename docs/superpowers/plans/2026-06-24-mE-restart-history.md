# M-E Restart History (Rollups) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Per-process `restarts_24h` count and `last_restart_unix` timestamp, surfaced on each process row from real restart events recorded by the supervisor.

**Architecture:** The supervisor fires an injected `onRestart` hook once per genuine restart; the manager wires that hook (with the instance's label) to a new agent-side `internal/eventstore` (SQLite, mirroring `metricstore`). The daemon computes per-label rollups from the store when building each `ProcInfo` and ships them as two point-in-time fields. No event shipping to the server, no ledger endpoint.

**Tech Stack:** Go 1.26, modernc.org/sqlite, protobuf (`make proto`), React/TypeScript SPA (`make ui`).

## Global Constraints

- **TDD:** failing test first, then implementation. `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (empty) before finishing.
- **Rollups only, point-in-time:** `restarts_24h` (count of restart events in the trailing 24h) + `last_restart_unix` (unix **seconds** of the most recent retained restart, 0 if none). NO event shipping to the server, NO server-side store, NO `/api/restarts` endpoint, NO per-event exit detail.
- **Event payload is `(ts_ms, label)` only.**
- **Supervisor stays storage-agnostic:** it calls an injected `onRestart func()`; it does not import the store.
- **Retention: 7 days** — prune events older than `now - 7*24h`.
- **Hook fires once per genuine restart** (the `restart == true` path, right after `i.restarts++`); NOT on a clean stop, operator stop, or `RestartNo` no-restart path.
- **Proto field naming:** to avoid protoc-gen-go's awkward `Restarts_24H` identifier (underscore-before-digit), the proto field is spelled `restarts24h` (→ Go `GetRestarts24H`); the JSON tag and TS field stay `restarts_24h`. `last_restart_unix` → `GetLastRestartUnix`.
- **Proto changes are additive:** `ProcInfo` continues at 17–18. Regenerate `internal/pb` with `make proto` (never hand-edit `*.pb.go`).
- **Changelog:** add an `[Unreleased]` entry as part of the work.
- **Commit trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Branch:** `mE-restart-history` (already created off `dev`; design spec committed at `c2c474b`).

---

## File Structure

- `internal/eventstore/store.go` (new) + test — SQLite restart-event store (Record/Rollups/Prune) (Task 1).
- `internal/supervisor/instance.go` — `Option`, `WithOnRestart`, variadic `NewInstance`, fire hook in `handleExit` (Task 2).
- `internal/manager/manager.go` — `RestartSink` interface, `WithRestartSink`, wire per-instance hook in `startInstance` (Task 3).
- `proto/marshal/v1/daemon.proto` + `internal/pb/*` — `ProcInfo.restarts24h=17`, `last_restart_unix=18` (Task 4).
- `internal/store/store.go` + `internal/daemon/server.go` + `internal/daemon/convert.go` + `internal/daemon/fleet.go` — store path, open eventstore, wire sink, prune, merge rollups into `ProcInfo` (Task 5).
- `internal/dashboard/fleet.go` — `procView` fields + mapping (Task 6).
- `web/src/api.ts` + `web/src/ProcessCard.tsx` + embedded bundle (Task 7).
- `CHANGELOG.md` + whole-branch verification (Task 8).

---

### Task 1: Event store — `internal/eventstore`

**Files:**
- Create: `internal/eventstore/store.go`
- Test: `internal/eventstore/store_test.go`

**Interfaces:**
- Produces: `eventstore.Open(path string) (*Store, error)`; `(*Store).Record(label string, tsMs int64) error`; `(*Store).Rollups(sinceMs int64) (map[string]Rollup, error)`; `(*Store).Prune(beforeMs int64) (int64, error)`; `(*Store).Close() error`; `type Rollup struct { Count24h int32; LastMs int64 }`.

- [ ] **Step 1: Write the failing test**

Create `internal/eventstore/store_test.go`:

```go
package eventstore

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "restarts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRollupsCountsWindowAndLast(t *testing.T) {
	s := open(t)
	// Two recent events for a#0, one old; one event for b#0.
	for _, e := range []struct {
		label string
		ts    int64
	}{
		{"a#0", 1000}, {"a#0", 2000}, {"a#0", 100}, {"b#0", 3000},
	} {
		if err := s.Record(e.label, e.ts); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	roll, err := s.Rollups(1000) // window = ts >= 1000
	if err != nil {
		t.Fatalf("rollups: %v", err)
	}
	if roll["a#0"].Count24h != 2 {
		t.Fatalf("a#0 count = %d, want 2 (ts 1000,2000; 100 excluded)", roll["a#0"].Count24h)
	}
	if roll["a#0"].LastMs != 2000 {
		t.Fatalf("a#0 last = %d, want 2000", roll["a#0"].LastMs)
	}
	if roll["b#0"].Count24h != 1 || roll["b#0"].LastMs != 3000 {
		t.Fatalf("b#0 = %+v, want {1, 3000}", roll["b#0"])
	}
}

func TestRollupsLastSetEvenWhenOutsideWindow(t *testing.T) {
	s := open(t)
	_ = s.Record("c#0", 500) // only event is older than the window
	roll, err := s.Rollups(1000)
	if err != nil {
		t.Fatalf("rollups: %v", err)
	}
	if roll["c#0"].Count24h != 0 || roll["c#0"].LastMs != 500 {
		t.Fatalf("c#0 = %+v, want {0, 500}", roll["c#0"])
	}
}

func TestPruneDeletesOld(t *testing.T) {
	s := open(t)
	_ = s.Record("a#0", 100)
	_ = s.Record("a#0", 5000)
	n, err := s.Prune(1000) // delete ts < 1000
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	roll, _ := s.Rollups(0)
	if roll["a#0"].LastMs != 5000 || roll["a#0"].Count24h != 1 {
		t.Fatalf("after prune a#0 = %+v, want {1, 5000}", roll["a#0"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/eventstore/ -run TestRollups -v`
Expected: FAIL — package/`Open` undefined (compile error).

- [ ] **Step 3: Write the implementation**

Create `internal/eventstore/store.go`:

```go
// Package eventstore persists per-instance restart events to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves trailing-window rollups.
package eventstore

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Rollup is one label's restart summary.
type Rollup struct {
	Count24h int32 // events with ts >= the rollup's sinceMs
	LastMs   int64 // most recent event ts in millis (0 if none)
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS restarts (
	ts    INTEGER NOT NULL,
	label TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_restarts_label_ts ON restarts(label, ts);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open restarts db: %w", err)
	}
	// One daemon process touches this DB from a few goroutines (restart hook +
	// snapshot reader + prune); serialize to sidestep SQLite locking. Volume tiny.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Record writes one restart event.
func (s *Store) Record(label string, tsMs int64) error {
	_, err := s.db.Exec(`INSERT INTO restarts(ts, label) VALUES(?, ?)`, tsMs, label)
	return err
}

// Rollups returns, per label, the count of events with ts >= sinceMs and the
// most recent event ts (set even when no event falls inside the window).
func (s *Store) Rollups(sinceMs int64) (map[string]Rollup, error) {
	rows, err := s.db.Query(`
		SELECT label,
		       SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END) AS count24h,
		       MAX(ts)                                  AS last_ms
		FROM restarts
		GROUP BY label`, sinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Rollup{}
	for rows.Next() {
		var label string
		var count int64
		var last int64
		if err := rows.Scan(&label, &count, &last); err != nil {
			return nil, err
		}
		out[label] = Rollup{Count24h: int32(count), LastMs: last}
	}
	return out, rows.Err()
}

// Prune deletes events with ts < beforeMs and returns the number removed.
func (s *Store) Prune(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM restarts WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/eventstore/ -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/eventstore/
git commit -m "feat(eventstore): SQLite restart-event store with trailing-window rollups

Record(label, tsMs); Rollups(sinceMs) -> per-label {count in window, last ts};
Prune(beforeMs). Mirrors metricstore (single-conn, busy_timeout).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Supervisor — `WithOnRestart` hook

**Files:**
- Modify: `internal/supervisor/instance.go` (`NewInstance` ~50-52; `handleExit` restart-accounting block)
- Test: `internal/supervisor/instance_test.go`

**Interfaces:**
- Produces: `type Option func(*Instance)`; `func WithOnRestart(fn func()) Option`; `NewInstance(spec proc.Spec, policy Policy, opts ...Option) *Instance`. The hook fires once per genuine restart.

- [ ] **Step 1: Write the failing test**

Add to `internal/supervisor/instance_test.go` (the imports already include `sync`, `time`, `config`, `proc`; add `"sync/atomic"`):

```go
func TestOnRestartFiresOncePerRestart(t *testing.T) {
	var n int32
	// Crashing process under on-failure: restarts until the cap, then errored.
	i := NewInstance(
		proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 1"}},
		testPolicy(config.RestartOnFailure),
		WithOnRestart(func() { atomic.AddInt32(&n, 1) }),
	)
	_, wait := runInstance(i)
	wait()
	if atomic.LoadInt32(&n) < 1 {
		t.Fatalf("onRestart fired %d times, want >= 1", atomic.LoadInt32(&n))
	}
	if got := i.Snapshot().Restarts; int32(got) != atomic.LoadInt32(&n) {
		t.Fatalf("hook count %d != restarts counter %d", atomic.LoadInt32(&n), got)
	}
}

func TestOnRestartDoesNotFireWhenNoRestart(t *testing.T) {
	var n int32
	// RestartNo: the process exits once and is not restarted.
	i := NewInstance(
		proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 1"}},
		testPolicy(config.RestartNo),
		WithOnRestart(func() { atomic.AddInt32(&n, 1) }),
	)
	_, wait := runInstance(i)
	wait()
	if atomic.LoadInt32(&n) != 0 {
		t.Fatalf("onRestart fired %d times, want 0 (RestartNo)", atomic.LoadInt32(&n))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/supervisor/ -run TestOnRestart -v`
Expected: FAIL — `WithOnRestart` undefined / `NewInstance` takes 2 args (compile error).

- [ ] **Step 3: Add the Option type, field, and variadic constructor**

In `internal/supervisor/instance.go`, add an `onRestart func()` field to the `Instance` struct (after `exitReason string`):

```go
	exitReason string // last observed exit reason ("" until first exit)
	onRestart  func() // M-E: fired once per genuine restart (nil if unset)
```

Add the option type and constructor option (place just above `NewInstance`):

```go
// Option configures an Instance.
type Option func(*Instance)

// WithOnRestart registers a hook fired once per genuine restart (not on a clean
// stop, operator stop, or no-restart path).
func WithOnRestart(fn func()) Option { return func(i *Instance) { i.onRestart = fn } }
```

Change `NewInstance` to accept options:

```go
// NewInstance builds an instance supervisor. Call Run to start it.
func NewInstance(spec proc.Spec, policy Policy, opts ...Option) *Instance {
	i := &Instance{spec: spec, policy: policy, state: StateStarting}
	for _, o := range opts {
		o(i)
	}
	return i
}
```

- [ ] **Step 4: Fire the hook at the restart-accounting point**

In `internal/supervisor/instance.go`, in `handleExit`, locate the stability-accounting block that increments `i.restarts`. After the `i.mu.Unlock()` that follows `i.restarts++` (and before the `if unstable > i.policy.MaxRestarts` check), add the hook call:

```go
	i.restarts++
	if uptime < i.policy.MinUptime {
		i.unstable++
	} else {
		i.unstable = 0
	}
	unstable := i.unstable
	i.mu.Unlock()

	if i.onRestart != nil {
		i.onRestart() // M-E: count this restart
	}

	if unstable > i.policy.MaxRestarts {
		i.set(StateErrored, 0, time.Time{})
		return false
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/supervisor/ -race -count=1`
Expected: PASS (new hook tests plus all existing lifecycle tests).

- [ ] **Step 6: Commit**

```bash
git add internal/supervisor/instance.go internal/supervisor/instance_test.go
git commit -m "feat(supervisor): WithOnRestart hook fires once per genuine restart

Storage-agnostic: Instance calls an injected onRestart at the restart-
accounting point (after restarts++), not on clean/operator/no-restart paths.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Manager — `WithRestartSink` wiring

**Files:**
- Modify: `internal/manager/manager.go` (`Option`/`WithLogs` region ~35-42; `Manager` struct ~70-74; `startInstance` ~95-105)
- Test: `internal/manager/manager_test.go` (add a new test; create the file's test if needed — it exists)

**Interfaces:**
- Consumes: `supervisor.WithOnRestart` (Task 2).
- Produces: `type RestartSink interface{ Record(label string, tsMs int64) error }`; `func WithRestartSink(s RestartSink) Option`. When set, each instance's restart calls `sink.Record("name#idx", nowMs)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/manager/manager_test.go` (package `manager`). The file already imports `context`, `sync`, `time`, `config`, `supervisor` — no new imports are needed.

```go
// fakeSink records restart events for assertions.
type fakeSink struct {
	mu     sync.Mutex
	events []string // labels
}

func (f *fakeSink) Record(label string, tsMs int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, label)
	return nil
}
func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestManagerWiresRestartSink(t *testing.T) {
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, WithRestartSink(sink))
	// A crashing app under on-failure restarts a few times, then errors.
	app := config.App{
		Name: "crash", Cmd: "sh", Args: []string{"-c", "exit 1"},
		Instances: 1, Restart: config.RestartOnFailure, MaxRestarts: 2,
		KillTimeout: config.Duration{Duration: time.Second},
	}
	if _, err := m.Add(app); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Wait for the crash-restart cycle to produce at least one recorded restart.
	deadline := time.Now().Add(5 * time.Second)
	for sink.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.count() < 1 {
		t.Fatalf("sink recorded %d restarts, want >= 1", sink.count())
	}
	if sink.events[0] != "crash#0" {
		t.Fatalf("label = %q, want crash#0", sink.events[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manager/ -run TestManagerWiresRestartSink -v`
Expected: FAIL — `WithRestartSink` undefined (compile error).

- [ ] **Step 3: Add the interface, option, and struct field**

In `internal/manager/manager.go`, add the interface + option near `WithLogs`:

```go
// RestartSink records a restart event for an instance label. M-E.
type RestartSink interface {
	Record(label string, tsMs int64) error
}

// WithRestartSink wires per-instance restart events to sink.
func WithRestartSink(s RestartSink) Option {
	return func(m *Manager) { m.restartSink = s }
}
```

Add the field to the `Manager` struct (after `logs LogProvider`):

```go
	logs        LogProvider
	restartSink RestartSink
```

- [ ] **Step 4: Wire the hook in `startInstance`**

In `internal/manager/manager.go`, in `startInstance`, build the options and pass them to `NewInstance`. Replace:

```go
	inst := supervisor.NewInstance(spec, policyFor(app))
```

with:

```go
	var sopts []supervisor.Option
	if m.restartSink != nil {
		l := label // capture this instance's "name#idx"
		sopts = append(sopts, supervisor.WithOnRestart(func() {
			_ = m.restartSink.Record(l, time.Now().UnixMilli())
		}))
	}
	inst := supervisor.NewInstance(spec, policyFor(app), sopts...)
```

Ensure `"time"` is imported in `internal/manager/manager.go` (add it to the import block if absent).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/manager/ -race -count=1`
Expected: PASS (new test plus existing manager tests).

- [ ] **Step 6: Commit**

```bash
git add internal/manager/manager.go internal/manager/manager_test.go
git commit -m "feat(manager): WithRestartSink wires per-instance restart events

Each instance's restart records (label, now) to the sink via the
supervisor onRestart hook.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Proto — `ProcInfo` restart rollup fields

**Files:**
- Modify: `proto/marshal/v1/daemon.proto` (`ProcInfo` message, after field 16)
- Regenerate: `internal/pb/daemon.pb.go` via `make proto`

**Interfaces:**
- Produces: `pb.ProcInfo` fields `Restarts24H int32` (`GetRestarts24H()`), `LastRestartUnix int64` (`GetLastRestartUnix()`).

- [ ] **Step 1: Add the fields**

In `proto/marshal/v1/daemon.proto`, inside `message ProcInfo`, after the `exit_reason = 16;` line:

```proto
  int32 restarts24h       = 17; // M-E: restart events in the trailing 24h (JSON: restarts_24h)
  int64 last_restart_unix = 18; // M-E: unix seconds of the most recent restart (0 = none in retention)
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `make proto`
Expected: succeeds; `internal/pb/daemon.pb.go` defines `Restarts24H` and `LastRestartUnix` on `ProcInfo`.

- [ ] **Step 3: Verify build + getters**

Run: `go build ./... && grep -c 'func (x \*ProcInfo) GetRestarts24H\|func (x \*ProcInfo) GetLastRestartUnix' internal/pb/daemon.pb.go`
Expected: build succeeds; grep prints `2`.

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/daemon.proto internal/pb/
git commit -m "feat(proto): add restarts24h and last_restart_unix to ProcInfo

Additive fields 17-18. Field spelled restarts24h (no underscore-digit) so
the Go getter is GetRestarts24H; JSON tag stays restarts_24h.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Daemon — open store, wire sink, prune, merge rollups

**Files:**
- Modify: `internal/store/store.go` (path helpers ~47-48)
- Modify: `internal/daemon/server.go` (`Server` struct ~36-46; manager construction ~276; store opens ~280; prune goroutine ~348-360)
- Modify: `internal/daemon/convert.go` (`snapshotToProc` ~67-86; `procList` ~88-101)
- Modify: `internal/daemon/fleet.go` (`fleetSnapshot` ~14-35)
- Test: `internal/daemon/fleet_test.go` (existing `snapshotToProc` calls + new assertion)

**Interfaces:**
- Consumes: `eventstore.Open`/`Rollups`/`Prune` (Task 1); `manager.WithRestartSink` (Task 3); `pb.ProcInfo` fields (Task 4); `metrics.Sample` (existing).
- Produces: `snapshotToProc(s manager.InstanceSnapshot, sm metrics.Sample, rs eventstore.Rollup) *pb.ProcInfo`; `Server.estore *eventstore.Store`.

- [ ] **Step 1: Add the store path helper**

In `internal/store/store.go`, after `MetricsDBPath` (~line 48):

```go
// RestartsDBPath is the SQLite file holding restart-event history.
func (s *Store) RestartsDBPath() string { return filepath.Join(s.base, "restarts.db") }
```

- [ ] **Step 2: Update the failing test for the new signature + add coverage**

In `internal/daemon/fleet_test.go`, add `"marshal/internal/eventstore"` to the imports. Update the two existing `snapshotToProc` calls to pass a third arg, and add a new test:

- `TestSnapshotToProcCredential`: change `snapshotToProc(manager.InstanceSnapshot{...}, metrics.Sample{})` to `snapshotToProc(manager.InstanceSnapshot{...}, metrics.Sample{}, eventstore.Rollup{})`.
- `TestSnapshotToProcExtendedMetrics`: change its call's tail from `metrics.Sample{Threads: 12, Fds: -1})` to `metrics.Sample{Threads: 12, Fds: -1}, eventstore.Rollup{})`.

Add:

```go
func TestSnapshotToProcRestartRollup(t *testing.T) {
	p := snapshotToProc(manager.InstanceSnapshot{Name: "api"}, metrics.Sample{Fds: -1},
		eventstore.Rollup{Count24h: 4, LastMs: 1_700_000_000_000})
	if p.GetRestarts24H() != 4 {
		t.Fatalf("restarts24h = %d, want 4", p.GetRestarts24H())
	}
	if p.GetLastRestartUnix() != 1_700_000_000 { // millis -> seconds
		t.Fatalf("last_restart_unix = %d, want 1700000000", p.GetLastRestartUnix())
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestSnapshotToProc' -v`
Expected: FAIL — `snapshotToProc` takes 2 args (compile error).

- [ ] **Step 4: Extend `snapshotToProc`**

In `internal/daemon/convert.go`, add `"marshal/internal/eventstore"` to the imports. Change `snapshotToProc` to take a rollup and set the two fields:

```go
// snapshotToProc converts a manager snapshot + metrics + restart rollup into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot, sm metrics.Sample, rs eventstore.Rollup) *pb.ProcInfo {
	var uptimeMs int64
	if s.State == supervisor.StateOnline && !s.StartedAt.IsZero() {
		uptimeMs = time.Since(s.StartedAt).Milliseconds()
	}
	var lastRestartUnix int64
	if rs.LastMs > 0 {
		lastRestartUnix = rs.LastMs / 1000
	}
	return &pb.ProcInfo{
		Id:              int32(s.ID),
		Name:            s.Name,
		InstanceId:      int32(s.InstanceID),
		State:           string(s.State),
		Pid:             int32(s.Pid),
		UptimeMs:        uptimeMs,
		Restarts:        int32(s.Restarts),
		Cpu:             sm.Cpu,
		Mem:             int64(sm.Mem),
		Source:          s.Source,
		Credential:      s.Credential,
		Threads:         sm.Threads,
		OpenFds:         sm.Fds,
		ExitCode:        s.ExitCode,
		ExitReason:      s.ExitReason,
		Restarts24H:     rs.Count24h,
		LastRestartUnix: lastRestartUnix,
	}
}
```

- [ ] **Step 5: Update `procList` to fetch rollups**

In `internal/daemon/convert.go`, in `procList`, fetch the rollups once and pass per-label. Replace the loop body so it reads:

```go
	var rollups map[string]eventstore.Rollup
	if srv.estore != nil {
		rollups, _ = srv.estore.Rollups(time.Now().UnixMilli() - 24*60*60*1000)
	}
	for _, s := range snaps {
		sm := metrics.Sample{Fds: -1}
		if srv.metrics != nil {
			if v, ok := srv.metrics.Get(s.Label); ok {
				sm = v
			}
		}
		procs = append(procs, snapshotToProc(s, sm, rollups[s.Label]))
	}
```

(`rollups[s.Label]` on a nil map yields the zero `Rollup` — safe.)

- [ ] **Step 6: Update `fleetSnapshot` likewise**

In `internal/daemon/fleet.go`, add `"marshal/internal/eventstore"` to the imports if not already pulled in transitively (import it explicitly). Replace the `for _, sn := range snaps { ... }` loop inside the closure with:

```go
		var rollups map[string]eventstore.Rollup
		if s.estore != nil {
			rollups, _ = s.estore.Rollups(time.Now().UnixMilli() - 24*60*60*1000)
		}
		for _, sn := range snaps {
			sm := metrics.Sample{Fds: -1}
			if s.metrics != nil {
				if v, ok := s.metrics.Get(sn.Label); ok {
					sm = v
				}
			}
			out = append(out, snapshotToProc(sn, sm, rollups[sn.Label]))
		}
```

Add `"time"` to `internal/daemon/fleet.go` imports if absent.

- [ ] **Step 7: Add the `estore` field and open/wire/prune it in `server.go`**

In `internal/daemon/server.go`:

(a) Add the field to the `Server` struct (after `mdb`):

```go
	mdb              *metricstore.Store // metric history
	estore           *eventstore.Store  // restart-event history (M-E)
```

(b) Add `"marshal/internal/eventstore"` to the imports.

(c) Open the event store and wire the manager sink. Change the manager construction (~line 276) and add the open after the metric DB open (~line 280):

```go
	estore, err := eventstore.Open(st.RestartsDBPath())
	if err != nil {
		return fmt.Errorf("open restarts db: %w", err)
	}
	mgr := manager.New(ctx, manager.WithLogs(reg), manager.WithRestartSink(estore))
```

(Place the `estore` open BEFORE `manager.New` so the sink is available; move the existing `mgr := manager.New(...)` line down accordingly. Keep the existing `mdb` open where it is.)

(d) Set `estore` on the `Server` literal (where `srv := &Server{...}` is built, ~line 312): add `estore: estore,`.

(e) Prune in the existing 10-minute ticker goroutine (~line 357). After the `mdb.Prune(...)` line, add:

```go
				_, _ = mdb.Prune(time.Now().UnixMilli() - cfg.retention.Milliseconds())
				_, _ = estore.Prune(time.Now().UnixMilli() - 7*24*60*60*1000) // M-E: 7-day retention
```

- [ ] **Step 8: Run the daemon + store suites to verify all pass**

Run: `go test ./internal/daemon/ ./internal/store/ -race -count=1`
Expected: PASS (updated + new tests, plus existing).

- [ ] **Step 9: Commit**

```bash
git add internal/store/store.go internal/daemon/server.go internal/daemon/convert.go internal/daemon/fleet.go internal/daemon/fleet_test.go
git commit -m "feat(daemon): record restarts and merge 24h rollups into ProcInfo

Open the restart event store, wire it as the manager's restart sink,
prune to 7 days, and stamp restarts24h/last_restart_unix on each ProcInfo
from a single per-snapshot Rollups query.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Dashboard — expose rollups in `/api/fleet`

**Files:**
- Modify: `internal/dashboard/fleet.go` (`procView` struct; `fleetView` mapping)
- Test: `internal/dashboard/fleet_test.go` (`TestFleetView`)

**Interfaces:**
- Consumes: `pb.ProcInfo.GetRestarts24H()`, `GetLastRestartUnix()` (Task 4).
- Produces: `procView.Restarts24h int32` (`json:"restarts_24h"`), `procView.LastRestartUnix int64` (`json:"last_restart_unix,omitempty"`).

- [ ] **Step 1: Extend the failing test**

In `internal/dashboard/fleet_test.go`, within `TestFleetView`, add the fields to the first `ProcInfo` literal (the `ticker` proc) so it includes:

```go
			Source: "command", Threads: 8, OpenFds: -1, ExitCode: 1, ExitReason: "exit status 1",
			Restarts24H: 5, LastRestartUnix: 1700000000,
```

Add assertions after the existing proc-field checks:

```go
	if p.Restarts24h != 5 || p.LastRestartUnix != 1700000000 {
		t.Fatalf("restart rollup = %d/%d, want 5/1700000000", p.Restarts24h, p.LastRestartUnix)
	}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run TestFleetView -v`
Expected: FAIL — `p.Restarts24h` / `p.LastRestartUnix` undefined (compile error).

- [ ] **Step 3: Add the `procView` fields**

In `internal/dashboard/fleet.go`, add to the `procView` struct (after `ExitReason`):

```go
	Restarts24h     int32  `json:"restarts_24h"`
	LastRestartUnix int64  `json:"last_restart_unix,omitempty"`
```

- [ ] **Step 4: Map them in `fleetView`**

In `internal/dashboard/fleet.go`, add to the `procView{...}` literal (after `ExitReason: p.GetExitReason(),`):

```go
				Restarts24h:     p.GetRestarts24H(),
				LastRestartUnix: p.GetLastRestartUnix(),
```

- [ ] **Step 5: Run the dashboard suite to verify all pass**

Run: `go test ./internal/dashboard/ -race -count=1 && gofmt -l internal/dashboard/fleet.go`
Expected: PASS; gofmt prints nothing.

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/fleet.go internal/dashboard/fleet_test.go
git commit -m "feat(dashboard): expose restarts_24h and last_restart in /api/fleet

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Web — type + render + rebuild bundle

**Files:**
- Modify: `web/src/api.ts` (`Proc` type)
- Modify: `web/src/ProcessCard.tsx` (meta render)
- Rebuild: embedded SPA bundle via `make ui`

**Interfaces:**
- Consumes: `/api/fleet` JSON `restarts_24h`, `last_restart_unix` (Task 6).
- Produces: process card shows `<n> restarts/24h` and `last restart <relative>` when present. Transitional surfacing — M-A does the real treatment.

- [ ] **Step 1: Extend the `Proc` type**

In `web/src/api.ts`, add to the `Proc` type (after `exit_reason?: string;`):

```ts
  restarts_24h: number;
  last_restart_unix?: number; // unix seconds; absent/0 = none in retention
```

- [ ] **Step 2: Render restart history on the card**

In `web/src/ProcessCard.tsx`, add a relative-time helper just after the existing `fds`/`stats` derivations (near line 34):

```tsx
  function ago(unix?: number): string {
    if (!unix) return "—";
    const s = Math.max(0, Math.floor(Date.now() / 1000 - unix));
    if (s < 60) return `${s}s ago`;
    if (s < 3600) return `${Math.floor(s / 60)}m ago`;
    if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
    return `${Math.floor(s / 86400)}d ago`;
  }
  const restartStats = `${proc.restarts_24h}/24h · last ${ago(proc.last_restart_unix)}`;
```

Render it on the online meta line by extending the stats segment. Replace:

```tsx
      <div className="pcard-meta">{meta}{state === "online" && ` · ${stats}`}</div>
```

with:

```tsx
      <div className="pcard-meta">{meta}{state === "online" && ` · ${stats}`}{state === "online" && ` · ${restartStats}`}</div>
```

- [ ] **Step 3: Type-check the web sources**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no type errors.

- [ ] **Step 4: Rebuild the embedded bundle**

Run: `make ui`
Expected: succeeds; the embedded bundle under `internal/dashboard/dist` is regenerated.

- [ ] **Step 5: Verify the Go build still embeds cleanly**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 6: Commit**

```bash
git add web/src/api.ts web/src/ProcessCard.tsx internal/dashboard/dist
git commit -m "feat(dashboard): show restarts/24h and last-restart on the process card

Minimal transitional surfacing; M-A delivers the real treatment. Rebuilt
the embedded bundle.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

(If `git add internal/dashboard/dist` reports a different regenerated path, run `git status` and stage what `make ui` actually produced.)

---

### Task 8: Changelog + whole-branch verification

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]` → `Added`)

- [ ] **Step 1: Add the changelog entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added` (first bullet):

```markdown
- **Restart history (M-E):** each process now shows how many times it restarted
  in the last 24h and when it last restarted, recorded from real supervisor
  restart events in a local SQLite event store (7-day retention) and surfaced on
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
git commit -m "docs(mE): changelog entry for restart history

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Final state check**

Run: `git log --oneline dev..HEAD && git status -s`
Expected: the M-E commits listed; working tree clean. Branch ready for code review → live demo → handoff → merge to `dev` (`--no-ff`).

---

## Post-plan steps (outside task checkboxes)

1. **Code review** — requesting-code-review (whole branch) before merge.
2. **Live demo** — per CLAUDE.md: scratch `XDG_DATA_HOME`, standard ports :9000/:9001, set password + rotate enroll token while the server is down, start the server, enroll an agent (`marshal start`) running a deliberately crashing app (e.g. `sh -c 'sleep 2; exit 1'`, restart on-failure), then confirm `/api/fleet` shows `restarts_24h` climbing and `last_restart_unix` recent, and the process card renders the restart line. Tear down by data dir; verify no orphans (`pgrep -fl marshal`).
3. **Handoff** — `docs/handoffs/2026-06-24-mE-restart-history.md`.
4. **Merge** `mE-restart-history` → `dev` (`--no-ff`); delete the branch.
