# Marshal M8 — Metric Records Up + Server-Side Storage — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Marshal central server durably stores per-agent CPU/mem metric history streamed up by agents and serves it back via `marshal fleet metrics`.

**Architecture:** The agent's fleet client ships raw samples from its local `metricstore` over the existing `Fleet.Connect` stream, sending only rows newer than a watermark the server reports in `HelloAck` (`max(ts)` from that agent's store). Live push and outage-backfill are the same mechanism. The server persists each agent's samples in a per-agent SQLite file (reusing `internal/metricstore` unchanged) and answers history queries with the existing query/merge logic, which is moved into `metricstore` so both daemon and server share it.

**Tech Stack:** Go 1.26, gRPC (`google.golang.org/grpc`), Protocol Buffers (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`), pure-Go SQLite (`modernc.org/sqlite`), Cobra CLI.

## Global Constraints

- Go module path is `marshal`; imports are `marshal/internal/...`. (verbatim from CLAUDE.md)
- TDD: write the failing test first, then implementation. Keep packages small and focused. (verbatim from CLAUDE.md)
- Commit messages: imperative subject; co-author trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. (verbatim from CLAUDE.md)
- Feature work on a branch, not `main`. Branch for this milestone: `m8-metric-storage`.
- Full gate before finishing: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (must list nothing), `go build ./...`.
- Proto regeneration: `go generate ./internal/pb` (requires `protoc` 35.0 on PATH; the directive already covers `fleet.proto`).
- Transport stays **plaintext / unauthenticated** (TLS + auth are M10). Do not add credentials.
- **Metrics only.** Logs streaming/storage is M8b — do not implement it here.

---

## Task 0: Create the feature branch

- [ ] **Step 1: Branch from main**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout main
git checkout -b m8-metric-storage
git log --oneline -1   # expect 859c49d docs: M8 ... design (or later)
```

---

## Task 1: `metricstore` read accessors (`SamplesSince`, `MaxTs`, `Labels`)

**Files:**
- Modify: `internal/metricstore/store.go`
- Test: `internal/metricstore/store_test.go`

**Interfaces:**
- Consumes: existing `Store`, `Sample`, `Open`, `Append`.
- Produces:
  - `type TimestampedSample struct { TsMs int64; Label string; Cpu float64; Mem uint64 }`
  - `func (s *Store) SamplesSince(tsMs int64) ([]TimestampedSample, error)` — rows with `ts > tsMs`, ordered by `ts` ascending.
  - `func (s *Store) MaxTs() (int64, error)` — the largest `ts`, or `0` when empty.
  - `func (s *Store) Labels() ([]string, error)` — distinct labels, ascending.

- [ ] **Step 1: Write the failing test**

Add to `internal/metricstore/store_test.go`:

```go
func TestSamplesSinceMaxTsLabels(t *testing.T) {
	st, err := Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if mx, err := st.MaxTs(); err != nil || mx != 0 {
		t.Fatalf("empty MaxTs = %d, %v; want 0, nil", mx, err)
	}

	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 10, Mem: 100}})
	_ = st.Append(2000, []Sample{{Label: "a#0", Cpu: 20, Mem: 200}, {Label: "b#0", Cpu: 5, Mem: 50}})

	got, err := st.SamplesSince(1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("SamplesSince(1000) len = %d, want 2 (strictly newer than 1000)", len(got))
	}
	if got[0].TsMs != 2000 || (got[0].Label != "a#0" && got[0].Label != "b#0") {
		t.Fatalf("unexpected first row: %+v", got[0])
	}

	mx, err := st.MaxTs()
	if err != nil || mx != 2000 {
		t.Fatalf("MaxTs = %d, %v; want 2000, nil", mx, err)
	}

	labels, err := st.Labels()
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 || labels[0] != "a#0" || labels[1] != "b#0" {
		t.Fatalf("Labels = %v, want [a#0 b#0]", labels)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/metricstore/ -run TestSamplesSinceMaxTsLabels -v`
Expected: FAIL — `st.MaxTs undefined`, `st.SamplesSince undefined`, `st.Labels undefined`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/metricstore/store.go` (after the `Sample` type, add `TimestampedSample`; methods near `Query`):

```go
// TimestampedSample is one stored row with its timestamp.
type TimestampedSample struct {
	TsMs  int64
	Label string
	Cpu   float64
	Mem   uint64
}

// SamplesSince returns raw rows with ts strictly greater than tsMs, oldest first.
func (s *Store) SamplesSince(tsMs int64) ([]TimestampedSample, error) {
	rows, err := s.db.Query(`SELECT ts, label, cpu, mem FROM samples WHERE ts > ? ORDER BY ts`, tsMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimestampedSample
	for rows.Next() {
		var ts int64
		var label string
		var cpu float64
		var mem int64
		if err := rows.Scan(&ts, &label, &cpu, &mem); err != nil {
			return nil, err
		}
		out = append(out, TimestampedSample{TsMs: ts, Label: label, Cpu: cpu, Mem: uint64(mem)})
	}
	return out, rows.Err()
}

// MaxTs returns the largest stored ts, or 0 when the store is empty.
func (s *Store) MaxTs() (int64, error) {
	var mx sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(ts) FROM samples`).Scan(&mx); err != nil {
		return 0, err
	}
	if !mx.Valid {
		return 0, nil
	}
	return mx.Int64, nil
}

// Labels returns the distinct sample labels, ascending.
func (s *Store) Labels() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT label FROM samples ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/metricstore/ -run TestSamplesSinceMaxTsLabels -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metricstore/store.go internal/metricstore/store_test.go
git commit -m "feat(metricstore): add SamplesSince, MaxTs, Labels accessors

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Move bucket merge + auto-width into `metricstore` (shared by daemon & server)

**Files:**
- Modify: `internal/metricstore/store.go`
- Test: `internal/metricstore/store_test.go`
- Modify: `internal/daemon/metrics.go` (use the shared helpers, delete local copies)
- Modify: `internal/daemon/metrics_test.go` (point the existing merge test at the new symbol)

**Interfaces:**
- Produces:
  - `func MergeBuckets(series [][]Bucket) []Bucket` — averages summed across series sharing a ts, maxes max'd, oldest first. (Moved verbatim from `internal/daemon`.)
  - `func AutoBucketMs(sinceMs, bucketMs int64) int64` — returns `bucketMs` if `> 0`, else `sinceMs/60` floored at `1000`.
- Consumes (daemon): the two functions above replace the package-private `mergeBuckets` and the inline bucket defaulting.

- [ ] **Step 1: Write the failing test**

Add to `internal/metricstore/store_test.go`:

```go
func TestMergeBucketsAndAutoBucket(t *testing.T) {
	a := []Bucket{{TsMs: 1000, CpuAvg: 10, CpuMax: 15, MemAvg: 100, MemMax: 150}}
	b := []Bucket{{TsMs: 1000, CpuAvg: 5, CpuMax: 20, MemAvg: 50, MemMax: 60}}
	got := MergeBuckets([][]Bucket{a, b})
	if len(got) != 1 || got[0].CpuAvg != 15 || got[0].CpuMax != 20 || got[0].MemAvg != 150 || got[0].MemMax != 150 {
		t.Fatalf("MergeBuckets = %+v, want summed avgs + max maxes", got)
	}
	if w := AutoBucketMs(0, 600000); w != 600000 {
		t.Fatalf("AutoBucketMs explicit = %d, want 600000", w)
	}
	if w := AutoBucketMs(60000, 0); w != 1000 {
		t.Fatalf("AutoBucketMs auto-floored = %d, want 1000", w)
	}
	if w := AutoBucketMs(600000, 0); w != 10000 {
		t.Fatalf("AutoBucketMs auto = %d, want 10000 (600000/60)", w)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/metricstore/ -run TestMergeBucketsAndAutoBucket -v`
Expected: FAIL — `MergeBuckets undefined`, `AutoBucketMs undefined`.

- [ ] **Step 3: Add the shared helpers to `metricstore`**

Add to `internal/metricstore/store.go` (add `import "sort"` to the import block):

```go
const (
	targetBuckets = 60
	minBucketMs   = 1000
)

// AutoBucketMs returns bucketMs when positive, else ~targetBuckets buckets over
// the window, floored at minBucketMs.
func AutoBucketMs(sinceMs, bucketMs int64) int64 {
	if bucketMs > 0 {
		return bucketMs
	}
	b := sinceMs / targetBuckets
	if b < minBucketMs {
		b = minBucketMs
	}
	return b
}

// MergeBuckets combines per-instance series sharing a bucket timestamp: averages
// are summed (whole-app total), maxes take the max across instances. Oldest first.
func MergeBuckets(series [][]Bucket) []Bucket {
	byTs := map[int64]*Bucket{}
	var order []int64
	for _, bs := range series {
		for _, b := range bs {
			cur, ok := byTs[b.TsMs]
			if !ok {
				nb := b
				byTs[b.TsMs] = &nb
				order = append(order, b.TsMs)
				continue
			}
			cur.CpuAvg += b.CpuAvg
			cur.MemAvg += b.MemAvg
			if b.CpuMax > cur.CpuMax {
				cur.CpuMax = b.CpuMax
			}
			if b.MemMax > cur.MemMax {
				cur.MemMax = b.MemMax
			}
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]Bucket, 0, len(order))
	for _, ts := range order {
		out = append(out, *byTs[ts])
	}
	return out
}
```

- [ ] **Step 4: Run the metricstore test to verify it passes**

Run: `go test ./internal/metricstore/ -run TestMergeBucketsAndAutoBucket -v`
Expected: PASS.

- [ ] **Step 5: Refactor the daemon to use the shared helpers**

In `internal/daemon/metrics.go`: delete the local `mergeBuckets` function and the `targetBuckets`/`minBucketMs` consts (keep `defaultHistory`). Replace the bucket-defaulting block and the merge call:

Change the `bucketMs` defaulting from:
```go
	bucketMs := req.GetBucketMs()
	if bucketMs <= 0 {
		bucketMs = sinceMs / targetBuckets
		if bucketMs < minBucketMs {
			bucketMs = minBucketMs
		}
	}
```
to:
```go
	bucketMs := metricstore.AutoBucketMs(sinceMs, req.GetBucketMs())
```

Change `for _, b := range mergeBuckets(series) {` to `for _, b := range metricstore.MergeBuckets(series) {`.

In `internal/daemon/metrics_test.go`: change the merge test's call from `mergeBuckets(...)` to `metricstore.MergeBuckets(...)` (the test already imports `metricstore`).

- [ ] **Step 6: Run daemon + metricstore tests to verify they pass**

Run: `go test ./internal/daemon/ ./internal/metricstore/ -count=1`
Expected: ok (both packages).

- [ ] **Step 7: Commit**

```bash
git add internal/metricstore/ internal/daemon/metrics.go internal/daemon/metrics_test.go
git commit -m "refactor(metricstore): share MergeBuckets and AutoBucketMs with daemon

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Proto — `MetricBatch`, `HelloAck.last_metric_ts_ms`, `FleetMetricsHistory`

**Files:**
- Modify: `proto/marshal/v1/fleet.proto`
- Regenerate: `internal/pb/fleet.pb.go`, `internal/pb/fleet_grpc.pb.go`

**Interfaces:**
- Produces (generated Go in package `pb`): `MetricSample{TsMs, Label, Cpu, Mem}`, `MetricBatch{Samples []*MetricSample}`, `AgentMessage_Metrics` oneof wrapper, `HelloAck{LastMetricTsMs int64}`, `FleetMetricsHistoryRequest{AgentName, Selector, SinceMs, BucketMs}`, and `FleetClient.FleetMetricsHistory` / `FleetServer.FleetMetricsHistory` reusing `MetricsHistoryResponse`.

- [ ] **Step 1: Edit `proto/marshal/v1/fleet.proto`**

Add the RPC to the `Fleet` service (after `ListFleet`):
```proto
  // Metric history for one agent's app/instance (M8); reuses daemon.proto's response.
  rpc FleetMetricsHistory(FleetMetricsHistoryRequest) returns (MetricsHistoryResponse);
```

Add the `metrics` arm to the `AgentMessage` oneof:
```proto
    MetricBatch metrics = 3; // raw CPU/mem samples (M8)
```

Replace `message HelloAck {}` with:
```proto
message HelloAck {
  int64 last_metric_ts_ms = 1; // server's stored high-water-mark for this agent (0 = none)
}
```

Add these messages at the end of the file:
```proto
// One stored metric row, flattened to map 1:1 to a metricstore row.
message MetricSample {
  int64  ts_ms = 1;
  string label = 2; // "app#instance"
  double cpu   = 3;
  int64  mem   = 4;
}

message MetricBatch { repeated MetricSample samples = 1; }

message FleetMetricsHistoryRequest {
  string agent_name = 1;
  string selector   = 2; // app name or "name#instance" label
  int64  since_ms   = 3;
  int64  bucket_ms  = 4;
}
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `go generate ./internal/pb`
Expected: no output; `internal/pb/fleet.pb.go` and `internal/pb/fleet_grpc.pb.go` updated.

- [ ] **Step 3: Verify it builds**

Run: `go build ./...`
Expected: success (the M7 server still compiles — `HelloAck{}` literal in `server.go` stays valid with the new optional field).

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/fleet.pb.go internal/pb/fleet_grpc.pb.go
git commit -m "feat(proto): add MetricBatch, HelloAck watermark, FleetMetricsHistory

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Server per-agent metric stores (`internal/server/stores.go`)

**Files:**
- Create: `internal/server/stores.go`
- Test: `internal/server/stores_test.go`

**Interfaces:**
- Consumes: `marshal/internal/metricstore`.
- Produces:
  - `type stores struct { ... }`
  - `func newStores(dir string) *stores`
  - `func (s *stores) get(agent string) (*metricstore.Store, error)` — lazily creates `<dir>/agents/<sanitized>/metrics.db` and caches the open handle.
  - `func (s *stores) has(agent string) bool` — true iff that agent's db dir exists.
  - `func (s *stores) closeAll() error`
  - `func sanitizeAgent(name string) string` — replaces path separators and `..` with `_`; empty → `_`.

- [ ] **Step 1: Write the failing test**

Create `internal/server/stores_test.go`:

```go
package server

import (
	"path/filepath"
	"testing"
)

func TestStoresLazyOpenAndHas(t *testing.T) {
	dir := t.TempDir()
	ss := newStores(dir)
	defer ss.closeAll()

	if ss.has("web-1") {
		t.Fatal("has(web-1) true before any contact")
	}
	st, err := ss.get("web-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := st.Append(1000, []sampleAlias{{Label: "a#0", Cpu: 1, Mem: 1}}[0:0]...); err != nil {
		_ = err // not exercising Append here
	}
	if !ss.has("web-1") {
		t.Fatal("has(web-1) false after get")
	}
	// Same handle returned on second get.
	st2, _ := ss.get("web-1")
	if st != st2 {
		t.Fatal("get returned a different handle for the same agent")
	}
	if _, err := filepath.Abs(filepath.Join(dir, "agents", "web-1", "metrics.db")); err != nil {
		t.Fatal(err)
	}
}

func TestSanitizeAgent(t *testing.T) {
	cases := map[string]string{
		"web-1":      "web-1",
		"a/b":        "a_b",
		"../etc":     "___etc",
		"":           "_",
		"x\\y":       "x_y",
	}
	for in, want := range cases {
		if got := sanitizeAgent(in); got != want {
			t.Errorf("sanitizeAgent(%q) = %q, want %q", in, got, want)
		}
	}
}
```

Note: the `sampleAlias` reference above is only to avoid an unused import; replace the `st.Append(...)` line with a no-op assertion if simpler — see Step 3 for the final minimal test. (The implementer should keep the test compiling; the canonical version is below.)

Replace the body of `TestStoresLazyOpenAndHas` with this simpler, compiling version:

```go
func TestStoresLazyOpenAndHas(t *testing.T) {
	dir := t.TempDir()
	ss := newStores(dir)
	defer ss.closeAll()

	if ss.has("web-1") {
		t.Fatal("has(web-1) true before any contact")
	}
	st, err := ss.get("web-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ss.has("web-1") {
		t.Fatal("has(web-1) false after get")
	}
	st2, _ := ss.get("web-1")
	if st != st2 {
		t.Fatal("get returned a different handle for the same agent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run 'TestStores|TestSanitize' -v`
Expected: FAIL — `newStores undefined`, `sanitizeAgent undefined`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/server/stores.go`:

```go
package server

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"marshal/internal/metricstore"
)

// stores manages lazily-opened per-agent metric stores under a data dir.
type stores struct {
	dir string
	mu  sync.Mutex
	m   map[string]*metricstore.Store
}

func newStores(dir string) *stores {
	return &stores{dir: dir, m: map[string]*metricstore.Store{}}
}

func (s *stores) agentDir(agent string) string {
	return filepath.Join(s.dir, "agents", sanitizeAgent(agent))
}

// get returns the agent's store, opening (and creating its directory) on first use.
func (s *stores) get(agent string) (*metricstore.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sanitizeAgent(agent)
	if st, ok := s.m[key]; ok {
		return st, nil
	}
	dir := s.agentDir(agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st, err := metricstore.Open(filepath.Join(dir, "metrics.db"))
	if err != nil {
		return nil, err
	}
	s.m[key] = st
	return st, nil
}

// has reports whether the agent's store directory exists on disk.
func (s *stores) has(agent string) bool {
	if _, err := os.Stat(s.agentDir(agent)); err == nil {
		return true
	}
	return false
}

func (s *stores) closeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, st := range s.m {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// sanitizeAgent turns an agent name into a safe single path segment.
func sanitizeAgent(name string) string {
	if name == "" {
		return "_"
	}
	r := strings.NewReplacer("/", "_", "\\", "_", ".", "_")
	return r.Replace(name)
}
```

Note on `sanitizeAgent`: `..` → `__` then `.`→`_` gives `__`? Verify against the test: `"../etc"` → replace `/`→`_`, `\`→`_`, `.`→`_`: `.` `.` `/` `e...` → `_` `_` `_` `etc` = `___etc`. ✓. `"a/b"`→`a_b` ✓. `"x\\y"`→`x_y` ✓.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ -run 'TestStores|TestSanitize' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/stores.go internal/server/stores_test.go
git commit -m "feat(server): per-agent lazily-opened metric stores

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Server ingest — `HelloAck` watermark, `MetricBatch` append, empty-name rejection

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `stores` (Task 4), `metricstore` (`Append`, `MaxTs`), generated `pb.MetricBatch`/`HelloAck`/`AgentMessage_Metrics`.
- Produces:
  - `func NewServer(reg *Registry, ss *stores) *Server` — **signature change**: now takes the stores. `ss` may be `nil` (storage disabled) — guard all store use.
  - `Connect` now: rejects empty `Hello.agent_name` with `codes.InvalidArgument`; sends `HelloAck{LastMetricTsMs: max(ts)}`; on `MetricBatch`, groups by ts and appends.
- For later tasks: `Server` gains a `stores *stores` field.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go` (use a fake bidi stream; check the existing test file for a fakeStream helper and reuse it — if none exists, add the minimal one below):

```go
// fakeConnectStream is a minimal in-memory Fleet_ConnectServer for tests.
type fakeConnectStream struct {
	grpc.ServerStream
	ctx    context.Context
	recv   []*pb.AgentMessage
	sent   []*pb.ServerMessage
	recvAt int
}

func (f *fakeConnectStream) Context() context.Context { return f.ctx }
func (f *fakeConnectStream) Send(m *pb.ServerMessage) error {
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeConnectStream) Recv() (*pb.AgentMessage, error) {
	if f.recvAt >= len(f.recv) {
		return nil, io.EOF
	}
	m := f.recv[f.recvAt]
	f.recvAt++
	return m, nil
}

func TestConnectRejectsEmptyName(t *testing.T) {
	srv := NewServer(NewRegistry(), newStores(t.TempDir()))
	st := &fakeConnectStream{ctx: context.Background(), recv: []*pb.AgentMessage{
		{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: ""}}},
	}}
	err := srv.Connect(st)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Connect with empty name err = %v, want InvalidArgument", err)
	}
}

func TestConnectAcksWatermarkAndStoresBatch(t *testing.T) {
	ss := newStores(t.TempDir())
	srv := NewServer(NewRegistry(), ss)

	// Pre-seed one sample so the second connect sees a non-zero watermark.
	pre, _ := ss.get("web-1")
	_ = pre.Append(5000, []metricstore.Sample{{Label: "api#0", Cpu: 1, Mem: 1}})

	st := &fakeConnectStream{ctx: context.Background(), recv: []*pb.AgentMessage{
		{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: "web-1"}}},
		{Msg: &pb.AgentMessage_Metrics{Metrics: &pb.MetricBatch{Samples: []*pb.MetricSample{
			{TsMs: 6000, Label: "api#0", Cpu: 12, Mem: 100},
			{TsMs: 6000, Label: "api#1", Cpu: 8, Mem: 80},
			{TsMs: 7000, Label: "api#0", Cpu: 20, Mem: 200},
		}}}},
	}}
	if err := srv.Connect(st); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// First sent message is the HelloAck carrying the watermark (5000).
	ack := st.sent[0].GetHelloAck()
	if ack == nil || ack.GetLastMetricTsMs() != 5000 {
		t.Fatalf("HelloAck watermark = %v, want 5000", st.sent[0])
	}
	// The batch landed: max(ts) is now 7000.
	store, _ := ss.get("web-1")
	if mx, _ := store.MaxTs(); mx != 7000 {
		t.Fatalf("after batch MaxTs = %d, want 7000", mx)
	}
}
```

Ensure the test file imports: `context`, `io`, `testing`, `marshal/internal/metricstore`, `marshal/internal/pb`, `google.golang.org/grpc`, `google.golang.org/grpc/codes`, `google.golang.org/grpc/status`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run 'TestConnect' -v`
Expected: FAIL — `NewServer` arg count mismatch / empty name not rejected / no watermark.

- [ ] **Step 3: Write minimal implementation**

In `internal/server/server.go`: add the field and change `NewServer`, the `Connect` Hello/Metrics handling. Replace the struct + constructor + `Connect`:

```go
// Server implements pb.FleetServer backed by an in-memory Registry and, when
// configured, per-agent metric storage.
type Server struct {
	pb.UnimplementedFleetServer
	reg    *Registry
	stores *stores
}

// NewServer wires a Fleet server to a registry and (optional) metric stores.
func NewServer(reg *Registry, ss *stores) *Server { return &Server{reg: reg, stores: ss} }

// Connect terminates one agent's upstream: reads Hello (acking the stored metric
// high-water-mark), StateSnapshot (live state), and MetricBatch (persisted).
func (s *Server) Connect(stream pb.Fleet_ConnectServer) error {
	var name string
	for {
		msg, err := stream.Recv()
		if err != nil {
			if name != "" {
				s.reg.Close(name)
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch m := msg.GetMsg().(type) {
		case *pb.AgentMessage_Hello:
			name = m.Hello.GetAgentName()
			if name == "" {
				return status.Error(codes.InvalidArgument, "agent_name must not be empty")
			}
			s.reg.Open(name)
			var watermark int64
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					watermark, _ = st.MaxTs()
				}
			}
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: watermark},
			}})
		case *pb.AgentMessage_Snapshot:
			if name != "" {
				s.reg.Update(name, m.Snapshot.GetProcs())
			}
		case *pb.AgentMessage_Metrics:
			if name != "" && s.stores != nil {
				s.storeBatch(name, m.Metrics.GetSamples())
			}
		}
	}
}

// storeBatch groups a flattened batch by ts and appends each group oldest-first,
// so the store's max(ts) always reflects a fully-committed prefix.
func (s *Server) storeBatch(agent string, samples []*pb.MetricSample) {
	st, err := s.stores.get(agent)
	if err != nil {
		log.Printf("fleet: open store for %s: %v", agent, err)
		return
	}
	byTs := map[int64][]metricstore.Sample{}
	var order []int64
	for _, sm := range samples {
		ts := sm.GetTsMs()
		if _, ok := byTs[ts]; !ok {
			order = append(order, ts)
		}
		byTs[ts] = append(byTs[ts], metricstore.Sample{
			Label: sm.GetLabel(), Cpu: sm.GetCpu(), Mem: uint64(sm.GetMem()),
		})
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, ts := range order {
		if err := st.Append(ts, byTs[ts]); err != nil {
			log.Printf("fleet: append for %s: %v", agent, err)
			return
		}
	}
}
```

Update the imports of `internal/server/server.go` to add: `log`, `sort`, `marshal/internal/metricstore`, `google.golang.org/grpc/codes`, `google.golang.org/grpc/status`. (`io`, `context`, `net`, `marshal/internal/pb`, `google.golang.org/grpc` already present.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run 'TestConnect' -v`
Expected: PASS.

- [ ] **Step 5: Run the whole server package (catch M7 test breakage from the `NewServer` signature)**

Run: `go test ./internal/server/ -count=1`
Expected: ok — if an existing M7 test calls `NewServer(reg)`, update it to `NewServer(reg, nil)`.

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): ack metric watermark, store MetricBatch, reject empty name

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Server `FleetMetricsHistory` handler

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `stores` (Task 4), `metricstore.Query`/`Labels`/`MergeBuckets`/`AutoBucketMs` (Tasks 1–2), `pb.FleetMetricsHistoryRequest`/`MetricsHistoryResponse`/`MetricBucket`.
- Produces: `func (s *Server) FleetMetricsHistory(ctx, *pb.FleetMetricsHistoryRequest) (*pb.MetricsHistoryResponse, error)`.
- Behavior: unknown agent → `codes.NotFound`; selector matches labels equal to `selector` or with prefix `selector+"#"`; window defaults to 1h, bucket via `AutoBucketMs`; per-label `Query` then `MergeBuckets`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go`:

```go
func TestFleetMetricsHistory(t *testing.T) {
	ss := newStores(t.TempDir())
	srv := NewServer(NewRegistry(), ss)
	st, _ := ss.get("web-1")
	now := time.Now().UnixMilli()
	_ = st.Append(now-2000, []metricstore.Sample{{Label: "api#0", Cpu: 10, Mem: 100}, {Label: "api#1", Cpu: 5, Mem: 50}})
	_ = st.Append(now-1000, []metricstore.Sample{{Label: "api#0", Cpu: 30, Mem: 300}})

	// Unknown agent → NotFound.
	if _, err := srv.FleetMetricsHistory(context.Background(), &pb.FleetMetricsHistoryRequest{AgentName: "ghost", Selector: "api"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown agent err = %v, want NotFound", err)
	}

	resp, err := srv.FleetMetricsHistory(context.Background(), &pb.FleetMetricsHistoryRequest{
		AgentName: "web-1", Selector: "api", SinceMs: (time.Hour).Milliseconds(), BucketMs: 1000,
	})
	if err != nil {
		t.Fatalf("FleetMetricsHistory: %v", err)
	}
	if len(resp.GetBuckets()) == 0 {
		t.Fatal("expected buckets for api across both instances")
	}
}
```

Ensure `time` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestFleetMetricsHistory -v`
Expected: FAIL — `srv.FleetMetricsHistory undefined`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/server/server.go`:

```go
const defaultHistoryMs = int64(60 * 60 * 1000) // 1h

// FleetMetricsHistory returns bucketed CPU/mem history for one agent's app/instance.
func (s *Server) FleetMetricsHistory(_ context.Context, req *pb.FleetMetricsHistoryRequest) (*pb.MetricsHistoryResponse, error) {
	if s.stores == nil || !s.stores.has(req.GetAgentName()) {
		return nil, status.Errorf(codes.NotFound, "no metric history for agent %q", req.GetAgentName())
	}
	st, err := s.stores.get(req.GetAgentName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "open store: %v", err)
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "labels: %v", err)
	}
	sel := req.GetSelector()
	var matched []string
	for _, l := range labels {
		if l == sel || strings.HasPrefix(l, sel+"#") {
			matched = append(matched, l)
		}
	}

	sinceMs := req.GetSinceMs()
	if sinceMs <= 0 {
		sinceMs = defaultHistoryMs
	}
	bucketMs := metricstore.AutoBucketMs(sinceMs, req.GetBucketMs())
	lowerMs := time.Now().UnixMilli() - sinceMs

	var series [][]metricstore.Bucket
	for _, l := range matched {
		bs, err := st.Query(metricstore.QueryReq{Label: l, SinceMs: lowerMs, BucketMs: bucketMs})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "query: %v", err)
		}
		series = append(series, bs)
	}

	resp := &pb.MetricsHistoryResponse{}
	for _, b := range metricstore.MergeBuckets(series) {
		resp.Buckets = append(resp.Buckets, &pb.MetricBucket{
			TsMs: b.TsMs, CpuAvg: b.CpuAvg, CpuMax: b.CpuMax, MemAvg: b.MemAvg, MemMax: b.MemMax,
		})
	}
	return resp, nil
}
```

Add `strings` and `time` to the imports of `internal/server/server.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestFleetMetricsHistory -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): FleetMetricsHistory query handler

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Wire stores into `Serve` and `marshal server --data-dir`

**Files:**
- Modify: `internal/server/server.go` (the `Serve` function)
- Modify: `cmd/marshal/server.go`
- Test: `cmd/marshal/server_test.go` (create if absent)

**Interfaces:**
- Consumes: `newStores`, `NewServer` (Tasks 4–5).
- Produces:
  - `func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores) error` — **signature change**: takes the stores; closes them on shutdown.
  - Exported constructor for the CLI: `func NewStores(dir string) *stores` is **not** needed — instead expose a server entry that takes a dir. Add `func ServeDir(ctx context.Context, lis net.Listener, dataDir string, opts ...RegOption) error` that builds the registry + stores, calls `Serve`, and closes stores. The CLI calls `ServeDir`.

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/server_test.go`:

```go
package main

import (
	"strings"
	"testing"
)

func TestServerCmdHasDataDirFlag(t *testing.T) {
	cmd := serverCmd()
	if cmd.Flags().Lookup("data-dir") == nil {
		t.Fatal("server command missing --data-dir flag")
	}
	if cmd.Flags().Lookup("listen") == nil {
		t.Fatal("server command missing --listen flag")
	}
	if !strings.Contains(cmd.Short, "central server") {
		t.Fatalf("unexpected Short: %q", cmd.Short)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestServerCmdHasDataDirFlag -v`
Expected: FAIL — no `data-dir` flag.

- [ ] **Step 3: Implement `ServeDir` and update `Serve`**

In `internal/server/server.go`, replace `Serve` with:

```go
// Serve registers the Fleet service on lis and serves until ctx is canceled.
// ss may be nil (no metric storage); when set it is closed on shutdown.
func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores) error {
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, NewServer(reg, ss))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
		if ss != nil {
			_ = ss.closeAll()
		}
	}()
	return gs.Serve(lis)
}

// ServeDir builds a registry + per-agent metric stores rooted at dataDir, then
// serves until ctx is canceled.
func ServeDir(ctx context.Context, lis net.Listener, dataDir string, opts ...RegOption) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	return Serve(ctx, lis, NewRegistry(opts...), newStores(dataDir))
}
```

Add `fmt` and `os` to `internal/server/server.go` imports.

- [ ] **Step 4: Update `cmd/marshal/server.go`**

Add the `--data-dir` flag and default, and call `ServeDir`. Replace the command body:

```go
func serverCmd() *cobra.Command {
	var listen, dataDir string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Marshal central server (fleet aggregation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			lis, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen %s: %w", listen, err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(cmd.OutOrStdout(), "marshal server: listening on %s, data %s\n", lis.Addr(), dataDir)
			return server.ServeDir(ctx, lis, dataDir)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "address to listen on")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "metric storage directory (default $XDG_DATA_HOME/marshal-server)")
	return cmd
}

