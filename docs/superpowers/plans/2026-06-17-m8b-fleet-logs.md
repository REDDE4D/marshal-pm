# M8b — Fleet Log Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship captured stdout/stderr lines from each agent to the central server, persist them per-agent in SQLite, and expose `marshal fleet logs <agent> <app>` for backfill (last-N) queries.

**Architecture:** Exact mirror of M8 metric storage. A new `internal/logstore` SQLite package (one DB per agent) stores flattened log rows. The daemon ships lines over the existing `Fleet.Connect` stream via a **pull-based** `LogsFunc(sinceTsMs)` that reads the in-memory log ring each 2s push tick (identical seam to `MetricsFunc`). The server persists `LogBatch` messages and serves a new `FleetLogsHistory` RPC.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure-Go), gRPC / protobuf (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`), cobra CLI.

## Refinement vs spec (read first)

The approved spec (`docs/superpowers/specs/2026-06-17-m8b-fleet-logs-design.md`) described the daemon shipping mechanism as "subscribe to the Sink's live channel and flush on the tick," with a `Registry.SubscribeFleet` tap that fans in all sinks (including a per-Sink observer hook).

**This plan instead uses a pull-based `LogsFunc(sinceTsMs)` that reads the ring each tick** — the exact seam the metrics path already uses (`MetricsFunc`). It satisfies every approved constraint (backfill bounded by the ~1000-line ring, no new local store, no write amplification) with far less surface area: **no Sink changes, no observer hook, no tap goroutines, no extra lock ordering.** A line becomes queryable on the server up to one push interval (~2s) later, which is immaterial because `fleet logs` is backfill-only (no live follow). All other spec sections (logstore schema, proto, server side, CLI, retention, watermark semantics) are unchanged.

## Global Constraints

- Go module path is `marshal`; imports are `marshal/internal/...`.
- Do all work on the existing branch `m8b-fleet-logs` (already checked out).
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Final gate before finishing: `gofmt -l .` prints nothing, `go vet ./...` clean, `go build ./...` clean, `go test ./... -race -count=1` passes.
- Server-side selector resolution is name / label-prefix only (`sel == l || strings.HasPrefix(l, sel+"#")`); no numeric-ID resolution.
- Retention for stored logs is 7 days, pruned by the existing `ServeDir` 10-minute ticker.
- Watermark semantics: server derives it from `MaxTs()`; agent ships rows with `ts > watermark` (same-millisecond edge accepted, identical to metrics).

---

## File structure

| File | Responsibility | New/Modify |
|------|----------------|-----------|
| `internal/logstore/store.go` | SQLite per-agent log record store + `MergeTail` | **New** |
| `internal/logstore/store_test.go` | Unit tests for the store | **New** |
| `proto/marshal/v1/fleet.proto` | `LogShipLine`, `LogBatch`, `HelloAck.last_log_ts_ms`, `AgentMessage.logs`, `FleetLogsHistory` RPC | Modify |
| `internal/pb/*.pb.go` | Regenerated protobuf code | Regenerate |
| `internal/logs/registry.go` | `LabeledLine` type + `RingSince(sinceMs)` reader | Modify |
| `internal/logs/registry_ringsince_test.go` | Test `RingSince` incl. future sinks | **New** |
| `internal/fleet/client.go` | `LogsFunc`, `WithLogs`, `pushLogs`, log watermark | Modify |
| `internal/fleet/client_test.go` | Extend fake server; test log shipping | Modify |
| `internal/daemon/fleet.go` | `logsSince` adapter (registry → `LogsFunc`) | Modify |
| `internal/daemon/fleet_test.go` | Test `logsSince` | Modify |
| `internal/daemon/server.go` | Wire `fleet.WithLogs(logsSince(reg))` | Modify |
| `internal/server/logstores.go` | Lazy per-agent log stores (mirror `stores`) | **New** |
| `internal/server/logstores_test.go` | Unit tests for `logStores` | **New** |
| `internal/server/server.go` | `Connect` LogBatch branch, HelloAck log watermark, `FleetLogsHistory`, `NewServer`/`Serve`/`ServeDir` wiring, log pruning | Modify |
| `internal/server/server_test.go` | Tests for LogBatch ingest + `FleetLogsHistory` | Modify |
| `internal/server/e2e_test.go` | `TestE2ELogsIngestAndBackfill` | Modify |
| `cmd/marshal/fleet.go` | `marshal fleet logs` command + render | Modify |
| `docs/handoffs/2026-06-17-m8b-fleet-logs.md` | Session handoff | **New** |

---

## Task 1: `internal/logstore` package

**Files:**
- Create: `internal/logstore/store.go`
- Test: `internal/logstore/store_test.go`

**Interfaces:**
- Produces:
  - `type Line struct { TsMs int64; Label string; Stderr bool; Text string }`
  - `type StoredLine struct { TsMs int64; Label string; Stderr bool; Text string }`
  - `type StreamFilter int` with `StreamAny`, `StreamStdout`, `StreamStderr`
  - `func Open(path string) (*Store, error)`
  - `func (s *Store) Append(lines []Line) error`
  - `func (s *Store) Tail(label string, limit int, filter StreamFilter) ([]StoredLine, error)` — newest `limit` lines for one label, returned **ts-ascending**
  - `func (s *Store) MaxTs() (int64, error)`
  - `func (s *Store) Labels() ([]string, error)`
  - `func (s *Store) Prune(beforeMs int64) (int64, error)`
  - `func (s *Store) Close() error`
  - `func MergeTail(series [][]StoredLine, limit int) []StoredLine` — merge ts-ascending series, keep newest `limit`

- [ ] **Step 1: Write the failing test**

Create `internal/logstore/store_test.go`:

```go
package logstore

import "testing"

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir() + "/logs.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppendTailMaxTsLabels(t *testing.T) {
	st := open(t)
	if mx, _ := st.MaxTs(); mx != 0 {
		t.Fatalf("MaxTs on empty = %d, want 0", mx)
	}
	err := st.Append([]Line{
		{TsMs: 1000, Label: "api#0", Stderr: false, Text: "a"},
		{TsMs: 2000, Label: "api#0", Stderr: true, Text: "b"},
		{TsMs: 3000, Label: "api#1", Stderr: false, Text: "c"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if mx, _ := st.MaxTs(); mx != 3000 {
		t.Fatalf("MaxTs = %d, want 3000", mx)
	}
	labels, _ := st.Labels()
	if len(labels) != 2 || labels[0] != "api#0" || labels[1] != "api#1" {
		t.Fatalf("Labels = %v, want [api#0 api#1]", labels)
	}
	got, _ := st.Tail("api#0", 10, StreamAny)
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("Tail(api#0) = %+v, want a then b ascending", got)
	}
}

func TestTailLimitAndStreamFilter(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1, Label: "x#0", Stderr: false, Text: "out1"},
		{TsMs: 2, Label: "x#0", Stderr: true, Text: "err1"},
		{TsMs: 3, Label: "x#0", Stderr: false, Text: "out2"},
	})
	// limit keeps the newest, still ascending
	got, _ := st.Tail("x#0", 2, StreamAny)
	if len(got) != 2 || got[0].Text != "err1" || got[1].Text != "out2" {
		t.Fatalf("Tail limit=2 = %+v, want err1 then out2", got)
	}
	// stderr filter
	gotErr, _ := st.Tail("x#0", 10, StreamStderr)
	if len(gotErr) != 1 || gotErr[0].Text != "err1" {
		t.Fatalf("Tail stderr = %+v, want [err1]", gotErr)
	}
	// stdout filter
	gotOut, _ := st.Tail("x#0", 10, StreamStdout)
	if len(gotOut) != 2 || gotOut[0].Text != "out1" || gotOut[1].Text != "out2" {
		t.Fatalf("Tail stdout = %+v, want out1 then out2", gotOut)
	}
}

func TestPrune(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1000, Label: "x#0", Text: "old"},
		{TsMs: 5000, Label: "x#0", Text: "new"},
	})
	n, _ := st.Prune(3000)
	if n != 1 {
		t.Fatalf("Prune removed %d, want 1", n)
	}
	if mx, _ := st.MaxTs(); mx != 5000 {
		t.Fatalf("MaxTs after prune = %d, want 5000", mx)
	}
}

func TestMergeTail(t *testing.T) {
	a := []StoredLine{{TsMs: 1, Text: "a1"}, {TsMs: 3, Text: "a3"}}
	b := []StoredLine{{TsMs: 2, Text: "b2"}, {TsMs: 4, Text: "b4"}}
	got := MergeTail([][]StoredLine{a, b}, 3)
	if len(got) != 3 || got[0].Text != "b2" || got[1].Text != "a3" || got[2].Text != "b4" {
		t.Fatalf("MergeTail = %+v, want b2,a3,b4", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logstore/`
Expected: FAIL — build error, `Open`/`Store`/`Line` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/logstore/store.go`:

```go
// Package logstore persists per-instance captured log lines to a local SQLite
// database (pure-Go modernc.org/sqlite) and serves tail / since queries. It is
// the log analog of metricstore.
package logstore

import (
	"database/sql"
	"fmt"
	"sort"

	_ "modernc.org/sqlite"
)

// Line is one captured line to append.
type Line struct {
	TsMs   int64
	Label  string // "app#instance"
	Stderr bool
	Text   string
}

// StoredLine is one row read back from the store.
type StoredLine struct {
	TsMs   int64
	Label  string
	Stderr bool
	Text   string
}

// StreamFilter selects which streams a query returns.
type StreamFilter int

const (
	StreamAny    StreamFilter = iota // both stdout and stderr
	StreamStdout                     // stderr = 0 only
	StreamStderr                     // stderr = 1 only
)

// Store is a SQLite-backed log line store.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS log_line (
	ts     INTEGER NOT NULL,
	label  TEXT    NOT NULL,
	stderr INTEGER NOT NULL,
	text   TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_log_label_ts ON log_line(label, ts);`

// Open opens (creating if needed) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open logs db: %w", err)
	}
	// One process touches this DB from two goroutines (ingest + query handler);
	// serialize to sidestep SQLite locking entirely.
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

// Append writes all lines in a single transaction, in slice order.
func (s *Store) Append(lines []Line) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO log_line(ts, label, stderr, text) VALUES(?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, ln := range lines {
		if _, err := stmt.Exec(ln.TsMs, ln.Label, b2i(ln.Stderr), ln.Text); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Tail returns the newest `limit` lines for one label (after stream filtering),
// ordered oldest-first. limit <= 0 means no limit.
func (s *Store) Tail(label string, limit int, filter StreamFilter) ([]StoredLine, error) {
	q := `SELECT ts, label, stderr, text FROM log_line WHERE label = ?`
	args := []any{label}
	switch filter {
	case StreamStdout:
		q += ` AND stderr = 0`
	case StreamStderr:
		q += ` AND stderr = 1`
	}
	q += ` ORDER BY ts DESC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var desc []StoredLine
	for rows.Next() {
		var ln StoredLine
		var se int64
		if err := rows.Scan(&ln.TsMs, &ln.Label, &se, &ln.Text); err != nil {
			return nil, err
		}
		ln.Stderr = se != 0
		desc = append(desc, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to ascending.
	for i, j := 0, len(desc)-1; i < j; i, j = i+1, j-1 {
		desc[i], desc[j] = desc[j], desc[i]
	}
	return desc, nil
}

// MaxTs returns the largest stored ts, or 0 when the store is empty.
func (s *Store) MaxTs() (int64, error) {
	var mx sql.NullInt64
	if err := s.db.QueryRow(`SELECT max(ts) FROM log_line`).Scan(&mx); err != nil {
		return 0, err
	}
	if !mx.Valid {
		return 0, nil
	}
	return mx.Int64, nil
}

// Labels returns the distinct labels, ascending.
func (s *Store) Labels() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT label FROM log_line ORDER BY label`)
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

// Prune deletes lines with ts < beforeMs, returning the count removed.
func (s *Store) Prune(beforeMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM log_line WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// MergeTail merges per-label ascending series into one ascending stream and
// keeps the newest `limit` lines (limit <= 0 keeps all). Stable on equal ts.
func MergeTail(series [][]StoredLine, limit int) []StoredLine {
	var all []StoredLine
	for _, s := range series {
		all = append(all, s...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].TsMs < all[j].TsMs })
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logstore/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logstore/
git commit -m "feat(logstore): SQLite per-agent log record store

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Proto additions + regenerate

**Files:**
- Modify: `proto/marshal/v1/fleet.proto`
- Regenerate: `internal/pb/fleet.pb.go`, `internal/pb/fleet_grpc.pb.go`

**Interfaces:**
- Consumes: existing `LogStream` enum and `LogLine` message from `daemon.proto` (already imported by `fleet.proto`).
- Produces (generated Go in package `pb`): `LogShipLine{TsMs, Label, Stderr, Text}`, `LogBatch{Lines}`, `AgentMessage_Logs{Logs *LogBatch}`, `HelloAck.LastLogTsMs`, `FleetLogsHistoryRequest{AgentName, Selector, Lines, Stream}`, `FleetLogsHistoryResponse{Lines []*LogLine}`, `FleetClient.FleetLogsHistory`, `FleetServer.FleetLogsHistory`.

- [ ] **Step 1: Edit `proto/marshal/v1/fleet.proto`**

In `service Fleet`, add after the `FleetMetricsHistory` line:

```proto
  // Log history for one agent's app/instance (M8b); reuses daemon.proto's LogLine.
  rpc FleetLogsHistory(FleetLogsHistoryRequest) returns (FleetLogsHistoryResponse);
```

In `message AgentMessage`'s oneof, add:

```proto
    LogBatch logs = 4;          // captured stdout/stderr lines (M8b)
```

In `message HelloAck`, add field 2:

```proto
  int64 last_log_ts_ms = 2;    // server's stored log high-water-mark (0 = none)
```

After `message MetricBatch { ... }`, add:

```proto
// One captured log line, flattened to map 1:1 to a logstore row.
message LogShipLine {
  int64  ts_ms  = 1;
  string label  = 2; // "app#instance"
  bool   stderr = 3;
  string text   = 4;
}

message LogBatch { repeated LogShipLine lines = 1; }

message FleetLogsHistoryRequest {
  string    agent_name = 1;
  string    selector   = 2; // app name or "name#instance" label
  int32     lines      = 3; // backfill count
  LogStream stream     = 4; // reuse daemon.proto enum; unspecified = merged
}

message FleetLogsHistoryResponse { repeated LogLine lines = 1; } // LogLine from daemon.proto
```

- [ ] **Step 2: Regenerate protobuf code**

Run: `go generate ./internal/pb`
Expected: no output; `git status` shows `internal/pb/fleet.pb.go` and `internal/pb/fleet_grpc.pb.go` modified.

(If `go generate` fails to find `protoc`, run it directly from `internal/pb`: `protoc --go_out=../.. --go_opt=module=marshal --go-grpc_out=../.. --go-grpc_opt=module=marshal -I ../../proto ../../proto/marshal/v1/daemon.proto ../../proto/marshal/v1/fleet.proto`.)

- [ ] **Step 3: Verify the build**

Run: `go build ./...`
Expected: success. (The new `FleetLogsHistory` server method is not implemented yet, but `UnimplementedFleetServer` provides a default, so the build passes.)

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/
git commit -m "feat(proto): LogBatch, log watermark, FleetLogsHistory RPC

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `logs.Registry.RingSince` + `LabeledLine`

**Files:**
- Modify: `internal/logs/registry.go`
- Test: `internal/logs/registry_ringsince_test.go`

**Interfaces:**
- Consumes: existing `Sink.Backfill(n int) []Line` (n <= 0 returns the whole ring), `Line{Ts time.Time; Stderr bool; Text string}`.
- Produces:
  - `type LabeledLine struct { Label string; Ts time.Time; Stderr bool; Text string }`
  - `func (r *Registry) RingSince(sinceMs int64) []LabeledLine` — every current sink's ring lines with `Ts.UnixMilli() > sinceMs`, merged ascending by ts.

- [ ] **Step 1: Write the failing test**

Create `internal/logs/registry_ringsince_test.go`:

```go
package logs

import (
	"testing"
	"time"
)

func TestRingSinceMergesAndFiltersIncludingFutureSinks(t *testing.T) {
	base := time.Now()
	tick := base
	reg := NewRegistry(t.TempDir())
	reg.now = func() time.Time { tick = tick.Add(time.Millisecond); return tick }

	// existing sink: two lines
	a := reg.For("api#0")
	_, _ = a.Writer(false).Write([]byte("a1\na2\n"))

	cut := tick.UnixMilli() // watermark after the two api#0 lines

	// a sink created AFTER we captured `cut` must still be covered
	b := reg.For("web#0")
	_, _ = b.Writer(true).Write([]byte("b1\n"))

	got := reg.RingSince(cut)
	if len(got) != 1 || got[0].Label != "web#0" || got[0].Text != "b1" || !got[0].Stderr {
		t.Fatalf("RingSince(cut) = %+v, want one web#0 stderr line b1", got)
	}

	all := reg.RingSince(0)
	if len(all) != 3 {
		t.Fatalf("RingSince(0) = %d lines, want 3", len(all))
	}
	// ascending by ts
	for i := 1; i < len(all); i++ {
		if all[i].Ts.Before(all[i-1].Ts) {
			t.Fatalf("RingSince(0) not ascending: %+v", all)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logs/ -run TestRingSince`
Expected: FAIL — `RingSince`/`LabeledLine` undefined.

- [ ] **Step 3: Write the implementation**

In `internal/logs/registry.go`, add `"sort"` to the import block (alongside `"strings"`), then append:

```go
// LabeledLine is one ring line tagged with its instance label.
type LabeledLine struct {
	Label  string
	Ts     time.Time
	Stderr bool
	Text   string
}

// RingSince returns every current sink's in-memory ring lines with a timestamp
// strictly newer than sinceMs, merged ascending by timestamp. New sinks created
// after a prior call are naturally included (the sink map is read fresh).
func (r *Registry) RingSince(sinceMs int64) []LabeledLine {
	r.mu.Lock()
	type entry struct {
		label string
		sink  *Sink
	}
	snap := make([]entry, 0, len(r.sinks))
	for l, s := range r.sinks {
		snap = append(snap, entry{l, s})
	}
	r.mu.Unlock()

	var out []LabeledLine
	for _, e := range snap {
		for _, ln := range e.sink.Backfill(0) { // whole ring
			if ln.Ts.UnixMilli() > sinceMs {
				out = append(out, LabeledLine{Label: e.label, Ts: ln.Ts, Stderr: ln.Stderr, Text: ln.Text})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/logs/ -run TestRingSince`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/logs/registry.go internal/logs/registry_ringsince_test.go
git commit -m "feat(logs): Registry.RingSince merges ring lines across sinks since ts

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Fleet client log shipping

**Files:**
- Modify: `internal/fleet/client.go`
- Modify: `internal/fleet/client_test.go`

**Interfaces:**
- Consumes: `pb.LogShipLine`, `pb.LogBatch`, `pb.AgentMessage_Logs`, `pb.HelloAck.GetLastLogTsMs()` (from Task 2).
- Produces:
  - `type LogsFunc func(sinceTsMs int64) []*pb.LogShipLine`
  - `func WithLogs(fn LogsFunc) Option`
  - internal `pushLogs(stream, *int64) error`, plus a `logs LogsFunc` field and a log watermark seeded from `HelloAck`.

- [ ] **Step 1: Write the failing test**

In `internal/fleet/client_test.go`, extend the fake server and add a test. Add to the `fakeFleetServer` struct (after the `samples` field):

```go
	lines []*pb.LogShipLine
```

In `fakeFleetServer.Connect`, add a case after the `*pb.AgentMessage_Metrics` case:

```go
		case *pb.AgentMessage_Logs:
			f.mu.Lock()
			f.lines = append(f.lines, m.Logs.GetLines()...)
			f.mu.Unlock()
```

Also include the log watermark in the HelloAck the fake sends — change the existing send in the `*pb.AgentMessage_Hello` case to:

```go
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: f.ackWatermark, LastLogTsMs: f.ackLogWatermark},
			}})
```

Add the `ackLogWatermark int64` field next to `ackWatermark`, and a helper:

```go
func (f *fakeFleetServer) sawLine(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if l.GetText() == text {
			return true
		}
	}
	return false
}
```

Then add the test:

```go
func TestClientShipsLogsAndSeedsLogWatermark(t *testing.T) {
	fs := newFakeServer(t)
	fs.ackLogWatermark = 5000

	logs := func(since int64) []*pb.LogShipLine {
		all := []*pb.LogShipLine{
			{TsMs: 4000, Label: "api#0", Text: "old"},  // already on server
			{TsMs: 6000, Label: "api#0", Text: "fresh"}, // new
		}
		var out []*pb.LogShipLine
		for _, l := range all {
			if l.TsMs > since {
				out = append(out, l)
			}
		}
		return out
	}

	c := fleet.New(fs.addr, "web-1", "test", func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logs))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, func() bool { return fs.sawLine("fresh") && !fs.sawLine("old") })
	cancel()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestClientShipsLogs`
Expected: FAIL — `WithLogs`/`LogsFunc` undefined (and `LastLogTsMs`/`AgentMessage_Logs` only if Task 2 incomplete — it is complete).

- [ ] **Step 3: Write the implementation**

In `internal/fleet/client.go`:

Add the type after `MetricsFunc`:

```go
// LogsFunc returns captured log lines strictly newer than sinceTsMs.
type LogsFunc func(sinceTsMs int64) []*pb.LogShipLine
```

Add a `logs LogsFunc` field to the `Client` struct (after `metrics MetricsFunc`).

Add the option after `WithMetrics`:

```go
// WithLogs enables upstream log shipping sourced from fn.
func WithLogs(fn LogsFunc) Option { return func(c *Client) { c.logs = fn } }
```

In `connectOnce`, change the HelloAck handling to seed both watermarks:

```go
	// Receive the HelloAck to seed the metric and log watermarks.
	var watermark, logWM int64
	if ack, err := stream.Recv(); err != nil {
		return true, err
	} else if a := ack.GetHelloAck(); a != nil {
		watermark = a.GetLastMetricTsMs()
		logWM = a.GetLastLogTsMs()
	}
```

After the initial `pushMetrics` call, add an initial `pushLogs`:

```go
	if err := c.pushLogs(stream, &logWM); err != nil { // immediate backfill
		return true, err
	}
```

Inside the ticker loop, after the `pushMetrics` call, add:

```go
			if err := c.pushLogs(stream, &logWM); err != nil {
				return true, err
			}
```

Add the method (mirror of `pushMetrics`) at the end of the file:

```go
// pushLogs ships local lines newer than *watermark; on success advances it to
// the max ts shipped. No-op when log shipping is disabled or nothing is new.
func (c *Client) pushLogs(stream pb.Fleet_ConnectClient, watermark *int64) error {
	if c.logs == nil {
		return nil
	}
	lines := c.logs(*watermark)
	if len(lines) == 0 {
		return nil
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Logs{
		Logs: &pb.LogBatch{Lines: lines},
	}}); err != nil {
		return err
	}
	var mx int64
	for _, l := range lines {
		if l.GetTsMs() > mx {
			mx = l.GetTsMs()
		}
	}
	if mx > *watermark {
		*watermark = mx
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/fleet/ -run TestClientShipsLogs`
Expected: PASS. Then `go test ./internal/fleet/` to confirm the existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/client.go internal/fleet/client_test.go
git commit -m "feat(fleet): ship log lines with HelloAck-seeded log watermark

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Daemon `logsSince` adapter + wiring

**Files:**
- Modify: `internal/daemon/fleet.go`
- Modify: `internal/daemon/fleet_test.go`
- Modify: `internal/daemon/server.go`

**Interfaces:**
- Consumes: `logs.Registry.RingSince` (Task 3), `fleet.LogsFunc`/`fleet.WithLogs` (Task 4), `pb.LogShipLine`.
- Produces: `func logsSince(reg *logs.Registry) fleet.LogsFunc`.

- [ ] **Step 1: Write the failing test**

In `internal/daemon/fleet_test.go`, add (the file already imports `marshal/internal/logs`? if not, add it):

```go
func TestLogsSinceShipsNewRingLines(t *testing.T) {
	reg := logs.NewRegistry(t.TempDir())
	s := reg.For("api#0")
	_, _ = s.Writer(false).Write([]byte("hello\nworld\n"))

	fn := logsSince(reg)
	got := fn(0)
	if len(got) != 2 || got[0].GetText() != "hello" || got[1].GetText() != "world" {
		t.Fatalf("logsSince(0) = %+v, want hello,world", got)
	}
	if got[0].GetLabel() != "api#0" {
		t.Fatalf("label = %q, want api#0", got[0].GetLabel())
	}
	// strictly-newer filter: everything already shipped -> nothing new
	wm := got[1].GetTsMs()
	if rest := fn(wm); len(rest) != 0 {
		t.Fatalf("logsSince(maxTs) = %+v, want none", rest)
	}
}
```

Ensure the test file imports `"marshal/internal/logs"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestLogsSince`
Expected: FAIL — `logsSince` undefined.

- [ ] **Step 3: Write the implementation**

In `internal/daemon/fleet.go`, add `"marshal/internal/logs"` to the imports, then append:

```go
// logsSince adapts the log registry's ring to the fleet client's LogsFunc:
// ring lines across all sinks strictly newer than sinceTsMs, as wire lines.
func logsSince(reg *logs.Registry) fleet.LogsFunc {
	return func(sinceTsMs int64) []*pb.LogShipLine {
		if reg == nil {
			return nil
		}
		lines := reg.RingSince(sinceTsMs)
		out := make([]*pb.LogShipLine, 0, len(lines))
		for _, ln := range lines {
			out = append(out, &pb.LogShipLine{
				TsMs: ln.Ts.UnixMilli(), Label: ln.Label, Stderr: ln.Stderr, Text: ln.Text,
			})
		}
		return out
	}
}
```

In `internal/daemon/server.go`, change the fleet client construction (currently lines ~237-238) to add the logs source:

```go
		fc := fleet.New(sc.Address, name, version.String(),
			fleetSnapshot(mgr, sampler),
			fleet.WithMetrics(metricsSince(mdb)),
			fleet.WithLogs(logsSince(reg)))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestLogsSince` then `go build ./...`
Expected: PASS and clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/fleet.go internal/daemon/fleet_test.go internal/daemon/server.go
git commit -m "feat(daemon): ship ring log lines to the fleet via logsSince

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Server `logStores` type

**Files:**
- Create: `internal/server/logstores.go`
- Test: `internal/server/logstores_test.go`

**Interfaces:**
- Consumes: `logstore.Open`/`logstore.Store`/`logstore.Line` (Task 1), existing `sanitizeAgent` (in `internal/server/stores.go`).
- Produces:
  - `type logStores struct{...}`
  - `func newLogStores(dir string) *logStores`
  - `func (s *logStores) get(agent string) (*logstore.Store, error)`
  - `func (s *logStores) has(agent string) bool`
  - `func (s *logStores) closeAll() error`
  - `func (s *logStores) pruneAll(beforeMs int64)`

- [ ] **Step 1: Write the failing test**

Create `internal/server/logstores_test.go`:

```go
package server

import (
	"testing"

	"marshal/internal/logstore"
)

func TestLogStoresLazyOpenAndHas(t *testing.T) {
	ss := newLogStores(t.TempDir())
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

func TestLogStoresPruneAll(t *testing.T) {
	ss := newLogStores(t.TempDir())
	defer ss.closeAll()
	st, _ := ss.get("web-1")
	_ = st.Append([]logstore.Line{{TsMs: 1000, Label: "a#0", Text: "old"}})
	_ = st.Append([]logstore.Line{{TsMs: 5000, Label: "a#0", Text: "new"}})
	ss.pruneAll(3000)
	if mx, _ := st.MaxTs(); mx != 5000 {
		t.Fatalf("MaxTs after prune = %d, want 5000", mx)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestLogStores`
Expected: FAIL — `newLogStores` undefined.

- [ ] **Step 3: Write the implementation**

Create `internal/server/logstores.go`:

```go
package server

import (
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/logstore"
)

// logStores manages lazily-opened per-agent log stores under a data dir.
type logStores struct {
	dir string
	mu  sync.Mutex
	m   map[string]*logstore.Store
}

func newLogStores(dir string) *logStores {
	return &logStores{dir: dir, m: map[string]*logstore.Store{}}
}

func (s *logStores) agentDir(agent string) string {
	return filepath.Join(s.dir, "agents", sanitizeAgent(agent))
}

// get returns the agent's store, opening (and creating its directory) on first use.
func (s *logStores) get(agent string) (*logstore.Store, error) {
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
	st, err := logstore.Open(filepath.Join(dir, "logs.db"))
	if err != nil {
		return nil, err
	}
	s.m[key] = st
	return st, nil
}

// has reports whether the agent's store directory exists on disk.
func (s *logStores) has(agent string) bool {
	if _, err := os.Stat(s.agentDir(agent)); err == nil {
		return true
	}
	return false
}

func (s *logStores) closeAll() error {
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

// pruneAll deletes lines older than beforeMs from every open store.
func (s *logStores) pruneAll(beforeMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		_, _ = st.Prune(beforeMs)
	}
}
```

Note: `has` uses the same `agents/<name>` directory as the metric `stores`; both stores live side by side (`metrics.db` and `logs.db`) in that directory, so `has` is true once either store has been opened. `FleetLogsHistory` (Task 8) guards on its own `Labels()` result, so a metrics-only agent dir simply yields no log labels.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestLogStores`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/logstores.go internal/server/logstores_test.go
git commit -m "feat(server): lazily-opened per-agent log stores

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Server ingest — `Connect` LogBatch + HelloAck watermark + wiring + pruning

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `logStores` (Task 6), `pb.AgentMessage_Logs`, `pb.HelloAck.LastLogTsMs`, `logstore.Line`.
- Produces:
  - `Server` gains a `logs *logStores` field.
  - `func NewServer(reg *Registry, ss *stores, ls *logStores) *Server` (signature change).
  - `func Serve(ctx, lis, reg, ss, ls) error` (signature change).
  - `ServeDir` creates a `logStores`, prunes it, and closes it.
  - internal `storeLogBatch(agent string, lines []*pb.LogShipLine)`.

- [ ] **Step 1: Write the failing test**

In `internal/server/server_test.go`, add a test that drives `Connect` with a Hello then a LogBatch and asserts storage + the acked watermark. Use the existing test helpers in that file for constructing a server; if the file builds a `Server` via `NewServer`, those calls will need the new third argument (update them in Step 3). Add:

```go
func TestConnectStoresLogBatchAndAcksWatermark(t *testing.T) {
	dir := t.TempDir()
	ls := newLogStores(dir)
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls)

	srv.storeLogBatch("web-1", []*pb.LogShipLine{
		{TsMs: 1000, Label: "api#0", Stderr: false, Text: "l1"},
		{TsMs: 2000, Label: "api#0", Stderr: true, Text: "l2"},
	})

	st, _ := ls.get("web-1")
	if mx, _ := st.MaxTs(); mx != 2000 {
		t.Fatalf("MaxTs = %d, want 2000", mx)
	}
	got, _ := st.Tail("api#0", 10, logstore.StreamAny)
	if len(got) != 2 || got[0].Text != "l1" || got[1].Text != "l2" {
		t.Fatalf("stored = %+v, want l1,l2", got)
	}
}
```

Add imports to the test file as needed: `"time"`, `"marshal/internal/logstore"`, `"marshal/internal/pb"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestConnectStoresLogBatch`
Expected: FAIL — `NewServer` arity / `storeLogBatch` undefined.

- [ ] **Step 3: Write the implementation**

In `internal/server/server.go`:

Add `"marshal/internal/logstore"` to imports.

Add a field to `Server`:

```go
type Server struct {
	pb.UnimplementedFleetServer
	reg    *Registry
	stores *stores
	logs   *logStores
}
```

Change the constructor:

```go
// NewServer wires a Fleet server to a registry and (optional) metric/log stores.
func NewServer(reg *Registry, ss *stores, ls *logStores) *Server {
	return &Server{reg: reg, stores: ss, logs: ls}
}
```

In `Connect`, set the log watermark in the Hello branch (replace the HelloAck send):

```go
			s.reg.Open(name)
			var watermark, logWM int64
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					watermark, _ = st.MaxTs()
				}
			}
			if s.logs != nil {
				if st, err := s.logs.get(name); err == nil {
					logWM, _ = st.MaxTs()
				}
			}
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: watermark, LastLogTsMs: logWM},
			}})
```

Add a new case in the `switch` (after `*pb.AgentMessage_Metrics`):

```go
		case *pb.AgentMessage_Logs:
			if name != "" && s.logs != nil {
				s.storeLogBatch(name, m.Logs.GetLines())
			}
```

Add the ingest method (near `storeBatch`):

```go
// storeLogBatch appends a flattened log batch oldest-first, so the store's
// max(ts) always reflects a fully-committed prefix.
func (s *Server) storeLogBatch(agent string, lines []*pb.LogShipLine) {
	st, err := s.logs.get(agent)
	if err != nil {
		log.Printf("fleet: open log store for %s: %v", agent, err)
		return
	}
	rows := make([]logstore.Line, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, logstore.Line{
			TsMs: l.GetTsMs(), Label: l.GetLabel(), Stderr: l.GetStderr(), Text: l.GetText(),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].TsMs < rows[j].TsMs })
	if err := st.Append(rows); err != nil {
		log.Printf("fleet: append logs for %s: %v", agent, err)
	}
}
```

Change `Serve` to take and manage `ls`:

```go
// Serve registers the Fleet service on lis and serves until ctx is canceled.
// ss/ls may be nil (no storage); when set they are closed on shutdown.
func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores, ls *logStores) error {
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, NewServer(reg, ss, ls))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
		if ss != nil {
			_ = ss.closeAll()
		}
		if ls != nil {
			_ = ls.closeAll()
		}
	}()
	return gs.Serve(lis)
}
```

Change `ServeDir` to build and prune the log stores:

```go
func ServeDir(ctx context.Context, lis net.Listener, dataDir string, opts ...RegOption) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	ss := newStores(dataDir)
	ls := newLogStores(dataDir)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		const retentionMs = int64(7 * 24 * 60 * 60 * 1000)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cutoff := time.Now().UnixMilli() - retentionMs
				ss.pruneAll(cutoff)
				ls.pruneAll(cutoff)
			}
		}
	}()
	return Serve(ctx, lis, NewRegistry(opts...), ss, ls)
}
```

Update **all other callers** of `NewServer` and `Serve` to the new arity (compiler will flag them). Known callers to fix:
- `internal/fleet/client_test.go`: `server.Serve(sctx, lis, reg, nil)` → `server.Serve(sctx, lis, reg, nil, nil)` (two places).
- Any `NewServer(reg, ss)` in `internal/server/server_test.go` → add `, nil` (or the appropriate `*logStores`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ ./internal/fleet/`
Expected: PASS (both packages build and pass with the new arity).

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go internal/fleet/client_test.go
git commit -m "feat(server): persist LogBatch, ack log watermark, prune logs

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: Server `FleetLogsHistory` handler

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `logStores` (Task 6), `logstore.Tail`/`logstore.MergeTail`/`logstore.StreamFilter` (Task 1), `pb.FleetLogsHistoryRequest`/`pb.FleetLogsHistoryResponse`/`pb.LogLine`/`pb.LogStream` (Task 2).
- Produces: `func (s *Server) FleetLogsHistory(ctx, req) (*pb.FleetLogsHistoryResponse, error)`.

- [ ] **Step 1: Write the failing test**

In `internal/server/server_test.go`, add:

```go
func TestFleetLogsHistorySelectorMergeAndFilter(t *testing.T) {
	dir := t.TempDir()
	ls := newLogStores(dir)
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls)

	srv.storeLogBatch("web-1", []*pb.LogShipLine{
		{TsMs: 1, Label: "api#0", Stderr: false, Text: "o0"},
		{TsMs: 2, Label: "api#1", Stderr: true, Text: "e1"},
		{TsMs: 3, Label: "api#0", Stderr: false, Text: "o0b"},
	})

	// selector "api" resolves both api#0 and api#1, merged ascending by ts.
	resp, err := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 10,
	})
	if err != nil {
		t.Fatalf("FleetLogsHistory: %v", err)
	}
	if len(resp.GetLines()) != 3 {
		t.Fatalf("got %d lines, want 3", len(resp.GetLines()))
	}
	if resp.GetLines()[0].GetLine() != "o0" || resp.GetLines()[1].GetLine() != "e1" {
		t.Fatalf("merge order wrong: %+v", resp.GetLines())
	}
	if resp.GetLines()[1].GetName() != "api" || resp.GetLines()[1].GetInstanceId() != 1 {
		t.Fatalf("label parse wrong: %+v", resp.GetLines()[1])
	}

	// stderr filter
	respErr, _ := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 10, Stream: pb.LogStream_LOG_STREAM_STDERR,
	})
	if len(respErr.GetLines()) != 1 || respErr.GetLines()[0].GetLine() != "e1" {
		t.Fatalf("stderr filter = %+v, want [e1]", respErr.GetLines())
	}
}

func TestFleetLogsHistoryUnknownAgent(t *testing.T) {
	ls := newLogStores(t.TempDir())
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls)
	_, err := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "ghost", Selector: "api", Lines: 10,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}
```

Ensure the test file imports `"context"`, `"google.golang.org/grpc/codes"`, `"google.golang.org/grpc/status"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestFleetLogsHistory`
Expected: FAIL — `FleetLogsHistory` returns `Unimplemented` (the generated default), so assertions fail.

- [ ] **Step 3: Write the implementation**

In `internal/server/server.go`, add the handler and a default line count constant, plus a label parser:

```go
const defaultLogLines = 15

// FleetLogsHistory returns the most recent stored log lines for one agent's
// app/instance selector, merged across instances and filtered by stream.
func (s *Server) FleetLogsHistory(_ context.Context, req *pb.FleetLogsHistoryRequest) (*pb.FleetLogsHistoryResponse, error) {
	if s.logs == nil || !s.logs.has(req.GetAgentName()) {
		return nil, status.Errorf(codes.NotFound, "no log history for agent %q", req.GetAgentName())
	}
	st, err := s.logs.get(req.GetAgentName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "open log store: %v", err)
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
	limit := int(req.GetLines())
	if limit <= 0 {
		limit = defaultLogLines
	}
	filter := streamFilter(req.GetStream())

	var series [][]logstore.StoredLine
	for _, l := range matched {
		lines, err := st.Tail(l, limit, filter)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "tail: %v", err)
		}
		series = append(series, lines)
	}

	resp := &pb.FleetLogsHistoryResponse{}
	for _, ln := range logstore.MergeTail(series, limit) {
		name, idx := splitLabel(ln.Label)
		resp.Lines = append(resp.Lines, &pb.LogLine{
			Name: name, InstanceId: idx, Stderr: ln.Stderr, Line: ln.Text,
		})
	}
	return resp, nil
}

// streamFilter maps the wire enum to a logstore filter.
func streamFilter(st pb.LogStream) logstore.StreamFilter {
	switch st {
	case pb.LogStream_LOG_STREAM_STDOUT:
		return logstore.StreamStdout
	case pb.LogStream_LOG_STREAM_STDERR:
		return logstore.StreamStderr
	default:
		return logstore.StreamAny
	}
}

// splitLabel parses "name#idx" into its parts (idx 0 when absent/unparseable).
func splitLabel(label string) (string, int32) {
	i := strings.LastIndexByte(label, '#')
	if i < 0 {
		return label, 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return label[:i], int32(n)
}
```

Add `"strconv"` to the imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): FleetLogsHistory tail/merge/filter handler

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: CLI — `marshal fleet logs`

**Files:**
- Modify: `cmd/marshal/fleet.go`

**Interfaces:**
- Consumes: `pb.NewFleetClient(...).FleetLogsHistory`, `pb.FleetLogsHistoryRequest`, `pb.LogLine`, `pb.LogStream`, existing `resolveServer`, existing `streamFromFlags` (in `cmd/marshal/control.go`).
- Produces: `func fleetLogsCmd() *cobra.Command`, wired into `fleetCmd()`.

- [ ] **Step 1: Add the command**

In `cmd/marshal/fleet.go`, register the subcommand in `fleetCmd()`:

```go
	cmd.AddCommand(fleetPsCmd())
	cmd.AddCommand(fleetMetricsCmd())
	cmd.AddCommand(fleetLogsCmd())
```

Add the command (it reuses `streamFromFlags` from `control.go`, same `main` package):

```go
func fleetLogsCmd() *cobra.Command {
	var serverAddr string
	var lines int
	var stdoutOnly, stderrOnly bool
	cmd := &cobra.Command{
		Use:   "logs <agent> <name|label>",
		Short: "Show recent captured logs for an app/instance on one agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			streamSel, err := streamFromFlags(stdoutOnly, stderrOnly)
			if err != nil {
				return err
			}
			conn, err := grpc.NewClient(resolveServer(serverAddr),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).FleetLogsHistory(ctx, &pb.FleetLogsHistoryRequest{
				AgentName: args[0],
				Selector:  args[1],
				Lines:     int32(lines),
				Stream:    streamSel,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, ln := range resp.GetLines() {
				fmt.Fprintf(out, "%s#%d | %s\n", ln.GetName(), ln.GetInstanceId(), ln.GetLine())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 15, "number of lines to show")
	cmd.Flags().BoolVar(&stdoutOnly, "stdout", false, "show only stdout")
	cmd.Flags().BoolVar(&stderrOnly, "stderr", false, "show only stderr")
	return cmd
}
```

- [ ] **Step 2: Verify the build and help**

Run: `go build -o marshal ./cmd/marshal && ./marshal fleet logs --help`
Expected: build succeeds; help shows the `<agent> <name|label>` usage and `--server`, `-n`, `--stdout`, `--stderr` flags.

- [ ] **Step 3: Commit**

```bash
git add cmd/marshal/fleet.go
git commit -m "feat(cli): marshal fleet logs backfill command

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 10: End-to-end ingest + reconnect-backfill test

**Files:**
- Modify: `internal/server/e2e_test.go`

**Interfaces:**
- Consumes: `ServeDir`, `fleet.New`/`WithLogs`/`WithInterval`, `pb.FleetLogsHistory*`, `pb.LogShipLine`.

- [ ] **Step 1: Write the test (it exercises already-implemented code)**

In `internal/server/e2e_test.go`, add a helper and a test mirroring `TestE2EMetricsIngestAndBackfill`:

```go
func waitForLogs(t *testing.T, conn *grpc.ClientConn, agent, selector string, wantLines int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := pb.NewFleetClient(conn).FleetLogsHistory(ctx, &pb.FleetLogsHistoryRequest{
			AgentName: agent, Selector: selector, Lines: 100,
		})
		cancel()
		if err == nil && len(resp.GetLines()) >= wantLines {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("logs for %s/%s never reached %d lines", agent, selector, wantLines)
}

func TestE2ELogsIngestAndBackfill(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Now().UnixMilli()

	var mu sync.Mutex
	local := []*pb.LogShipLine{
		{TsMs: base - 2000, Label: "api#0", Text: "line1"},
		{TsMs: base - 1000, Label: "api#0", Text: "line2"},
	}
	logsFn := func(since int64) []*pb.LogShipLine {
		mu.Lock()
		defer mu.Unlock()
		var out []*pb.LogShipLine
		for _, l := range local {
			if l.TsMs > since {
				out = append(out, l)
			}
		}
		return out
	}

	// --- leg 1: serve, connect, ship the first two lines ---
	lis1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = ServeDir(ctx1, lis1, dataDir) }()

	c1 := fleet.New(lis1.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logsFn))
	cctx1, ccancel1 := context.WithCancel(context.Background())
	go c1.Run(cctx1)

	conn1 := e2eDialFleet(t, lis1.Addr().String())
	waitForLogs(t, conn1, "web-1", "api", 2)
	conn1.Close()
	ccancel1()
	cancel1()
	lis1.Close()

	// --- leg 2: add a gap line, restart server on SAME dir, reconnect ---
	mu.Lock()
	local = append(local, &pb.LogShipLine{TsMs: base, Label: "api#0", Text: "line3"})
	mu.Unlock()

	lis2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = ServeDir(ctx2, lis2, dataDir) }()

	c2 := fleet.New(lis2.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logsFn))
	cctx2, ccancel2 := context.WithCancel(context.Background())
	defer ccancel2()
	go c2.Run(cctx2)

	conn2 := e2eDialFleet(t, lis2.Addr().String())
	defer conn2.Close()
	// After reconnect the watermark is seeded from the persisted max(ts), so the
	// client ships only the gap line — total 3 lines proves backfill works.
	waitForLogs(t, conn2, "web-1", "api", 3)
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/server/ -run TestE2ELogs`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/server/e2e_test.go
git commit -m "test(server): e2e log ingest + reconnect backfill

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 11: Full gate + handoff

**Files:**
- Create: `docs/handoffs/2026-06-17-m8b-fleet-logs.md`

- [ ] **Step 1: Run the full gate**

Run:
```bash
gofmt -l .
go vet ./...
go build ./...
go test ./... -race -count=1
```
Expected: `gofmt -l .` prints nothing; vet/build clean; all tests pass. Fix anything that fails before continuing.

- [ ] **Step 2: Manual smoke test**

```bash
go build -o marshal ./cmd/marshal
XDG_DATA_HOME=/tmp/m8bsmoke ./marshal server --listen :9000 &   # server
# In another shell, with an app.yaml that has server:{address: localhost:9000, name: dev-1}
#   and an app that prints output, e.g. a `while true; do echo tick; sleep 1; done` ticker:
./marshal start /tmp/m8bapp.yaml
sleep 5
./marshal fleet logs dev-1 ticker --server localhost:9000   # expect recent "tick" lines
```
Capture the actual output for the handoff. Stop the server and daemon when done.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-17-m8b-fleet-logs.md` covering: current state (M8b complete, branch `m8b-fleet-logs`), what changed and key decisions (pull-based ring shipping refinement vs the spec's subscribe mechanism, per-agent `logs.db`, log watermark in `HelloAck`), build/run/test commands, the smoke-test proof from Step 2, deferred/known issues (ring-bounded backfill, same-ms watermark edge, no live follow, name/label-prefix selector only), and the concrete next step (merge `m8b-fleet-logs` to `main` via the `finishing-a-development-branch` skill; then the next milestone).

- [ ] **Step 4: Commit**

```bash
git add docs/handoffs/2026-06-17-m8b-fleet-logs.md
git commit -m "docs: M8b fleet-logs handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-review notes

- **Spec coverage:** logstore (§3) → Task 1; proto (§4) → Task 2; daemon ring shipping (§5, refined to pull-based) → Tasks 3-5; server `logStores` + ingest + watermark + pruning (§6) → Tasks 6-7; `FleetLogsHistory` (§6) → Task 8; CLI (§7) → Task 9; testing (§8) → tests in each task + Task 10; retention (§6) → Task 7 `ServeDir`. All spec sections map to tasks.
- **Type consistency:** `logstore.Line`/`StoredLine`/`StreamFilter`/`Tail`/`MergeTail` defined in Task 1 are used verbatim in Tasks 6-8; `LabeledLine`/`RingSince` (Task 3) used in Task 5; `LogsFunc`/`WithLogs` (Task 4) used in Task 5; `NewServer`/`Serve` new arity (Task 7) is fixed in all callers in the same task.
- **No placeholders:** every code and test step contains complete code; commands have expected output.
