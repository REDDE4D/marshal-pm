# M13 — Live Log Tailing in the Dashboard — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface a process's captured stdout/stderr logs in the web dashboard, tailing new lines as they arrive, via cursor-based polling — the log analog of the M12 metric charts.

**Architecture:** Logs are already captured server-side in per-agent SQLite (`internal/logstore`). This plan adds a rowid-cursor query to the store, a thin `*server.logStores` wrapper, a session-guarded `GET /api/logs` endpoint backed by a structural `LogsHistory` interface (no import cycle — the dashboard imports only the leaf `logstore` package), and a **Charts | Logs** tab inside the existing M12 expandable per-process detail panel that polls every 1.5s.

**Tech Stack:** Go (stdlib + `modernc.org/sqlite`), React + TypeScript (Vite), hand-rolled UI (no charting/log libraries).

## Global Constraints

- TDD: failing test first, then implementation. Go packages stay small and focused.
- Cursor is **rowid**, never `ts` (avoids same-millisecond drop/duplicate; monotonic because `Prune` deletes the oldest/smallest rowids).
- No new runtime dependency; no new transport (poll only, consistent with fleet/metrics).
- Unknown agent / no-match selector → graceful empty, HTTP 200 (mirrors `/api/metrics`).
- Frontend has **no JS test harness**; frontend tasks are verified by `make ui` building cleanly. The built SPA is committed under `internal/dashboard/dist/` so `go build` embeds it without a Node toolchain.
- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Defaults: poll interval **1.5s**, default backfill **500**, backfill options **100/500/1000**, client buffer cap **5000**, server `limit` cap **5000**.
- Final gate before finishing: `go test ./... -race -count=1`, `gofmt -l .` (silent), `go vet ./...`, `make ui`, `go build -o marshal ./cmd/marshal`.
- All work on a feature branch `m13-logs` (branch from `main`), not directly on `main`.

---

### Task 0: Create the feature branch

- [ ] **Step 1: Branch from main**

Run:
```bash
cd "/Users/sebastiankuprat/process manager"
git checkout -b m13-logs
git status
```
Expected: `On branch m13-logs`, working tree clean.

---

### Task 1: `logstore.Since` — rowid cursor query

**Files:**
- Modify: `internal/logstore/store.go`
- Test: `internal/logstore/store_test.go`

**Interfaces:**
- Consumes: existing `Store`, `StoredLine`, `StreamFilter`, `Append`, `Prune`.
- Produces:
  - `StoredLine.RowID int64` (new field).
  - `func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter) ([]StoredLine, int64, error)` — lines with `rowid > afterRowID` ascending by rowid (or, when `afterRowID <= 0`, the newest `limit` ascending), plus the max rowid returned as the next cursor. Empty result returns the unchanged `afterRowID` as cursor.

- [ ] **Step 1: Write the failing tests**

Add to `internal/logstore/store_test.go`:

```go
func TestSinceBackfillAndFollow(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1, Label: "a#0", Text: "l1"},
		{TsMs: 1, Label: "a#1", Text: "l2"}, // same ts, different instance
		{TsMs: 2, Label: "a#0", Stderr: true, Text: "l3"},
	})
	// backfill: newest 2 across both labels, ascending by rowid
	got, cur, err := st.Since([]string{"a#0", "a#1"}, 0, 2, StreamAny)
	if err != nil {
		t.Fatalf("since backfill: %v", err)
	}
	if len(got) != 2 || got[0].Text != "l2" || got[1].Text != "l3" {
		t.Fatalf("backfill = %+v, want l2 then l3", got)
	}
	if cur != got[1].RowID || got[1].RowID == 0 {
		t.Fatalf("cursor = %d, want max rowid %d", cur, got[1].RowID)
	}
	// follow after cursor: nothing new, cursor unchanged
	got2, cur2, _ := st.Since([]string{"a#0", "a#1"}, cur, 100, StreamAny)
	if len(got2) != 0 || cur2 != cur {
		t.Fatalf("follow empty = %+v cur=%d, want none and cur=%d", got2, cur2, cur)
	}
	// append then follow returns only the new line, advancing the cursor
	_ = st.Append([]Line{{TsMs: 3, Label: "a#0", Text: "l4"}})
	got3, cur3, _ := st.Since([]string{"a#0", "a#1"}, cur, 100, StreamAny)
	if len(got3) != 1 || got3[0].Text != "l4" || cur3 <= cur {
		t.Fatalf("follow new = %+v cur=%d", got3, cur3)
	}
}

func TestSinceStreamFilter(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1, Label: "a#0", Text: "out"},
		{TsMs: 2, Label: "a#0", Stderr: true, Text: "err"},
	})
	got, _, _ := st.Since([]string{"a#0"}, 0, 100, StreamStderr)
	if len(got) != 1 || got[0].Text != "err" {
		t.Fatalf("stderr filter = %+v, want [err]", got)
	}
}

func TestSinceCursorSafeAfterPrune(t *testing.T) {
	st := open(t)
	_ = st.Append([]Line{
		{TsMs: 1000, Label: "a#0", Text: "old"},
		{TsMs: 5000, Label: "a#0", Text: "new"},
	})
	_, cur, _ := st.Since([]string{"a#0"}, 0, 100, StreamAny)
	if _, err := st.Prune(3000); err != nil { // removes "old" (smallest rowid)
		t.Fatalf("prune: %v", err)
	}
	_ = st.Append([]Line{{TsMs: 6000, Label: "a#0", Text: "newer"}})
	got, _, _ := st.Since([]string{"a#0"}, cur, 100, StreamAny)
	if len(got) != 1 || got[0].Text != "newer" {
		t.Fatalf("after prune follow = %+v, want [newer]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/logstore/ -run TestSince -v`
Expected: compile error / FAIL — `st.Since undefined` and `StoredLine.RowID` unknown.

- [ ] **Step 3: Add the `RowID` field**

In `internal/logstore/store.go`, change the `StoredLine` struct:

```go
// StoredLine is one row read back from the store.
type StoredLine struct {
	RowID  int64
	TsMs   int64
	Label  string
	Stderr bool
	Text   string
}
```

- [ ] **Step 4: Add `strings` to the imports**

In `internal/logstore/store.go`, update the import block to:

```go
import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)
```

- [ ] **Step 5: Implement `Since`**

Append to `internal/logstore/store.go` (after `Tail`):

```go
// Since returns lines for the given labels with rowid > afterRowID, ascending by
// rowid, plus the max rowid returned (the next cursor). When afterRowID <= 0 it
// returns the newest `limit` lines instead (backfill), still ascending by rowid.
// limit <= 0 means no limit. An empty result returns the unchanged afterRowID so
// the caller's cursor never goes backwards.
func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter) ([]StoredLine, int64, error) {
	if len(labels) == 0 {
		return nil, afterRowID, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+2)
	for i, l := range labels {
		ph[i] = "?"
		args = append(args, l)
	}
	q := `SELECT rowid, ts, label, stderr, text FROM log_line WHERE label IN (` + strings.Join(ph, ",") + `)`
	switch filter {
	case StreamStdout:
		q += ` AND stderr = 0`
	case StreamStderr:
		q += ` AND stderr = 1`
	}
	reverse := false
	if afterRowID > 0 {
		q += ` AND rowid > ? ORDER BY rowid`
		args = append(args, afterRowID)
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
	} else {
		// backfill: newest `limit` by rowid, reversed below to ascending.
		q += ` ORDER BY rowid DESC`
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		reverse = true
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, afterRowID, err
	}
	defer rows.Close()
	var out []StoredLine
	for rows.Next() {
		var ln StoredLine
		var se int64
		if err := rows.Scan(&ln.RowID, &ln.TsMs, &ln.Label, &se, &ln.Text); err != nil {
			return nil, afterRowID, err
		}
		ln.Stderr = se != 0
		out = append(out, ln)
	}
	if err := rows.Err(); err != nil {
		return nil, afterRowID, err
	}
	if reverse {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	cursor := afterRowID
	for _, ln := range out {
		if ln.RowID > cursor {
			cursor = ln.RowID
		}
	}
	return out, cursor, nil
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/logstore/ -v`
Expected: PASS (including the existing `Tail`/`Prune`/`MergeTail` tests — `RowID` defaults to 0 there, which is fine).

- [ ] **Step 7: Commit**