// defaultServerDataDir resolves $XDG_DATA_HOME/marshal-server, falling back to
// $HOME/.local/share/marshal-server, mirroring the agent store's resolution.
func defaultServerDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "marshal-server")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "marshal-server")
}
```

Add `path/filepath` to `cmd/marshal/server.go` imports.

- [ ] **Step 5: Run tests + build**

Run: `go test ./cmd/marshal/ -run TestServerCmdHasDataDirFlag -v && go build ./...`
Expected: PASS and a clean build. If the M7 `server_test.go`/smoke calls `Serve(ctx, lis, reg)`, update to `Serve(ctx, lis, reg, nil)`.

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go cmd/marshal/server.go cmd/marshal/server_test.go
git commit -m "feat(server): --data-dir, ServeDir wiring for metric storage

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Fleet client — watermark, metric push, ack receive, clean-shutdown log fix

**Files:**
- Modify: `internal/fleet/client.go`
- Test: `internal/fleet/client_test.go`

**Interfaces:**
- Consumes: `pb.MetricSample`/`MetricBatch`/`HelloAck`, the existing `SnapshotFunc`.
- Produces:
  - `type MetricsFunc func(sinceTsMs int64) []*pb.MetricSample`
  - `func WithMetrics(fn MetricsFunc) Option`
  - `New` keeps its signature; `WithMetrics` is an option. Internally the client: receives `HelloAck` after `Hello` to seed the watermark; on each tick sends the snapshot (unchanged) and, if `metrics != nil`, a `MetricBatch` of `metrics(watermark)`, advancing the watermark to the max ts sent.
  - `Run` no longer logs when the connection ends with `context.Canceled`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/fleet/client_test.go` (reuse the existing fake-server harness in that file; the snippet below assumes a bufconn/fake `Fleet` server the M7 tests already use — adapt names to the existing helper). The behaviors to assert:

