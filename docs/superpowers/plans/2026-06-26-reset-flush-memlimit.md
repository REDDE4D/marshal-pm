# Reset / Flush / max_memory_restart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `marshal reset` (zero restart counters), `marshal flush` (clear logs), a per-app
`max_memory_restart` auto-restart guard, and a color-coded merged log tail.

**Architecture:** Bottom-up. Pure-Go units first (supervisor / eventstore / manager / logs / config /
a new `memguard` package), then one proto change + regen, then daemon wiring (gRPC handlers + fleet
dispatch + the memory guard hooked into the existing metrics tick), then CLI, then dashboard + web,
then docs. Each layer is independently tested before the next consumes it.

**Tech Stack:** Go 1.26.4, gRPC over a Unix socket (`internal/pb` generated from
`proto/marshal/v1/*.proto`), `modernc.org/sqlite` (eventstore), `lumberjack` (log rotation),
`gopsutil` (metrics), cobra (CLI), React/TypeScript (dashboard under `web/`).

## Global Constraints

- Module path is `github.com/REDDE4D/marshal-pm`; imports are `github.com/REDDE4D/marshal-pm/internal/...`.
- TDD: write the failing test first, run it red, implement minimally, run it green, commit.
- All work happens on a feature branch off **`dev`** (never `main`). Branch name: `reset-flush-memlimit`.
- Every commit message uses the trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Regenerate protobufs with `make proto` (runs `./scripts/gen-proto.sh`) — never hand-edit `internal/pb`.
- Update `CHANGELOG.md` under `## [Unreleased]` as part of the final task (Added section).
- Before declaring done: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (must list nothing).
- Target release: **v0.12.0** (not tagged in this plan; release is a separate step).

---

## File Structure

**Create:**
- `internal/memguard/guard.go` — memory-limit breach guard (debounced restart trigger).
- `internal/memguard/guard_test.go` — guard unit tests.
- `docs/handoffs/2026-06-26-reset-flush-memlimit.md` — session handoff (final task).

**Modify:**
- `proto/marshal/v1/daemon.proto` — `Reset`/`Flush` RPCs; `AppSpec.max_memory_restart`.
- `proto/marshal/v1/fleet.proto` — `ControlOp` `reset`/`flush` oneof members.
- `internal/supervisor/instance.go` — `Instance.ResetCounters`.
- `internal/eventstore/store.go` — `Store.DeleteLabels`.
- `internal/manager/manager.go` — `Manager.ResetCounters`.
- `internal/logs/sink.go` — `Sink.Truncate` + `rotatedGlob`.
- `internal/logs/registry.go` — `Registry.Truncate`.
- `internal/config/config.go` — `ByteSize` type + `parseByteSize` + `App.MaxMemoryRestart`.
- `internal/daemon/server.go` — `Reset`/`Flush` handlers; `guard` field + wiring; `Delete` rewrite.
- `internal/daemon/convert.go` — carry `max_memory_restart` in `appSpecToConfig`.
- `internal/daemon/command.go` — fleet dispatch for `reset`/`flush`; `guard.Remove` on fleet delete.
- `cmd/marshal/main.go` — register `reset` + `flush` commands.
- `cmd/marshal/control.go` — `flushCmd`; `appToSpec` field; `labelColor`; colored `printLogLine`.
- `cmd/marshal/fleet.go` — `fleet reset` + `fleet flush`.
- `internal/dashboard/control.go` — `controlOp` `reset`/`flush` actions.
- `internal/dashboard/apps.go` — `max_memory_restart` in the two `AppSpec` builders + request body.
- `web/src/api.ts` — control action union + `AppSpec`/proc types.
- `web/src/ControlButtons.tsx` — `reset` + `flush` buttons.
- `web/src/AddAppModal.tsx` — `max_memory_restart` input.
- `CHANGELOG.md` — `[Unreleased]` Added entries.

**Tests added/modified:** `internal/supervisor/instance_test.go`, `internal/eventstore/store_test.go`,
`internal/manager/manager_test.go`, `internal/logs/sink_test.go`, `internal/logs/registry_test.go`,
`internal/config/config_test.go`, `internal/memguard/guard_test.go`, `internal/daemon/server_test.go`,
`internal/daemon/command_test.go`, `internal/daemon/convert_test.go`, `cmd/marshal/control_test.go`.

---

### Task 0: Branch setup

- [ ] **Step 1: Create the feature branch off `dev`**

```bash
git checkout dev
git pull --ff-only
git checkout -b reset-flush-memlimit
```

- [ ] **Step 2: Confirm a clean baseline**

Run: `go build ./... && go test ./... -count=1`
Expected: builds and all tests pass before any changes.

---

### Task 1: `Instance.ResetCounters` (supervisor)

**Files:**
- Modify: `internal/supervisor/instance.go`
- Test: `internal/supervisor/instance_test.go`

**Interfaces:**
- Produces: `func (i *Instance) ResetCounters()` — zeroes `i.restarts` and `i.unstable` under `i.mu`.

- [ ] **Step 1: Write the failing test** (append to `internal/supervisor/instance_test.go`)

```go
func TestResetCounters(t *testing.T) {
	i := NewInstance(proc.Spec{}, Policy{})
	i.restarts = 5
	i.unstable = 3
	i.ResetCounters()
	if got := i.Snapshot().Restarts; got != 0 {
		t.Fatalf("restarts = %d, want 0", got)
	}
	i.mu.Lock()
	u := i.unstable
	i.mu.Unlock()
	if u != 0 {
		t.Fatalf("unstable = %d, want 0", u)
	}
}
```

