# M6 — Log history & retention Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `marshal logs` read deep history from the rotated files on disk (beyond the 1000-line ring, surviving daemon restarts), add age-based retention + gzip compression, add a per-stream `--stdout`/`--stderr` view, and fix two carried log bugs.

**Architecture:** Keep the raw byte log files (Approach A). A new `internal/logs/filetail.go` reads the last N lines of one stream newest→oldest across rotated/`.gz` segments. `logs.Policy` carries retention/compression into the two `lumberjack.Logger`s. The daemon's `Logs` handler routes backfill: ring for the exact merged-recent window, files for per-stream and deep/cold history. Two bug fixes: a 64 KiB partial-line cap in `Sink`, and an atomic snapshot+subscribe primitive that closes the backfill→subscribe race.

**Tech Stack:** Go 1.26, `gopkg.in/natefinch/lumberjack.v2` (existing), gRPC/protobuf (protoc 35.0), cobra, stdlib `compress/gzip`.

Spec: `docs/superpowers/specs/2026-06-17-marshal-agent-core-m6-log-history-design.md`

## Global Constraints

- Go module path is `marshal`; imports are `marshal/internal/...`.
- TDD: write the failing test first, run it red, then the minimal implementation, run it green, commit.
- No new third-party dependency — retention/compression is configuration on the existing lumberjack loggers; gzip read uses stdlib `compress/gzip`.
- Instance label format is `name#idx` (e.g. `web#0`); log files are `<label>.out.log` / `<label>.err.log`.
- Persisted log lines have **no timestamp** on disk; lines read from files carry `Stderr` set and a zero `Ts`. The wire protocol does not send timestamps, so display is unaffected.
- Keep existing constructors (`newSink`, `newSinkWithLimits`, `NewRegistry(dir)`) signature-compatible so every task builds green; new behaviour is added via new fields/constructors that default to current behaviour.
- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Feature work on branch `m6-log-history` (cut from `main`), never directly on `main`.
- Final gate must be green: `go build ./...`, `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (lists nothing).

---

## File structure

New:
- `internal/logs/filetail.go` — `fileBackfill(dir,label,stderr,n)` + line reading (gzip-aware). Test: `internal/logs/filetail_test.go`.

Modified:
- `internal/logs/sink.go` — `Policy`, policy-aware constructor (lumberjack `MaxAge`/`Compress`), 64 KiB partial-line cap, `RingSaturated`, `SubscribeWithRing`, `FileBackfill`.
- `internal/logs/registry.go` — default + per-app policy map; `SetDefaultPolicy`, `SetPolicy`; policy selection in `For`.
- `internal/config/config.go` — `App.Logs *LogRetention` (pointer fields for correct default fallback).
- `internal/daemon/server.go` — `WithLogRetention` Option; push default + per-app policy into the registry; `Server.logPolicyDefault` field.
- `internal/daemon/logs.go` — stream filter + ring-vs-file backfill routing; atomic follow via `SubscribeWithRing`.
- `internal/daemon/convert.go` — read the retention block out of `AppSpec` into `config.App.Logs`.
- `proto/marshal/v1/daemon.proto` (+ regenerated `internal/pb/daemon{,_grpc}.pb.go`) — `LogStream` enum + `LogRequest.stream`; `LogRetention` message + `AppSpec.logs` so per-app retention survives the `start` hop.
- `cmd/marshal/control.go` — write `a.Logs` into `AppSpec` in `appToSpec`; `--stdout`/`--stderr` flags on `logs`.

---

## Setup

- [ ] **Create the feature branch**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout main
git checkout -b m6-log-history
```

---

## Task 1: `logs.Policy` + retention/compression in `Sink`

Introduce the policy value type and wire `MaxAge`/`Compress` into the lumberjack loggers. Keep existing constructors working by delegating to a new policy-aware one.

**Files:**
- Modify: `internal/logs/sink.go`
- Test: `internal/logs/sink_test.go`

**Interfaces:**
- Produces: `type Policy struct { MaxSizeMB, MaxBackups, MaxAgeDays int; Compress bool }`; `var DefaultPolicy = Policy{MaxSizeMB: 10, MaxBackups: 5, MaxAgeDays: 14, Compress: true}`; `func newSinkP(dir, label string, p Policy, now func() time.Time) *Sink`.

- [ ] **Step 1: Write the failing test**

Add to `internal/logs/sink_test.go`:

```go
func TestPolicyReachesLoggers(t *testing.T) {
	p := Policy{MaxSizeMB: 7, MaxBackups: 3, MaxAgeDays: 9, Compress: true}
	s := newSinkP(t.TempDir(), "app#0", p, stepClock())
	defer s.Close()
	if s.outFile.MaxSize != 7 || s.outFile.MaxBackups != 3 || s.outFile.MaxAge != 9 || !s.outFile.Compress {
		t.Fatalf("out logger not configured from policy: %+v", s.outFile)
	}
	if s.errFile.MaxAge != 9 || !s.errFile.Compress {
		t.Fatalf("err logger not configured from policy: %+v", s.errFile)
	}
}

func TestDefaultPolicyAppliedByNewSink(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	if s.outFile.MaxAge != DefaultPolicy.MaxAgeDays || !s.outFile.Compress {
		t.Fatalf("newSink did not apply DefaultPolicy: %+v", s.outFile)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run 'TestPolicyReachesLoggers|TestDefaultPolicyAppliedByNewSink' -v`
Expected: FAIL — `newSinkP` undefined / `DefaultPolicy` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/logs/sink.go`, replace the two constructor functions and add the policy type. Keep `maxSizeMB`/`maxBackups` consts for `DefaultPolicy`’s values:

```go
// Policy controls log-file rotation, retention, and compression for one sink.
type Policy struct {
	MaxSizeMB  int  // rotate threshold in MB (lumberjack MaxSize)
	MaxBackups int  // rotated files kept (lumberjack MaxBackups)
	MaxAgeDays int  // delete rotated files older than this many days (0 = no age limit)
	Compress   bool // gzip rotated files
}

