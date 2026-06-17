# M5 — Metric History Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist per-instance CPU%/RSS samples to a local SQLite DB and surface the history through `marshal metrics` and a sparkline in `describe`.

**Architecture:** A new leaf package `internal/metricstore` (pure-Go `modernc.org/sqlite`) stores raw samples and serves query-time bucket aggregation. The daemon feeds it from the existing `metrics.Sampler` via a tick callback, prunes by age, and exposes a `MetricsHistory` gRPC RPC. The CLI renders Unicode sparklines.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (new, pure-Go/cgo-free), gRPC/protobuf, cobra.

Spec: `docs/superpowers/specs/2026-06-17-marshal-agent-core-m5-metric-history-design.md`

## Global Constraints

- **cgo-free only.** Use `modernc.org/sqlite` (pure Go). Never `mattn/go-sqlite3`. The single static binary must survive.
- Go module path is `marshal`; imports are `marshal/internal/...`.
- TDD: failing test first, then minimal implementation.
- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Feature work on branch `m5-metric-history` (cut from `main`), never directly on `main`.
- Final gate must be green: `go build ./...`, `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (lists nothing).
- Instance label format is `name#idx` (e.g. `web#0`), the same key logs use.

---

## Setup

- [ ] **Create the feature branch**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout main
git checkout -b m5-metric-history
```

---

## Task 1: `internal/metricstore` package

**Files:**
- Create: `internal/metricstore/store.go`
- Test: `internal/metricstore/store_test.go`
- Modify: `go.mod`, `go.sum` (add `modernc.org/sqlite`)

**Interfaces:**
- Consumes: nothing (leaf; stdlib + `modernc.org/sqlite` only).
- Produces:
  - `type Sample struct { Label string; Cpu float64; Mem uint64 }`
  - `type Bucket struct { TsMs int64; CpuAvg float64; CpuMax float64; MemAvg uint64; MemMax uint64 }`
  - `type QueryReq struct { Label string; SinceMs int64; BucketMs int64 }`
  - `func Open(path string) (*Store, error)`
  - `func (s *Store) Append(tsMs int64, samples []Sample) error`
  - `func (s *Store) Query(req QueryReq) ([]Bucket, error)`
  - `func (s *Store) Prune(beforeMs int64) (int64, error)`
  - `func (s *Store) Close() error`

- [ ] **Step 1: Add the dependency**

```bash
go get modernc.org/sqlite@latest
```

Expected: `go.mod` gains a `modernc.org/sqlite` require line; `go.sum` updated.

- [ ] **Step 2: Write the failing tests**

Create `internal/metricstore/store_test.go`:

```go
package metricstore

import (
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppendQueryRoundTrip(t *testing.T) {
	st := openTemp(t)
	if err := st.Append(1000, []Sample{{Label: "a#0", Cpu: 10, Mem: 100}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Append(2000, []Sample{{Label: "a#0", Cpu: 20, Mem: 200}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(got), got)
	}
	if got[0].TsMs != 1000 || got[1].TsMs != 2000 {
		t.Fatalf("bucket timestamps = %d,%d want 1000,2000", got[0].TsMs, got[1].TsMs)
	}
}

func TestBucketAggregation(t *testing.T) {
	st := openTemp(t)
	// Two samples in the same 1000ms bucket [2000,3000): cpu 10 & 30, mem 100 & 300.
	_ = st.Append(2000, []Sample{{Label: "a#0", Cpu: 10, Mem: 100}})
	_ = st.Append(2500, []Sample{{Label: "a#0", Cpu: 30, Mem: 300}})
	got, err := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d buckets, want 1", len(got))
	}
	b := got[0]
	if b.TsMs != 2000 || b.CpuAvg != 20 || b.CpuMax != 30 || b.MemAvg != 200 || b.MemMax != 300 {
		t.Fatalf("bucket = %+v, want ts=2000 cpuAvg=20 cpuMax=30 memAvg=200 memMax=300", b)
	}
}

func TestQueryRespectsSinceAndLabel(t *testing.T) {
	st := openTemp(t)
	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 1, Mem: 1}, {Label: "b#0", Cpu: 9, Mem: 9}})
	_ = st.Append(5000, []Sample{{Label: "a#0", Cpu: 2, Mem: 2}})
	got, err := st.Query(QueryReq{Label: "a#0", SinceMs: 3000, BucketMs: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].TsMs != 5000 {
		t.Fatalf("got %+v, want single bucket at 5000 (since filter + label filter)", got)
	}
}