```go
func TestClientSeedsWatermarkFromAckAndBackfills(t *testing.T) {
	// Fake server: replies to Hello with HelloAck{LastMetricTsMs: 5000},
	// records received MetricBatches.
	fs := newFakeServer(t) // existing helper; configure ack watermark below
	fs.ackWatermark = 5000

	var mu sync.Mutex
	shipped := []*pb.MetricSample{}
	metrics := func(since int64) []*pb.MetricSample {
		mu.Lock()
		defer mu.Unlock()
		// Local "history": one row at 4000 (already on server), one at 6000 (new).
		all := []*pb.MetricSample{
			{TsMs: 4000, Label: "api#0", Cpu: 1, Mem: 1},
			{TsMs: 6000, Label: "api#0", Cpu: 2, Mem: 2},
		}
		var out []*pb.MetricSample
		for _, s := range all {
			if s.TsMs > since {
				out = append(out, s)
			}
		}
		return out
	}
	_ = shipped

	c := New(fs.addr, "web-1", "test", func() []*pb.ProcInfo { return nil },
		WithInterval(20*time.Millisecond), WithMetrics(metrics))
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)

	// Within a couple ticks the server should have received only the 6000 row
	// (5000 watermark filters out 4000), and never re-send it.
	waitFor(t, 2*time.Second, func() bool {
		return fs.sawSample(6000) && !fs.sawSample(4000)
	})
	cancel()
}
```