```bash
git add internal/logstore/store.go internal/logstore/store_test.go
git commit -m "feat(logstore): rowid-cursor Since query for tailing

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `server.logStores.Since` selector wrapper

**Files:**
- Modify: `internal/server/logstores.go`
- Test: `internal/server/logstores_test.go`

**Interfaces:**
- Consumes: `logstore.Store.Since` (Task 1), existing `logStores.has`/`get`.
- Produces: `func (s *logStores) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error)` — resolves the selector to labels (exact match or `selector#` prefix), then delegates to the store. Unknown agent returns `(nil, 0, nil)`; a known agent with no matching labels returns `(nil, afterRowID, nil)`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/logstores_test.go`:

```go
func TestLogStoresSinceSelector(t *testing.T) {
	ls := newLogStores(t.TempDir())
	st, err := ls.get("dev-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = st.Append([]logstore.Line{
		{TsMs: 1, Label: "web#0", Text: "a"},
		{TsMs: 2, Label: "web#1", Text: "b"},
		{TsMs: 3, Label: "api#0", Text: "c"},
	})
	// selector "web" matches web#0 and web#1 (prefix), not api#0
	got, cur, err := ls.Since("dev-1", "web", 0, 100, logstore.StreamAny)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(got) != 2 || got[0].Text != "a" || got[1].Text != "b" {
		t.Fatalf("Since web = %+v, want a then b", got)
	}
	if cur != got[1].RowID {
		t.Fatalf("cursor = %d, want %d", cur, got[1].RowID)
	}
	// unknown agent -> graceful empty
	got2, cur2, err := ls.Since("ghost", "web", 0, 100, logstore.StreamAny)
	if err != nil || len(got2) != 0 || cur2 != 0 {
		t.Fatalf("unknown agent = (%+v, %d, %v), want empty/0/nil", got2, cur2, err)
	}
}
```

If `internal/server/logstores_test.go` does not already import `marshal/internal/logstore`, add it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestLogStoresSinceSelector -v`
Expected: FAIL — `ls.Since undefined`.

- [ ] **Step 3: Add `strings` import and the wrapper**

In `internal/server/logstores.go`, add `"strings"` to the import block, then append:

```go
// Since resolves selector to the agent's matching labels (exact or "selector#"
// prefix) and returns lines with rowid > afterRowID (afterRowID <= 0 means the
// newest `limit`), the next cursor, and any error. An unknown agent returns
// (nil, 0, nil); a known agent with no matching labels returns (nil, afterRowID, nil).
func (s *logStores) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error) {
	if !s.has(agent) {
		return nil, 0, nil
	}
	st, err := s.get(agent)
	if err != nil {
		return nil, 0, err
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, 0, err
	}
	var matched []string
	for _, l := range labels {
		if l == selector || strings.HasPrefix(l, selector+"#") {
			matched = append(matched, l)
		}
	}
	if len(matched) == 0 {
		return nil, afterRowID, nil
	}
	return st.Since(matched, afterRowID, limit, filter)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestLogStoresSinceSelector -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/logstores.go internal/server/logstores_test.go
git commit -m "feat(server): logStores.Since selector wrapper

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `GET /api/logs` dashboard endpoint

**Files:**
- Create: `internal/dashboard/logs.go`
- Create: `internal/dashboard/logs_test.go`
- Modify: `internal/dashboard/handlers.go` (field, `newHandler`/`NewHandler` signatures, route)
- Modify: `internal/dashboard/server.go` (`Serve` signature)
- Modify: `internal/server/server.go:361` (pass `ls` to `dashboard.Serve`)
- Modify: `internal/dashboard/metrics_test.go`, `internal/dashboard/server_test.go` (update `NewHandler` call sites)

**Interfaces:**
- Consumes: `server.logStores.Since` (Task 2) via a structural interface.
- Produces:
  - `type LogsHistory interface { Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error) }`
  - `newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, auth Authenticator, ttl time.Duration) *handler`
  - `NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, auth Authenticator, ttl time.Duration) http.Handler`
  - `Serve(ctx, addr, lister, metrics, logs, auth, cert)`
  - JSON: `{ "cursor": <int64>, "lines": [ {"ts","name","instance","stderr","text"} ] }`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dashboard/logs_test.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/logstore"
)

type fakeLogs struct{ afters []int64 }