func TestPrune(t *testing.T) {
	st := openTemp(t)
	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 1, Mem: 1}})
	_ = st.Append(5000, []Sample{{Label: "a#0", Cpu: 2, Mem: 2}})
	n, err := st.Prune(3000)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	got, _ := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if len(got) != 1 || got[0].TsMs != 5000 {
		t.Fatalf("after prune got %+v, want only ts=5000", got)
	}
}

func TestReopenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 7, Mem: 70}})
	_ = st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	got, _ := st2.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if len(got) != 1 || got[0].CpuAvg != 7 {
		t.Fatalf("after reopen got %+v, want cpuAvg=7", got)
	}
}

func TestQueryRejectsZeroBucket(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 0}); err == nil {
		t.Fatal("expected error for zero bucket width")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/metricstore/ -run . -v`
Expected: FAIL — `undefined: Open` / package has no non-test files.

- [ ] **Step 4: Write the implementation**

Create `internal/metricstore/store.go`:

```go
// Package metricstore persists per-instance CPU%/RSS samples to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves time-bucketed history queries.
package metricstore

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Sample is one instance reading at the Append timestamp.
type Sample struct {
	Label string
	Cpu   float64
	Mem   uint64
}

// Bucket is one aggregated time bucket for a label, oldest first in query order.
type Bucket struct {
	TsMs   int64
	CpuAvg float64
	CpuMax float64
	MemAvg uint64
	MemMax uint64
}

// QueryReq selects a single label's history.
type QueryReq struct {
	Label    string
	SinceMs  int64 // inclusive lower bound on ts
	BucketMs int64 // bucket width in ms; must be > 0
}

// Store is a SQLite-backed sample store.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS samples (
	ts    INTEGER NOT NULL,
	label TEXT    NOT NULL,
	cpu   REAL    NOT NULL,
	mem   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_samples_label_ts ON samples(label, ts);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open metrics db: %w", err)
	}
	// Single daemon process touches this DB from two goroutines (sampler append +
	// query handler); serialize to sidestep SQLite locking entirely. Volume is tiny.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Append writes one row per sample, all stamped tsMs, in a single transaction.