If the M7 test file has no reusable fake server, add a minimal in-process gRPC `Fleet` server backed by a stub that (a) sends `HelloAck{LastMetricTsMs: ackWatermark}` on Hello, (b) appends received `MetricBatch` samples to a slice guarded by a mutex, with `sawSample(ts)` and `addr` helpers, and a `waitFor(t, d, cond)` poll helper (copy the one already used by the M7 reconnect test).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestClientSeedsWatermark -v`
Expected: FAIL — `WithMetrics undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/fleet/client.go`:

Add the field, type, and option:
```go
// MetricsFunc returns local metric rows strictly newer than sinceTsMs.
type MetricsFunc func(sinceTsMs int64) []*pb.MetricSample

// WithMetrics enables upstream metric shipping sourced from fn.
func WithMetrics(fn MetricsFunc) Option { return func(c *Client) { c.metrics = fn } }
```
Add `metrics MetricsFunc` to the `Client` struct. Add `"context"` and `"errors"` to imports (context already imported; add `errors`).

In `Run`, gate the log line:
```go
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("fleet: connection to %s ended: %v", c.addr, err)
		}
```

Replace `connectOnce` to receive the ack and ship metrics. Add a per-connection watermark seeded from the ack:
```go
func (c *Client) connectOnce(ctx context.Context) (bool, error) {
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()

	stream, err := pb.NewFleetClient(conn).Connect(ctx)
	if err != nil {
		return false, err
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{
		Hello: &pb.Hello{AgentName: c.name, MarshalVersion: c.version},
	}}); err != nil {
		return false, err
	}
	// Receive the HelloAck to seed the metric watermark.
	var watermark int64
	if ack, err := stream.Recv(); err != nil {
		return true, err
	} else if a := ack.GetHelloAck(); a != nil {
		watermark = a.GetLastMetricTsMs()
	}

	if err := c.pushSnapshot(stream); err != nil { // immediate first snapshot
		return true, err
	}
	if err := c.pushMetrics(stream, &watermark); err != nil { // immediate backfill
		return true, err
	}

	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-t.C:
			if err := c.pushSnapshot(stream); err != nil {
				return true, err
			}
			if err := c.pushMetrics(stream, &watermark); err != nil {
				return true, err
			}
		}
	}
}

