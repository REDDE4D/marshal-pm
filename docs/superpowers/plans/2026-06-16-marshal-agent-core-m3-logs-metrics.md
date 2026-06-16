# Marshal Agent-Core M3 — Logs + Live Metrics — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture each supervised instance's stdout/stderr to rotated files + an in-memory ring buffer, stream them to `marshal logs <name|id> [-n N] [-f]`, and sample live per-instance CPU%/RSS (whole process group) every 5s, surfaced in `marshal list`/`describe`.

**Architecture:** Approach A from the spec — the daemon owns a `logs.Registry` (per-instance `Sink`: tee raw bytes → lumberjack file, split lines → ring buffer + subscriber fanout) and a `metrics.Sampler` (gopsutil, process-group sums). `proc.Spec` grows optional `Stdout`/`Stderr` writers (nil → inherit the terminal, so M1 `run` is unchanged); the `manager` gets a `WithLogs` option to inject per-instance writers. The daemon merges cpu/mem into `ProcInfo` and serves the `Logs` stream. The gRPC contract already reserves `Logs`/`cpu`/`mem`, so no `.proto` change.

**Tech Stack:** Go 1.26, gRPC over UDS, `gopkg.in/natefinch/lumberjack.v2` (rotation), `github.com/shirou/gopsutil/v3/process` (metrics), cobra (CLI).

**Spec:** `docs/superpowers/specs/2026-06-16-marshal-agent-core-m3-logs-metrics-design.md`

**Branch:** cut `m3-logs-metrics` from `main` before Task 1:
```bash
git checkout -b m3-logs-metrics
```