func (f *fakeLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error) {
	f.afters = append(f.afters, afterRowID)
	return []logstore.StoredLine{
		{RowID: 7, TsMs: 1000, Label: "web#0", Stderr: false, Text: "hello"},
		{RowID: 8, TsMs: 1001, Label: "web#1", Stderr: true, Text: "oops"},
	}, 8, nil
}

func TestLogsRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/logs?agent=dev-1&selector=web")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie logs = %d; want 401", resp.StatusCode)
	}
}

func TestLogsBackfill(t *testing.T) {
	fl := &fakeLogs{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs?agent=dev-1&selector=web", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs = %d; want 200", resp.StatusCode)
	}
	var got logsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cursor != 8 || len(got.Lines) != 2 {
		t.Fatalf("logs = %+v, want cursor 8 + 2 lines", got)
	}
	if got.Lines[0].Name != "web" || got.Lines[0].Instance != 0 || got.Lines[0].Text != "hello" || got.Lines[0].Stderr {
		t.Fatalf("line0 = %+v", got.Lines[0])
	}
	if got.Lines[1].Instance != 1 || !got.Lines[1].Stderr {
		t.Fatalf("line1 = %+v", got.Lines[1])
	}
	if len(fl.afters) != 1 || fl.afters[0] != 0 {
		t.Fatalf("backfill afters = %v, want [0]", fl.afters)
	}
}

func TestLogsFollowForwardsAfter(t *testing.T) {
	fl := &fakeLogs{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs?agent=dev-1&selector=web&after=8&stream=stderr", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow logs = %d; want 200", resp.StatusCode)
	}
	if len(fl.afters) != 1 || fl.afters[0] != 8 {
		t.Fatalf("follow afters = %v, want [8]", fl.afters)
	}
}