// DefaultPolicy is the daemon-wide default when an app declares no override.
var DefaultPolicy = Policy{MaxSizeMB: maxSizeMB, MaxBackups: maxBackups, MaxAgeDays: 14, Compress: true}

func newSink(dir, label string, now func() time.Time) *Sink {
	return newSinkP(dir, label, DefaultPolicy, now)
}

// newSinkWithLimits is retained for existing tests; size/backups only, no age/compress.
func newSinkWithLimits(dir, label string, sizeMB, backups int, now func() time.Time) *Sink {
	return newSinkP(dir, label, Policy{MaxSizeMB: sizeMB, MaxBackups: backups}, now)
}

func newSinkP(dir, label string, p Policy, now func() time.Time) *Sink {
	mk := func(suffix string) *lumberjack.Logger {
		return &lumberjack.Logger{
			Filename:   filepath.Join(dir, label+suffix),
			MaxSize:    p.MaxSizeMB,
			MaxBackups: p.MaxBackups,
			MaxAge:     p.MaxAgeDays,
			Compress:   p.Compress,
		}
	}
	return &Sink{
		outFile: mk(".out.log"),
		errFile: mk(".err.log"),
		now:     now,
		ring:    newRing(ringCap),
		subs:    map[int]chan Line{},
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logs/ -v`
Expected: PASS (all existing logs tests plus the two new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/logs/sink.go internal/logs/sink_test.go
git commit -m "feat(logs): add Policy with age/compress retention on sinks

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Per-app policy selection in `Registry`

The registry picks a `Policy` per app name (default otherwise) at sink-creation time.

**Files:**
- Modify: `internal/logs/registry.go`
- Test: `internal/logs/registry_test.go`

**Interfaces:**
- Consumes: `Policy`, `DefaultPolicy`, `newSinkP` (Task 1).
- Produces: `func (r *Registry) SetDefaultPolicy(p Policy)`; `func (r *Registry) SetPolicy(app string, p Policy)`; `For(label)` now creates sinks with the resolved policy.

- [ ] **Step 1: Write the failing test**

Add to `internal/logs/registry_test.go`:

```go
func TestRegistryPerAppPolicy(t *testing.T) {
	r := NewRegistry(t.TempDir())
	r.SetDefaultPolicy(Policy{MaxSizeMB: 1, MaxBackups: 1, MaxAgeDays: 2, Compress: false})
	r.SetPolicy("web", Policy{MaxSizeMB: 9, MaxBackups: 4, MaxAgeDays: 30, Compress: true})

	web := r.For("web#0")
	if web.outFile.MaxSize != 9 || web.outFile.MaxAge != 30 || !web.outFile.Compress {
		t.Fatalf("web#0 did not get web policy: %+v", web.outFile)
	}
	other := r.For("api#0")
	if other.outFile.MaxSize != 1 || other.outFile.MaxAge != 2 || other.outFile.Compress {
		t.Fatalf("api#0 did not get default policy: %+v", other.outFile)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestRegistryPerAppPolicy -v`
Expected: FAIL — `SetDefaultPolicy`/`SetPolicy` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/logs/registry.go`, add policy fields and selection. Add `"strings"` to imports.

```go
type Registry struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	sinks    map[string]*Sink
	def      Policy
	policies map[string]Policy // by app name (label without #idx)
}

func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir, now: time.Now, sinks: map[string]*Sink{}, def: DefaultPolicy, policies: map[string]Policy{}}
}

// SetDefaultPolicy sets the fallback policy for apps without an override.
func (r *Registry) SetDefaultPolicy(p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.def = p
}

// SetPolicy registers a per-app retention policy, keyed by app name.
func (r *Registry) SetPolicy(app string, p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies[app] = p
}

func (r *Registry) policyFor(label string) Policy {
	name := label
	if i := strings.LastIndexByte(label, '#'); i >= 0 {
		name = label[:i]
	}
	if p, ok := r.policies[name]; ok {
		return p
	}
	return r.def
}
```

Then change `For` to use the policy:

```go
func (r *Registry) For(label string) *Sink {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sinks[label]
	if !ok {
		s = newSinkP(r.dir, label, r.policyFor(label), r.now)
		r.sinks[label] = s
	}
	return s
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logs/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/registry.go internal/logs/registry_test.go
git commit -m "feat(logs): select retention policy per app in registry

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Partial-line cap (bug fix 1)

A stream that never emits `\n` must not grow `outPart`/`errPart` without bound.

**Files:**
- Modify: `internal/logs/sink.go`
- Test: `internal/logs/sink_test.go`

**Interfaces:**
- Produces: `const maxLineBytes = 64 * 1024` — a newline-less partial is flushed as a synthetic `Line` once it reaches this cap.

- [ ] **Step 1: Write the failing test**

Add to `internal/logs/sink_test.go`:

```go
func TestPartialLineCapForcesFlush(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	w := s.Writer(false)
	// 200 KiB with no newline must not stay buffered; it flushes in <=64 KiB chunks.
	if _, err := w.Write(bytes.Repeat([]byte("x"), 200*1024)); err != nil {
		t.Fatal(err)
	}
	got := s.Backfill(0)
	if len(got) < 3 {
		t.Fatalf("expected >=3 forced-flush lines, got %d", len(got))
	}
	for _, ln := range got {
		if len(ln.Text) > maxLineBytes {
			t.Fatalf("line exceeds cap: %d bytes", len(ln.Text))
		}
	}
}
```

(Confirm `bytes` is already imported in the test file; add it if not.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestPartialLineCapForcesFlush -v`
Expected: FAIL — only 0 lines emitted (everything stays in the partial buffer) / `maxLineBytes` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/logs/sink.go`, add the constant near the other consts:

```go
const maxLineBytes = 64 * 1024 // force-flush a newline-less line at this size
```

In `write`, after the newline-splitting `for` loop, add a cap check before `return`:

```go
		s.emit(Line{Ts: s.now(), Stderr: stderr, Text: text})
	}
	if len(*part) >= maxLineBytes {
		s.emit(Line{Ts: s.now(), Stderr: stderr, Text: string(*part)})
		*part = (*part)[:0]
	}
	return len(p), nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/logs/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/sink.go internal/logs/sink_test.go
git commit -m "fix(logs): cap newline-less partial line at 64 KiB

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Deep file backfill (`filetail.go`) + `Sink.FileBackfill` + `RingSaturated`

Read the last N lines of one stream from disk, newest→oldest across rotated/`.gz` segments.

**Files:**
- Create: `internal/logs/filetail.go`
- Test: `internal/logs/filetail_test.go`
- Modify: `internal/logs/sink.go` (add `FileBackfill`, `RingSaturated`)

**Interfaces:**
- Produces: `func fileBackfill(dir, label string, stderr bool, n int) ([]Line, error)` (lines oldest→newest, at most `n`, `Stderr` set, `Ts` zero); `func (s *Sink) FileBackfill(stderr bool, n int) ([]Line, error)`; `func (s *Sink) RingSaturated() bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/logs/filetail_test.go`:

```go
package logs

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func TestFileBackfillAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	// Oldest -> newest. Rotated names sort lexically by lumberjack's timestamp.
	writeGz(t, filepath.Join(dir, "app#0.out-2026-06-17T10-00-00.000.log.gz"), "a1\na2\n")
	writeFile(t, filepath.Join(dir, "app#0.out-2026-06-17T11-00-00.000.log"), "b1\nb2\n")
	writeFile(t, filepath.Join(dir, "app#0.out.log"), "c1\nc2\n") // active = newest

	got, err := fileBackfill(dir, "app#0", false, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a2", "b1", "b2", "c1", "c2"}
	if g := texts(got); !equalStrings(g, want) {
		t.Fatalf("got %v want %v", g, want)
	}
}

func TestFileBackfillStreamSeparation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app#0.out.log"), "out1\nout2\n")
	writeFile(t, filepath.Join(dir, "app#0.err.log"), "err1\n")
	got, err := fileBackfill(dir, "app#0", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if g := texts(got); !equalStrings(g, []string{"err1"}) {
		t.Fatalf("stderr stream: got %v", g)
	}
}

func TestFileBackfillNoFiles(t *testing.T) {
	got, err := fileBackfill(t.TempDir(), "missing#0", false, 10)
	if err != nil {
		t.Fatalf("absent files must not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", texts(got))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestFileBackfill -v`
Expected: FAIL — `fileBackfill` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `internal/logs/filetail.go`:

```go
package logs

import (
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fileBackfill returns up to n lines from one stream's on-disk segments,
// scanning newest segment first and stopping once n lines are gathered.
// Returned lines are ordered oldest->newest, carry Stderr set and a zero Ts,
// and absent files are not an error.
func fileBackfill(dir, label string, stderr bool, n int) ([]Line, error) {
	stream := "out"
	if stderr {
		stream = "err"
	}
	base := label + "." + stream // e.g. "app#0.out"
	active := base + ".log"

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rotated []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, base+"-") && (strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz")) {
			rotated = append(rotated, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rotated))) // newest first
	order := append([]string{active}, rotated...)      // active is newest

	var segs [][]string // newest segment first
	total := 0
	for _, fn := range order {
		if n > 0 && total >= n {
			break
		}
		lines, err := readSegmentLines(filepath.Join(dir, fn))
		if err != nil {
			return nil, err
		}
		if len(lines) == 0 {
			continue
		}
		segs = append(segs, lines)
		total += len(lines)
	}

	var out []Line
	for i := len(segs) - 1; i >= 0; i-- { // oldest segment first
		for _, t := range segs[i] {
			out = append(out, Line{Stderr: stderr, Text: t})
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// readSegmentLines reads all newline-terminated lines from a log segment,
// gunzipping when the path ends in .gz. A missing file yields no lines.
func readSegmentLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // concurrent rotation removed it; skip
		}
		return nil, err
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, nil // truncated/partial gz; skip rather than fail the whole read
		}
		defer zr.Close()
		r = zr
	}

	var lines []string
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lines = append(lines, strings.TrimSuffix(line, "\n"))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	// A trailing newline yields a final empty element; drop it.
	if k := len(lines); k > 0 && lines[k-1] == "" {
		lines = lines[:k-1]
	}
	return lines, nil
}
```

- [ ] **Step 4: Add `FileBackfill` and `RingSaturated` to `Sink`**

In `internal/logs/sink.go`, add methods. `FileBackfill` derives `dir` from the logger filename:

```go
// FileBackfill returns up to n lines for one stream read from the rotated
// files on disk (deep history that outlives the in-memory ring).
func (s *Sink) FileBackfill(stderr bool, n int) ([]Line, error) {
	f := s.outFile.Filename
	if stderr {
		f = s.errFile.Filename
	}
	dir := filepath.Dir(f)
	// base file name is "<label>.out.log"; strip ".out.log"/".err.log" to recover the label.
	name := filepath.Base(f)
	label := strings.TrimSuffix(strings.TrimSuffix(name, ".err.log"), ".out.log")
	return fileBackfill(dir, label, stderr, n)
}

// RingSaturated reports whether the in-memory ring has wrapped — i.e. older
// history exists on disk beyond what the ring can serve.
func (s *Sink) RingSaturated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ring.full
}
```

Add `"strings"` to `sink.go` imports (it already imports `path/filepath` and `bytes`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/logs/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/logs/filetail.go internal/logs/filetail_test.go internal/logs/sink.go
git commit -m "feat(logs): deep on-disk backfill across rotated/gz segments

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Atomic snapshot + subscribe (bug fix 2)

Close the backfill→subscribe race in `logs -f` by snapshotting the ring and registering the subscriber under one lock.

**Files:**
- Modify: `internal/logs/sink.go`
- Test: `internal/logs/sink_test.go`

**Interfaces:**
- Produces: `func (s *Sink) SubscribeWithRing(n int) (backfill []Line, live <-chan Line, cancel func())` — every emitted line lands in exactly one of {backfill, live}; no gap, no duplicate.

- [ ] **Step 1: Write the failing test**

Add to `internal/logs/sink_test.go`:

```go
func TestSubscribeWithRingNoGapNoDup(t *testing.T) {
	s := newSink(t.TempDir(), "app#0", stepClock())
	defer s.Close()
	w := s.Writer(false)
	// Pre-existing history in the ring.
	for i := 0; i < 5; i++ {
		_, _ = w.Write([]byte(fmt.Sprintf("pre-%d\n", i)))
	}
	backfill, live, cancel := s.SubscribeWithRing(100)
	defer cancel()
	// New lines after the atomic snapshot must arrive only on the live channel.
	for i := 0; i < 5; i++ {
		_, _ = w.Write([]byte(fmt.Sprintf("post-%d\n", i)))
	}
	seen := map[string]int{}
	for _, ln := range backfill {
		seen[ln.Text]++
	}
	for i := 0; i < 5; i++ {
		ln := <-live
		seen[ln.Text]++
	}
	for i := 0; i < 5; i++ {
		if seen[fmt.Sprintf("pre-%d", i)] != 1 {
			t.Fatalf("pre-%d seen %d times", i, seen[fmt.Sprintf("pre-%d", i)])
		}
		if seen[fmt.Sprintf("post-%d", i)] != 1 {
			t.Fatalf("post-%d seen %d times", i, seen[fmt.Sprintf("post-%d", i)])
		}
	}
}
```

(Confirm `fmt` is imported in the test file; add it if not.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestSubscribeWithRingNoGapNoDup -v`
Expected: FAIL — `SubscribeWithRing` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/logs/sink.go`, add (mirrors `Subscribe`, but snapshots the ring under the same lock):

```go
// SubscribeWithRing atomically snapshots the last n ring lines and registers a
// live follower under one lock, so no line falls between backfill and live and
// none is delivered twice. Call cancel to unsubscribe.
func (s *Sink) SubscribeWithRing(n int) ([]Line, <-chan Line, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	backfill := s.ring.last(n)
	ch := make(chan Line, subBuffer)
	if s.closed {
		close(ch)
		return backfill, ch, func() {}
	}
	id := s.nextID
	s.nextID++
	s.subs[id] = ch
	return backfill, ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass (with race)**

Run: `go test ./internal/logs/ -race -v`
Expected: PASS, no race.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/sink.go internal/logs/sink_test.go
git commit -m "fix(logs): atomic ring snapshot + subscribe to close follow race

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Per-app `logs:` config block

Let `marshal.yaml` override retention per app. Pointer fields so each omitted field falls back to the default (and an explicit `max_age_days: 0` means "no age limit", not "unset").

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `type LogRetention struct { MaxSizeMB, MaxBackups, MaxAgeDays *int; Compress *bool }` with yaml/json tags; `App.Logs *LogRetention`.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go` (create the file if absent, with `package config` and the imports below):

```go
func TestLoadLogRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marshal.yaml")
	yaml := "apps:\n" +
		"  - name: api\n" +
		"    cmd: ./api\n" +
		"    logs:\n" +
		"      max_size_mb: 50\n" +
		"      max_age_days: 0\n" +
		"      compress: false\n" +
		"  - name: web\n" +
		"    cmd: ./web\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	api := cfg.Apps[0]
	if api.Logs == nil || api.Logs.MaxSizeMB == nil || *api.Logs.MaxSizeMB != 50 {
		t.Fatalf("api max_size_mb not parsed: %+v", api.Logs)
	}
	if api.Logs.MaxAgeDays == nil || *api.Logs.MaxAgeDays != 0 {
		t.Fatalf("explicit max_age_days:0 must be preserved, not nil")
	}
	if api.Logs.Compress == nil || *api.Logs.Compress != false {
		t.Fatalf("compress:false must be preserved")
	}
	if api.Logs.MaxBackups != nil {
		t.Fatalf("omitted max_backups must stay nil for default fallback")
	}
	if cfg.Apps[1].Logs != nil {
		t.Fatalf("web has no logs block; want nil")
	}
}
```

Ensure the test file imports `os`, `path/filepath`, `testing`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadLogRetention -v`
Expected: FAIL — `Logs` field undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/config/config.go`, add the type and field. Place `LogRetention` above `App` and add the field to `App`:

```go
// LogRetention overrides per-app log rotation/retention. Nil fields fall back
// to the daemon default; a non-nil pointer is honoured verbatim.
type LogRetention struct {
	MaxSizeMB  *int  `yaml:"max_size_mb" json:"max_size_mb,omitempty"`
	MaxBackups *int  `yaml:"max_backups" json:"max_backups,omitempty"`
	MaxAgeDays *int  `yaml:"max_age_days" json:"max_age_days,omitempty"`
	Compress   *bool `yaml:"compress" json:"compress,omitempty"`
}
```

Add to the `App` struct (after `KillTimeout`):

```go
	Logs        *LogRetention     `yaml:"logs" json:"logs,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add per-app logs retention block

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: `LogStream` enum, `LogRequest.stream`, and `AppSpec.logs` in proto

The stream selector for backfill, plus a `LogRetention` message on `AppSpec` so a per-app `logs:` block survives the CLI→daemon `start` hop (without it, `appToSpec`/`appSpecToConfig` silently drop the override and the feature only ever uses the default).

**Files:**
- Modify: `proto/marshal/v1/daemon.proto`
- Regenerate: `internal/pb/daemon.pb.go`, `internal/pb/daemon_grpc.pb.go`

**Interfaces:**
- Produces: `pb.LogStream_LOG_STREAM_UNSPECIFIED|STDOUT|STDERR`; `LogRequest.GetStream() pb.LogStream`; `pb.LogRetention` with `optional` scalar fields (presence via pointer getters); `AppSpec.GetLogs() *pb.LogRetention`.

- [ ] **Step 1: Edit the proto**

In `proto/marshal/v1/daemon.proto`, add the enum just above `LogRequest`, add `stream` to `LogRequest`, add the `LogRetention` message, and add `logs` to `AppSpec` (use proto3 `optional` so unset fields are distinguishable from zero — `max_age_days: 0` legitimately means "no age limit"):

```proto
enum LogStream {
  LOG_STREAM_UNSPECIFIED = 0; // merged (default)
  LOG_STREAM_STDOUT      = 1;
  LOG_STREAM_STDERR      = 2;
}

message LogRequest {
  string    target = 1;
  int32     lines  = 2;
  bool      follow = 3;
  LogStream stream = 4; // M6
}

message LogRetention {
  optional int32 max_size_mb  = 1;
  optional int32 max_backups  = 2;
  optional int32 max_age_days = 3;
  optional bool  compress     = 4;
}
```

Add to `message AppSpec` (after `kill_timeout = 9;`):

```proto
  optional LogRetention logs = 10; // M6 per-app retention override
```

- [ ] **Step 2: Regenerate**

Run: `go generate ./internal/pb`
Expected: no output; `git status` shows modified `internal/pb/daemon.pb.go` (and `_grpc` unchanged is fine).

- [ ] **Step 3: Verify it compiles and the symbols exist**

Run: `go build ./... && go doc marshal/internal/pb LogStream && go doc marshal/internal/pb LogRetention`
Expected: build succeeds; doc lists the three `LogStream_*` constants and the `LogRetention` fields.

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/daemon.proto internal/pb/daemon.pb.go internal/pb/daemon_grpc.pb.go
git commit -m "feat(proto): add LogStream selector + AppSpec.logs retention

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Daemon backfill routing + atomic follow + policy plumbing

Wire the stream filter and ring-vs-file routing into `Logs`, switch follow to the atomic primitive, and push policies from config into the registry.

**Files:**
- Modify: `internal/daemon/logs.go`, `internal/daemon/server.go`
- Test: `internal/daemon/logs_test.go`

**Interfaces:**
- Consumes: `Sink.FileBackfill`, `Sink.RingSaturated`, `Sink.SubscribeWithRing` (Tasks 4–5); `Registry.SetDefaultPolicy`/`SetPolicy` (Task 2); `pb.LogStream` (Task 7); `config.LogRetention` (Task 6).
- Produces: `func WithLogRetention(p logs.Policy) Option`; helpers `streamMatch(pb.LogStream, bool) bool` and `func backfillLines(labeled []logs.Labeled, n int, st pb.LogStream) []fanLine`.

- [ ] **Step 1: Write the failing test for the pure helpers**

Add to `internal/daemon/logs_test.go`:

```go
func TestStreamMatch(t *testing.T) {
	cases := []struct {
		f      pb.LogStream
		stderr bool
		want   bool
	}{
		{pb.LogStream_LOG_STREAM_UNSPECIFIED, false, true},
		{pb.LogStream_LOG_STREAM_UNSPECIFIED, true, true},
		{pb.LogStream_LOG_STREAM_STDOUT, false, true},
		{pb.LogStream_LOG_STREAM_STDOUT, true, false},
		{pb.LogStream_LOG_STREAM_STDERR, true, true},
		{pb.LogStream_LOG_STREAM_STDERR, false, false},
	}
	for _, c := range cases {
		if got := streamMatch(c.f, c.stderr); got != c.want {
			t.Fatalf("streamMatch(%v,%v)=%v want %v", c.f, c.stderr, got, c.want)
		}
	}
}

func TestBackfillRoutingPerStreamReadsFiles(t *testing.T) {
	reg := logs.NewRegistry(t.TempDir())
	out, errw := reg.WriterPair("app#0")
	_, _ = out.Write([]byte("o1\no2\n"))
	_, _ = errw.Write([]byte("e1\n"))
	labeled := reg.ResolveLabeled([]string{"app#0"})

	got := backfillLines(labeled, 10, pb.LogStream_LOG_STREAM_STDERR)
	if len(got) != 1 || got[0].line.Text != "e1" || !got[0].line.Stderr {
		t.Fatalf("stderr-only backfill wrong: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestStreamMatch|TestBackfillRoutingPerStreamReadsFiles' -v`
Expected: FAIL — `streamMatch`/`backfillLines` undefined.

- [ ] **Step 3: Implement the helpers and rewrite the handler**

In `internal/daemon/logs.go`, add the helpers and rework `Logs`. Replace the file body (keeping `fanLine`, `lineToProto`, `splitLabel`) with:

```go
// streamMatch reports whether a line on the given stream passes the filter.
func streamMatch(f pb.LogStream, stderr bool) bool {
	switch f {
	case pb.LogStream_LOG_STREAM_STDOUT:
		return !stderr
	case pb.LogStream_LOG_STREAM_STDERR:
		return stderr
	default:
		return true
	}
}

// backfillLines chooses the backfill source per the M6 routing rule:
//   - per-stream filter -> read that stream from files (deep, restart-durable);
//   - merged -> ring when it still holds all history; else files (best-effort merge).
func backfillLines(labeled []logs.Labeled, n int, st pb.LogStream) []fanLine {
	if st == pb.LogStream_LOG_STREAM_STDOUT || st == pb.LogStream_LOG_STREAM_STDERR {
		stderr := st == pb.LogStream_LOG_STREAM_STDERR
		var all []fanLine
		for _, ls := range labeled {
			lines, _ := ls.Sink.FileBackfill(stderr, n)
			for _, ln := range lines {
				all = append(all, fanLine{label: ls.Label, line: ln})
			}
		}
		return trimTail(all, n)
	}

	// Merged: prefer the exact ring window unless a ring has wrapped and we
	// still want more than it can serve.
	ring := mergeBackfill(labeled, n)
	if n == 0 || len(ring) >= n || !anyRingSaturated(labeled) {
		return ring
	}
	var all []fanLine
	for _, ls := range labeled {
		for _, stderr := range []bool{false, true} {
			lines, _ := ls.Sink.FileBackfill(stderr, n)
			for _, ln := range lines {
				all = append(all, fanLine{label: ls.Label, line: ln})
			}
		}
	}
	return trimTail(all, n)
}

func anyRingSaturated(labeled []logs.Labeled) bool {
	for _, ls := range labeled {
		if ls.Sink.RingSaturated() {
			return true
		}
	}
	return false
}

func trimTail(all []fanLine, n int) []fanLine {
	if n > 0 && len(all) > n {
		return all[len(all)-n:]
	}
	return all
}
```

Then rewrite `Logs` to use the filter and the atomic follow:

```go
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
	st := req.GetStream()
	n := int(req.GetLines())

	if !req.GetFollow() {
		for _, fl := range backfillLines(labeled, n, st) {
			if err := stream.Send(lineToProto(fl)); err != nil {
				return err
			}
		}
		return nil
	}

	// Follow: atomically snapshot the ring and subscribe per sink (closes the
	// backfill->subscribe race); deeper file history is not replayed for -f.
	agg := make(chan fanLine, 256)
	var cancels []func()
	var bf []fanLine
	for _, ls := range labeled {
		ring, ch, cancel := ls.Sink.SubscribeWithRing(n)
		cancels = append(cancels, cancel)
		for _, ln := range ring {
			if streamMatch(st, ln.Stderr) {
				bf = append(bf, fanLine{label: ls.Label, line: ln})
			}
		}
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

	sortByTs(bf)
	for _, fl := range trimTail(bf, n) {
		if err := stream.Send(lineToProto(fl)); err != nil {
			return err
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case fl := <-agg:
			if streamMatch(st, fl.line.Stderr) {
				if err := stream.Send(lineToProto(fl)); err != nil {
					return err
				}
			}
		}
	}
}
```

Extract the existing sort in `mergeBackfill` into a shared helper so both paths use it; add near `mergeBackfill`:

```go
func sortByTs(all []fanLine) {
	sort.SliceStable(all, func(i, j int) bool { return all[i].line.Ts.Before(all[j].line.Ts) })
}
```

and replace the inline `sort.SliceStable(...)` in `mergeBackfill` with `sortByTs(all)`.

- [ ] **Step 4: Add `WithLogRetention` and policy plumbing in `server.go`**

In `internal/daemon/server.go`:

Add to `runOptions`: `logRetention logs.Policy`. Default it in `Run`’s `cfg` literal: `logRetention: logs.DefaultPolicy`. Add the Option:

```go
// WithLogRetention overrides the default log retention/compression policy.
func WithLogRetention(p logs.Policy) Option {
	return func(o *runOptions) { o.logRetention = p }
}
```

After `reg := logs.NewRegistry(st.LogsDir())` in `Run`, push the default and per-app policies:

```go
	reg.SetDefaultPolicy(cfg.logRetention)
	if apps, err := st.Load(); err == nil {
		for _, app := range apps {
			reg.SetPolicy(app.Name, logPolicy(app, cfg.logRetention))
		}
	}
```

(There is already an `if apps, err := st.Load(); err == nil { ... mgr.Add ... }` block below; leave that one as-is — this added block only sets policies and must run before any sink is created.)

Add the conversion helper (new file `internal/daemon/logpolicy.go` to keep `server.go` focused):

```go
package daemon

import (
	"marshal/internal/config"
	"marshal/internal/logs"
)

// logPolicy resolves an app's effective log policy: the default with any
// per-app override fields applied.
func logPolicy(app config.App, def logs.Policy) logs.Policy {
	p := def
	if app.Logs == nil {
		return p
	}
	if app.Logs.MaxSizeMB != nil {
		p.MaxSizeMB = *app.Logs.MaxSizeMB
	}
	if app.Logs.MaxBackups != nil {
		p.MaxBackups = *app.Logs.MaxBackups
	}
	if app.Logs.MaxAgeDays != nil {
		p.MaxAgeDays = *app.Logs.MaxAgeDays
	}
	if app.Logs.Compress != nil {
		p.Compress = *app.Logs.Compress
	}
	return p
}
```

Store the effective default on the `Server` so the `Start` RPC can resolve per-app policy without threading `cfg`. In the `Server` struct (`internal/daemon/server.go`), add a field:

```go
	logPolicyDefault logs.Policy // effective default log policy (from WithLogRetention)
```

Set it when constructing `srv` in `Run` — change the literal to include `logPolicyDefault: cfg.logRetention`:

```go
	srv := &Server{mgr: mgr, store: st, logs: reg, metrics: sampler, mdb: mdb, logPolicyDefault: cfg.logRetention}
```

Then in `server.go`’s `Start` handler, inside `for _, spec := range req.GetApps()`, after `app, err := appSpecToConfig(spec)` succeeds and before `s.mgr.Add(app)`, register the app’s policy:

```go
		s.logs.SetPolicy(app.Name, logPolicy(app, s.logPolicyDefault))
```

- [ ] **Step 5: Read the retention block out of `AppSpec` (convert.go) with a test**

The override only reaches `app.Logs` if `appSpecToConfig` copies it. Add to `internal/daemon/convert_test.go` (create if absent, `package daemon`):

```go
func TestAppSpecToConfigReadsLogs(t *testing.T) {
	sz := int32(50)
	age := int32(0)
	comp := false
	app, err := appSpecToConfig(&pb.AppSpec{
		Name: "api", Cmd: "./api",
		Logs: &pb.LogRetention{MaxSizeMb: &sz, MaxAgeDays: &age, Compress: &comp},
	})
	if err != nil {
		t.Fatal(err)
	}
	if app.Logs == nil || app.Logs.MaxSizeMB == nil || *app.Logs.MaxSizeMB != 50 {
		t.Fatalf("max_size_mb not copied: %+v", app.Logs)
	}
	if app.Logs.MaxAgeDays == nil || *app.Logs.MaxAgeDays != 0 {
		t.Fatalf("explicit age 0 must be preserved")
	}
	if app.Logs.MaxBackups != nil {
		t.Fatalf("omitted max_backups must stay nil")
	}
}
```

Run it red: `go test ./internal/daemon/ -run TestAppSpecToConfigReadsLogs -v` → FAIL (`app.Logs` nil).

In `internal/daemon/convert.go`, inside `appSpecToConfig`, before `cfg := config.Config{...}`, add:

```go
	if lr := s.GetLogs(); lr != nil {
		app.Logs = &config.LogRetention{}
		if lr.MaxSizeMb != nil {
			v := int(lr.GetMaxSizeMb())
			app.Logs.MaxSizeMB = &v
		}
		if lr.MaxBackups != nil {
			v := int(lr.GetMaxBackups())
			app.Logs.MaxBackups = &v
		}
		if lr.MaxAgeDays != nil {
			v := int(lr.GetMaxAgeDays())
			app.Logs.MaxAgeDays = &v
		}
		if lr.Compress != nil {
			v := lr.GetCompress()
			app.Logs.Compress = &v
		}
	}
```

(Ensure `config` is imported in `convert.go` — it already is.)

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -race -v`
Expected: PASS (new helper + convert tests plus existing `logs_test.go`/`server_test.go`).

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/logs.go internal/daemon/server.go internal/daemon/logpolicy.go internal/daemon/convert.go internal/daemon/logs_test.go internal/daemon/convert_test.go
git commit -m "feat(daemon): route log backfill by stream + file tier; atomic follow; per-app policy

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: CLI — send per-app retention in `appToSpec`; `--stdout` / `--stderr` flags

**Files:**
- Modify: `cmd/marshal/control.go`
- Test: `cmd/marshal/control_test.go` (create if absent)

**Interfaces:**
- Consumes: `pb.LogStream`, `pb.LogRetention` (Task 7).
- Produces: `func streamFromFlags(stdoutOnly, stderrOnly bool) (pb.LogStream, error)` — both true is an error; `appToSpec` now sets `AppSpec.Logs` from `a.Logs`.

- [ ] **Step 1: Write the failing test for retention round-trip**

Create/append `cmd/marshal/control_test.go`:

```go
package main

import (
	"testing"

	"marshal/internal/config"
	"marshal/internal/pb"
)

func TestAppToSpecWritesLogs(t *testing.T) {
	sz := 50
	comp := false
	a := config.App{Name: "api", Cmd: "./api", Logs: &config.LogRetention{MaxSizeMB: &sz, Compress: &comp}}
	spec := appToSpec(a)
	if spec.GetLogs() == nil || spec.GetLogs().MaxSizeMb == nil || *spec.GetLogs().MaxSizeMb != 50 {
		t.Fatalf("max_size_mb not sent: %+v", spec.GetLogs())
	}
	if spec.GetLogs().Compress == nil || *spec.GetLogs().Compress != false {
		t.Fatalf("compress:false not sent")
	}
	if spec.GetLogs().MaxBackups != nil {
		t.Fatalf("omitted max_backups must stay nil")
	}
	// No logs block -> nil, not an empty message.
	if appToSpec(config.App{Name: "web", Cmd: "./web"}).GetLogs() != nil {
		t.Fatalf("absent logs block must send nil")
	}
}
```

(Confirm the conversion function is named `appToSpec`; if the file names it differently, use that name in the test and Step 3.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestAppToSpecWritesLogs -v`
Expected: FAIL — `spec.GetLogs()` is nil.

- [ ] **Step 3: Write `appToSpec` retention mapping**

In `cmd/marshal/control.go`, in `appToSpec`, build the spec then attach logs (replace the bare `return &pb.AppSpec{...}` with a named local + conditional):

```go
	spec := &pb.AppSpec{
		Name:        a.Name,
		Cmd:         a.Cmd,
		Args:        a.Args,
		Cwd:         a.Cwd,
		Instances:   int32(a.Instances),
		Env:         a.Env,
		Restart:     string(a.Restart),
		MaxRestarts: int32(a.MaxRestarts),
		KillTimeout: a.KillTimeout.Duration.String(),
	}
	if a.Logs != nil {
		lr := &pb.LogRetention{}
		if a.Logs.MaxSizeMB != nil {
			v := int32(*a.Logs.MaxSizeMB)
			lr.MaxSizeMb = &v
		}
		if a.Logs.MaxBackups != nil {
			v := int32(*a.Logs.MaxBackups)
			lr.MaxBackups = &v
		}
		if a.Logs.MaxAgeDays != nil {
			v := int32(*a.Logs.MaxAgeDays)
			lr.MaxAgeDays = &v
		}
		if a.Logs.Compress != nil {
			v := *a.Logs.Compress
			lr.Compress = &v
		}
		spec.Logs = lr
	}
	return spec
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -run TestAppToSpecWritesLogs -v`
Expected: PASS.

- [ ] **Step 5: Add the `--stdout`/`--stderr` flag test**

Append to `cmd/marshal/control_test.go`:

```go
func TestStreamFromFlags(t *testing.T) {
	if s, err := streamFromFlags(false, false); err != nil || s != pb.LogStream_LOG_STREAM_UNSPECIFIED {
		t.Fatalf("merged: got %v err %v", s, err)
	}
	if s, err := streamFromFlags(true, false); err != nil || s != pb.LogStream_LOG_STREAM_STDOUT {
		t.Fatalf("stdout: got %v err %v", s, err)
	}
	if s, err := streamFromFlags(false, true); err != nil || s != pb.LogStream_LOG_STREAM_STDERR {
		t.Fatalf("stderr: got %v err %v", s, err)
	}
	if _, err := streamFromFlags(true, true); err == nil {
		t.Fatal("both flags must be rejected")
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestStreamFromFlags -v`
Expected: FAIL — `streamFromFlags` undefined.

- [ ] **Step 7: Write minimal implementation**

In `cmd/marshal/control.go`, add the helper and wire two flags into `logsCmd`. Add the helper:

```go
func streamFromFlags(stdoutOnly, stderrOnly bool) (pb.LogStream, error) {
	switch {
	case stdoutOnly && stderrOnly:
		return pb.LogStream_LOG_STREAM_UNSPECIFIED, fmt.Errorf("--stdout and --stderr are mutually exclusive")
	case stdoutOnly:
		return pb.LogStream_LOG_STREAM_STDOUT, nil
	case stderrOnly:
		return pb.LogStream_LOG_STREAM_STDERR, nil
	default:
		return pb.LogStream_LOG_STREAM_UNSPECIFIED, nil
	}
}
```

Add `"fmt"` to the imports if not present. In `logsCmd`, add flag vars and registration alongside `lines`/`follow`:

```go
	var stdoutOnly, stderrOnly bool
	// ... inside command setup, after existing flag definitions:
	cmd.Flags().BoolVar(&stdoutOnly, "stdout", false, "show only stdout")
	cmd.Flags().BoolVar(&stderrOnly, "stderr", false, "show only stderr")
```

In the `RunE`, compute the stream before the gRPC call and include it in the request. Note the existing handler already has a `st, err := store.New()` local, so name the selector `streamSel` to avoid shadowing:

```go
			streamSel, errFlag := streamFromFlags(stdoutOnly, stderrOnly)
			if errFlag != nil {
				return errFlag
			}
			// ... existing store/connect/ctx setup (st, err := store.New(); ...) ...
			stream, err := c.Logs(ctx, &pb.LogRequest{
				Target: args[0], Lines: int32(lines), Follow: follow, Stream: streamSel,
			})
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -run 'TestStreamFromFlags|TestAppToSpecWritesLogs' -v && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 9: Commit**

```bash
git add cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): send per-app log retention; add --stdout/--stderr to logs

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: Full gate + real-binary smoke

**Files:** none (verification only).

- [ ] **Step 1: Format, vet, build**

Run: `gofmt -l . && go vet ./... && go build ./...`
Expected: `gofmt -l .` prints nothing; vet and build succeed.

- [ ] **Step 2: Full race test**

Run: `go test ./... -race -count=1`
Expected: all packages PASS.

- [ ] **Step 3: Real-binary smoke — deep backfill survives restart**

```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m6smoke
rm -rf "$XDG_DATA_HOME"
# An app that prints >1000 lines to stdout and some to stderr, then idles.
cat > /tmp/m6app.yaml <<'YAML'
apps:
  - name: noisy
    cmd: bash
    args: ["-c", "for i in $(seq 1 1500); do echo out-$i; echo err-$i 1>&2; done; sleep 600"]
    logs: { max_age_days: 7, compress: true }
YAML
./marshal start /tmp/m6app.yaml
sleep 3
# Restart the daemon to empty the in-memory ring, proving disk backfill.
./marshal kill; sleep 1
./marshal start /tmp/m6app.yaml    # re-attaches; ring is cold
./marshal logs noisy -n 1200 | tail -n 3          # deep merged from files
./marshal logs noisy -n 5 --stderr                # per-stream, exact
ls "$XDG_DATA_HOME"/marshal/logs/                 # *.out.log / *.err.log present
./marshal kill
```
Expected: `logs -n 1200` returns ~1200 lines read from disk after a restart (more than the 1000-line ring); `--stderr` shows only `err-*` lines; the logs dir holds the rotated files.

- [ ] **Step 4: Update CLAUDE.md run section (if needed) and write the handoff**

Per the repo handoff convention, write `docs/handoffs/2026-06-17-m6-log-history.md` describing final state, the merge step (local `--no-ff` to `main` via the finishing-a-development-branch flow), and the next milestone (sub-project #3 — central server). Then commit.

```bash
git add docs/handoffs/2026-06-17-m6-log-history.md
git commit -m "docs: M6 log-history completion handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Done criteria

- `marshal logs <app> -n N` returns up to N lines read from disk, exceeding the ring and surviving a daemon restart.
- `marshal logs <app> --stdout` / `--stderr` shows exactly one stream.
- Rotated log files are gzip-compressed and age-deleted per the default (10 MB × 5, 14 days, compress) or per-app `logs:` override.
- A newline-less line cannot grow `Sink` memory past 64 KiB.
- `logs -f` neither drops nor duplicates a line across the backfill→live boundary (race test green).
- Full gate green: `go build ./...`, `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (empty).
```