func (c *Client) pushSnapshot(stream pb.Fleet_ConnectClient) error {
	return stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{
		Snapshot: &pb.StateSnapshot{Procs: c.snapshot()},
	}})
}

// pushMetrics ships local rows newer than *watermark; on success advances it to
// the max ts shipped. No-op when metrics shipping is disabled or nothing is new.
func (c *Client) pushMetrics(stream pb.Fleet_ConnectClient, watermark *int64) error {
	if c.metrics == nil {
		return nil
	}
	samples := c.metrics(*watermark)
	if len(samples) == 0 {
		return nil
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Metrics{
		Metrics: &pb.MetricBatch{Samples: samples},
	}}); err != nil {
		return err
	}
	var mx int64
	for _, s := range samples {
		if s.GetTsMs() > mx {
			mx = s.GetTsMs()
		}
	}
	if mx > *watermark {
		*watermark = mx
	}
	return nil
}
```
Delete the old `push` method (replaced by `pushSnapshot`). Note: existing M7 tests may reference `push` — update them to `pushSnapshot` or, preferably, assert via the stream.

- [ ] **Step 4: Run fleet tests to verify they pass**

Run: `go test ./internal/fleet/ -count=1 -race`
Expected: ok (new test + the M7 reconnect/heartbeat tests; fix any references to the removed `push`).

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/client.go internal/fleet/client_test.go
git commit -m "feat(fleet): ship metrics with watermark backfill; silence canceled-shutdown log

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Daemon wiring — metrics source, live cpu/mem in fleet snapshot, agent-name default

**Files:**
- Modify: `internal/daemon/fleet.go`
- Modify: `internal/daemon/server.go`
- Test: `internal/daemon/fleet_test.go`

**Interfaces:**
- Consumes: `fleet.WithMetrics`, `metrics.Sampler.Get`, `metricstore.Store.SamplesSince`, `manager.InstanceSnapshot.Label`.
- Produces:
  - `func fleetSnapshot(m *manager.Manager, smp *metrics.Sampler) fleet.SnapshotFunc` — **signature change**: takes the sampler, merges live cpu/mem.
  - `func metricsSince(mdb *metricstore.Store) fleet.MetricsFunc` — converts `SamplesSince` rows to `[]*pb.MetricSample`.
  - `server.go` `Run`: name defaults to `"unknown"` when hostname fails; passes `WithMetrics(metricsSince(mdb))` and the sampler into `fleetSnapshot`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/daemon/fleet_test.go`:

```go
func TestMetricsSinceConverts(t *testing.T) {
	st, err := metricstore.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.Append(1000, []metricstore.Sample{{Label: "api#0", Cpu: 10, Mem: 100}})
	_ = st.Append(2000, []metricstore.Sample{{Label: "api#0", Cpu: 20, Mem: 200}})

	fn := metricsSince(st)
	got := fn(1000) // strictly newer than 1000
	if len(got) != 1 || got[0].GetTsMs() != 2000 || got[0].GetLabel() != "api#0" || got[0].GetMem() != 200 {
		t.Fatalf("metricsSince(1000) = %+v, want one row ts=2000 mem=200", got)
	}
}
```

(Imports: `testing`, `marshal/internal/metricstore`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestMetricsSinceConverts -v`
Expected: FAIL — `metricsSince undefined`.

- [ ] **Step 3: Implement in `internal/daemon/fleet.go`**

Replace the file body with (keeps `procInfos` for any other callers; adds the sampler-aware snapshot + metrics source):

```go
package daemon

import (
	"marshal/internal/fleet"
	"marshal/internal/manager"
	"marshal/internal/metrics"
	"marshal/internal/metricstore"
	"marshal/internal/pb"
)

// fleetSnapshot returns a SnapshotFunc over the manager's current instances,
// merging the sampler's latest cpu/mem (zero until the first sample tick).
func fleetSnapshot(m *manager.Manager, smp *metrics.Sampler) fleet.SnapshotFunc {
	return func() []*pb.ProcInfo {
		snaps := m.List()
		out := make([]*pb.ProcInfo, 0, len(snaps))
		for _, s := range snaps {
			var cpu float64
			var mem uint64
			if smp != nil {
				if sm, ok := smp.Get(s.Label); ok {
					cpu, mem = sm.Cpu, sm.Mem
				}
			}
			out = append(out, snapshotToProc(s, cpu, mem))
		}
		return out
	}
}

// metricsSince adapts a local metric store to the fleet client's MetricsFunc:
// raw rows strictly newer than sinceTsMs, as wire samples.
func metricsSince(mdb *metricstore.Store) fleet.MetricsFunc {
	return func(sinceTsMs int64) []*pb.MetricSample {
		if mdb == nil {
			return nil
		}
		rows, err := mdb.SamplesSince(sinceTsMs)
		if err != nil {
			return nil
		}
		out := make([]*pb.MetricSample, 0, len(rows))
		for _, r := range rows {
			out = append(out, &pb.MetricSample{
				TsMs: r.TsMs, Label: r.Label, Cpu: r.Cpu, Mem: int64(r.Mem),
			})
		}
		return out
	}
}
```

If `procInfos` is now unused anywhere, delete it (and its test, if any) to keep `go vet`/build clean; otherwise leave it.

- [ ] **Step 4: Update `internal/daemon/server.go` `Run`**

In the fleet-client startup block, default the name and pass the new options:

```go
	if sc, err := st.LoadServer(); err == nil && sc != nil {
		name := sc.Name
		if name == "" {
			if h, hErr := os.Hostname(); hErr == nil {
				name = h
			}
		}
		if name == "" {
			name = "unknown"
		}
		fc := fleet.New(sc.Address, name, version.String(),
			fleetSnapshot(mgr, sampler), fleet.WithMetrics(metricsSince(mdb)))
		go fc.Run(serveCtx)
	}
```

- [ ] **Step 5: Run daemon tests + build**

Run: `go test ./internal/daemon/ -count=1 && go build ./...`
Expected: ok and clean build. (The `fleetSnapshot` signature changed; fix any test call sites to pass a sampler or `nil`.)

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/fleet.go internal/daemon/server.go internal/daemon/fleet_test.go
git commit -m "feat(daemon): ship local metrics to fleet; live cpu/mem in snapshots; unknown-name default

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: CLI — `marshal fleet metrics <agent> <selector>`

**Files:**
- Modify: `cmd/marshal/fleet.go`
- Test: `cmd/marshal/fleet_test.go` (create if absent)

**Interfaces:**
- Consumes: `pb.NewFleetClient(...).FleetMetricsHistory`, the existing `printMetrics` and `resolveServer`.
- Produces: `func fleetMetricsCmd() *cobra.Command`, registered via `cmd.AddCommand(fleetMetricsCmd())` in `fleetCmd()`.

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/fleet_test.go`:

```go
package main

import "testing"

func TestFleetMetricsCmdShape(t *testing.T) {
	cmd := fleetCmd()
	var metrics bool
	for _, c := range cmd.Commands() {
		if c.Name() == "metrics" {
			metrics = true
			if c.Flags().Lookup("since") == nil || c.Flags().Lookup("server") == nil {
				t.Fatal("fleet metrics missing --since/--server flags")
			}
		}
	}
	if !metrics {
		t.Fatal("fleet has no metrics subcommand")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestFleetMetricsCmdShape -v`
Expected: FAIL — no `metrics` subcommand under `fleet`.

- [ ] **Step 3: Implement in `cmd/marshal/fleet.go`**

Register the subcommand in `fleetCmd()`:
```go
	cmd.AddCommand(fleetPsCmd())
	cmd.AddCommand(fleetMetricsCmd())
```

Add the command (mirrors local `metricsCmd`, but targets the server and takes an agent arg):
```go
func fleetMetricsCmd() *cobra.Command {
	var serverAddr string
	var since, bucket time.Duration
	var cpuOnly, memOnly bool
	cmd := &cobra.Command{
		Use:   "metrics <agent> <name|id>",
		Short: "Show CPU/memory history for an app/instance on one agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := grpc.NewClient(resolveServer(serverAddr),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).FleetMetricsHistory(ctx, &pb.FleetMetricsHistoryRequest{
				AgentName: args[0],
				Selector:  args[1],
				SinceMs:   since.Milliseconds(),
				BucketMs:  bucket.Milliseconds(),
			})
			if err != nil {
				return err
			}
			printMetrics(cmd.OutOrStdout(), resp, args[0]+"/"+args[1], since, cpuOnly, memOnly)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().DurationVar(&since, "since", time.Hour, "history window (e.g. 30m, 6h)")
	cmd.Flags().DurationVar(&bucket, "bucket", 0, "bucket width (0 = auto)")
	cmd.Flags().BoolVar(&cpuOnly, "cpu", false, "show only CPU")
	cmd.Flags().BoolVar(&memOnly, "mem", false, "show only memory")
	return cmd
}
```

(`context`, `time`, `grpc`, `insecure`, `pb` are already imported in `fleet.go`.)

- [ ] **Step 4: Run test + build**

Run: `go test ./cmd/marshal/ -run TestFleetMetricsCmdShape -v && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/fleet.go cmd/marshal/fleet_test.go
git commit -m "feat(cli): add marshal fleet metrics

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: End-to-end test — sample → query, and reconnect backfill

**Files:**
- Create: `internal/server/e2e_test.go` (in-process bufconn or loopback TCP)

**Interfaces:**
- Consumes: `Serve`/`ServeDir`, the real `fleet.Client`, `pb.NewFleetClient`.

- [ ] **Step 1: Write the E2E test**

Create `internal/server/e2e_test.go`. Use a real TCP loopback listener (`net.Listen("tcp", "127.0.0.1:0")`) so the agent client's `grpc.NewClient(addr)` works unchanged. Drive a `fleet.Client` whose `MetricsFunc` returns from an in-memory slice you control (simulating the agent's local store), then assert history via `FleetMetricsHistory`. For the backfill leg: cancel the agent ctx (or stop the server), advance the simulated local store, restart serving on the **same data dir**, reconnect a new client, and assert the gap rows are present.

```go
package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/pb"
)