func TestLogsMissingParams(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs?agent=dev-1", nil) // no selector
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing selector = %d; want 400", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dashboard/ -run TestLogs -v`
Expected: compile error — `NewHandler` arg count wrong, `logsView` / `fakeLogs` route undefined.

- [ ] **Step 3: Create the endpoint**

Create `internal/dashboard/logs.go`:

```go
package dashboard

import (
	"net/http"
	"strconv"
	"strings"

	"marshal/internal/logstore"
)

// LogsHistory is the read side of stored log lines. *server.logStores satisfies it.
type LogsHistory interface {
	Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error)
}

type logLineView struct {
	Ts       int64  `json:"ts"`
	Name     string `json:"name"`
	Instance int    `json:"instance"`
	Stderr   bool   `json:"stderr"`
	Text     string `json:"text"`
}

type logsView struct {
	Cursor int64         `json:"cursor"`
	Lines  []logLineView `json:"lines"`
}

const (
	defaultLogLimit = 500
	maxLogLimit     = 5000
)

// logs serves GET /api/logs for a single proc selector. With ?after=<cursor> it
// returns only lines newer than the cursor (follow); otherwise the newest `limit`
// lines (backfill). Unknown agent -> 200 {"cursor":0,"lines":[]}.
func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	selector := q.Get("selector")
	if agent == "" || selector == "" {
		http.Error(w, "agent and selector required", http.StatusBadRequest)
		return
	}
	lines, cursor, err := h.logsHist.Since(agent, selector, parseAfter(q.Get("after")), parseLimit(q.Get("limit")), streamFilterFor(q.Get("stream")))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := logsView{Cursor: cursor, Lines: make([]logLineView, 0, len(lines))}
	for _, ln := range lines {
		name, idx := splitLogLabel(ln.Label)
		out.Lines = append(out.Lines, logLineView{Ts: ln.TsMs, Name: name, Instance: idx, Stderr: ln.Stderr, Text: ln.Text})
	}
	writeJSON(w, http.StatusOK, out)
}

func streamFilterFor(s string) logstore.StreamFilter {
	switch s {
	case "stdout":
		return logstore.StreamStdout
	case "stderr":
		return logstore.StreamStderr
	default:
		return logstore.StreamAny
	}
}

// parseLimit clamps to [1, maxLogLimit]; empty/invalid -> defaultLogLimit.
func parseLimit(s string) int {
	if s == "" {
		return defaultLogLimit
	}
	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return defaultLogLimit
	}
	if v > maxLogLimit {
		return maxLogLimit
	}
	return v
}

// parseAfter parses a non-negative cursor; empty/invalid -> 0 (backfill).
func parseAfter(s string) int64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// splitLogLabel parses "name#idx" into its parts (idx 0 when absent/unparseable).
func splitLogLabel(label string) (string, int) {
	i := strings.LastIndexByte(label, '#')
	if i < 0 {
		return label, 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return label[:i], n
}
```

- [ ] **Step 4: Wire the handler**

In `internal/dashboard/handlers.go`:

Add the field to the `handler` struct (after `metricsHist`):

```go
	metricsHist MetricsHistory
	logsHist    LogsHistory
```

Change `newHandler` signature and body — new signature line:

```go
func newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, auth Authenticator, ttl time.Duration) *handler {
```

Set the field inside the `&handler{...}` literal (after `metricsHist: metrics,`):

```go
		metricsHist: metrics,
		logsHist:    logs,
```

Register the route (after the `/api/metrics` line):

```go
	mux.HandleFunc("GET /api/metrics", h.requireSession(h.metrics))
	mux.HandleFunc("GET /api/logs", h.requireSession(h.logs))
```

Change `NewHandler`:

```go
func NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, metrics, logs, auth, ttl).mux
}
```

- [ ] **Step 5: Update `Serve`**

In `internal/dashboard/server.go`, change the signature and the `newHandler` call:

```go
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, auth Authenticator, cert tls.Certificate) error {
	h := newHandler(lister, metrics, logs, auth, 24*time.Hour)
```

- [ ] **Step 6: Update the production caller**

In `internal/server/server.go` around line 361, pass `ls`:

```go
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, auth, cert); err != nil {
```

- [ ] **Step 7: Update existing dashboard test call sites**

`*server.logStores` is not available in the dashboard package tests, so all existing `NewHandler` calls must pass `&fakeLogs{}` (defined in `logs_test.go`) as the new third argument.

In `internal/dashboard/metrics_test.go` (lines 33, 47, 76) and `internal/dashboard/server_test.go` (lines 31, 90, 107), insert `&fakeLogs{}` after the metrics argument. Examples:

```go
// metrics_test.go:33
srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
// metrics_test.go:47 and :76
srv := httptest.NewServer(NewHandler(lister, fm, &fakeLogs{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
// server_test.go:31
srv := httptest.NewServer(NewHandler(lister, &fakeMetrics{}, &fakeLogs{}, auth, time.Hour))
// server_test.go:90 and :107
srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fakeAuth{}, time.Hour))
```

- [ ] **Step 8: Run the dashboard + server tests**

Run: `go test ./internal/dashboard/ ./internal/server/ -v`
Expected: PASS (new `TestLogs*` plus all existing tests).

- [ ] **Step 9: Build everything to catch other callers**

Run: `go build ./... && go vet ./...`
Expected: no errors. (The only `dashboard.Serve`/`NewHandler` callers are the ones updated above; `e2e_test.go`/`server_test.go` in `internal/server` call `server.Serve`, which is unchanged.)

- [ ] **Step 10: Commit**

```bash
git add internal/dashboard/logs.go internal/dashboard/logs_test.go internal/dashboard/handlers.go internal/dashboard/server.go internal/dashboard/metrics_test.go internal/dashboard/server_test.go internal/server/server.go
git commit -m "feat(dashboard): GET /api/logs cursor-based tail endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `api.ts` — `getLogs` client

**Files:**
- Modify: `web/src/api.ts`

**Interfaces:**
- Produces:
  - `type LogLine = { ts: number; name: string; instance: number; stderr: boolean; text: string }`
  - `type LogsResponse = { cursor: number; lines: LogLine[] }`
  - `async function getLogs(agent, selector, opts: { stream: string; limit: number; after: number }): Promise<LogsResponse>` — throws on 401, like `getMetricsForProc`.

- [ ] **Step 1: Add the types and fetch helper**

Append to `web/src/api.ts`:

```ts
export type LogLine = {
  ts: number;
  name: string;
  instance: number;
  stderr: boolean;
  text: string;
};

export type LogsResponse = { cursor: number; lines: LogLine[] };

export async function getLogs(
  agent: string,
  selector: string,
  opts: { stream: string; limit: number; after: number },
): Promise<LogsResponse> {
  const q = new URLSearchParams({
    agent,
    selector,
    stream: opts.stream,
    limit: String(opts.limit),
    after: String(opts.after),
  });
  const r = await fetch(`/api/logs?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as LogsResponse;
}
```

- [ ] **Step 2: Type-check via build (deferred to Task 6's `make ui`)**

No standalone JS test harness exists. This file compiles as part of `make ui` in Task 6. No commit yet — commit with the UI in Task 6 so `dist/` and `web/src/` stay in one consistent commit.

---

### Task 5: `LogView.tsx` — presentational log pane

**Files:**
- Create: `web/src/LogView.tsx`

**Interfaces:**
- Consumes: `LogLine` (Task 4).
- Produces: `function LogView({ lines, search }: { lines: LogLine[]; search: string })` — monospace pane; stderr lines styled distinctly; auto-scrolls to bottom while "stuck", pauses when the user scrolls up, with a "Jump to latest" button to resume.

- [ ] **Step 1: Create the component**

Create `web/src/LogView.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import { LogLine } from "./api";

export function LogView({ lines, search }: { lines: LogLine[]; search: string }) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);

  const needle = search.trim().toLowerCase();
  const shown = needle ? lines.filter((l) => l.text.toLowerCase().includes(needle)) : lines;

  useEffect(() => {
    if (stick && ref.current) {
      ref.current.scrollTop = ref.current.scrollHeight;
    }
  }, [shown, stick]);

  function onScroll() {
    const el = ref.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    setStick(atBottom);
  }

  return (
    <div className="logview-wrap">
      <div className="logview" ref={ref} onScroll={onScroll}>
        {shown.length === 0 ? (
          <p className="chart-empty">No log lines.</p>
        ) : (
          shown.map((l, i) => (
            <div key={i} className={l.stderr ? "logline err" : "logline"}>
              <span className="logts">{new Date(l.ts).toLocaleTimeString()}</span>
              <span className="logtext">{l.text}</span>
            </div>
          ))
        )}
      </div>
      {!stick && (
        <button className="jump" onClick={() => setStick(true)}>
          Jump to latest ↓
        </button>
      )}
    </div>
  );
}
```

- [ ] **Step 2: No commit yet** — built and committed with Task 6.

---

### Task 6: `Fleet.tsx` Charts/Logs tab + logs poll + styles, rebuild & commit

**Files:**
- Modify: `web/src/Fleet.tsx`
- Modify: `web/src/styles.css`
- Regenerate: `internal/dashboard/dist/` (via `make ui`)

**Interfaces:**
- Consumes: `getLogs`, `LogLine` (Task 4), `LogView` (Task 5), existing detail-panel structure.

- [ ] **Step 1: Update imports and add constants in `Fleet.tsx`**

Change the import block at the top of `web/src/Fleet.tsx` to add the log imports:

```tsx
import { Fragment, useEffect, useState } from "react";
import {
  Agent,
  AgentMetrics,
  Bucket,
  LogLine,
  getFleet,
  getLogs,
  getMetrics,
  getMetricsForProc,
  logout,
} from "./api";
import { Sparkline } from "./Sparkline";
import { MetricChart } from "./MetricChart";
import { LogView } from "./LogView";
```

Add constants after the `WINDOWS` array:

```tsx
const LOG_LIMITS = [100, 500, 1000];
const LOG_CAP = 5000;
const STREAMS = ["all", "stdout", "stderr"];
```

- [ ] **Step 2: Add log state**

Inside `Fleet`, after the existing `const [detail, setDetail] = useState<Bucket[]>([]);`:

```tsx
  const [tab, setTab] = useState<"charts" | "logs">("charts");
  const [logStream, setLogStream] = useState("all");
  const [logLimit, setLogLimit] = useState(500);
  const [logLines, setLogLines] = useState<LogLine[]>([]);
  const [logSearch, setLogSearch] = useState("");
```

- [ ] **Step 3: Add the logs poll effect**

After the existing metrics-detail `useEffect` (the one with deps `[expanded, windowMs]`), add:

```tsx
  useEffect(() => {
    if (!expanded || tab !== "logs") {
      setLogLines([]);
      return;
    }
    let stop = false;
    let cursor = 0;
    let first = true;
    setLogLines([]);
    async function tick() {
      try {
        const res = await getLogs(expanded!.agent, expanded!.proc, {
          stream: logStream,
          limit: logLimit,
          after: first ? 0 : cursor,
        });
        if (stop) return;
        cursor = res.cursor || cursor;
        first = false;
        if (res.lines.length > 0) {
          setLogLines((prev) => {
            const next = prev.concat(res.lines);
            return next.length > LOG_CAP ? next.slice(next.length - LOG_CAP) : next;
          });
        }
      } catch {
        // best-effort; the fleet poll owns auth/logout.
      }
    }
    tick();
    const id = setInterval(tick, 1500);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, [expanded, tab, logStream, logLimit]);
```

- [ ] **Step 4: Replace the detail panel body with the tabbed layout**

In the `isOpen && (...)` detail `<tr className="detail">`, replace the inner content of `<td colSpan={7}>` (the `.windows` + `.charts` divs) with:

```tsx
                        <td colSpan={7}>
                          <div className="tabs">
                            <button
                              className={tab === "charts" ? "active" : ""}
                              onClick={(e) => {
                                e.stopPropagation();
                                setTab("charts");
                              }}
                            >
                              Charts
                            </button>
                            <button
                              className={tab === "logs" ? "active" : ""}
                              onClick={(e) => {
                                e.stopPropagation();
                                setTab("logs");
                              }}
                            >
                              Logs
                            </button>
                          </div>
                          {tab === "charts" ? (
                            <>
                              <div className="windows">
                                {WINDOWS.map((wnd) => (
                                  <button
                                    key={wnd.label}
                                    className={windowMs === wnd.ms ? "active" : ""}
                                    onClick={(e) => {
                                      e.stopPropagation();
                                      setWindowMs(wnd.ms);
                                    }}
                                  >
                                    {wnd.label}
                                  </button>
                                ))}
                              </div>
                              <div className="charts">
                                <div>
                                  <h4>CPU</h4>
                                  <MetricChart buckets={detail} metric="cpu" />
                                </div>
                                <div>
                                  <h4>Memory</h4>
                                  <MetricChart buckets={detail} metric="mem" />
                                </div>
                              </div>
                            </>
                          ) : (
                            <div className="logs-panel" onClick={(e) => e.stopPropagation()}>
                              <div className="log-controls">
                                <div className="seg">
                                  {STREAMS.map((s) => (
                                    <button
                                      key={s}
                                      className={logStream === s ? "active" : ""}
                                      onClick={() => setLogStream(s)}
                                    >
                                      {s}
                                    </button>
                                  ))}
                                </div>
                                <div className="seg">
                                  {LOG_LIMITS.map((n) => (
                                    <button
                                      key={n}
                                      className={logLimit === n ? "active" : ""}
                                      onClick={() => setLogLimit(n)}
                                    >
                                      {n}
                                    </button>
                                  ))}
                                </div>
                                <input
                                  className="log-search"
                                  placeholder="search…"
                                  value={logSearch}
                                  onChange={(e) => setLogSearch(e.target.value)}
                                />
                              </div>
                              <LogView lines={logLines} search={logSearch} />
                            </div>
                          )}
                        </td>
```

- [ ] **Step 5: Add styles**

Append to `web/src/styles.css`:

```css
.tabs { display: flex; gap: 4px; margin: 6px 0; }
.tabs button { padding: 2px 12px; background: #e3e7ef; color: #333; }
.tabs button.active { background: #2d6cdf; color: #fff; }
.log-controls { display: flex; gap: 12px; align-items: center; margin: 6px 0; flex-wrap: wrap; }
.seg { display: flex; gap: 4px; }
.seg button { padding: 2px 8px; background: #e3e7ef; color: #333; }
.seg button.active { background: #2d6cdf; color: #fff; }
.log-search { padding: 3px 8px; border: 1px solid #ccc; border-radius: 6px; font-size: 0.85rem; }
.logview-wrap { position: relative; }
.logview {
  max-height: 320px;
  overflow-y: auto;
  background: #0f1117;
  color: #d6d9e0;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 0.8rem;
  line-height: 1.45;
  border-radius: 6px;
  padding: 6px 8px;
}
.logline { display: flex; gap: 8px; white-space: pre-wrap; word-break: break-word; }
.logline.err .logtext { color: #ff8a8a; }
.logts { color: #6b7280; flex: 0 0 auto; }
.logtext { flex: 1 1 auto; }
.jump { position: absolute; right: 12px; bottom: 12px; font-size: 0.75rem; padding: 3px 8px; }
```

- [ ] **Step 6: Rebuild the embedded SPA**

Run: `make ui`
Expected: builds with no TypeScript errors; regenerates `internal/dashboard/dist/` (new hashed `index-*.js` / `index-*.css`).

- [ ] **Step 7: Build the binary to confirm the embed**

Run: `go build -o marshal ./cmd/marshal`
Expected: success (the new `dist/` is embedded).

- [ ] **Step 8: Commit**

```bash
git add web/src/api.ts web/src/LogView.tsx web/src/Fleet.tsx web/src/styles.css internal/dashboard/dist
git commit -m "feat(dashboard): live log tailing tab in the detail panel

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Full verification gate + handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m13-log-tailing.md`

- [ ] **Step 1: Run the full gate**

Run:
```bash
go test ./... -race -count=1
gofmt -l .
go vet ./...
make ui
go build -o marshal ./cmd/marshal
```
Expected: all tests PASS; `gofmt -l .` prints nothing; `go vet` clean; `make ui` and `go build` succeed.

- [ ] **Step 2: Live demo (per project convention)**

Spin up a scratch server (`XDG_DATA_HOME=/tmp/marshal-m13-demo/...`), set the dashboard password while the server is **down**, rotate an enroll token, start with `--http-listen`, enroll an agent running a **chatty** demo process (one that prints to stdout and stderr on an interval). In the browser: expand the process row, switch to the **Logs** tab, and confirm:
- backfill populates with recent lines;
- new lines tail in within ~1.5s;
- the stream filter (all/stdout/stderr) and backfill-depth buttons refetch;
- the search box filters the buffer;
- auto-scroll pauses on scroll-up and "Jump to latest" resumes;
- an empty/unknown selector renders "No log lines." gracefully.

Tear down (stop processes + daemon + server, remove the scratch dir) and confirm no orphan `marshal` processes remain (`pgrep -fl marshal`). Record observations in the handoff.

- [ ] **Step 3: Write the handoff**

Write `docs/handoffs/2026-06-18-m13-log-tailing.md` covering: current state + branch (`m13-logs`), what was built (the four tasks), build/run/test, the live-demo result, deferred items (carry the spec's "Out of scope" list), and the concrete next step (merge `m13-logs` via the `finishing-a-development-branch` skill; pick M14).

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-18-m13-log-tailing.md
git commit -m "docs: M13 log-tailing handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Poll-with-cursor transport → Tasks 1–4, 6. ✓
- rowid cursor (not ts) → Task 1. ✓
- `logstore.Since` + `StoredLine.RowID` → Task 1. ✓
- `logStores.Since` selector wrapper, unknown-agent empty → Task 2. ✓
- `GET /api/logs` envelope `{cursor,lines}`, session guard, unknown-agent 200-empty, `LogsHistory` interface, wiring through `Serve`/`NewHandler` → Task 3. ✓
- Placement: Charts|Logs tab in M12 detail panel → Task 6. ✓
- Controls: stream filter, auto-scroll+pause, backfill depth, client-side search → Tasks 5–6. ✓
- Defaults (1.5s / 500 / 100,500,1000 / 5000 cap / 5000 server cap) → Global Constraints, Tasks 3 & 6. ✓
- stderr styling, buffer cap, best-effort poll → Tasks 5–6. ✓
- Testing (logstore, dashboard, server) → Tasks 1–3. ✓
- Live demo + handoff → Task 7. ✓
- Deferred list (SSE, server-side search, export, persisted UI state, ANSI, firehose) → carried in spec; nothing implemented for them. ✓

**Placeholder scan:** No TBD/TODO; every code step has complete code. ✓

**Type consistency:** `Since(labels []string, afterRowID int64, limit int, filter StreamFilter) ([]StoredLine, int64, error)` (Task 1) is consumed verbatim by `logStores.Since` (Task 2), whose signature `Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error)` is the `LogsHistory` interface (Task 3) and the `fakeLogs` method (Task 3 test). `logsView`/`logLineView` JSON shape matches the TS `LogsResponse`/`LogLine` (Task 4) and the React consumers (Tasks 5–6). `NewHandler`/`newHandler`/`Serve` gain `logs` as the third (post-`metrics`) parameter consistently across handlers.go, server.go, the production caller, and all six test call sites. ✓