**Conventions (from CLAUDE.md):** TDD (failing test first). Commit each task with a co-author trailer:
`Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
Gotcha: any test that binds a Unix socket must use a short `/tmp` base (`os.MkdirTemp("/tmp", ...)`), never `t.TempDir()` — macOS caps `sun_path` at ~104 bytes. Pure file tests may use `t.TempDir()`.

---

## File Structure

**New:**
- `internal/logs/sink.go` — `Sink` (tee → lumberjack + ring + fanout), `Line`, internal `ring`.
- `internal/logs/sink_test.go`
- `internal/logs/registry.go` — `Registry` (`For`/`WriterPair`/`Remove`/`ResolveLabeled`).
- `internal/logs/registry_test.go`
- `internal/metrics/sampler.go` — `Sample`, `Instance`, `Sampler` (gopsutil process-group sampling).
- `internal/metrics/sampler_test.go`
- `internal/daemon/logs.go` — the `Logs` server-stream handler + label/line helpers.
- `internal/daemon/logs_test.go`
- `internal/pb/tools.go` — build-tagged protoc-plugin pins.

**Modified:**
- `internal/proc/proc.go` (+ `proc_test.go`) — `Spec.Stdout/Stderr io.Writer`.
- `internal/store/store.go` (+ `store_test.go`) — `LogsDir()` / `EnsureLogsDir()`.
- `internal/manager/manager.go` (+ `manager_test.go`) — `Option`, `WithLogs`, `LogProvider`, writer wiring, `Remove` on delete.
- `internal/daemon/server.go` — `Run` options, registry + sampler wiring, `Server` fields.
- `internal/daemon/convert.go` — `procList` method filling cpu/mem.
- `cmd/marshal/control.go` — `logs` command, CPU/MEM columns, `humanizeBytes`.
- `cmd/marshal/main.go` — register `logsCmd`, `cliError` unwrap.
- `cmd/marshal/main_test.go` (new) — `cliError` / `humanizeBytes` unit tests.
- `cmd/marshal/daemon_e2e_test.go` — logs + metrics e2e.
- `go.mod` / `go.sum` — lumberjack, gopsutil, pinned plugins.

---

## Task 1: `proc.Spec` output writers

**Files:**
- Modify: `internal/proc/proc.go`
- Test: `internal/proc/proc_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/proc/proc_test.go`:

```go
func TestStartWritesToProvidedWriters(t *testing.T) {
	var out, errb safeBuf
	p, err := Start(Spec{
		Cmd:    "sh",
		Args:   []string{"-c", "echo to-out; echo to-err 1>&2"},
		Stdout: &out,
		Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(out.String(), "to-out") {
		t.Errorf("stdout = %q, want to contain to-out", out.String())
	}
	if !strings.Contains(errb.String(), "to-err") {
		t.Errorf("stderr = %q, want to contain to-err", errb.String())
	}
}

// safeBuf is a mutex-guarded buffer: os/exec copies child output on a goroutine,
// so the test must not race the copier (it reads only after Wait, but -race is strict).
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string             { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
```

Ensure the test file imports `bytes`, `strings`, `sync` (add to its import block).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/proc/ -run TestStartWritesToProvidedWriters`
Expected: FAIL — `Spec` has no field `Stdout`/`Stderr` (compile error).

- [ ] **Step 3: Add the fields and wire them**

In `internal/proc/proc.go`, add `"io"` to imports. Extend `Spec`:

```go
// Spec describes one process to launch.
type Spec struct {
	Cmd        string
	Args       []string
	Cwd        string
	Env        map[string]string
	InstanceID int
	Stdout     io.Writer // nil → inherit os.Stdout
	Stderr     io.Writer // nil → inherit os.Stderr
}
```

In `Start`, replace the two `cmd.Stdout`/`cmd.Stderr` assignments:

```go
	cmd.Stdout = spec.Stdout
	if cmd.Stdout == nil {
		cmd.Stdout = os.Stdout
	}
	cmd.Stderr = spec.Stderr
	if cmd.Stderr == nil {
		cmd.Stderr = os.Stderr
	}
```

Update the `Start` doc comment line about stdout/stderr to:
`// Stdout/Stderr go to spec.Stdout/spec.Stderr when set, else inherit the parent's.`

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/proc/`
Expected: PASS (all proc tests, including the new one).

- [ ] **Step 5: Commit**

```bash
git add internal/proc/proc.go internal/proc/proc_test.go
git commit -m "feat(proc): optional Stdout/Stderr writers on Spec

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `store` logs directory

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/store/store_test.go`:

```go
func TestLogsDirCreatedPrivate(t *testing.T) {
	base := t.TempDir()
	s := NewAt(base)
	want := filepath.Join(base, "logs")
	if s.LogsDir() != want {
		t.Fatalf("LogsDir = %q, want %q", s.LogsDir(), want)
	}
	if err := s.EnsureLogsDir(); err != nil {
		t.Fatalf("EnsureLogsDir: %v", err)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir perm = %v, want 0700", info.Mode().Perm())
	}
}
```

Ensure `store_test.go` imports `os`, `path/filepath`, `testing` (add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestLogsDirCreatedPrivate`
Expected: FAIL — `s.LogsDir` / `s.EnsureLogsDir` undefined.

- [ ] **Step 3: Add the methods**

In `internal/store/store.go`, after `LogPath`:

```go
// LogsDir is the directory holding per-instance rotated log files.
func (s *Store) LogsDir() string { return filepath.Join(s.base, "logs") }

// EnsureLogsDir creates the logs directory (0700) if it does not exist.
func (s *Store) EnsureLogsDir() error { return os.MkdirAll(s.LogsDir(), 0o700) }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/store/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): logs directory path + EnsureLogsDir (0700)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `logs.Sink` — tee, ring buffer, fanout

**Files:**
- Create: `internal/logs/sink.go`
- Test: `internal/logs/sink_test.go`

Adds the lumberjack dependency.

- [ ] **Step 1: Write the failing tests**

Create `internal/logs/sink_test.go`:

```go
package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stepClock returns a now() that advances 1ns per call, for deterministic ordering.
func stepClock() func() time.Time {
	var n int64
	return func() time.Time { n++; return time.Unix(0, n) }
}

func TestSinkTeesToFileAndRing(t *testing.T) {
	dir := t.TempDir()
	s := newSink(dir, "app#0", stepClock())
	defer s.Close()

	if _, err := s.Writer(false).Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "app#0.out.log"))
	if err != nil {
		t.Fatalf("read out file: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("file = %q, want hello", data)
	}

	lines := s.Backfill(10)
	if len(lines) != 1 || lines[0].Text != "hello" || lines[0].Stderr {
		t.Fatalf("backfill = %+v, want one stdout line 'hello'", lines)
	}
}

func TestSinkAssemblesPartialLines(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	w := s.Writer(false)
	w.Write([]byte("ab"))
	w.Write([]byte("cd\n"))
	lines := s.Backfill(10)
	if len(lines) != 1 || lines[0].Text != "abcd" {
		t.Fatalf("backfill = %+v, want one line 'abcd'", lines)
	}
}

func TestSinkBackfillOrderedAcrossStreams(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	s.Writer(false).Write([]byte("1\n"))
	s.Writer(true).Write([]byte("2\n"))
	s.Writer(false).Write([]byte("3\n"))
	lines := s.Backfill(10)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0].Text != "1" || lines[1].Text != "2" || lines[2].Text != "3" {
		t.Fatalf("order = %+v", lines)
	}
	if !lines[1].Stderr {
		t.Fatalf("line 2 should be stderr")
	}
}

func TestSinkRingCaps(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	w := s.Writer(false)
	for i := 0; i < ringCap+5; i++ {
		w.Write([]byte("x\n"))
	}
	if got := len(s.Backfill(ringCap + 100)); got != ringCap {
		t.Fatalf("ring held %d, want %d", got, ringCap)
	}
}

func TestSinkSubscribeReceivesLive(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	ch, cancel := s.Subscribe()
	defer cancel()
	s.Writer(false).Write([]byte("live\n"))
	select {
	case ln := <-ch:
		if ln.Text != "live" {
			t.Fatalf("got %q, want live", ln.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("no line received")
	}
}

func TestSinkSubscribeDropsWhenFull(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	_, cancel := s.Subscribe() // never drained
	defer cancel()
	w := s.Writer(false)
	for i := 0; i < subBuffer+50; i++ {
		w.Write([]byte("x\n")) // must not block
	}
	// Ring still has everything despite the slow subscriber.
	if got := len(s.Backfill(subBuffer + 100)); got != subBuffer+50 {
		t.Fatalf("ring = %d, want %d", got, subBuffer+50)
	}
}

func TestSinkCloseClosesSubscribers(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	ch, _ := s.Subscribe()
	s.Close()
	if _, ok := <-ch; ok {
		t.Fatal("subscriber channel should be closed after Close")
	}
}

func TestSinkRotates(t *testing.T) {
	dir := t.TempDir()
	s := newSinkWithLimits(dir, "app#0", 1, 2, stepClock()) // 1MB cap
	defer s.Close()
	line := strings.Repeat("a", 1024) + "\n" // 1KB
	w := s.Writer(false)
	for i := 0; i < 1200; i++ {                // ~1.2MB > 1MB → rotates once
		w.Write([]byte(line))
	}
	// lumberjack names backups "<prefix>-<timestamp><ext>": app#0.out-<ts>.log
	entries, _ := filepath.Glob(filepath.Join(dir, "app#0.out-*.log"))
	if len(entries) == 0 {
		t.Fatalf("expected at least one rotated backup file, found none")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/`
Expected: FAIL — package/`newSink` undefined (compile error).

- [ ] **Step 3: Implement the sink**

Create `internal/logs/sink.go`:

```go
// Package logs captures per-instance process output to rotated files and an
// in-memory ring buffer, and fans new lines out to live followers.
package logs

import (
	"bytes"
	"io"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	maxSizeMB  = 10   // rotate at 10MB
	maxBackups = 5    // keep 5 rotated files
	ringCap    = 1000 // ~1000 lines/instance in memory
	subBuffer  = 256  // per-subscriber channel buffer
)

// Line is one captured output line with its origin.
type Line struct {
	Ts     time.Time
	Stderr bool
	Text   string
}

// Sink captures one instance's stdout and stderr.
type Sink struct {
	outFile, errFile *lumberjack.Logger
	now              func() time.Time

	mu      sync.Mutex
	ring    *ring
	outPart []byte // partial (newline-less) stdout tail
	errPart []byte // partial stderr tail
	subs    map[int]chan Line
	nextID  int
	closed  bool
}

func newSink(dir, label string, now func() time.Time) *Sink {
	return newSinkWithLimits(dir, label, maxSizeMB, maxBackups, now)
}

func newSinkWithLimits(dir, label string, sizeMB, backups int, now func() time.Time) *Sink {
	return &Sink{
		outFile: &lumberjack.Logger{Filename: filepath.Join(dir, label+".out.log"), MaxSize: sizeMB, MaxBackups: backups},
		errFile: &lumberjack.Logger{Filename: filepath.Join(dir, label+".err.log"), MaxSize: sizeMB, MaxBackups: backups},
		now:     now,
		ring:    newRing(ringCap),
		subs:    map[int]chan Line{},
	}
}

// Writer returns the io.Writer for one stream; the process's stdout/stderr is
// wired to it. Each write tees raw bytes to the rotated file and split lines to
// the ring buffer + subscribers.
func (s *Sink) Writer(stderr bool) io.Writer {
	return writerFunc(func(p []byte) (int, error) { return s.write(stderr, p) })
}

func (s *Sink) write(stderr bool, p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return len(p), nil
	}
	if stderr {
		_, _ = s.errFile.Write(p)
	} else {
		_, _ = s.outFile.Write(p)
	}
	part := &s.outPart
	if stderr {
		part = &s.errPart
	}
	*part = append(*part, p...)
	for {
		i := bytes.IndexByte(*part, '\n')
		if i < 0 {
			break
		}
		text := string((*part)[:i])
		*part = (*part)[i+1:]
		s.emit(Line{Ts: s.now(), Stderr: stderr, Text: text})
	}
	return len(p), nil
}

// emit appends to the ring and fans out to subscribers. Caller holds s.mu.
func (s *Sink) emit(ln Line) {
	s.ring.add(ln)
	for _, ch := range s.subs {
		select {
		case ch <- ln:
		default: // drop: a slow follower must never stall the process
		}
	}
}

// Backfill returns the last n captured lines (merged across streams, in order).
func (s *Sink) Backfill(n int) []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.last(n)
}

// Subscribe registers a live follower; call the returned func to unsubscribe.
func (s *Sink) Subscribe() (<-chan Line, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan Line, subBuffer)
	if s.closed {
		close(ch)
		return ch, func() {}
	}
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}

// Close flushes the files and closes all subscribers.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for id, ch := range s.subs {
		delete(s.subs, id)
		close(ch)
	}
	e1 := s.outFile.Close()
	e2 := s.errFile.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// ring is a fixed-capacity circular buffer of Lines.
type ring struct {
	buf  []Line
	size int
	head int
	full bool
}

func newRing(n int) *ring { return &ring{buf: make([]Line, n), size: n} }

func (r *ring) add(l Line) {
	r.buf[r.head] = l
	r.head = (r.head + 1) % r.size
	if r.head == 0 {
		r.full = true
	}
}

func (r *ring) snapshot() []Line {
	if !r.full {
		out := make([]Line, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]Line, r.size)
	copy(out, r.buf[r.head:])
	copy(out[r.size-r.head:], r.buf[:r.head])
	return out
}

func (r *ring) last(n int) []Line {
	all := r.snapshot()
	if n > 0 && n < len(all) {
		return all[len(all)-n:]
	}
	return all
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test ./internal/logs/`
Expected: PASS. `go mod tidy` pulls `gopkg.in/natefinch/lumberjack.v2` into `go.mod`/`go.sum`.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/sink.go internal/logs/sink_test.go go.mod go.sum
git commit -m "feat(logs): per-instance Sink — tee to rotated file + ring + fanout

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: `logs.Registry`

**Files:**
- Create: `internal/logs/registry.go`
- Test: `internal/logs/registry_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/logs/registry_test.go`:

```go
package logs

import "testing"

func TestRegistryReusesSinkPerLabel(t *testing.T) {
	r := NewRegistry(t.TempDir())
	a := r.For("app#0")
	b := r.For("app#0")
	if a != b {
		t.Fatal("For should return the same sink for the same label")
	}
	if r.For("app#1") == a {
		t.Fatal("different labels must get different sinks")
	}
}

func TestRegistryWriterPair(t *testing.T) {
	r := NewRegistry(t.TempDir())
	out, errw := r.WriterPair("app#0")
	out.Write([]byte("o\n"))
	errw.Write([]byte("e\n"))
	lines := r.For("app#0").Backfill(10)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
}

func TestRegistryRemoveDropsSink(t *testing.T) {
	r := NewRegistry(t.TempDir())
	first := r.For("app#0")
	r.Remove("app#0")
	if r.For("app#0") == first {
		t.Fatal("Remove should drop the sink; For must build a fresh one")
	}
}

func TestRegistryResolveLabeledSkipsUnknown(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.For("app#0")
	got := r.ResolveLabeled([]string{"app#0", "ghost#0"})
	if len(got) != 1 || got[0].Label != "app#0" {
		t.Fatalf("ResolveLabeled = %+v, want only app#0", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestRegistry`
Expected: FAIL — `NewRegistry` undefined.

- [ ] **Step 3: Implement the registry**

Create `internal/logs/registry.go`:

```go
package logs

import (
	"io"
	"sync"
	"time"
)

// Registry owns one Sink per instance label, keyed by "name#idx".
type Registry struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	sinks map[string]*Sink
}

// Labeled pairs a sink with its instance label.
type Labeled struct {
	Label string
	Sink  *Sink
}

// NewRegistry builds a registry writing rotated files under dir.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir, now: time.Now, sinks: map[string]*Sink{}}
}

// For returns the sink for label, creating it on first use (and reusing it
// across instance restarts so files and history persist).
func (r *Registry) For(label string) *Sink {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sinks[label]
	if !ok {
		s = newSink(r.dir, label, r.now)
		r.sinks[label] = s
	}
	return s
}

// WriterPair returns the stdout/stderr writers for label. Satisfies the
// manager.LogProvider interface.
func (r *Registry) WriterPair(label string) (io.Writer, io.Writer) {
	s := r.For(label)
	return s.Writer(false), s.Writer(true)
}

// Remove closes and drops the sink for label (on delete).
func (r *Registry) Remove(label string) {
	r.mu.Lock()
	s, ok := r.sinks[label]
	delete(r.sinks, label)
	r.mu.Unlock()
	if ok {
		_ = s.Close()
	}
}

// ResolveLabeled returns the existing sinks for labels, skipping unknown ones,
// preserving the input order.
func (r *Registry) ResolveLabeled(labels []string) []Labeled {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Labeled, 0, len(labels))
	for _, l := range labels {
		if s, ok := r.sinks[l]; ok {
			out = append(out, Labeled{Label: l, Sink: s})
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logs/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/registry.go internal/logs/registry_test.go
git commit -m "feat(logs): Registry of per-label sinks (reuse, remove, resolve)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: manager `WithLogs` option + sink disposal

**Files:**
- Modify: `internal/manager/manager.go`
- Test: `internal/manager/manager_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/manager/manager_test.go` (add `bytes`, `io`, `sync` to its imports):

```go
type fakeLogs struct {
	mu      sync.Mutex
	writers map[string]*safeBuf
	removed []string
}

func newFakeLogs() *fakeLogs { return &fakeLogs{writers: map[string]*safeBuf{}} }

func (f *fakeLogs) WriterPair(label string) (io.Writer, io.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := &safeBuf{}
	f.writers[label] = b
	return b, b
}

func (f *fakeLogs) Remove(label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, label)
}

func (f *fakeLogs) bufFor(label string) *safeBuf {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writers[label]
}

func (f *fakeLogs) removedLabels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string             { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func echoApp(name string) config.App {
	return config.App{
		Name: name, Cmd: "sh", Args: []string{"-c", "echo captured; sleep 30"},
		Instances: 1, Restart: config.RestartAlways, MaxRestarts: 3,
		KillTimeout: config.Duration{Duration: time.Second},
	}
}

func TestWithLogsCapturesOutputAndRemovesOnDelete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fl := newFakeLogs()
	m := New(ctx, WithLogs(fl))

	if _, err := m.Add(echoApp("a")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b := fl.bufFor("a#0"); b != nil && strings.Contains(b.String(), "captured") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b := fl.bufFor("a#0"); b == nil || !strings.Contains(b.String(), "captured") {
		t.Fatalf("a#0 output not captured: %v", b)
	}

	if _, err := m.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got := fl.removedLabels()
	if len(got) != 1 || got[0] != "a#0" {
		t.Fatalf("removed = %v, want [a#0]", got)
	}
}
```

Add `"strings"` to the test imports as well.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/manager/ -run TestWithLogs`
Expected: FAIL — `WithLogs` / `LogProvider` undefined.

- [ ] **Step 3: Implement the option and wiring**

In `internal/manager/manager.go` add `"io"` to imports. Add after the `InstanceSnapshot` type:

```go
// LogProvider supplies per-instance output writers and disposes of them.
type LogProvider interface {
	WriterPair(label string) (stdout, stderr io.Writer)
	Remove(label string)
}

// Option configures a Manager.
type Option func(*Manager)

// WithLogs wires per-instance stdout/stderr capture.
func WithLogs(lp LogProvider) Option {
	return func(m *Manager) { m.logs = lp }
}
```

Add a `logs LogProvider` field to the `Manager` struct (e.g. after `nextID int`).

Change `New` to accept options:

```go
// New builds an empty manager rooted at ctx. Instances spawned by Add run until
// ctx is canceled, the manager is StopAll'd, or they are individually stopped.
func New(ctx context.Context, opts ...Option) *Manager {
	m := &Manager{ctx: ctx}
	for _, o := range opts {
		o(m)
	}
	return m
}
```

In `startInstance`, compute the label once and inject writers:

```go
// startInstance launches one instance goroutine. Caller holds m.mu.
func (m *Manager) startInstance(app config.App, idx int) *managedInstance {
	label := fmt.Sprintf("%s#%d", app.Name, idx)
	spec := proc.Spec{Cmd: app.Cmd, Args: app.Args, Cwd: app.Cwd, Env: app.Env, InstanceID: idx}
	if m.logs != nil {
		spec.Stdout, spec.Stderr = m.logs.WriterPair(label)
	}
	inst := supervisor.NewInstance(spec, policyFor(app))
	ictx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.Run(ictx)
	}()
	return &managedInstance{
		instanceID: idx,
		label:      label,
		inst:       inst,
		cancel:     cancel,
		done:       done,
	}
}
```

In `Delete`, after `stopInstances(insts)` and before `return removed, nil`, dispose sinks:

```go
	stopInstances(insts)
	if m.logs != nil {
		for _, s := range removed {
			m.logs.Remove(s.Label)
		}
	}
	return removed, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manager/`
Expected: PASS (new test + all existing manager tests, since `New(ctx)` still compiles via the variadic).

- [ ] **Step 5: Commit**

```bash
git add internal/manager/manager.go internal/manager/manager_test.go
git commit -m "feat(manager): WithLogs option injects per-instance writers; dispose on delete

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `metrics.Sampler`

**Files:**
- Create: `internal/metrics/sampler.go`
- Test: `internal/metrics/sampler_test.go`

Adds the gopsutil dependency.

- [ ] **Step 1: Write the failing tests**

Create `internal/metrics/sampler_test.go`:

```go
package metrics

import (
	"os/exec"
	"testing"
	"time"
)

// startGroup launches a shell that backgrounds a child and waits, so the
// process group has a parent + at least one child.
func startGroup(t *testing.T) (pid int, stop func()) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 5 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Give the shell a moment to fork the child.
	time.Sleep(200 * time.Millisecond)
	return cmd.Process.Pid, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }
}

func TestGroupPidsIncludesChildren(t *testing.T) {
	pid, stop := startGroup(t)
	defer stop()
	pids := groupPids(int32(pid))
	if len(pids) < 2 {
		t.Fatalf("groupPids(%d) = %v, want >= 2 (parent + child)", pid, pids)
	}
}

func TestSamplerRecordsMemAndPrunesDeadHandles(t *testing.T) {
	pid, stop := startGroup(t)
	s := NewSampler(time.Hour) // manual sampling
	s.sample([]Instance{{Label: "a#0", Pid: pid, Online: true}})

	got, ok := s.Get("a#0")
	if !ok || got.Mem == 0 {
		t.Fatalf("Get(a#0) = %+v ok=%v, want non-zero Mem", got, ok)
	}

	stop()
	s.sample(nil) // no live instances → handles pruned
	s.mu.Lock()
	n := len(s.procs)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("procs handles = %d after pruning, want 0", n)
	}
}

func TestSamplerSkipsOfflineInstances(t *testing.T) {
	s := NewSampler(time.Hour)
	s.sample([]Instance{{Label: "a#0", Pid: 99999999, Online: false}})
	if _, ok := s.Get("a#0"); ok {
		t.Fatal("offline instance should not be sampled")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/metrics/`
Expected: FAIL — package/`NewSampler`/`groupPids` undefined.

- [ ] **Step 3: Implement the sampler**

Create `internal/metrics/sampler.go`:

```go
// Package metrics samples live per-instance CPU% and RSS (summed over each
// process group) via gopsutil.
package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// Sample is one instance's latest reading.
type Sample struct {
	Cpu float64 // percent, summed over the process group
	Mem uint64  // RSS bytes, summed over the process group
}

// Instance is the minimal view the sampler needs of a supervised instance.
type Instance struct {
	Label  string
	Pid    int
	Online bool
}

// Sampler periodically samples process-group CPU%/RSS keyed by instance label.
type Sampler struct {
	interval time.Duration

	mu    sync.Mutex
	last  map[string]Sample
	procs map[int32]*process.Process // retained across ticks for CPU% deltas
}

// NewSampler builds a sampler with the given tick interval (default 5s).
func NewSampler(interval time.Duration) *Sampler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Sampler{
		interval: interval,
		last:     map[string]Sample{},
		procs:    map[int32]*process.Process{},
	}
}

// Get returns the latest sample for a label.
func (s *Sampler) Get(label string) (Sample, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.last[label]
	return v, ok
}

// Run samples every interval until ctx is canceled. snapshot returns the
// current instances each tick.
func (s *Sampler) Run(ctx context.Context, snapshot func() []Instance) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.sample(snapshot()) // prime CPU% handles immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sample(snapshot())
		}
	}
}

func (s *Sampler) sample(insts []Instance) {
	live := map[int32]bool{}
	result := make(map[string]Sample, len(insts))
	for _, in := range insts {
		if !in.Online || in.Pid <= 0 {
			continue
		}
		var sum Sample
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
		}
		result[in.Label] = sum
	}
	s.mu.Lock()
	s.last = result
	for pid := range s.procs {
		if !live[pid] {
			delete(s.procs, pid)
		}
	}
	s.mu.Unlock()
}

// handle returns a cached process handle (created on first use), so Percent(0)
// computes a delta against the previous tick.
func (s *Sampler) handle(pid int32) *process.Process {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.procs[pid]; ok {
		return p
	}
	p, err := process.NewProcess(pid)
	if err != nil {
		return nil
	}
	s.procs[pid] = p
	return p
}

// groupPids returns pid plus all descendant pids.
func groupPids(pid int32) []int32 {
	out := []int32{pid}
	p, err := process.NewProcess(pid)
	if err != nil {
		return out
	}
	kids, err := p.Children()
	if err != nil {
		return out
	}
	for _, k := range kids {
		out = append(out, groupPids(k.Pid)...)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go mod tidy && go test ./internal/metrics/`
Expected: PASS. `go mod tidy` pulls `github.com/shirou/gopsutil/v3`.
(If `TestGroupPidsIncludesChildren` is flaky on a slow machine because the child hasn't forked yet, the 200ms sleep covers it; do not loosen the >= 2 assertion otherwise.)

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/sampler.go internal/metrics/sampler_test.go go.mod go.sum
git commit -m "feat(metrics): process-group CPU%/RSS Sampler via gopsutil

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: daemon wiring — registry, sampler, cpu/mem in ProcInfo

**Files:**
- Modify: `internal/daemon/server.go`
- Modify: `internal/daemon/convert.go`
- Test: `internal/daemon/server_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/daemon/server_test.go` (it already imports `context`, `testing`, `manager`, `pb`; add `"time"`):

```go
func TestListIncludesMetricsAfterSample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := newTestRegistry(t)
	mgr := manager.New(ctx, manager.WithLogs(reg))
	sampler := metricsSampler(t)
	srv := &Server{mgr: mgr, logs: reg, metrics: sampler}
	defer mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// wait until online, then sample once
	waitListOnline(t, srv, 1)
	sampler.SampleOnce(srv.testInstances())

	list, err := srv.List(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Procs) != 1 || list.Procs[0].Mem == 0 {
		t.Fatalf("proc = %+v, want non-zero Mem", list.GetProcs())
	}
}
```

Add these test helpers to `server_test.go`:

```go
func newTestRegistry(t *testing.T) *logs.Registry {
	t.Helper()
	return logs.NewRegistry(t.TempDir())
}

func metricsSampler(t *testing.T) *metrics.Sampler {
	t.Helper()
	return metrics.NewSampler(time.Hour)
}

func (s *Server) testInstances() []metrics.Instance { return metricsSnapshot(s.mgr)() }

func waitListOnline(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := srv.List(context.Background(), &pb.Empty{})
		online := 0
		for _, p := range list.GetProcs() {
			if p.GetState() == "online" {
				online++
			}
		}
		if online >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d online", want)
}
```

The test calls `sampler.SampleOnce(...)` for a synchronous one-shot sample. Add that exported method to `internal/metrics/sampler.go` (alongside the existing `sample`):

```go
// SampleOnce performs a single synchronous sample (used by the daemon's tests
// and any caller that wants an immediate reading without waiting for a tick).
func (s *Sampler) SampleOnce(insts []Instance) { s.sample(insts) }
```

Add `"marshal/internal/logs"` and `"marshal/internal/metrics"` to `server_test.go`'s imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestListIncludesMetrics`
Expected: FAIL — `Server` has no `logs`/`metrics` fields, `metricsSnapshot` undefined.

- [ ] **Step 3: Implement daemon wiring**

In `internal/daemon/server.go`:

Add imports `"time"`, `"marshal/internal/logs"`, `"marshal/internal/metrics"`, `"marshal/internal/supervisor"`.

Extend the `Server` struct:

```go
type Server struct {
	pb.UnimplementedDaemonServer
	mgr     *manager.Manager
	store   *store.Store
	logs    *logs.Registry
	metrics *metrics.Sampler
	kill    func() // triggers daemon shutdown (set by Run)
}
```

Replace the four `return toProcList(...)` calls in `Start`, `mutate`, `List`, `Resurrect` with `return s.procList(...)` (same arguments). For example `Start`'s last line becomes `return s.procList(out), nil`, `mutate` ends `return s.procList(snaps), nil`, `List` returns `s.procList(s.mgr.List()), nil`, `Resurrect` ends `return s.procList(out), nil`.

Add the run-options type and the metrics snapshot adapter near the bottom of `server.go`:

```go
type runOptions struct{ sampleInterval time.Duration }

// Option configures Run.
type Option func(*runOptions)

// WithSampleInterval overrides the 5s metrics tick (used by tests).
func WithSampleInterval(d time.Duration) Option {
	return func(o *runOptions) { o.sampleInterval = d }
}

// metricsSnapshot adapts the manager's instance list to the sampler's view.
func metricsSnapshot(m *manager.Manager) func() []metrics.Instance {
	return func() []metrics.Instance {
		snaps := m.List()
		out := make([]metrics.Instance, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, metrics.Instance{
				Label:  s.Label,
				Pid:    s.Pid,
				Online: s.State == supervisor.StateOnline,
			})
		}
		return out
	}
}
```

Rewrite `Run` to build the registry + sampler, pass `WithLogs`, and run the sampler under `serveCtx`:

```go
func Run(ctx context.Context, st *store.Store, opts ...Option) error {
	cfg := runOptions{sampleInterval: 5 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}
	if err := st.EnsureDir(); err != nil {
		return err
	}
	if err := st.EnsureLogsDir(); err != nil {
		return err
	}
	reg := logs.NewRegistry(st.LogsDir())
	mgr := manager.New(ctx, manager.WithLogs(reg))
	sampler := metrics.NewSampler(cfg.sampleInterval)
	if apps, err := st.Load(); err == nil {
		for _, app := range apps {
			_, _ = mgr.Add(app)
		}
	}

	sock := st.SocketPath()
	removeStaleSocket(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	gs := grpc.NewServer()
	srv := &Server{mgr: mgr, store: st, logs: reg, metrics: sampler}
	var once sync.Once
	stopped := make(chan struct{})
	srv.kill = func() { once.Do(func() { close(stopped) }) }
	pb.RegisterDaemonServer(gs, srv)

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sampler.Run(serveCtx, metricsSnapshot(mgr))
	go func() {
		select {
		case <-serveCtx.Done():
		case <-stopped:
		}
		gs.GracefulStop()
	}()

	serveErr := gs.Serve(lis)
	cancel() // unblock the watcher if Serve returned on its own
	mgr.StopAll()
	_ = os.Remove(sock)
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		return serveErr
	}
	return nil
}
```

In `internal/daemon/convert.go`, change `snapshotToProc` to take cpu/mem, replace `toProcList` with a `procList` method on `Server`:

```go
// snapshotToProc converts a manager snapshot + metrics into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot, cpu float64, mem uint64) *pb.ProcInfo {
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
		Cpu:        cpu,
		Mem:        int64(mem),
	}
}

// procList renders snapshots as a ProcList, merging in the latest metrics.
func (srv *Server) procList(snaps []manager.InstanceSnapshot) *pb.ProcList {
	procs := make([]*pb.ProcInfo, 0, len(snaps))
	for _, s := range snaps {
		var cpu float64
		var mem uint64
		if srv.metrics != nil {
			if sm, ok := srv.metrics.Get(s.Label); ok {
				cpu, mem = sm.Cpu, sm.Mem
			}
		}
		procs = append(procs, snapshotToProc(s, cpu, mem))
	}
	return &pb.ProcList{Procs: procs}
}
```

Delete the old `toProcList` function from `convert.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/`
Expected: PASS (new metrics test + all existing daemon tests, since `newTestServer` builds a `Server` with nil `logs`/`metrics` and `procList` nil-guards).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go internal/daemon/convert.go internal/daemon/server_test.go internal/metrics/sampler.go
git commit -m "feat(daemon): wire logs registry + metrics sampler; cpu/mem in ProcInfo

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: daemon `Logs` server-stream handler

**Files:**
- Create: `internal/daemon/logs.go`
- Test: `internal/daemon/logs_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/logs_test.go`:

```go
package daemon

import (
	"context"
	"testing"
	"time"

	"marshal/internal/logs"
	"marshal/internal/manager"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeLogStream is an in-memory pb.Daemon_LogsServer.
type fakeLogStream struct {
	pb.Daemon_LogsServer
	ctx  context.Context
	recv chan *pb.LogLine
}

func newFakeLogStream(ctx context.Context) *fakeLogStream {
	return &fakeLogStream{ctx: ctx, recv: make(chan *pb.LogLine, 256)}
}
func (f *fakeLogStream) Send(l *pb.LogLine) error { f.recv <- l; return nil }
func (f *fakeLogStream) Context() context.Context { return f.ctx }

func TestLogsBackfillNoFollow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	mgr := manager.New(ctx, manager.WithLogs(reg))
	srv := &Server{mgr: mgr, logs: reg}
	defer mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "a", Cmd: "sh", Args: []string{"-c", "echo one; echo two; sleep 30"}, Instances: 1},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait until the ring buffer has both lines.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(reg.For("a#0").Backfill(10)) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	stream := newFakeLogStream(ctx)
	if err := srv.Logs(&pb.LogRequest{Target: "a", Lines: 10, Follow: false}, stream); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	close(stream.recv)
	var texts []string
	for l := range stream.recv {
		texts = append(texts, l.GetLine())
	}
	if len(texts) < 2 || texts[0] != "one" || texts[1] != "two" {
		t.Fatalf("got %v, want [one two ...]", texts)
	}
}

func TestLogsUnknownTargetNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	srv := &Server{mgr: manager.New(ctx, manager.WithLogs(reg)), logs: reg}
	err := srv.Logs(&pb.LogRequest{Target: "ghost"}, newFakeLogStream(ctx))
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestLogsFollowStreamsLiveLines(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	mgr := manager.New(ctx, manager.WithLogs(reg))
	srv := &Server{mgr: mgr, logs: reg}
	defer mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "a", Cmd: "sh", Args: []string{"-c", "i=0; while true; do echo tick-$i; i=$((i+1)); sleep 0.1; done"}, Instances: 1},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	streamCtx, streamCancel := context.WithCancel(ctx)
	stream := newFakeLogStream(streamCtx)
	done := make(chan error, 1)
	go func() { done <- srv.Logs(&pb.LogRequest{Target: "a", Lines: 0, Follow: true}, stream) }()

	select {
	case l := <-stream.recv:
		if l.GetLine() == "" {
			t.Fatalf("empty line")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no live line within 3s")
	}
	streamCancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Logs did not return after stream cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestLogs`
Expected: FAIL — `srv.Logs` not implemented (the embedded `UnimplementedDaemonServer.Logs` returns `codes.Unimplemented`, so assertions fail).

- [ ] **Step 3: Implement the handler**

Create `internal/daemon/logs.go`:

```go
package daemon

import (
	"sort"
	"strconv"
	"strings"

	"marshal/internal/logs"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fanLine struct {
	label string
	line  logs.Line
}

// Logs streams an app's captured output: a backfill of the last N lines, then
// (if follow) live lines until the client disconnects.
func (s *Server) Logs(req *pb.LogRequest, stream pb.Daemon_LogsServer) error {
	if s.logs == nil {
		return status.Error(codes.Unavailable, "logs not configured")
	}
	snaps, err := s.mgr.Describe(req.GetTarget())
	if err != nil {
		return status.Errorf(codes.NotFound, "%v", err)
	}
	labels := make([]string, 0, len(snaps))
	for _, sn := range snaps {
		labels = append(labels, sn.Label)
	}
	labeled := s.logs.ResolveLabeled(labels)

	n := int(req.GetLines())
	for _, fl := range mergeBackfill(labeled, n) {
		if err := stream.Send(lineToProto(fl)); err != nil {
			return err
		}
	}
	if !req.GetFollow() {
		return nil
	}

	agg := make(chan fanLine, 256)
	var cancels []func()
	for _, ls := range labeled {
		ch, cancel := ls.Sink.Subscribe()
		cancels = append(cancels, cancel)
		go func(label string, ch <-chan logs.Line) {
			for ln := range ch {
				select {
				case agg <- fanLine{label: label, line: ln}:
				case <-stream.Context().Done():
					return
				}
			}
		}(ls.Label, ch)
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case fl := <-agg:
			if err := stream.Send(lineToProto(fl)); err != nil {
				return err
			}
		}
	}
}

// mergeBackfill collects each sink's last-n lines, orders them by timestamp,
// and trims to the n most recent overall.
func mergeBackfill(labeled []logs.Labeled, n int) []fanLine {
	var all []fanLine
	for _, ls := range labeled {
		for _, ln := range ls.Sink.Backfill(n) {
			all = append(all, fanLine{label: ls.Label, line: ln})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].line.Ts.Before(all[j].line.Ts) })
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

func lineToProto(fl fanLine) *pb.LogLine {
	name, idx := splitLabel(fl.label)
	return &pb.LogLine{
		Name:       name,
		InstanceId: idx,
		Stderr:     fl.line.Stderr,
		Line:       fl.line.Text,
	}
}

// splitLabel parses "name#idx" into its parts.
func splitLabel(label string) (string, int32) {
	i := strings.LastIndexByte(label, '#')
	if i < 0 {
		return label, 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return label[:i], int32(n)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/`
Expected: PASS (all daemon tests).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/logs.go internal/daemon/logs_test.go
git commit -m "feat(daemon): implement Logs server-stream (backfill + follow)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: CLI error unwrap (polish item 1)

**Files:**
- Modify: `cmd/marshal/main.go`
- Test: `cmd/marshal/main_test.go` (new)

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/main_test.go`:

```go
package main

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCLIErrorStripsGRPCPrefix(t *testing.T) {
	err := status.Error(codes.NotFound, `no app matching "x"`)
	if got := cliError(err); got != `no app matching "x"` {
		t.Fatalf("cliError = %q, want clean message", got)
	}
}

func TestCLIErrorPassesPlainErrors(t *testing.T) {
	if got := cliError(errors.New("boom")); got != "boom" {
		t.Fatalf("cliError = %q, want boom", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestCLIError`
Expected: FAIL — `cliError` undefined.

- [ ] **Step 3: Implement and use it**

In `cmd/marshal/main.go`, add `"google.golang.org/grpc/status"` to imports. Replace `main`:

```go
func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", cliError(err))
		os.Exit(1)
	}
}

// cliError strips the gRPC status wrapper so users see just the message
// (e.g. `no app matching "x"` rather than `rpc error: code = NotFound ...`).
func cliError(err error) string {
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -run TestCLIError`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/main.go cmd/marshal/main_test.go
git commit -m "feat(cli): strip gRPC status prefix from error output

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: CLI `logs` command

**Files:**
- Modify: `cmd/marshal/control.go`
- Modify: `cmd/marshal/main.go`

(No standalone unit test — the streaming path is covered by the daemon handler tests and the Task 13 e2e test. This task wires the CLI surface.)

- [ ] **Step 1: Implement `logsCmd`**

In `cmd/marshal/control.go`, add imports: `"io"`, `"os"`, `"os/signal"`, `"syscall"` (keep existing `client`, `store`, `time`, `context`). Add:

```go
func logsCmd() *cobra.Command {
	var lines int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <name|id|all>",
		Short: "Stream captured stdout/stderr for app(s)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := store.New()
			if err != nil {
				return err
			}
			c, conn, err := client.Connect(st)
			if err != nil {
				return err
			}
			defer conn.Close()

			// Follow streams until Ctrl-C; one-shot backfill gets a 30s cap.
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			if !follow {
				var c2 context.CancelFunc
				ctx, c2 = context.WithTimeout(ctx, 30*time.Second)
				defer c2()
			}

			stream, err := c.Logs(ctx, &pb.LogRequest{Target: args[0], Lines: int32(lines), Follow: follow})
			if err != nil {
				return err
			}
			for {
				ln, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					if ctx.Err() != nil {
						return nil // expected on Ctrl-C
					}
					return err
				}
				printLogLine(cmd, ln)
			}
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 15, "number of backfilled lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new lines as they arrive")
	return cmd
}

// printLogLine writes a tagged log line: stdout lines to stdout, stderr to stderr.
func printLogLine(cmd *cobra.Command, ln *pb.LogLine) {
	w := cmd.OutOrStdout()
	if ln.GetStderr() {
		w = cmd.ErrOrStderr()
	}
	fmt.Fprintf(w, "%s#%d | %s\n", ln.GetName(), ln.GetInstanceId(), ln.GetLine())
}
```

- [ ] **Step 2: Register the command**

In `cmd/marshal/main.go`, add `logsCmd(),` to the `root.AddCommand(...)` list (e.g. right after `describeCmd(),`).

- [ ] **Step 3: Verify it builds and the command is wired**

Run: `go build ./... && go run ./cmd/marshal logs --help`
Expected: build succeeds; help shows `--lines`/`-n` and `--follow`/`-f` flags.

- [ ] **Step 4: Commit**

```bash
git add cmd/marshal/control.go cmd/marshal/main.go
git commit -m "feat(cli): add 'marshal logs' command (backfill + follow)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: CLI CPU/MEM columns

**Files:**
- Modify: `cmd/marshal/control.go`
- Test: `cmd/marshal/main_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/marshal/main_test.go`:

```go
func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1536, "1.5KB"},
		{5 * 1024 * 1024, "5.0MB"},
	}
	for _, c := range cases {
		if got := humanizeBytes(c.in); got != c.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestHumanizeBytes`
Expected: FAIL — `humanizeBytes` undefined.

- [ ] **Step 3: Add `humanizeBytes` and the columns**

In `cmd/marshal/control.go`, add:

```go
func humanizeBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%s", float64(b)/float64(div), []string{"KB", "MB", "GB", "TB"}[exp])
}
```

Rewrite `printProcs` to include CPU and MEM (shown only when online):

```go
// printProcs renders a ProcList as an aligned table.
func printProcs(cmd *cobra.Command, list *pb.ProcList) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tINST\tSTATE\tPID\tCPU\tMEM\tUPTIME\tRESTARTS")
	for _, p := range list.GetProcs() {
		uptime, cpu, mem := "-", "-", "-"
		if p.GetUptimeMs() > 0 {
			uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
		}
		if p.GetState() == "online" {
			cpu = fmt.Sprintf("%.1f%%", p.GetCpu())
			mem = humanizeBytes(p.GetMem())
		}
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%d\t%s\t%s\t%s\t%d\n",
			p.GetId(), p.GetName(), p.GetInstanceId(), p.GetState(), p.GetPid(), cpu, mem, uptime, p.GetRestarts())
	}
	_ = w.Flush()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/marshal/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/control.go cmd/marshal/main_test.go
git commit -m "feat(cli): add CPU/MEM columns to list/describe output

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: `tools.go` — pin protoc plugins (polish item 2)

**Files:**
- Create: `internal/pb/tools.go`
- Modify: `go.mod` / `go.sum` (via `go mod tidy`)

- [ ] **Step 1: Create the build-tagged tools file**

Create `internal/pb/tools.go`:

```go
//go:build tools

// This file pins the protoc plugins used to regenerate the gRPC code (see
// doc.go's go:generate directive). The "tools" build tag keeps it out of the
// compiled binary; `go mod tidy` still records the plugin modules.
package pb

import (
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)
```

- [ ] **Step 2: Tidy modules to record the pins**

Run: `go mod tidy`
Expected: `go.mod` gains `google.golang.org/grpc/cmd/protoc-gen-go-grpc` and `google.golang.org/protobuf/cmd/protoc-gen-go` as direct requirements.

- [ ] **Step 3: Verify the build ignores the tools file**

Run: `go build ./... && go vet ./...`
Expected: success; the `tools`-tagged file is not compiled into `marshal`.

- [ ] **Step 4: Commit**

```bash
git add internal/pb/tools.go go.mod go.sum
git commit -m "build(pb): pin protoc plugins via tools.go for reproducible regen

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 13: end-to-end logs + metrics over the socket

**Files:**
- Modify: `cmd/marshal/daemon_e2e_test.go`

- [ ] **Step 1: Write the failing e2e tests**

Append to `cmd/marshal/daemon_e2e_test.go` (it already imports `context`, `net`, `os`, `testing`, `time`, `client`, `daemon`, `pb`, `store`, `grpc`; add `"io"`):

```go
// startDaemon runs an in-process daemon with a short metrics interval under a
// short /tmp base (socket path limit) and returns a connected client.
func startDaemon(t *testing.T) pb.DaemonClient {
	t.Helper()
	base, err := os.MkdirTemp("/tmp", "marshal-m3")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("XDG_DATA_HOME", base)

	st, err := store.New()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = daemon.Run(ctx, st, daemon.WithSampleInterval(150*time.Millisecond))
	}()
	t.Cleanup(func() { cancel(); <-done })

	c, conn := dialReady(t, st)
	t.Cleanup(func() { _ = conn.Close() })
	return c
}

func TestDaemonLogsE2E(t *testing.T) {
	c := startDaemon(t)
	rpc(t, "Start", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
			{Name: "noisy", Cmd: "sh", Args: []string{"-c", "echo line-one; echo line-two; sleep 30"}, Instances: 1},
		}})
	})
	waitState(t, c, "noisy", "online", 1)

	// Poll Logs (no follow) until both backfilled lines are present.
	deadline := time.Now().Add(3 * time.Second)
	var texts []string
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		stream, err := c.Logs(ctx, &pb.LogRequest{Target: "noisy", Lines: 10, Follow: false})
		if err != nil {
			cancel()
			t.Fatalf("Logs: %v", err)
		}
		texts = texts[:0]
		for {
			ln, err := stream.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			texts = append(texts, ln.GetLine())
		}
		cancel()
		if len(texts) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(texts) < 2 || texts[0] != "line-one" || texts[1] != "line-two" {
		t.Fatalf("logs = %v, want [line-one line-two]", texts)
	}
}

func TestDaemonMetricsE2E(t *testing.T) {
	c := startDaemon(t)
	rpc(t, "Start", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
			{Name: "svc", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 1},
		}})
	})
	waitState(t, c, "svc", "online", 1)

	// Within a few sample intervals, MEM should be populated.
	deadline := time.Now().Add(3 * time.Second)
	var mem int64
	for time.Now().Before(deadline) {
		list := rpc(t, "List", func(ctx context.Context) (*pb.ProcList, error) {
			return c.List(ctx, &pb.Empty{})
		})
		if len(list.GetProcs()) == 1 {
			mem = list.GetProcs()[0].GetMem()
			if mem > 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if mem == 0 {
		t.Fatalf("metrics MEM never populated")
	}
}
```

- [ ] **Step 2: Run the e2e tests to verify they pass**

Run: `go test ./cmd/marshal/ -run 'TestDaemonLogsE2E|TestDaemonMetricsE2E' -v`
Expected: PASS. (They auto-skip nothing; if `dialReady`/socket setup fails, re-check the short `/tmp` base.)

- [ ] **Step 3: Commit**

```bash
git add cmd/marshal/daemon_e2e_test.go
git commit -m "test(cli): e2e logs backfill + live metrics over the socket

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 14: full verification + handoff

**Files:** none (verification + docs)

- [ ] **Step 1: Run the full gate**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
gofmt -l .
go mod tidy
```
Expected: build clean; all tests green under `-race`; `go vet` clean; `gofmt -l .` prints nothing; `go mod tidy` makes no changes (clean `git status`).

If `gofmt -l .` lists files, run `gofmt -w <file>` and re-run the gate, then amend the relevant commit.

- [ ] **Step 2: Smoke-test the real binary**

```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m3smoke && rm -rf "$XDG_DATA_HOME"
printf 'apps:\n  - name: clock\n    cmd: sh\n    args: ["-c","i=0; while true; do echo tick-$i; i=$((i+1)); sleep 1; done"]\n    instances: 2\n' > /tmp/m3.yaml
./marshal start /tmp/m3.yaml
sleep 6
./marshal list            # expect CPU/MEM populated for online procs
./marshal logs clock -n 5 # expect recent tick lines, tagged clock#0 / clock#1
./marshal kill
```
Expected: `list` shows non-`-` CPU/MEM; `logs` prints tagged tick lines; `kill` stops the daemon.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-16-m3-complete.md` per the CLAUDE.md handoff convention: current state (branch `m3-logs-metrics`, merge status), what changed (logs `Sink`/`Registry`, `metrics.Sampler`, `Logs` RPC, `logs` CLI, CPU/MEM columns, error-prefix strip, `tools.go`), build/run/test commands, deferred items (deep history beyond ring buffer, log compression/retention, per-stream view, metric history — all sub-project #2), and the concrete next step (M4 — boot startup via systemd/launchd; finish M3 via the finishing-a-development-branch flow, local `--no-ff` merge to `main`).

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-16-m3-complete.md
git commit -m "docs: M3 completion handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Finish the branch**

Invoke the `superpowers:finishing-a-development-branch` skill to integrate `m3-logs-metrics` into `main` (no git remote → local `--no-ff` merge, as with M1/M2).

---

## Self-Review notes (coverage map)

- Spec §4 Logs (rotated files 10MB×5, ring ~1000, fanout) → Tasks 3–4.
- Spec §5 capture seam (proc writers, manager option, M1 `run` unchanged) → Tasks 1, 5.
- Spec §6 Metrics (process-group CPU%/RSS, 5s, daemon-owned, manager metrics-free) → Tasks 6–7.
- Spec §7 Logs RPC + `marshal logs` (merged, tagged, `-n`/`-f`, ring-only backfill) → Tasks 8, 10.
- Spec §8 list/describe CPU/MEM columns → Task 11.
- Spec §9 proto/build (no contract change; `tools.go`; error-prefix strip) → Tasks 9, 12.
- Spec §10 testing (logs unit, metrics unit, daemon e2e, full race gate) → Tasks 3, 6, 13, 14.
- Store logs dir (0700) → Task 2.