(If `proc` is not yet imported in the test file, add `"github.com/REDDE4D/marshal-pm/internal/proc"`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/supervisor/ -run TestResetCounters -v`
Expected: FAIL — `i.ResetCounters undefined`.

- [ ] **Step 3: Implement** (add to `internal/supervisor/instance.go`, after `Snapshot`)

```go
// ResetCounters zeroes the lifetime and crash-loop restart counters. It does not
// change process state or restart anything; for a running instance it restores
// the crash-loop headroom before MaxRestarts is reached again.
func (i *Instance) ResetCounters() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.restarts = 0
	i.unstable = 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/supervisor/ -run TestResetCounters -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/supervisor/instance.go internal/supervisor/instance_test.go
git commit -m "feat(supervisor): ResetCounters zeroes restart counters

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `Store.DeleteLabels` (eventstore)

**Files:**
- Modify: `internal/eventstore/store.go`
- Test: `internal/eventstore/store_test.go`

**Interfaces:**
- Produces: `func (s *Store) DeleteLabels(labels []string) (int64, error)` — deletes all restart
  rows for each given label; returns the total rows removed.

- [ ] **Step 1: Write the failing test** (append to `internal/eventstore/store_test.go`)

```go
func TestDeleteLabels(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, l := range []string{"a#0", "a#0", "b#0"} {
		if err := s.Record(l, 1000); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteLabels([]string{"a#0"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2", n)
	}
	r, _ := s.Rollups(0)
	if _, ok := r["a#0"]; ok {
		t.Fatalf("a#0 should be gone, got %+v", r["a#0"])
	}
	if r["b#0"].Count24h != 1 {
		t.Fatalf("b#0 count = %d, want 1", r["b#0"].Count24h)
	}
}
```

(Ensure `"path/filepath"` is imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/eventstore/ -run TestDeleteLabels -v`
Expected: FAIL — `s.DeleteLabels undefined`.

- [ ] **Step 3: Implement** (add to `internal/eventstore/store.go`, after `Prune`)

```go
// DeleteLabels removes all restart events for the given labels and returns the
// total number of rows deleted.
func (s *Store) DeleteLabels(labels []string) (int64, error) {
	var total int64
	for _, l := range labels {
		res, err := s.db.Exec(`DELETE FROM restarts WHERE label = ?`, l)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/eventstore/ -run TestDeleteLabels -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/eventstore/store.go internal/eventstore/store_test.go
git commit -m "feat(eventstore): DeleteLabels removes a label's restart events

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `Manager.ResetCounters` (manager)

**Files:**
- Modify: `internal/manager/manager.go`
- Test: `internal/manager/manager_test.go`

**Interfaces:**
- Consumes: `Instance.ResetCounters()` (Task 1).
- Produces: `func (m *Manager) ResetCounters(sel string) ([]InstanceSnapshot, error)` — resolves the
  selector, zeroes each instance's counters, returns `Describe(sel)`. Unknown selector → error.

- [ ] **Step 1: Write the failing test** (append to `internal/manager/manager_test.go`)

```go
func TestResetCountersZeroesAfterCrashes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	defer m.StopAll()

	// A command that fails immediately accrues restarts under restart=on-failure.
	app := config.App{
		Name: "crasher", Cmd: "sh", Args: []string{"-c", "exit 1"},
		Instances: 1, Restart: config.RestartOnFailure, MaxRestarts: 100,
	}
	if _, err := m.Add(app); err != nil {
		t.Fatal(err)
	}

	// Wait until at least one restart is recorded.
	deadline := time.Now().Add(10 * time.Second)
	for {
		snaps, _ := m.Describe("crasher")
		if len(snaps) == 1 && snaps[0].Restarts >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never accrued a restart")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Stop freezes the counter (the supervisor loop exits on ctx cancel).
	if _, err := m.Stop("crasher"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResetCounters("crasher"); err != nil {
		t.Fatal(err)
	}
	snaps, _ := m.Describe("crasher")
	if snaps[0].Restarts != 0 {
		t.Fatalf("restarts = %d after reset, want 0", snaps[0].Restarts)
	}
}

func TestResetCountersUnknownSelector(t *testing.T) {
	m := New(context.Background())
	if _, err := m.ResetCounters("ghost"); err == nil {
		t.Fatal("expected error for unknown selector")
	}
}
```

(Ensure `"context"`, `"time"`, and `"github.com/REDDE4D/marshal-pm/internal/config"` are imported.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manager/ -run TestResetCounters -v`
Expected: FAIL — `m.ResetCounters undefined`.

- [ ] **Step 3: Implement** (add to `internal/manager/manager.go`, after `Restart`)

```go
// ResetCounters zeroes the restart counters of the selected apps' instances and
// returns their refreshed snapshots. It does not restart anything.
func (m *Manager) ResetCounters(sel string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	m.mu.Unlock()

	for _, in := range insts {
		in.inst.ResetCounters()
	}
	return m.Describe(sel)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/manager/ -run TestResetCounters -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/manager/manager.go internal/manager/manager_test.go
git commit -m "feat(manager): ResetCounters resets selected apps' restart counters

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `Sink.Truncate` + `Registry.Truncate` (logs)

**Files:**
- Modify: `internal/logs/sink.go`, `internal/logs/registry.go`
- Test: `internal/logs/sink_test.go`, `internal/logs/registry_test.go`

**Interfaces:**
- Produces: `func (s *Sink) Truncate() error` — empties active files, deletes rotated backups,
  resets the ring and partial buffers; a write after truncate still lands.
- Produces: `func (r *Registry) Truncate(labels []string) error` — truncates the existing sinks for
  the given labels (unknown labels skipped).

- [ ] **Step 1: Write the failing Sink test** (append to `internal/logs/sink_test.go`)

```go
func TestSinkTruncate(t *testing.T) {
	dir := t.TempDir()
	s := newSink(dir, "app#0", time.Now)
	w := s.Writer(false)
	if _, err := w.Write([]byte("line1\nline2\n")); err != nil {
		t.Fatal(err)
	}
	if len(s.Backfill(0)) == 0 {
		t.Fatal("expected ring lines before truncate")
	}
	if err := s.Truncate(); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Backfill(0)); got != 0 {
		t.Fatalf("ring = %d after truncate, want 0", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "app#0.out.log"))
	if len(b) != 0 {
		t.Fatalf("active file len = %d, want 0", len(b))
	}
	if _, err := w.Write([]byte("line3\n")); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Backfill(0)); got != 1 {
		t.Fatalf("ring = %d after post-truncate write, want 1", got)
	}
}
```

(Ensure `"os"`, `"path/filepath"`, `"time"` are imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestSinkTruncate -v`
Expected: FAIL — `s.Truncate undefined`.

- [ ] **Step 3: Implement in `internal/logs/sink.go`**

Add `"os"` to the import block if not present. Add these methods after `Close`:

```go
// Truncate empties the active log files, deletes rotated backups, and clears the
// in-memory ring. Lumberjack keeps appending to the same path after an external
// truncate, so no reopen is needed.
func (s *Sink) Truncate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.ring = newRing(ringCap)
	s.outPart = nil
	s.errPart = nil
	var firstErr error
	for _, f := range []string{s.outFile.Filename, s.errFile.Filename} {
		if err := os.Truncate(f, 0); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
		matches, _ := filepath.Glob(rotatedGlob(f))
		for _, m := range matches {
			if m == f {
				continue
			}
			_ = os.Remove(m)
		}
	}
	return firstErr
}

// rotatedGlob returns a glob matching lumberjack's rotated siblings of filename,
// e.g. for "dir/app#0.out.log" it yields "dir/app#0.out-*.log*" (covers .gz too).
func rotatedGlob(filename string) string {
	ext := filepath.Ext(filename)
	prefix := strings.TrimSuffix(filename, ext)
	return prefix + "-*" + ext + "*"
}
```

(`"strings"` and `"path/filepath"` are already imported in `sink.go`.)

- [ ] **Step 4: Run the Sink test to verify it passes**

Run: `go test ./internal/logs/ -run TestSinkTruncate -v`
Expected: PASS.

- [ ] **Step 5: Write the failing Registry test** (append to `internal/logs/registry_test.go`)

```go
func TestRegistryTruncate(t *testing.T) {
	r := NewRegistry(t.TempDir())
	s := r.For("app#0")
	if _, err := s.Writer(false).Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := r.Truncate([]string{"app#0", "ghost#0"}); err != nil {
		t.Fatal(err)
	}
	if got := len(s.Backfill(0)); got != 0 {
		t.Fatalf("ring = %d after truncate, want 0", got)
	}
}
```

- [ ] **Step 6: Run it red**

Run: `go test ./internal/logs/ -run TestRegistryTruncate -v`
Expected: FAIL — `r.Truncate undefined`.

- [ ] **Step 7: Implement in `internal/logs/registry.go`** (add after `Remove`)

```go
// Truncate clears the logs of the existing sinks for the given labels. Unknown
// labels are skipped. Errors from individual sinks are joined; the first is returned.
func (r *Registry) Truncate(labels []string) error {
	r.mu.Lock()
	sinks := make([]*Sink, 0, len(labels))
	for _, l := range labels {
		if s, ok := r.sinks[l]; ok {
			sinks = append(sinks, s)
		}
	}
	r.mu.Unlock()
	var firstErr error
	for _, s := range sinks {
		if err := s.Truncate(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
```

- [ ] **Step 8: Run both logs tests to verify they pass**

Run: `go test ./internal/logs/ -run 'TestSinkTruncate|TestRegistryTruncate' -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/logs/sink.go internal/logs/registry.go internal/logs/sink_test.go internal/logs/registry_test.go
git commit -m "feat(logs): Truncate clears a sink's files, backups, and ring

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `ByteSize` config type + `App.MaxMemoryRestart`

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `type ByteSize struct{ Bytes uint64 }` with `UnmarshalYAML`/`UnmarshalJSON`/`MarshalJSON`
  and `func parseByteSize(s string) (uint64, error)` (K/M/G, 1024-based; `KB`/`MB`/`GB` aliases; plain
  integers; `""` → 0).
- Produces: `App.MaxMemoryRestart ByteSize` (`yaml:"max_memory_restart"`), zero = disabled.

- [ ] **Step 1: Write the failing test** (append to `internal/config/config_test.go`)

```go
func TestParseByteSize(t *testing.T) {
	cases := map[string]uint64{
		"300M": 300 << 20, "1G": 1 << 30, "512K": 512 << 10,
		"1048576": 1048576, "5GB": 5 << 30, "256MB": 256 << 20, "": 0,
	}
	for in, want := range cases {
		got, err := parseByteSize(in)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("parseByteSize(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseByteSize("bogus"); err == nil {
		t.Fatal("expected error for \"bogus\"")
	}
}

func TestByteSizeFromYAML(t *testing.T) {
	cfg, err := Parse([]byte("apps:\n  - name: web\n    cmd: ./web\n    max_memory_restart: 300M\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Apps[0].MaxMemoryRestart.Bytes; got != 300<<20 {
		t.Fatalf("MaxMemoryRestart = %d, want %d", got, 300<<20)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestParseByteSize|TestByteSizeFromYAML' -v`
Expected: FAIL — `parseByteSize` / `MaxMemoryRestart` undefined.

- [ ] **Step 3: Implement in `internal/config/config.go`**

Add the type + parser (place after the `Duration` block):

```go
// ByteSize is a byte count that unmarshals from "300M"/"1G"/"512K" or a plain
// integer. Suffixes are 1024-based; KB/MB/GB are accepted as aliases of K/M/G.
type ByteSize struct{ Bytes uint64 }

func parseByteSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	upper := strings.ToUpper(s)
	mult := uint64(1)
	switch {
	case strings.HasSuffix(upper, "GB"):
		mult, upper = 1<<30, strings.TrimSuffix(upper, "GB")
	case strings.HasSuffix(upper, "G"):
		mult, upper = 1<<30, strings.TrimSuffix(upper, "G")
	case strings.HasSuffix(upper, "MB"):
		mult, upper = 1<<20, strings.TrimSuffix(upper, "MB")
	case strings.HasSuffix(upper, "M"):
		mult, upper = 1<<20, strings.TrimSuffix(upper, "M")
	case strings.HasSuffix(upper, "KB"):
		mult, upper = 1<<10, strings.TrimSuffix(upper, "KB")
	case strings.HasSuffix(upper, "K"):
		mult, upper = 1<<10, strings.TrimSuffix(upper, "K")
	case strings.HasSuffix(upper, "B"):
		upper = strings.TrimSuffix(upper, "B")
	}
	n, err := strconv.ParseUint(strings.TrimSpace(upper), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	return n * mult, nil
}

// UnmarshalYAML parses a size string ("300M") or a bare integer (bytes).
func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		var n uint64
		if err2 := value.Decode(&n); err2 == nil {
			b.Bytes = n
			return nil
		}
		return err
	}
	n, err := parseByteSize(s)
	if err != nil {
		return err
	}
	b.Bytes = n
	return nil
}

// MarshalJSON renders the size as a plain byte count (for dump.json round-trips).
func (b ByteSize) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatUint(b.Bytes, 10)), nil
}

// UnmarshalJSON parses either a number (bytes) or a quoted size string.
func (b *ByteSize) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	n, err := parseByteSize(s)
	if err != nil {
		return err
	}
	b.Bytes = n
	return nil
}
```

Add the field to the `App` struct (after `KillTimeout`):

```go
	MaxMemoryRestart ByteSize `yaml:"max_memory_restart" json:"max_memory_restart,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'TestParseByteSize|TestByteSizeFromYAML' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): ByteSize type and max_memory_restart app field

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: `memguard` package

**Files:**
- Create: `internal/memguard/guard.go`, `internal/memguard/guard_test.go`

**Interfaces:**
- Consumes: `metrics.Sample` (`internal/metrics`), field `Mem uint64`.
- Produces:
  - `func New(restart func(name string), logf func(string, ...any)) *Guard`
  - `func (g *Guard) SetLimit(app string, bytes uint64)` (bytes==0 removes the limit)
  - `func (g *Guard) Remove(app string)`
  - `func (g *Guard) Check(samples map[string]metrics.Sample)` — increments per-label breach counts;
    fires `restart(name)` once per app when any of its labels reaches the threshold (default 3),
    clearing that app's breach counts; resets a label's count when it drops back under the limit.

- [ ] **Step 1: Write the failing test** (`internal/memguard/guard_test.go`)

```go
package memguard

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/metrics"
)

func TestGuardFiresAfterThreshold(t *testing.T) {
	var restarted []string
	g := New(func(name string) { restarted = append(restarted, name) }, nil)
	g.SetLimit("web", 100)
	over := map[string]metrics.Sample{"web#0": {Mem: 200}}
	under := map[string]metrics.Sample{"web#0": {Mem: 50}}

	g.Check(over) // 1
	g.Check(over) // 2
	if len(restarted) != 0 {
		t.Fatalf("fired early: %v", restarted)
	}
	g.Check(over) // 3 -> fire
	if len(restarted) != 1 || restarted[0] != "web" {
		t.Fatalf("want [web], got %v", restarted)
	}

	g.Check(over) // breach cleared on fire; counting restarts
	g.Check(over)
	if len(restarted) != 1 {
		t.Fatalf("fired too soon after reset: %v", restarted)
	}

	g.Check(under) // drop under limit resets the counter
	g.Check(over)
	g.Check(over)
	if len(restarted) != 1 {
		t.Fatalf("under-limit did not reset: %v", restarted)
	}
	g.Check(over) // 3rd consecutive -> fire again
	if len(restarted) != 2 {
		t.Fatalf("want 2 fires total, got %v", restarted)
	}
}

func TestGuardNoLimitNeverFires(t *testing.T) {
	n := 0
	g := New(func(string) { n++ }, nil)
	for i := 0; i < 5; i++ {
		g.Check(map[string]metrics.Sample{"web#0": {Mem: 1 << 40}})
	}
	if n != 0 {
		t.Fatalf("fired without a configured limit: %d", n)
	}
}

func TestGuardRemoveDropsState(t *testing.T) {
	n := 0
	g := New(func(string) { n++ }, nil)
	g.SetLimit("web", 100)
	over := map[string]metrics.Sample{"web#0": {Mem: 200}}
	g.Check(over)
	g.Check(over)
	g.Remove("web")
	g.Check(over) // would have been the 3rd, but limit + breach are gone
	if n != 0 {
		t.Fatalf("fired after Remove: %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memguard/ -v`
Expected: FAIL — package/symbols undefined.

- [ ] **Step 3: Implement** (`internal/memguard/guard.go`)

```go
// Package memguard restarts an app when its sampled RSS exceeds a configured
// limit for a sustained number of consecutive metric samples (debounced).
package memguard

import (
	"strings"
	"sync"

	"github.com/REDDE4D/marshal-pm/internal/metrics"
)

// defaultThreshold is the number of consecutive over-limit samples required
// before a restart fires (~10-15s at the daemon's default 5s tick).
const defaultThreshold = 3

// Guard tracks per-app memory limits and per-instance breach streaks.
type Guard struct {
	mu        sync.Mutex
	limits    map[string]uint64 // by app name; absent = no limit
	breach    map[string]int    // by instance label ("name#idx")
	threshold int
	restart   func(name string)
	logf      func(string, ...any)
}

// New builds a Guard. restart is called with an app name when it should be
// restarted; logf (may be nil) records the reason.
func New(restart func(name string), logf func(string, ...any)) *Guard {
	return &Guard{
		limits:    map[string]uint64{},
		breach:    map[string]int{},
		threshold: defaultThreshold,
		restart:   restart,
		logf:      logf,
	}
}

// SetLimit sets (or, when bytes==0, removes) an app's memory limit.
func (g *Guard) SetLimit(app string, bytes uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if bytes == 0 {
		delete(g.limits, app)
		return
	}
	g.limits[app] = bytes
}

// Remove drops an app's limit and any pending breach state (on delete).
func (g *Guard) Remove(app string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.limits, app)
	for label := range g.breach {
		if appName(label) == app {
			delete(g.breach, label)
		}
	}
}

// Check evaluates one tick's samples and fires restarts for apps that have
// exceeded their limit for `threshold` consecutive ticks. At most one restart
// per app per Check.
func (g *Guard) Check(samples map[string]metrics.Sample) {
	type fire struct {
		name, label  string
		mem, limit   uint64
	}
	var fires []fire

	g.mu.Lock()
	fired := map[string]bool{}
	for label, sm := range samples {
		name := appName(label)
		limit, ok := g.limits[name]
		if !ok {
			continue
		}
		if sm.Mem <= limit {
			delete(g.breach, label)
			continue
		}
		g.breach[label]++
		if g.breach[label] < g.threshold || fired[name] {
			continue
		}
		fired[name] = true
		fires = append(fires, fire{name, label, sm.Mem, limit})
		for l := range g.breach {
			if appName(l) == name {
				delete(g.breach, l)
			}
		}
	}
	g.mu.Unlock()

	for _, f := range fires {
		if g.logf != nil {
			g.logf("memguard: %s rss %d exceeded limit %d for %d samples; restarting %s",
				f.label, f.mem, f.limit, g.threshold, f.name)
		}
		if g.restart != nil {
			g.restart(f.name)
		}
	}
}

func appName(label string) string {
	if i := strings.LastIndexByte(label, '#'); i >= 0 {
		return label[:i]
	}
	return label
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/memguard/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add internal/memguard/
git commit -m "feat(memguard): debounced per-app memory-limit restart guard

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Protobuf — `Reset`/`Flush` RPCs, `AppSpec.max_memory_restart`, `ControlOp` reset/flush

**Files:**
- Modify: `proto/marshal/v1/daemon.proto`, `proto/marshal/v1/fleet.proto`
- Regenerate: `internal/pb/*` via `make proto`

**Interfaces:**
- Produces (generated): `pb.DaemonClient.Reset`, `pb.DaemonClient.Flush`, `pb.DaemonServer` Reset/Flush;
  `pb.AppSpec.GetMaxMemoryRestart() int64`; `pb.ControlOp_Reset`, `pb.ControlOp_Flush` oneof wrappers.

- [ ] **Step 1: Edit `proto/marshal/v1/daemon.proto` — add RPCs to the `Daemon` service**

In `service Daemon { ... }` (after the `Restart` / `Delete` lines, near line 13), add:

```protobuf
  rpc Reset(Selector) returns (ProcList);
  rpc Flush(Selector) returns (Ack);
```

- [ ] **Step 2: Edit `proto/marshal/v1/daemon.proto` — add the AppSpec field**

In `message AppSpec { ... }`, after `optional GitSource source = 11;` (line 53), add:

```protobuf
  int64 max_memory_restart = 12; // bytes; 0 = disabled
```

- [ ] **Step 3: Edit `proto/marshal/v1/fleet.proto` — add ControlOp members**

In `message ControlOp { oneof op { ... } }`, after `Selector reload = 10;` (line 203), add:

```protobuf
    Selector reset = 11; // zero restart counters
    Selector flush = 12; // clear logs
```

- [ ] **Step 4: Regenerate**

Run: `make proto`
Expected: `internal/pb/*.pb.go` regenerated, no errors.

- [ ] **Step 5: Verify the project still builds**

Run: `go build ./...`
Expected: builds (the new server methods are not implemented yet, but `UnimplementedDaemonServer`
provides defaults, so compilation succeeds).

- [ ] **Step 6: Commit**

```bash
git add proto/ internal/pb/
git commit -m "proto: Reset/Flush RPCs, AppSpec.max_memory_restart, ControlOp reset/flush

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Daemon `Reset` & `Flush` gRPC handlers

**Files:**
- Modify: `internal/daemon/server.go`
- Test: `internal/daemon/server_test.go`

**Interfaces:**
- Consumes: `mgr.ResetCounters` (Task 3), `mgr.Describe`, `estore.DeleteLabels` (Task 2),
  `logs.Registry.Truncate` (Task 4), generated `pb` (Task 7).
- Produces: `func (s *Server) Reset(context.Context, *pb.Selector) (*pb.ProcList, error)` and
  `func (s *Server) Flush(context.Context, *pb.Selector) (*pb.Ack, error)`.

- [ ] **Step 1: Write the failing test** (append to `internal/daemon/server_test.go`)

```go
func TestResetAndFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	es, err := eventstore.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	srv := &Server{mgr: manager.New(ctx), logs: reg, estore: es}
	defer srv.mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Reset prunes the eventstore for the app's labels.
	if err := es.Record("a#0", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Reset(ctx, &pb.Selector{Target: "a"}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if r, _ := es.Rollups(0); r["a#0"].Count24h != 0 {
		t.Fatalf("eventstore not pruned: %+v", r["a#0"])
	}

	// Flush clears the ring for the app's labels.
	if _, err := reg.For("a#0").Writer(false).Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Flush(ctx, &pb.Selector{Target: "a"}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := len(reg.For("a#0").Backfill(0)); n != 0 {
		t.Fatalf("ring = %d after flush, want 0", n)
	}
}

func TestResetUnknownIsNotFound(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	_, err := srv.Reset(context.Background(), &pb.Selector{Target: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}
```

Add imports to the test file: `"path/filepath"`, `"github.com/REDDE4D/marshal-pm/internal/eventstore"`.
(`logs` and `manager` are already imported.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestResetAndFlush|TestResetUnknownIsNotFound' -v`
Expected: FAIL — `srv.Reset` / `srv.Flush` undefined.

- [ ] **Step 3: Implement** (add to `internal/daemon/server.go`, after the `Delete` handler)

```go
// Reset zeroes the restart counters of the selected apps and prunes their
// recorded restart events.
func (s *Server) Reset(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	snaps, err := s.mgr.ResetCounters(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if s.estore != nil {
		labels := make([]string, 0, len(snaps))
		for _, sn := range snaps {
			labels = append(labels, sn.Label)
		}
		_, _ = s.estore.DeleteLabels(labels)
	}
	return s.procList(snaps), nil
}

// Flush clears captured logs for the selected apps.
func (s *Server) Flush(_ context.Context, sel *pb.Selector) (*pb.Ack, error) {
	snaps, err := s.mgr.Describe(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if s.logs != nil {
		labels := make([]string, 0, len(snaps))
		for _, sn := range snaps {
			labels = append(labels, sn.Label)
		}
		_ = s.logs.Truncate(labels)
	}
	return &pb.Ack{Ok: true, Message: fmt.Sprintf("flushed %d instance(s)", len(snaps))}, nil
}
```

(`fmt`, `codes`, `status` are already imported in `server.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run 'TestResetAndFlush|TestResetUnknownIsNotFound' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go internal/daemon/server_test.go
git commit -m "feat(daemon): Reset and Flush gRPC handlers

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Daemon — carry `max_memory_restart` + wire the memory guard

**Files:**
- Modify: `internal/daemon/convert.go`, `internal/daemon/server.go`
- Test: `internal/daemon/convert_test.go`

**Interfaces:**
- Consumes: `config.ByteSize` (Task 5), `memguard.Guard` (Task 6), `pb.AppSpec.GetMaxMemoryRestart` (Task 7).
- Produces: `appSpecToConfig` populates `App.MaxMemoryRestart`; `Server` gains a `guard *memguard.Guard`
  field set in `Run`, fed by the metrics `onTick`, kept in sync by `launchApp` (SetLimit) and `Delete`
  (Remove).

- [ ] **Step 1: Write the failing convert test** (append to `internal/daemon/convert_test.go`)

```go
func TestAppSpecToConfigCarriesMaxMemoryRestart(t *testing.T) {
	app, err := appSpecToConfig(&pb.AppSpec{
		Name: "web", Cmd: "./web", MaxMemoryRestart: 300 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.MaxMemoryRestart.Bytes != 300<<20 {
		t.Fatalf("MaxMemoryRestart = %d, want %d", app.MaxMemoryRestart.Bytes, 300<<20)
	}
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./internal/daemon/ -run TestAppSpecToConfigCarriesMaxMemoryRestart -v`
Expected: FAIL — field not copied (Bytes == 0).

- [ ] **Step 3: Implement in `internal/daemon/convert.go`**

In `appSpecToConfig`, add to the `config.App{...}` literal (alongside `MaxRestarts`):

```go
		MaxMemoryRestart: config.ByteSize{Bytes: uint64(s.GetMaxMemoryRestart())},
```

- [ ] **Step 4: Run it green**

Run: `go test ./internal/daemon/ -run TestAppSpecToConfigCarriesMaxMemoryRestart -v`
Expected: PASS.

- [ ] **Step 5: Add the guard field to `Server`** (`internal/daemon/server.go`, in the struct at line 37)

Add a field to the `Server` struct:

```go
	guard            *memguard.Guard    // memory-limit restart guard (M-?)
```

Add the import `"github.com/REDDE4D/marshal-pm/internal/memguard"`.

- [ ] **Step 6: Register limits in `launchApp`** (`internal/daemon/server.go`, in `launchApp`)

After `s.logs.SetPolicy(...)` block and before `return s.mgr.Add(app)`, add:

```go
	if s.guard != nil {
		s.guard.SetLimit(app.Name, app.MaxMemoryRestart.Bytes)
	}
```

- [ ] **Step 7: Rewrite the `Delete` handler to remove limits** (`internal/daemon/server.go`)

Replace the existing one-line `Delete` (currently `return s.mutate(s.mgr.Delete, sel)`) with:

```go
func (s *Server) Delete(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	snaps, err := s.mgr.Delete(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if s.guard != nil {
		seen := map[string]bool{}
		for _, sn := range snaps {
			if !seen[sn.Name] {
				seen[sn.Name] = true
				s.guard.Remove(sn.Name)
			}
		}
	}
	return s.procList(snaps), nil
}
```

- [ ] **Step 8: Construct and wire the guard in `Run`** (`internal/daemon/server.go`)

After `srv := &Server{...}` (line ~325) and before `srv.deployer = ...`, add:

```go
	srv.guard = memguard.New(
		func(name string) { go func() { _, _ = mgr.Restart(name) }() },
		log.Printf,
	)
	for _, app := range func() []config.App { a, _ := st.Load(); return a }() {
		srv.guard.SetLimit(app.Name, app.MaxMemoryRestart.Bytes)
	}
```

Then, inside the existing `sampler.SetOnTick(func(m map[string]metrics.Sample) { ... })` closure
(line ~297), add as the first statement of the callback body:

```go
		srv.guard.Check(m)
```

(Note: `srv` is declared after `sampler.SetOnTick` in the current code. Move the
`sampler.SetOnTick(...)` call to **after** `srv := &Server{...}` and after the guard is constructed,
so the closure can reference `srv.guard`. The `mdb.Append` logic inside the callback is unchanged.)

- [ ] **Step 9: Build and run the full daemon test suite**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS (existing tests still green; convert test green).

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/convert.go internal/daemon/server.go internal/daemon/convert_test.go
git commit -m "feat(daemon): wire max_memory_restart guard into the metrics tick

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Fleet command dispatch — `reset`, `flush`, and `guard.Remove` on delete

**Files:**
- Modify: `internal/daemon/command.go`
- Test: `internal/daemon/command_test.go`

**Interfaces:**
- Consumes: `mgr.ResetCounters`, `mgr.Describe`, `estore.DeleteLabels`, `logs.Registry.Truncate`,
  `pb.ControlOp_Reset`, `pb.ControlOp_Flush` (Task 7).
- Produces: `handleFleetCommand` handles `reset` (returns affected procs) and `flush` (returns ok);
  the existing `delete` case also calls `guard.Remove`.

- [ ] **Step 1: Write the failing test** (append to `internal/daemon/command_test.go`)

```go
func TestHandleFleetCommandResetAndFlush(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()
	// Give the command server logs + eventstore for reset/flush side effects.
	s.logs = logs.NewRegistry(t.TempDir())
	es, err := eventstore.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	s.estore = es

	start := &pb.Command{RequestId: 1, Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
		Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app1")}}}}}
	if res := s.handleFleetCommand(start); !res.GetOk() {
		t.Fatalf("start failed: %s", res.GetError())
	}

	reset := &pb.Command{RequestId: 2, Op: &pb.ControlOp{Op: &pb.ControlOp_Reset{
		Reset_: &pb.Selector{Target: "app1"}}}}
	if res := s.handleFleetCommand(reset); !res.GetOk() {
		t.Fatalf("reset failed: %s", res.GetError())
	}

	flush := &pb.Command{RequestId: 3, Op: &pb.ControlOp{Op: &pb.ControlOp_Flush{
		Flush: &pb.Selector{Target: "app1"}}}}
	if res := s.handleFleetCommand(flush); !res.GetOk() {
		t.Fatalf("flush failed: %s", res.GetError())
	}
}
```

Add imports to `command_test.go`: `"path/filepath"`, `"github.com/REDDE4D/marshal-pm/internal/eventstore"`,
`"github.com/REDDE4D/marshal-pm/internal/logs"`.

> Note: the generated Go field for the proto `reset` member is `ControlOp_Reset` with inner field
> `Reset_` (the trailing underscore avoids clashing with the `Reset()` proto method). Verify the exact
> name in `internal/pb/fleet.pb.go` after Task 7 and use whatever was generated.

- [ ] **Step 2: Run it red**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommandResetAndFlush -v`
Expected: FAIL — the `reset`/`flush` cases are unhandled (fall through / no Ok).

- [ ] **Step 3: Implement in `internal/daemon/command.go`**

Add two new cases to the `switch v := op.GetOp().(type)` block (after the `ControlOp_Reload` case):

```go
	case *pb.ControlOp_Reset:
		snaps, err = s.mgr.ResetCounters(v.Reset_.GetTarget())
		if err == nil && s.estore != nil {
			labels := make([]string, 0, len(snaps))
			for _, sn := range snaps {
				labels = append(labels, sn.Label)
			}
			_, _ = s.estore.DeleteLabels(labels)
		}

	case *pb.ControlOp_Flush:
		var fsnaps []manager.InstanceSnapshot
		fsnaps, err = s.mgr.Describe(v.Flush.GetTarget())
		if err == nil && s.logs != nil {
			labels := make([]string, 0, len(fsnaps))
			for _, sn := range fsnaps {
				labels = append(labels, sn.Label)
			}
			_ = s.logs.Truncate(labels)
		}
```

In the existing `case *pb.ControlOp_Delete:` block, after `snaps, err = s.mgr.Delete(...)`, add:

```go
		if err == nil && s.guard != nil {
			seen := map[string]bool{}
			for _, sn := range snaps {
				if !seen[sn.Name] {
					seen[sn.Name] = true
					s.guard.Remove(sn.Name)
				}
			}
		}
```

(`manager` is already imported in `command.go`.)

- [ ] **Step 4: Run it green**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommandResetAndFlush -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/command.go internal/daemon/command_test.go
git commit -m "feat(daemon): fleet dispatch for reset/flush; drop mem limit on delete

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: CLI — `marshal reset` & `marshal flush`

**Files:**
- Modify: `cmd/marshal/main.go`, `cmd/marshal/control.go`
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `selectorCmd` (control.go:139), `withClient` (control.go:26), `targetsFromArg`,
  `pb.DaemonClient.Reset`/`.Flush` (Task 7).
- Produces: `func flushCmd() *cobra.Command`; `reset` registered via `selectorCmd`; both added to
  `rootCmd`.

- [ ] **Step 1: Write the failing test** (append to `cmd/marshal/control_test.go`)

```go
func TestResetAndFlushCommandsRegistered(t *testing.T) {
	root := rootCmd()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[strings.Fields(c.Use)[0]] = true
	}
	for _, name := range []string{"reset", "flush"} {
		if !have[name] {
			t.Errorf("root command %q not registered", name)
		}
	}
}
```

(`strings` is already imported in `control_test.go`.)

- [ ] **Step 2: Run it red**

Run: `go test ./cmd/marshal/ -run TestResetAndFlushCommandsRegistered -v`
Expected: FAIL — `reset`/`flush` not registered.

- [ ] **Step 3: Add `flushCmd` to `cmd/marshal/control.go`** (place near `selectorCmd`)

```go
// flushCmd clears captured logs for app(s). The selector argument is optional
// and defaults to "all" (matching `pm2 flush`).
func flushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "flush [name|id|all]",
		Short: "Clear captured logs for app(s) (default: all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "all"
			if len(args) == 1 {
				target = args[0]
			}
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				ack, err := c.Flush(ctx, &pb.Selector{Target: target})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "marshal:", ack.GetMessage())
				return nil
			})
		},
	}
}
```

- [ ] **Step 4: Register both commands in `cmd/marshal/main.go`**

In `rootCmd()`'s `root.AddCommand(...)` list, after the `restart` `selectorCmd(...)` entry, add:

```go
		selectorCmd("reset <name|id|all>", "Reset restart counter(s) for app(s)",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Reset(ctx, sel)
			}),
		flushCmd(),
```

- [ ] **Step 5: Run it green**

Run: `go test ./cmd/marshal/ -run TestResetAndFlushCommandsRegistered -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/main.go cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): add reset and flush commands

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: CLI — color-coded merged log tail

**Files:**
- Modify: `cmd/marshal/control.go`
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `isTerminal` (control.go:448), `pb.LogLine`.
- Produces: `func labelColor(label string) string` (returns an ANSI color code from a fixed palette,
  stable per label); `printLogLine` colorizes the `name#idx` prefix when the destination is a TTY.

- [ ] **Step 1: Write the failing test** (append to `cmd/marshal/control_test.go`)

```go
func TestLabelColorStableAndInPalette(t *testing.T) {
	a := labelColor("web#0")
	b := labelColor("web#0")
	if a != b {
		t.Fatalf("labelColor not stable: %q vs %q", a, b)
	}
	found := false
	for _, c := range logPalette {
		if c == a {
			found = true
		}
	}
	if !found {
		t.Fatalf("labelColor returned %q not in palette", a)
	}
}

func TestPrintLogLinePlainWhenNotTTY(t *testing.T) {
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printLogLine(cmd, &pb.LogLine{Name: "web", InstanceId: 0, Line: "hello"})
	got := buf.String()
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("non-TTY output should not contain ANSI codes: %q", got)
	}
	if got != "web#0 | hello\n" {
		t.Fatalf("got %q, want %q", got, "web#0 | hello\n")
	}
}
```

Add imports to `control_test.go` if missing: `"github.com/spf13/cobra"`. (`bytes`, `strings` already present.)

- [ ] **Step 2: Run it red**

Run: `go test ./cmd/marshal/ -run 'TestLabelColor|TestPrintLogLine' -v`
Expected: FAIL — `labelColor`/`logPalette` undefined.

- [ ] **Step 3: Implement in `cmd/marshal/control.go`** (replace `printLogLine` and add the palette)

```go
// logPalette is the set of ANSI foreground colors cycled for per-app log prefixes.
var logPalette = []string{
	"\x1b[36m", // cyan
	"\x1b[32m", // green
	"\x1b[33m", // yellow
	"\x1b[35m", // magenta
	"\x1b[34m", // blue
	"\x1b[91m", // bright red
}

const ansiReset = "\x1b[0m"

// labelColor maps a label to a stable color from logPalette.
func labelColor(label string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(label); i++ {
		h ^= uint32(label[i])
		h *= 16777619
	}
	return logPalette[int(h)%len(logPalette)]
}

// printLogLine writes a tagged log line: stdout lines to stdout, stderr to stderr.
// The "name#idx" prefix is colorized when the destination is a terminal.
func printLogLine(cmd *cobra.Command, ln *pb.LogLine) {
	w := cmd.OutOrStdout()
	if ln.GetStderr() {
		w = cmd.ErrOrStderr()
	}
	prefix := fmt.Sprintf("%s#%d", ln.GetName(), ln.GetInstanceId())
	if isTerminal(w) {
		prefix = labelColor(prefix) + prefix + ansiReset
	}
	fmt.Fprintf(w, "%s | %s\n", prefix, ln.GetLine())
}
```

- [ ] **Step 4: Run it green**

Run: `go test ./cmd/marshal/ -run 'TestLabelColor|TestPrintLogLine' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): color-coded per-app prefix in merged log tail

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: CLI — carry `max_memory_restart` into `AppSpec`

**Files:**
- Modify: `cmd/marshal/control.go`
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `appToSpec` (control.go:41), `config.App.MaxMemoryRestart` (Task 5),
  `pb.AppSpec.MaxMemoryRestart` (Task 7). `appToSpec` is shared by local `start`, `fleet start`, and
  self-enroll, so this one edit covers all CLI start paths.
- Produces: `appToSpec` sets `MaxMemoryRestart`.

- [ ] **Step 1: Write the failing test** (append to `cmd/marshal/control_test.go`)

```go
func TestAppToSpecCarriesMaxMemoryRestart(t *testing.T) {
	spec := appToSpec(config.App{
		Name: "web", Cmd: "./web",
		MaxMemoryRestart: config.ByteSize{Bytes: 300 << 20},
	})
	if spec.GetMaxMemoryRestart() != 300<<20 {
		t.Fatalf("MaxMemoryRestart = %d, want %d", spec.GetMaxMemoryRestart(), 300<<20)
	}
}
```

(`config` is already imported in `control_test.go`.)

- [ ] **Step 2: Run it red**

Run: `go test ./cmd/marshal/ -run TestAppToSpecCarriesMaxMemoryRestart -v`
Expected: FAIL — field is 0.

- [ ] **Step 3: Implement in `cmd/marshal/control.go`** (in `appToSpec`, in the `&pb.AppSpec{...}` literal)

Add after `KillTimeout: a.KillTimeout.Duration.String(),`:

```go
		MaxMemoryRestart: int64(a.MaxMemoryRestart.Bytes),
```

- [ ] **Step 4: Run it green**

Run: `go test ./cmd/marshal/ -run TestAppToSpecCarriesMaxMemoryRestart -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): carry max_memory_restart into AppSpec

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 14: CLI — `marshal fleet reset` & `marshal fleet flush`

**Files:**
- Modify: `cmd/marshal/fleet.go`
- Test: `cmd/marshal/fleet_test.go`

**Interfaces:**
- Consumes: `fleetSelectorCmd` (fleet.go:297), `pb.ControlOp_Reset`/`_Flush` (Task 7).
- Produces: `fleet reset` and `fleet flush` subcommands.

- [ ] **Step 1: Write the failing test** (append to `cmd/marshal/fleet_test.go`)

```go
func TestFleetResetFlushRegistered(t *testing.T) {
	have := map[string]bool{}
	for _, c := range fleetCmd().Commands() {
		have[strings.Fields(c.Use)[0]] = true
	}
	for _, name := range []string{"reset", "flush"} {
		if !have[name] {
			t.Errorf("fleet subcommand %q not registered", name)
		}
	}
}
```

(Ensure `"strings"` is imported in `fleet_test.go`.)

- [ ] **Step 2: Run it red**

Run: `go test ./cmd/marshal/ -run TestFleetResetFlushRegistered -v`
Expected: FAIL.

- [ ] **Step 3: Implement in `cmd/marshal/fleet.go`** (in `fleetCmd()`, after the `restart`/`delete` `AddCommand` calls)

```go
	cmd.AddCommand(fleetSelectorCmd("reset", "Reset restart counter(s) for app(s) on one agent",
		func(t string) *pb.ControlOp {
			return &pb.ControlOp{Op: &pb.ControlOp_Reset{Reset_: &pb.Selector{Target: t}}}
		}))
	cmd.AddCommand(fleetSelectorCmd("flush", "Clear captured logs for app(s) on one agent",
		func(t string) *pb.ControlOp {
			return &pb.ControlOp{Op: &pb.ControlOp_Flush{Flush: &pb.Selector{Target: t}}}
		}))
```

> Confirm the generated wrapper/field names (`ControlOp_Reset`, `.Reset_`) against
> `internal/pb/fleet.pb.go` and adjust if the generator produced different names.

- [ ] **Step 4: Run it green**

Run: `go test ./cmd/marshal/ -run TestFleetResetFlushRegistered -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/fleet.go cmd/marshal/fleet_test.go
git commit -m "feat(cli): fleet reset and fleet flush subcommands

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 15: Dashboard backend — `reset`/`flush` actions + `max_memory_restart`

**Files:**
- Modify: `internal/dashboard/control.go`, `internal/dashboard/apps.go`
- Test: `internal/dashboard/control_test.go` (create if absent)

**Interfaces:**
- Consumes: `pb.ControlOp_Reset`/`_Flush` (Task 7), `controlOp` (control.go:62).
- Produces: `controlOp` maps `"reset"` and `"flush"` actions; the two `AppSpec` builders in `apps.go`
  set `MaxMemoryRestart` from a new request body field.

- [ ] **Step 1: Write the failing test** (`internal/dashboard/control_test.go`)

```go
package dashboard

import "testing"

func TestControlOpResetFlush(t *testing.T) {
	if controlOp("reset", "web") == nil {
		t.Fatal("reset action not mapped")
	}
	if controlOp("flush", "web") == nil {
		t.Fatal("flush action not mapped")
	}
	if controlOp("bogus", "web") != nil {
		t.Fatal("bogus action should be nil")
	}
}
```

- [ ] **Step 2: Run it red**

Run: `go test ./internal/dashboard/ -run TestControlOpResetFlush -v`
Expected: FAIL — reset/flush return nil.

- [ ] **Step 3: Implement in `internal/dashboard/control.go`** (in the `controlOp` switch)

Add cases before the `default`:

```go
	case "reset":
		return &pb.ControlOp{Op: &pb.ControlOp_Reset{Reset_: &pb.Selector{Target: selector}}}
	case "flush":
		return &pb.ControlOp{Op: &pb.ControlOp_Flush{Flush: &pb.Selector{Target: selector}}}
```

- [ ] **Step 4: Add `max_memory_restart` to the app-create path in `internal/dashboard/apps.go`**

Add a field to the create-app request body struct (near `MaxRestarts int32 \`json:"max_restarts"\``):

```go
	MaxMemoryRestart int64 `json:"max_memory_restart"`
```

In **both** `AppSpec` builders (the command-app builder around line 161 and the one around line 178),
add to the `&pb.AppSpec{...}` literal:

```go
		MaxMemoryRestart: body.MaxMemoryRestart,
```

(Use the actual request-body variable name in scope at each site — read the surrounding code.)

- [ ] **Step 5: Run it green + build**

Run: `go test ./internal/dashboard/ -run TestControlOpResetFlush -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/control.go internal/dashboard/apps.go internal/dashboard/control_test.go
git commit -m "feat(dashboard): reset/flush control actions and max_memory_restart

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 16: Dashboard frontend — buttons + memory-limit field

**Files:**
- Modify: `web/src/api.ts`, `web/src/ControlButtons.tsx`, `web/src/AddAppModal.tsx`

**Interfaces:**
- Consumes: the backend `/api/control` actions and `/api/apps` body field from Task 15.
- Produces: `control(...)` accepts `"reset"`/`"flush"`; `ControlButtons` renders the two buttons;
  `AddAppModal` has a `max_memory_restart` input that posts to the create body.

- [ ] **Step 1: Extend the control action union in `web/src/api.ts`**

Change the `control` function's `action` parameter type (api.ts:151) to include the new actions:

```ts
  action: "restart" | "stop" | "delete" | "reload" | "reset" | "flush",
```

Add `max_memory_restart?: string` to the create-app request type used by `AddAppModal` (the type with
`max_restarts?: number` near api.ts:178) — keep it a string so "300M" passes through; the backend
parses it. If the backend expects an integer, convert in the modal (see Step 3). Also add
`max_memory_restart?: number` to the proc/`AppInfo` display type if one exists (optional display).

- [ ] **Step 2: Add buttons in `web/src/ControlButtons.tsx`**

Widen the `Op` type and add two buttons. Update the type (line 6):

```tsx
type Op = "restart" | "stop" | "reload" | "reset" | "flush";
```

Add inside the returned button row (after the `stop` button, before `{msg && ...}`):

```tsx
      <button className="ctl-btn" disabled={!connected} onClick={() => ask("reset", "reset")}>reset</button>
      <button className="ctl-btn" disabled={!connected} onClick={() => ask("flush", "flush")}>flush</button>
```

(reset/flush are safe whether running or stopped, so they gate on `connected` only.)

- [ ] **Step 3: Add the memory-limit field in `web/src/AddAppModal.tsx`**

Add state near the other fields (e.g. after `maxRestarts`):

```tsx
  const [maxMemoryRestart, setMaxMemoryRestart] = useState("");
```

In the command-app submit builder (near the `cs.max_restarts` assignment around line 78), add:

```tsx
      if (maxMemoryRestart.trim()) cs.max_memory_restart = maxMemoryRestart.trim();
```

> If the create-app body field is typed as a number on the backend, instead send
> `Number(maxMemoryRestart)` and document that the UI expects bytes. The chosen backend type in Task 15
> is `int64`, so convert here: `cs.max_memory_restart = Number(maxMemoryRestart)` and label the field
> "max memory restart (bytes)". Pick one representation and keep backend + frontend consistent.

Add the input near the `maxRestarts` field (around line 230):

```tsx
        <Field label="max memory restart (bytes, 0 = off)">
          <Input value={maxMemoryRestart} onChange={(e) => setMaxMemoryRestart(e.target.value)} placeholder="0" inputMode="numeric" />
        </Field>
```

- [ ] **Step 4: Build the frontend**

Run: `make ui` (or the project's UI build; check the Makefile `ui` target)
Expected: TypeScript compiles, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/ControlButtons.tsx web/src/AddAppModal.tsx internal/dashboard/dist
git commit -m "feat(dashboard): reset/flush buttons and max_memory_restart field

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

> Note: `make ui` rebuilds the embedded `internal/dashboard/dist` assets; include them in the commit
> if the build emits them there (check `git status`).

---

### Task 17: Changelog, full verification, and handoff

**Files:**
- Modify: `CHANGELOG.md`
- Create: `docs/handoffs/2026-06-26-reset-flush-memlimit.md`

- [ ] **Step 1: Add changelog entries under `## [Unreleased]` → `### Added`**

```markdown
- `marshal reset <name|id|all>` — zero an app's restart counters (lifetime, crash-loop, and the
  24h restart-event history). Also available as `marshal fleet reset` and a dashboard control.
- `marshal flush [name|id|all]` — clear an app's captured logs (active files, rotated backups, and
  in-memory ring); defaults to all. Also `marshal fleet flush` and a dashboard control.
- `max_memory_restart` per-app config (e.g. `300M`) — auto-restarts an app when its RSS exceeds the
  limit for 3 consecutive metric samples. Note: a multi-instance app restarts all instances when any
  one exceeds the limit (per-instance restart is a future refinement).
- Color-coded per-app prefix in `marshal logs -f` when the output is a terminal.
```

- [ ] **Step 2: Run the full verification suite**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all tests pass; vet clean; `gofmt -l .` prints nothing.

- [ ] **Step 3: Write the handoff** (`docs/handoffs/2026-06-26-reset-flush-memlimit.md`)

Include, per the project handoff convention: current state (branch `reset-flush-memlimit`, all tasks
done, tests green), what changed and why (the four features + the multi-instance memory-restart
limitation), how to build/run/test (`make build`, `go test ./... -race`), deferred issues (TUI
monitor + live fleet logs slated for the v0.13.0 observability spec; per-instance memory restart),
and the concrete next step (live demo, then merge `dev`, then cut v0.12.0).

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md docs/handoffs/2026-06-26-reset-flush-memlimit.md
git commit -m "docs: changelog + handoff for reset/flush/max_memory_restart

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Live demo (per project convention)**

Per CLAUDE.md, spin up a scratch demo on the standard ports (`XDG_DATA_HOME=/tmp/marshal-demo/...`,
server on `:9000`/`:9001`), start a couple of demo apps with `marshal start`, then:
- exercise `marshal logs all -f` and confirm colored per-app prefixes,
- `marshal flush <app>` and confirm the log pane/file clears,
- drive an app's restart count up, `marshal reset <app>`, confirm CLI + dashboard show 0,
- start an app with `max_memory_restart` set low plus a memory-growing command, confirm the
  auto-restart fires after the debounce and is logged.
Report observations, then tear down (kill the demo daemon by data dir — **no broad pkill**; the user
runs a standing launchd daemon) and confirm no orphans with `pgrep -fl marshal`.

---

## Self-Review

**Spec coverage:**
- reset (3 counters) → Tasks 1, 2, 3, 8 (handler prunes eventstore), 10 (fleet), 11 (CLI), 15/16 (dashboard). ✓
- flush (files+backups+ring) → Tasks 4, 8, 10, 11, 14, 15, 16. ✓
- max_memory_restart (ByteSize, debounced guard, wire, multi-instance caveat) → Tasks 5, 6, 7, 9, 13, 15, 16; caveat in CHANGELOG (Task 17). ✓
- color-coded merged tail → Task 12. ✓
- wire propagation (AppSpec field + convert + appToSpec + dashboard builders) → Tasks 7, 9, 13, 15. ✓
- tests for every unit → Tasks 1–16 each include tests. ✓
- CHANGELOG / handoff / live demo → Task 17. ✓

**Placeholder scan:** No "TBD"/"implement later"; every code step shows real code. The two
generator-name notes (`ControlOp_Reset` / `Reset_`) explicitly instruct verifying against the
generated `internal/pb` — these are correctness checks, not placeholders.

**Type consistency:** `ResetCounters` (supervisor + manager), `DeleteLabels`, `Truncate` (Sink +
Registry), `ByteSize{Bytes}`, `memguard.New/SetLimit/Remove/Check`, `appToSpec`,
`MaxMemoryRestart` field names are used identically across producing and consuming tasks.

**Known fragility:** `TestResetCountersZeroesAfterCrashes` (Task 3) polls a crashing process; the
10s deadline and post-stop freeze make it robust, consistent with the project's existing timing-based
supervisor tests.