func (s *Store) Append(tsMs int64, samples []Sample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO samples(ts, label, cpu, mem) VALUES(?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, sm := range samples {
		if _, err := stmt.Exec(tsMs, sm.Label, sm.Cpu, int64(sm.Mem)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Query returns time-bucketed aggregates for one label, oldest first.
func (s *Store) Query(req QueryReq) ([]Bucket, error) {
	if req.BucketMs <= 0 {
		return nil, fmt.Errorf("bucket width must be > 0")
	}
	rows, err := s.db.Query(`
		SELECT (ts/?)*? AS bucket, avg(cpu), max(cpu), avg(mem), max(mem)
		FROM samples
		WHERE label = ? AND ts >= ?
		GROUP BY bucket
		ORDER BY bucket`,
		req.BucketMs, req.BucketMs, req.Label, req.SinceMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		var memAvg float64
		var memMax int64
		if err := rows.Scan(&b.TsMs, &b.CpuAvg, &b.CpuMax, &memAvg, &memMax); err != nil {
			return nil, err
		}
		b.MemAvg = uint64(memAvg)
		b.MemMax = uint64(memMax)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Prune deletes samples with ts < beforeMs, returning the count removed.
func (s *Store) Prune(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM samples WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/metricstore/ -race -count=1 -v`
Expected: PASS (all six tests).

- [ ] **Step 6: Tidy and commit**

```bash
go mod tidy
gofmt -w internal/metricstore/
git add internal/metricstore/ go.mod go.sum
git commit -m "feat(metricstore): pure-Go SQLite sample store with bucket queries + prune

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `MetricsHistory` gRPC RPC

**Files:**
- Modify: `proto/marshal/v1/daemon.proto`
- Regenerate: `internal/pb/daemon.pb.go`, `internal/pb/daemon_grpc.pb.go`

**Interfaces:**
- Consumes: nothing.
- Produces (generated Go): `pb.MetricsHistoryRequest{Selector string, SinceMs int64, BucketMs int64}`, `pb.MetricBucket{TsMs int64, CpuAvg float64, CpuMax float64, MemAvg uint64, MemMax uint64}`, `pb.MetricsHistoryResponse{Buckets []*pb.MetricBucket}`, plus `DaemonClient.MetricsHistory` and the `MetricsHistory` server method on `DaemonServer`.

- [ ] **Step 1: Edit the proto**

In `proto/marshal/v1/daemon.proto`, add the RPC to the `Daemon` service (after the `Logs` line):

```proto
  rpc Logs(LogRequest) returns (stream LogLine); // M3
  rpc MetricsHistory(MetricsHistoryRequest) returns (MetricsHistoryResponse); // M5
```

And append these messages at the end of the file:

```proto
// M5 — metric history.
message MetricsHistoryRequest {
  string selector  = 1;            // name or id, resolved like logs/describe
  int64  since_ms  = 2;            // lookback window in ms; server queries ts >= now - since_ms
  int64  bucket_ms = 3;            // bucket width in ms; 0 = server auto-picks (~60 buckets)
}

message MetricBucket {
  int64  ts_ms   = 1;
  double cpu_avg = 2;
  double cpu_max = 3;
  uint64 mem_avg = 4;
  uint64 mem_max = 5;
}

message MetricsHistoryResponse { repeated MetricBucket buckets = 1; }
```

(The `// M3 — defined, not implemented` comment on the `Logs` line was already updated when M3 shipped; leave whatever is there and just add the new line.)

- [ ] **Step 2: Regenerate**

Run: `go generate ./internal/pb`
Expected: `internal/pb/daemon.pb.go` and `internal/pb/daemon_grpc.pb.go` updated with the new types/methods.

> Requires `protoc` on PATH (M3 used protoc v7.35.0; the `protoc-gen-go`/`protoc-gen-go-grpc` plugins are pinned as Go tools in `go.mod`). If `protoc` is missing, install it (`brew install protobuf`) before running.

- [ ] **Step 3: Verify it builds**

Run: `go build ./...`
Expected: success. `pb.UnimplementedDaemonServer` now provides a default `MetricsHistory`, so the existing `Server` still satisfies the interface.

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/daemon.proto internal/pb/
git commit -m "feat(proto): add MetricsHistory RPC + messages

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `metrics.Sampler` tick callback

**Files:**
- Modify: `internal/metrics/sampler.go`
- Test: `internal/metrics/sampler_test.go` (add one test)

**Interfaces:**
- Consumes: nothing new.
- Produces: `func (s *Sampler) SetOnTick(fn func(map[string]Sample))` — `fn` is invoked once per sample tick with the fresh per-label results (keyed by instance label). Must be called before `Run`.

- [ ] **Step 1: Write the failing test**

Append to `internal/metrics/sampler_test.go`:

```go
func TestSetOnTickFiresWithLabeledSamples(t *testing.T) {
	s := NewSampler(time.Hour)
	var got map[string]Sample
	s.SetOnTick(func(m map[string]Sample) { got = m })
	s.sample([]Instance{{Label: "a#0", Pid: 99999999, Online: true}})
	if got == nil {
		t.Fatal("onTick was not invoked")
	}
	if _, ok := got["a#0"]; !ok {
		t.Fatalf("onTick map = %+v, want an entry for a#0", got)
	}
}
```

(`sample` records an entry for every online instance regardless of whether the pid resolves, so a bogus pid still yields the `a#0` key.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/metrics/ -run TestSetOnTick -v`
Expected: FAIL — `s.SetOnTick undefined`.

- [ ] **Step 3: Implement**

In `internal/metrics/sampler.go`, add a field to the `Sampler` struct (alongside `last`/`procs`):

```go
	onTick func(map[string]Sample) // optional; fired each tick with fresh results
```

Add the setter (place it near `NewSampler`):

```go
// SetOnTick registers a callback fired once per sample tick with the fresh
// per-label results. Call before Run; not safe to change concurrently with it.
func (s *Sampler) SetOnTick(fn func(map[string]Sample)) { s.onTick = fn }
```

In `sample`, after the existing `s.mu.Unlock()` that follows `s.last = result`, add (outside the lock, so the callback can't deadlock on the sampler):

```go
	if s.onTick != nil {
		s.onTick(result)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/metrics/ -race -count=1 -v`
Expected: PASS (existing tests + the new one).

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/sampler.go internal/metrics/sampler_test.go
git commit -m "feat(metrics): add per-tick sample callback (SetOnTick)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Daemon wiring + `MetricsHistory` handler

**Files:**
- Modify: `internal/store/store.go` (add `MetricsDBPath`)
- Modify: `internal/daemon/server.go` (Server field, `WithRetention`, Run wiring)
- Create: `internal/daemon/metrics.go` (handler + `mergeBuckets`)
- Test: `internal/daemon/metrics_test.go`

**Interfaces:**
- Consumes: `metricstore.{Open,Store,Sample,QueryReq,Bucket}` (Task 1); `metrics.Sampler.SetOnTick` (Task 3); `pb.MetricsHistory*` (Task 2).
- Produces: `func (s *Store) MetricsDBPath() string`; `func WithRetention(d time.Duration) Option`; `func (s *Server) MetricsHistory(context.Context, *pb.MetricsHistoryRequest) (*pb.MetricsHistoryResponse, error)`; `func mergeBuckets(series [][]metricstore.Bucket) []metricstore.Bucket`.

- [ ] **Step 1: Add the DB path to the store**

In `internal/store/store.go`, after `LogsDir`:

```go
// MetricsDBPath is the SQLite file holding metric history.
func (s *Store) MetricsDBPath() string { return filepath.Join(s.base, "metrics.db") }
```

- [ ] **Step 2: Write the failing tests**

Create `internal/daemon/metrics_test.go`:

```go
package daemon

import (
	"context"
	"testing"
	"time"

	"marshal/internal/manager"
	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMergeBucketsSumsAndMaxes(t *testing.T) {
	a := []metricstore.Bucket{{TsMs: 1000, CpuAvg: 10, CpuMax: 15, MemAvg: 100, MemMax: 150}}
	b := []metricstore.Bucket{
		{TsMs: 1000, CpuAvg: 20, CpuMax: 12, MemAvg: 200, MemMax: 120},
		{TsMs: 2000, CpuAvg: 5, CpuMax: 5, MemAvg: 50, MemMax: 50},
	}
	got := mergeBuckets([][]metricstore.Bucket{a, b})
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2", len(got))
	}
	// Bucket 1000: avg summed across instances, max = max of maxes.
	if got[0].TsMs != 1000 || got[0].CpuAvg != 30 || got[0].CpuMax != 15 || got[0].MemAvg != 300 || got[0].MemMax != 150 {
		t.Fatalf("merged[0] = %+v", got[0])
	}
	if got[1].TsMs != 2000 || got[1].CpuAvg != 5 {
		t.Fatalf("merged[1] = %+v", got[1])
	}
}

func TestMetricsHistoryUnknownSelectorIsNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openTempStore(t)
	srv := &Server{mgr: manager.New(ctx), mdb: st}
	_, err := srv.MetricsHistory(ctx, &pb.MetricsHistoryRequest{Selector: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestMetricsHistoryReturnsBuckets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openTempStore(t)
	srv := &Server{mgr: manager.New(ctx), mdb: st}
	defer srv.mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitListOnline(t, srv, 1)

	// Seed two samples under the instance label, 1s apart, "now"-ish.
	now := time.Now().UnixMilli()
	_ = st.Append(now-2000, []metricstore.Sample{{Label: "a#0", Cpu: 10, Mem: 100}})
	_ = st.Append(now-1000, []metricstore.Sample{{Label: "a#0", Cpu: 30, Mem: 300}})

	resp, err := srv.MetricsHistory(ctx, &pb.MetricsHistoryRequest{
		Selector: "a", SinceMs: int64(time.Hour / time.Millisecond), BucketMs: 1000,
	})
	if err != nil {
		t.Fatalf("MetricsHistory: %v", err)
	}
	if len(resp.GetBuckets()) == 0 {
		t.Fatalf("got no buckets, want >= 1")
	}
}

func openTempStore(t *testing.T) *metricstore.Store {
	t.Helper()
	st, err := metricstore.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/daemon/ -run "TestMerge|TestMetricsHistory" -v`
Expected: FAIL — `mergeBuckets` / `Server.mdb` / `Server.MetricsHistory` undefined.

- [ ] **Step 4: Implement the handler**

Create `internal/daemon/metrics.go`:

```go
package daemon

import (
	"context"
	"sort"
	"time"

	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	targetBuckets   = 60
	defaultHistory  = time.Hour
	minBucketMs     = 1000
)

// MetricsHistory returns time-bucketed CPU/RSS history for the selected app,
// summed across its instances per bucket.
func (s *Server) MetricsHistory(_ context.Context, req *pb.MetricsHistoryRequest) (*pb.MetricsHistoryResponse, error) {
	if s.mdb == nil {
		return nil, status.Error(codes.Unavailable, "metrics history not configured")
	}
	snaps, err := s.mgr.Describe(req.GetSelector())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}

	sinceMs := req.GetSinceMs()
	if sinceMs <= 0 {
		sinceMs = int64(defaultHistory / time.Millisecond)
	}
	bucketMs := req.GetBucketMs()
	if bucketMs <= 0 {
		bucketMs = sinceMs / targetBuckets
		if bucketMs < minBucketMs {
			bucketMs = minBucketMs
		}
	}
	lowerMs := time.Now().UnixMilli() - sinceMs

	var series [][]metricstore.Bucket
	for _, sn := range snaps {
		bs, err := s.mdb.Query(metricstore.QueryReq{Label: sn.Label, SinceMs: lowerMs, BucketMs: bucketMs})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "query metrics: %v", err)
		}
		series = append(series, bs)
	}

	resp := &pb.MetricsHistoryResponse{}
	for _, b := range mergeBuckets(series) {
		resp.Buckets = append(resp.Buckets, &pb.MetricBucket{
			TsMs:   b.TsMs,
			CpuAvg: b.CpuAvg,
			CpuMax: b.CpuMax,
			MemAvg: b.MemAvg,
			MemMax: b.MemMax,
		})
	}
	return resp, nil
}

// mergeBuckets combines per-instance series sharing a bucket timestamp: averages
// are summed (whole-app total), maxes take the max across instances. Result is
// ordered oldest first.
func mergeBuckets(series [][]metricstore.Bucket) []metricstore.Bucket {
	byTs := map[int64]*metricstore.Bucket{}
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
	out := make([]metricstore.Bucket, 0, len(order))
	for _, ts := range order {
		out = append(out, *byTs[ts])
	}
	return out
}
```

- [ ] **Step 5: Add the Server field + run wiring**

In `internal/daemon/server.go`:

(a) Add the import `"marshal/internal/metricstore"` to the import block.

(b) Add a field to `Server`:

```go
type Server struct {
	pb.UnimplementedDaemonServer
	mgr     *manager.Manager
	store   *store.Store
	logs    *logs.Registry
	metrics *metrics.Sampler
	mdb     *metricstore.Store // metric history
	kill    func()
}
```

(c) Add `retention` to `runOptions` and a `WithRetention` option (next to `WithSampleInterval`):

```go
type runOptions struct {
	sampleInterval time.Duration
	retention      time.Duration
}

// WithRetention overrides the 7-day metric-history retention window (used by tests).
func WithRetention(d time.Duration) Option {
	return func(o *runOptions) { o.retention = d }
}
```

(d) In `Run`, set the retention default in the `cfg` literal:

```go
	cfg := runOptions{sampleInterval: 5 * time.Second, retention: 168 * time.Hour}
```

(e) In `Run`, after the sampler is created (`sampler := metrics.NewSampler(cfg.sampleInterval)`) and after `EnsureDir`/`EnsureLogsDir` have run, open the store and wire the tick callback:

```go
	mdb, err := metricstore.Open(st.MetricsDBPath())
	if err != nil {
		return fmt.Errorf("open metrics db: %w", err)
	}
	sampler.SetOnTick(func(m map[string]metrics.Sample) {
		if len(m) == 0 {
			return
		}
		samples := make([]metricstore.Sample, 0, len(m))
		for label, sm := range m {
			samples = append(samples, metricstore.Sample{Label: label, Cpu: sm.Cpu, Mem: sm.Mem})
		}
		_ = mdb.Append(time.Now().UnixMilli(), samples)
	})
```

(f) Include `mdb` when constructing the server:

```go
	srv := &Server{mgr: mgr, store: st, logs: reg, metrics: sampler, mdb: mdb}
```

(g) Start a prune goroutine next to the sampler goroutine (after `go sampler.Run(serveCtx, metricsSnapshot(mgr))`):

```go
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-serveCtx.Done():
				return
			case <-t.C:
				_, _ = mdb.Prune(time.Now().UnixMilli() - cfg.retention.Milliseconds())
			}
		}
	}()
```

(h) Close the store during shutdown, next to `mgr.StopAll()`:

```go
	mgr.StopAll()
	_ = mdb.Close()
	_ = os.Remove(sock)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/daemon/ ./internal/store/ -race -count=1`
Expected: PASS (new metrics tests + all existing daemon/store tests).

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/daemon/ internal/store/
git add internal/daemon/ internal/store/store.go
git commit -m "feat(daemon): persist samples, prune by age, serve MetricsHistory

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Sparkline rendering helper

**Files:**
- Create: `cmd/marshal/sparkline.go`
- Test: `cmd/marshal/sparkline_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func sparkline(vals []float64) string`; `func summarize(vals []float64) (min, avg, max float64)`.

- [ ] **Step 1: Write the failing tests**

Create `cmd/marshal/sparkline_test.go`:

```go
package main

import "testing"

func TestSparklineEmpty(t *testing.T) {
	if got := sparkline(nil); got != "" {
		t.Fatalf("sparkline(nil) = %q, want empty", got)
	}
}

func TestSparklineLengthMatchesInput(t *testing.T) {
	got := []rune(sparkline([]float64{1, 2, 3, 4, 5}))
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
}

func TestSparklineRampLowToHigh(t *testing.T) {
	got := []rune(sparkline([]float64{0, 50, 100}))
	if got[0] != '▁' {
		t.Fatalf("first rune = %q, want ▁", string(got[0]))
	}
	if got[len(got)-1] != '█' {
		t.Fatalf("last rune = %q, want █", string(got[len(got)-1]))
	}
}

func TestSparklineAllEqual(t *testing.T) {
	got := []rune(sparkline([]float64{7, 7, 7}))
	for _, r := range got {
		if r != got[0] {
			t.Fatalf("all-equal input should render one rune, got %q", string(got))
		}
	}
}

func TestSummarize(t *testing.T) {
	min, avg, max := summarize([]float64{2, 4, 6})
	if min != 2 || max != 6 || avg != 4 {
		t.Fatalf("summarize = (%v,%v,%v), want (2,4,6)", min, avg, max)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/marshal/ -run "Sparkline|Summarize" -v`
Expected: FAIL — `sparkline` / `summarize` undefined.

- [ ] **Step 3: Implement**

Create `cmd/marshal/sparkline.go`:

```go
package main

import "strings"

// sparkline renders values as Unicode block characters scaled to the data's own
// min..max range. Empty input yields "". A flat series renders the lowest block.
func sparkline(vals []float64) string {
	if len(vals) == 0 {
		return ""
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	min, max := vals[0], vals[0]
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	span := max - min
	var b strings.Builder
	for _, v := range vals {
		idx := 0
		if span > 0 {
			idx = int((v-min)/span*float64(len(blocks)-1) + 0.5)
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
}

// summarize returns the min, mean, and max of vals (all zero for empty input).
func summarize(vals []float64) (min, avg, max float64) {
	if len(vals) == 0 {
		return 0, 0, 0
	}
	min, max = vals[0], vals[0]
	var sum float64
	for _, v := range vals {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
		sum += v
	}
	return min, sum / float64(len(vals)), max
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/marshal/ -run "Sparkline|Summarize" -race -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/sparkline.go cmd/marshal/sparkline_test.go
git commit -m "feat(cli): add sparkline + summarize rendering helpers

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `marshal metrics` command

**Files:**
- Create: `cmd/marshal/metrics.go`
- Modify: `cmd/marshal/main.go` (register the command)
- Test: `cmd/marshal/metrics_test.go`

**Interfaces:**
- Consumes: `withClient` (control.go), `humanizeBytes` (control.go), `sparkline`/`summarize` (Task 5), `pb.MetricsHistory*` (Task 2).
- Produces: `func metricsCmd() *cobra.Command`; `func printMetrics(w io.Writer, resp *pb.MetricsHistoryResponse, selector string, since time.Duration, cpuOnly, memOnly bool)`.

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/metrics_test.go` (tests the pure renderer; the cobra/daemon plumbing is covered by the gate build + smoke):

```go
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"marshal/internal/pb"
)

func TestPrintMetricsRendersRows(t *testing.T) {
	resp := &pb.MetricsHistoryResponse{Buckets: []*pb.MetricBucket{
		{TsMs: 1000, CpuAvg: 10, CpuMax: 12, MemAvg: 100, MemMax: 120},
		{TsMs: 2000, CpuAvg: 20, CpuMax: 25, MemAvg: 200, MemMax: 240},
	}}
	var buf bytes.Buffer
	printMetrics(&buf, resp, "web", time.Hour, false, false)
	out := buf.String()
	if !strings.Contains(out, "CPU") || !strings.Contains(out, "MEM") {
		t.Fatalf("output missing CPU/MEM rows:\n%s", out)
	}
}

func TestPrintMetricsEmpty(t *testing.T) {
	var buf bytes.Buffer
	printMetrics(&buf, &pb.MetricsHistoryResponse{}, "web", time.Hour, false, false)
	if !strings.Contains(buf.String(), "no metric history") {
		t.Fatalf("empty output = %q, want a 'no metric history' notice", buf.String())
	}
}

func TestPrintMetricsCPUOnly(t *testing.T) {
	resp := &pb.MetricsHistoryResponse{Buckets: []*pb.MetricBucket{
		{TsMs: 1000, CpuAvg: 10, CpuMax: 12, MemAvg: 100, MemMax: 120},
	}}
	var buf bytes.Buffer
	printMetrics(&buf, resp, "web", time.Hour, true, false)
	if strings.Contains(buf.String(), "MEM") {
		t.Fatalf("--cpu output should not contain MEM:\n%s", buf.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestPrintMetrics -v`
Expected: FAIL — `printMetrics` undefined.

- [ ] **Step 3: Implement**

Create `cmd/marshal/metrics.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/pb"
)

func metricsCmd() *cobra.Command {
	var since, bucket time.Duration
	var cpuOnly, memOnly bool
	cmd := &cobra.Command{
		Use:   "metrics <name|id>",
		Short: "Show CPU/memory history for an app/instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				resp, err := c.MetricsHistory(ctx, &pb.MetricsHistoryRequest{
					Selector: args[0],
					SinceMs:  since.Milliseconds(),
					BucketMs: bucket.Milliseconds(),
				})
				if err != nil {
					return err
				}
				printMetrics(cmd.OutOrStdout(), resp, args[0], since, cpuOnly, memOnly)
				return nil
			})
		},
	}
	cmd.Flags().DurationVar(&since, "since", time.Hour, "history window (e.g. 30m, 6h)")
	cmd.Flags().DurationVar(&bucket, "bucket", 0, "bucket width (0 = auto)")
	cmd.Flags().BoolVar(&cpuOnly, "cpu", false, "show only CPU")
	cmd.Flags().BoolVar(&memOnly, "mem", false, "show only memory")
	return cmd
}

// printMetrics renders the history as labeled sparklines with min/avg/max.
func printMetrics(w io.Writer, resp *pb.MetricsHistoryResponse, selector string, since time.Duration, cpuOnly, memOnly bool) {
	buckets := resp.GetBuckets()
	if len(buckets) == 0 {
		fmt.Fprintf(w, "no metric history for %q in the last %s\n", selector, since)
		return
	}
	cpu := make([]float64, len(buckets))
	mem := make([]float64, len(buckets))
	for i, b := range buckets {
		cpu[i] = b.GetCpuAvg()
		mem[i] = float64(b.GetMemAvg())
	}
	fmt.Fprintf(w, "%s — last %s, %d buckets\n", selector, since, len(buckets))
	if !memOnly {
		mn, av, mx := summarize(cpu)
		fmt.Fprintf(w, "CPU  %s  min %.1f%%  avg %.1f%%  max %.1f%%\n", sparkline(cpu), mn, av, mx)
	}
	if !cpuOnly {
		mn, av, mx := summarize(mem)
		fmt.Fprintf(w, "MEM  %s  min %s  avg %s  max %s\n",
			sparkline(mem), humanizeBytes(int64(mn)), humanizeBytes(int64(av)), humanizeBytes(int64(mx)))
	}
}
```

- [ ] **Step 4: Register the command**

In `cmd/marshal/main.go`, add `metricsCmd(),` to the `root.AddCommand(...)` list (after `logsCmd(),`).

- [ ] **Step 5: Run tests + build to verify**

Run: `go test ./cmd/marshal/ -run TestPrintMetrics -race -count=1 -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/marshal/
git add cmd/marshal/metrics.go cmd/marshal/metrics_test.go cmd/marshal/main.go
git commit -m "feat(cli): add 'marshal metrics' history command

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: `describe` last-hour sparkline

**Files:**
- Modify: `cmd/marshal/control.go` (`describeCmd`)

**Interfaces:**
- Consumes: `withClient`, `printProcs` (control.go), `sparkline` (Task 5), `pb.MetricsHistory*` (Task 2).
- Produces: updated `describeCmd` that appends a compact last-hour CPU+MEM sparkline below the table.

- [ ] **Step 1: Replace `describeCmd`**

In `cmd/marshal/control.go`, replace the existing `describeCmd` (which delegates to `selectorCmd`) with a custom command that also fetches history. Add `"context"` and `"time"` to imports if not already present (both are already imported in control.go):

```go
func describeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <name|id>",
		Short: "Show detail for an app/instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.Describe(ctx, &pb.Selector{Target: args[0]})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				// Best-effort last-hour history; never fail describe on its absence.
				resp, err := c.MetricsHistory(ctx, &pb.MetricsHistoryRequest{
					Selector: args[0],
					SinceMs:  time.Hour.Milliseconds(),
				})
				if err == nil && len(resp.GetBuckets()) > 0 {
					cpu := make([]float64, len(resp.Buckets))
					mem := make([]float64, len(resp.Buckets))
					for i, b := range resp.Buckets {
						cpu[i] = b.GetCpuAvg()
						mem[i] = float64(b.GetMemAvg())
					}
					out := cmd.OutOrStdout()
					fmt.Fprintf(out, "\nCPU (1h)  %s\n", sparkline(cpu))
					fmt.Fprintf(out, "MEM (1h)  %s\n", sparkline(mem))
				}
				return nil
			})
		},
	}
}
```

- [ ] **Step 2: Build + verify existing tests still pass**

Run: `go build ./... && go test ./cmd/marshal/ -race -count=1`
Expected: clean build; PASS.

- [ ] **Step 3: Commit**

```bash
gofmt -w cmd/marshal/control.go
git add cmd/marshal/control.go
git commit -m "feat(cli): add last-hour sparkline to describe

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Full gate + smoke + handoff

**Files:**
- Create: `docs/handoffs/2026-06-17-m5-metric-history.md`

- [ ] **Step 1: Full gate**

```bash
cd "/Users/sebastiankuprat/process manager"
go build ./...
go vet ./...
gofmt -l .          # must print nothing
go test ./... -race -count=1
```

Expected: build clean, vet clean, gofmt silent, all tests pass.

- [ ] **Step 2: Real-binary smoke**

```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m5smoke
./marshal start <(printf 'apps:\n  - name: tick\n    cmd: sh\n    args: ["-c", "while true; do echo hi; sleep 1; done"]\n') 2>/dev/null || \
  ./marshal start /tmp/m5app.yaml   # if process substitution is unavailable, write the yaml to a file first
sleep 30                            # let the 5s sampler write several rows
./marshal metrics tick --since 5m
./marshal describe tick
./marshal kill
rm -rf /tmp/m5smoke
```

Expected: `marshal metrics tick` prints CPU/MEM sparklines with min/avg/max; `describe` shows the table plus a `CPU (1h)` / `MEM (1h)` sparkline. Confirm `/tmp/m5smoke/metrics.db` was created.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-17-m5-metric-history.md` per the CLAUDE.md handoff convention (current state, what changed + why, build/run/test, deferred items — carry forward the M6 log items — and the concrete next step: merge `m5-metric-history` to `main` via finishing-a-development-branch, then start M6). Commit it.

```bash
git add docs/handoffs/2026-06-17-m5-metric-history.md marshal
git status   # ensure the built ./marshal binary is gitignored (it is: /marshal in .gitignore) — do NOT commit it
git restore --staged marshal 2>/dev/null || true
git commit -m "docs: M5 metric-history completion handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 4: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to merge `m5-metric-history` into `main` with a local `--no-ff` merge (no git remote), as M1–M4 were.

---

## Self-Review (completed by plan author)

- **Spec coverage:** SQLite store (T1) ✓; pure-Go/cgo-free constraint (T1, Global Constraints) ✓; sampler-fed write path (T3, T4) ✓; age-out retention default 7d + `WithRetention` (T4) ✓; `MetricsHistory` RPC + server-side bucket aggregation (T2, T4) ✓; `marshal metrics` with `--since`/`--bucket`/`--cpu`/`--mem` + sparkline + min/avg/max (T5, T6) ✓; `describe` last-hour sparkline (T7) ✓; downtime-as-gaps (inherent: store only gets online-instance rows; absent buckets render as gaps) ✓; DB at `<stateDir>/metrics.db` (T4) ✓.
- **Placeholder scan:** none — every code step shows complete code; every command shows expected output.
- **Type consistency:** `metricstore.{Sample,Bucket,QueryReq,Store}` signatures identical across T1/T4; `pb.MetricBucket` field names (`CpuAvg/CpuMax/MemAvg/MemMax/TsMs`) consistent T2→T4→T6→T7; `sparkline`/`summarize` signatures consistent T5→T6→T7; `Server.mdb` consistent T4 tests + wiring.
- **Carried to M6:** deep log backfill across rotated segments, retention-by-age, per-stream `logs` view, max-line cap in `logs.Sink`, backfill→subscribe race.