func TestE2EMetricsIngestAndBackfill(t *testing.T) {
	dataDir := t.TempDir()

	// local "store" the agent ships from, strictly-newer-than semantics.
	var mu sync.Mutex
	local := []*pb.MetricSample{
		{TsMs: 1000, Label: "api#0", Cpu: 10, Mem: 100},
		{TsMs: 2000, Label: "api#0", Cpu: 20, Mem: 200},
	}
	metricsFn := func(since int64) []*pb.MetricSample {
		mu.Lock()
		defer mu.Unlock()
		var out []*pb.MetricSample
		for _, s := range local {
			if s.TsMs > since {
				out = append(out, s)
			}
		}
		return out
	}

	// --- leg 1: serve, connect, ship 1000+2000 ---
	lis1, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = ServeDir(ctx1, lis1, dataDir) }()

	c1 := fleet.New(lis1.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metricsFn))
	cctx1, ccancel1 := context.WithCancel(context.Background())
	go c1.Run(cctx1)

	// query the server until 2000 is present
	conn1 := dialFleet(t, lis1.Addr().String())
	waitForHistory(t, conn1, "web-1", "api", 2)
	conn1.Close()
	ccancel1()
	cancel1()
	lis1.Close()

	// --- leg 2: simulate a gap, restart server on SAME dir, reconnect ---
	mu.Lock()
	local = append(local, &pb.MetricSample{TsMs: 3000, Label: "api#0", Cpu: 30, Mem: 300})
	mu.Unlock()

	lis2, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = ServeDir(ctx2, lis2, dataDir) }()

	c2 := fleet.New(lis2.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metricsFn))
	cctx2, ccancel2 := context.WithCancel(context.Background())
	defer ccancel2()
	go c2.Run(cctx2)

	conn2 := dialFleet(t, lis2.Addr().String())
	defer conn2.Close()
	// after reconnect, the server's history must include ts=3000 (3 buckets at 1s).
	waitForHistory(t, conn2, "web-1", "api", 3)
}
```

Add helpers in the same file:
```go
func dialFleet(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func waitForHistory(t *testing.T, conn *grpc.ClientConn, agent, selector string, wantBuckets int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := pb.NewFleetClient(conn).FleetMetricsHistory(ctx, &pb.FleetMetricsHistoryRequest{
			AgentName: agent, Selector: selector, SinceMs: time.Hour.Milliseconds(), BucketMs: 1000,
		})
		cancel()
		if err == nil && len(resp.GetBuckets()) >= wantBuckets {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("history for %s/%s never reached %d buckets", agent, selector, wantBuckets)
}
```
Add imports: `google.golang.org/grpc`, `google.golang.org/grpc/credentials/insecure`. Note the `SinceMs: time.Hour` with sample ts of 1000–3000 (epoch-relative) means `lowerMs = now - 1h`, which is far in the future relative to ts 1000–3000 — so the query window would EXCLUDE them. **Fix in the test:** stamp samples at real `time.Now().UnixMilli()`-relative values instead of 1000/2000/3000. Use `base := time.Now().UnixMilli()` and `base-2000, base-1000, base` so they fall inside the 1h window. Update `local` and the appended gap row accordingly.

- [ ] **Step 2: Run the E2E test (race)**

Run: `go test ./internal/server/ -run TestE2EMetricsIngestAndBackfill -race -count=1 -v`
Expected: PASS — leg 1 reaches 2 buckets; after reconnect on the same data dir, leg 2 reaches 3 (the gap row backfilled because the agent's watermark seeds from the persisted `max(ts)`).

- [ ] **Step 3: Commit**

```bash
git add internal/server/e2e_test.go
git commit -m "test(server): e2e metric ingest + reconnect backfill

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: Server-side pruning + full gate + handoff

**Files:**
- Modify: `internal/server/server.go` (prune loop in `ServeDir`)
- Test: `internal/server/stores_test.go` (prune-all helper)
- Create: `docs/handoffs/2026-06-17-m8-metric-storage.md`

**Interfaces:**
- Produces: `func (s *stores) pruneAll(beforeMs int64)` — prunes every open store; called on a ticker in `ServeDir`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/stores_test.go`:

```go
func TestStoresPruneAll(t *testing.T) {
	ss := newStores(t.TempDir())
	defer ss.closeAll()
	st, _ := ss.get("web-1")
	_ = st.Append(1000, []metricstore.Sample{{Label: "a#0", Cpu: 1, Mem: 1}})
	_ = st.Append(5000, []metricstore.Sample{{Label: "a#0", Cpu: 2, Mem: 2}})
	ss.pruneAll(3000) // drop ts < 3000
	if mx, _ := st.MaxTs(); mx != 5000 {
		t.Fatalf("MaxTs after prune = %d, want 5000", mx)
	}
	rows, _ := st.SamplesSince(0)
	if len(rows) != 1 {
		t.Fatalf("rows after prune = %d, want 1", len(rows))
	}
}
```
(Add `marshal/internal/metricstore` to the test imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestStoresPruneAll -v`
Expected: FAIL — `ss.pruneAll undefined`.

- [ ] **Step 3: Implement `pruneAll` and the prune loop**

Add to `internal/server/stores.go`:
```go
// pruneAll deletes samples older than beforeMs from every open store.
func (s *stores) pruneAll(beforeMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		_, _ = st.Prune(beforeMs)
	}
}
```

In `internal/server/server.go` `ServeDir`, start a prune goroutine (7-day retention, mirroring the daemon):
```go
func ServeDir(ctx context.Context, lis net.Listener, dataDir string, opts ...RegOption) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	ss := newStores(dataDir)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		const retentionMs = int64(7 * 24 * 60 * 60 * 1000)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				ss.pruneAll(time.Now().UnixMilli() - retentionMs)
			}
		}
	}()
	return Serve(ctx, lis, NewRegistry(opts...), ss)
}
```
(`time` already added in Task 6/7.)

- [ ] **Step 4: Run the test**

Run: `go test ./internal/server/ -run TestStoresPruneAll -v`
Expected: PASS.

- [ ] **Step 5: Full gate**

Run:
```bash
gofmt -l .            # must print nothing
go vet ./...
go build ./...
go test ./... -race -count=1
```
Expected: gofmt silent; vet/build clean; all packages ok. Fix anything that fails before continuing.

- [ ] **Step 6: Manual smoke (real binary)**

```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m8smoke
rm -rf /tmp/m8smoke && mkdir -p /tmp/m8smoke
./marshal server --listen :9000 &      # data dir defaults to /tmp/m8smoke/marshal-server
# /tmp/m8app.yaml: server{address: localhost:9000, name: dev-1} + a ticker app (cmd that prints + sleeps)
./marshal start /tmp/m8app.yaml
sleep 12                               # let a few 5s sampler ticks accrue
./marshal fleet ps --server localhost:9000        # cpu/mem now populated for online procs
./marshal fleet metrics dev-1 <appname> --server localhost:9000   # CPU/MEM sparkline with buckets
./marshal kill
kill %1                                # stop the server
```
Expected: `fleet ps` shows non-zero cpu/mem once sampled; `fleet metrics` prints a sparkline summary. Capture the observed output for the handoff.

- [ ] **Step 7: Write the handoff**

Create `docs/handoffs/2026-06-17-m8-metric-storage.md` following the CLAUDE.md handoff convention: current state (branch `m8-metric-storage`, commit range, gate green), what changed + key decisions (watermark-pull design, per-agent SQLite, derived high-water-mark, shared `MergeBuckets`/`AutoBucketMs`, folded-in M7 fixes), build/run/test commands, the smoke proof from Step 6, deferred/known issues (backfill bounded by local retention; server binds all interfaces unauthenticated until M10; selector resolution is name/label-prefix only, no numeric-id on the server), and the concrete next step (merge `m8-metric-storage` to `main` via finishing-a-development-branch; then **M8b — logs up + server-side log files + Approach-B timestamped records**).

- [ ] **Step 8: Commit**

```bash
git add internal/server/server.go internal/server/stores.go internal/server/stores_test.go docs/handoffs/2026-06-17-m8-metric-storage.md
git commit -m "feat(server): prune stored metrics on retention; M8 handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed during planning)

**Spec coverage:** §4 contract → Task 3; §5 agent (SamplesSince, watermark, push) → Tasks 1, 8, 9; §6 server (per-agent store, derived watermark, MetricBatch ingest, FleetMetricsHistory, pruning, --data-dir) → Tasks 4, 5, 6, 7, 12; §7 CLI + live cpu/mem → Tasks 9, 10; §8 folded-in fixes (empty name, canceled log) → Tasks 5, 8; §9 error handling → Tasks 5, 6, 8; §10 testing (metricstore, client, server, e2e, empty-name, canceled) → Tasks 1, 8, 5, 11, 5, 8.

**Placeholder scan:** no TBD/TODO; every code step shows complete code. The two test snippets that needed care (the `stores_test` unused-import and the E2E `SinceMs` window) carry explicit corrective notes rather than leaving ambiguity.

**Type consistency:** `TimestampedSample`, `MetricsFunc`, `NewServer(reg, ss)`, `Serve(ctx, lis, reg, ss)`, `ServeDir(ctx, lis, dir, opts...)`, `fleetSnapshot(m, smp)`, `metricsSince(mdb)`, `MergeBuckets`/`AutoBucketMs` are used consistently across tasks. The `pb` field names (`LastMetricTsMs`, `TsMs`, `Samples`, `AgentName`) match the proto in Task 3.
