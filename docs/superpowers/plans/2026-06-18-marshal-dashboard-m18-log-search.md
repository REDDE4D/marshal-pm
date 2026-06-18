# Marshal Dashboard M18 — Server-Side Log Search — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a case-insensitive substring log search, pushed into SQLite, surfaced on the dashboard (`/api/logs?q=`) and the CLI (`fleet logs --grep`).

**Architecture:** A `text` argument is added to `logstore.Tail`/`Since` (and `logStores.Since`); when non-empty the SQL gains `AND text LIKE ? ESCAPE '\'` with the needle escaped to a literal. The dashboard handler reads `q` and the frontend search box is lifted server-side; the gRPC `FleetLogsHistoryRequest` gains a `grep` field that the CLI sets.

**Tech Stack:** Go 1.26, modernc.org/sqlite (already used), protobuf (protoc + plugins present), React/TypeScript frontend (`make ui`).

## Global Constraints

- Module path `marshal`; imports `marshal/internal/...`. No new Go dependencies.
- TDD: failing test first, then minimal implementation (frontend is verified by build + live demo, not unit tests).
- Match semantics: **case-insensitive literal substring** via `text LIKE ? ESCAPE '\'`, needle = `"%" + escapeLike(text) + "%"`. Empty text/`q`/`grep` ⇒ no filtering (unchanged behavior).
- Gate before finishing: `go build -o marshal ./cmd/marshal`, `go test ./... -race -count=1`, `gofmt -l .` silent, `go vet ./...` clean, `make ui` (frontend builds).
- Commit subject imperative + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work on branch `m18-log-search`, not `main`.
- Search stays scoped per agent+selector (no cross-agent search); no proto/agent/manager changes beyond the one `grep` field.

---

## File structure

- `internal/logstore/store.go` — `escapeLike`; `text` arg on `Tail`/`Since`; `AND text LIKE` SQL.
- `internal/logstore/store_test.go` — new filter tests; existing call sites get `, ""`.
- `internal/server/logstores.go` — `Since` gains `text`, forwards it.
- `internal/server/server.go` — `FleetLogsHistory` passes `req.GetGrep()` (Task 4); Tail call threads `""` first (Task 1).
- `internal/server/{logstores_test.go,server_test.go}` — call sites get `, ""`; new grep test (Task 4).
- `internal/dashboard/logs.go` — `LogsHistory.Since` gains `text`; handler reads `q` (Task 2).
- `internal/dashboard/logs_test.go` — fake `Since` gains `text`; new threading test (Task 2).
- `web/src/{api.ts,Fleet.tsx,LogView.tsx}` — send `q`, lift search server-side (Task 3).
- `proto/marshal/v1/fleet.proto` + `internal/pb/*` — `grep` field (Task 4).
- `cmd/marshal/fleet.go` — `--grep` flag (Task 5).

---

## Task 1: logstore text filter (core + compile-green threading)

Adds the `text` argument everywhere it must exist so the whole module compiles, with **no
behavior change** (every existing caller passes `""`). New tests cover the filter itself.

**Files:**
- Modify: `internal/logstore/store.go`
- Modify: `internal/logstore/store_test.go` (new tests + update existing call sites)
- Modify: `internal/server/logstores.go`, `internal/server/server.go:234`
- Modify: `internal/server/logstores_test.go`, `internal/server/server_test.go:368`
- Modify: `internal/dashboard/logs.go` (interface + handler call), `internal/dashboard/logs_test.go` (fake)

**Interfaces:**
- Produces:
  - `func escapeLike(s string) string`
  - `func (s *Store) Tail(label string, limit int, filter StreamFilter, text string) ([]StoredLine, error)`
  - `func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter, text string) ([]StoredLine, int64, error)`
  - `func (s *logStores) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error)`
  - `LogsHistory.Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) (...)`

- [ ] **Step 1: Write the failing logstore tests**

Add to `internal/logstore/store_test.go`. Also add `"strings"` to its imports (currently only `"testing"`):

```go
func TestEscapeLikeLiteral(t *testing.T) {
	if got := escapeLike(`a%b_c\d`); got != `a\%b\_c\\d` {
		t.Fatalf("escapeLike = %q; want %q", got, `a\%b\_c\\d`)
	}
}

func TestTailTextFilter(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 1, Label: "x#0", Stderr: false, Text: "hello world"},
		{TsMs: 2, Label: "x#0", Stderr: false, Text: "goodbye"},
		{TsMs: 3, Label: "x#0", Stderr: false, Text: "HELLO again"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Tail("x#0", 10, StreamAny, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("text filter got %d; want 2 (case-insensitive substring)", len(got))
	}
	all, err := st.Tail("x#0", 10, StreamAny, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("empty filter got %d; want 3 (unchanged)", len(all))
	}
}

func TestSinceTextFilter(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 1, Label: "a#0", Stderr: false, Text: "error: boom"},
		{TsMs: 2, Label: "a#0", Stderr: false, Text: "ok"},
		{TsMs: 3, Label: "a#0", Stderr: false, Text: "another ERROR"},
	}); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.Since([]string{"a#0"}, 0, 100, StreamAny, "error")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Since text filter got %d; want 2", len(got))
	}
	for _, ln := range got {
		if !strings.Contains(strings.ToLower(ln.Text), "error") {
			t.Errorf("non-matching line returned: %q", ln.Text)
		}
	}
}

func TestTextFilterWildcardLiteral(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 1, Label: "w#0", Stderr: false, Text: "100% done"},
		{TsMs: 2, Label: "w#0", Stderr: false, Text: "1000 done"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Tail("w#0", 10, StreamAny, "100%")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "100% done" {
		t.Fatalf("literal %% match = %+v; want [100%% done]", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/logstore/ -run 'TestEscapeLikeLiteral|TextFilter' -count=1`
Expected: FAIL — `escapeLike` undefined and `Tail`/`Since` take too few args (compile error).

- [ ] **Step 3: Implement `escapeLike` and the SQL filter**

In `internal/logstore/store.go`, add the helper (near the bottom, beside `b2i`):

```go
// escapeLike backslash-escapes the LIKE metacharacters in s so it matches as a
// literal substring under `LIKE ? ESCAPE '\'`.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
```

Change `Tail`'s signature and add the filter clause **after** the stream `switch` and
**before** `q += ` ORDER BY ts DESC``:

```go
func (s *Store) Tail(label string, limit int, filter StreamFilter, text string) ([]StoredLine, error) {
	q := `SELECT ts, label, stderr, text FROM log_line WHERE label = ?`
	args := []any{label}
	switch filter {
	case StreamStdout:
		q += ` AND stderr = 0`
	case StreamStderr:
		q += ` AND stderr = 1`
	}
	if text != "" {
		q += ` AND text LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(text)+"%")
	}
	q += ` ORDER BY ts DESC`
```

(The rest of `Tail` is unchanged.)

Change `Since`'s signature and add the same clause **after** the stream `switch` and
**before** `reverse := false`:

```go
func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter, text string) ([]StoredLine, int64, error) {
	if len(labels) == 0 {
		return nil, afterRowID, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+3)
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
	if text != "" {
		q += ` AND text LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(text)+"%")
	}
	reverse := false
```

(The rest of `Since` — the `afterRowID`/backfill branching, scan, reverse, cursor — is
unchanged. The text clause is appended to the query string before the `rowid`/`LIMIT` clauses,
so its placeholder order matches `args`.)

- [ ] **Step 4: Run the new tests to verify they pass**

Run: `go test ./internal/logstore/ -run 'TestEscapeLikeLiteral|TextFilter' -race -count=1`
Expected: PASS (4 tests).

- [ ] **Step 5: Thread `""` through all other callers so the module compiles**

Update each call site to pass `""` for the new `text` arg (no behavior change):

Production:
- `internal/server/server.go:234`: `lines, err := st.Tail(l, limit, filter, "")`
- `internal/server/logstores.go`: change the signature to add `text string` and forward it:
  ```go
  func (s *logStores) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error) {
  ```
  and the final line: `return st.Since(matched, afterRowID, limit, filter, text)`
- `internal/dashboard/logs.go`: add `text string` to the `LogsHistory.Since` interface, and in
  the handler pass `""` for now (Task 2 wires `q`):
  ```go
  type LogsHistory interface {
  	Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error)
  }
  ```
  ```go
  lines, cursor, err := h.logsHist.Since(agent, selector, parseAfter(q.Get("after")), parseLimit(q.Get("limit")), streamFilterFor(q.Get("stream")), "")
  ```

Tests (append `, ""` to each existing call):
- `internal/logstore/store_test.go`: the existing `Tail(...)` calls at the original lines 35, 49, 54, 59 and `Since(...)` calls at 97, 108, 114, 126, 138, 143 — add `, ""` as the final argument to each.
- `internal/server/logstores_test.go:52,63`: `ls.Since("dev-1", "web", 0, 100, logstore.StreamAny, "")`.
- `internal/server/server_test.go:368`: `st.Tail("api#0", 10, logstore.StreamAny, "")`.
- `internal/dashboard/logs_test.go:15`: update the fake's signature to
  `func (f *fakeLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error) {` (body unchanged).

- [ ] **Step 6: Build and run the affected packages**

Run: `go build ./... && go test ./internal/logstore/ ./internal/server/ ./internal/dashboard/ -race -count=1`
Expected: PASS — new filter tests pass; all existing tests still pass; module compiles.
Then `gofmt -l internal/logstore/ internal/server/ internal/dashboard/` (must print nothing).

- [ ] **Step 7: Commit**

```bash
git add internal/logstore/ internal/server/logstores.go internal/server/server.go \
  internal/server/logstores_test.go internal/server/server_test.go \
  internal/dashboard/logs.go internal/dashboard/logs_test.go
git commit -m "feat(logstore): case-insensitive substring filter on Tail/Since

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Dashboard `/api/logs?q=` backend

**Files:**
- Modify: `internal/dashboard/logs.go` (read `q`, pass instead of `""`)
- Test: `internal/dashboard/logs_test.go` (new threading test)

**Interfaces:**
- Consumes: `LogsHistory.Since(..., text string)` (Task 1).

- [ ] **Step 1: Write the failing test**

Add to `internal/dashboard/logs_test.go` (a recording fake proves the `q` value reaches
`Since`; `h.logs` is called directly, bypassing the session wrapper, which only reads query
params):

```go
type recordingLogs struct{ gotText string }

func (r *recordingLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error) {
	r.gotText = text
	return []logstore.StoredLine{{RowID: 1, TsMs: 1, Label: "web#0", Stderr: false, Text: "x"}}, 1, nil
}

func TestLogsThreadsQueryFilter(t *testing.T) {
	rl := &recordingLogs{}
	h := newHandler(fakeLister{}, &fakeMetrics{}, rl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "")
	req := httptest.NewRequest("GET", "/api/logs?agent=dev-1&selector=web&q=boom", nil)
	rec := httptest.NewRecorder()
	h.logs(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if rl.gotText != "boom" {
		t.Fatalf("Since received text %q; want %q", rl.gotText, "boom")
	}
}
```

Ensure `logs_test.go` imports `net/http/httptest`, `time`, and `marshal/internal/logstore`
(add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestLogsThreadsQueryFilter -count=1`
Expected: FAIL — `rl.gotText` is `""` (handler still passes `""`).

- [ ] **Step 3: Read `q` and pass it**

In `internal/dashboard/logs.go`, change the `Since` call in `logs`:

```go
	lines, cursor, err := h.logsHist.Since(agent, selector, parseAfter(q.Get("after")), parseLimit(q.Get("limit")), streamFilterFor(q.Get("stream")), q.Get("q"))
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestLogsThreadsQueryFilter -race -count=1`
Expected: PASS. Then `gofmt -l internal/dashboard/` (must print nothing).

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/logs.go internal/dashboard/logs_test.go
git commit -m "feat(dashboard): thread ?q= log search into Since

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Frontend — lift the search box server-side

No Go tests; verified by `make ui` building and the live demo. The existing client-side
substring filter is removed (the server is now authoritative), and the search string is sent
as `q`, with a debounce so each keystroke does not refetch.

**Files:**
- Modify: `web/src/api.ts`, `web/src/Fleet.tsx`, `web/src/LogView.tsx`

- [ ] **Step 1: Send `q` from `getLogs`**

In `web/src/api.ts`, extend the `opts` type and query string:

```ts
export async function getLogs(
  agent: string,
  selector: string,
  opts: { stream: string; limit: number; after: number; q: string },
): Promise<LogsResponse> {
  const q = new URLSearchParams({
    agent,
    selector,
    stream: opts.stream,
    limit: String(opts.limit),
    after: String(opts.after),
    q: opts.q,
  });
  const r = await fetch(`/api/logs?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as LogsResponse;
}
```

- [ ] **Step 2: Debounce the search and drive the poll with it**

In `web/src/Fleet.tsx`, add a debounced mirror of `logSearch` (place near the existing
`logSearch` state at line ~99):

```tsx
  const [logSearchDebounced, setLogSearchDebounced] = useState("");
  useEffect(() => {
    const id = setTimeout(() => setLogSearchDebounced(logSearch), 250);
    return () => clearTimeout(id);
  }, [logSearch]);
```

In the log-polling `useEffect` (currently lines ~173–208), pass `q` and add
`logSearchDebounced` to the dependency array so a new query restarts the poll with a fresh
backfill:

```tsx
        const res = await getLogs(expanded!.agent, expanded!.proc, {
          stream: logStream,
          limit: logLimit,
          after: first ? 0 : cursor,
          q: logSearchDebounced,
        });
```

and change the dependency array on that effect from
`[expanded, tab, logStream, logLimit]` to
`[expanded, tab, logStream, logLimit, logSearchDebounced]`.

- [ ] **Step 3: Remove the client-side filter in `LogView`**

In `web/src/LogView.tsx`, drop the `search` prop and the `lines.filter(...)` — render
`lines` directly:

```tsx
export function LogView({ lines }: { lines: LogLine[] }) {
  const ref = useRef<HTMLDivElement>(null);
  const [stick, setStick] = useState(true);

  useEffect(() => {
    if (stick && ref.current) {
      ref.current.scrollTop = ref.current.scrollHeight;
    }
  }, [lines, stick]);
```

Replace the two later uses of `shown` with `lines` (the empty check `lines.length === 0` and
the `lines.map(...)` render). Update the call site in `Fleet.tsx` (~line 352) from
`<LogView lines={logLines} search={logSearch} />` to `<LogView lines={logLines} />`.

- [ ] **Step 4: Build the frontend**

Run: `make ui`
Expected: `npm run build` succeeds with no TypeScript errors and writes the SPA into
`internal/dashboard/dist`.

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/Fleet.tsx web/src/LogView.tsx internal/dashboard/dist
git commit -m "feat(web): server-side log search via ?q= (debounced)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Proto `grep` field + gRPC server

**Files:**
- Modify: `proto/marshal/v1/fleet.proto`; regenerate `internal/pb/*`
- Modify: `internal/server/server.go` (`FleetLogsHistory` passes `req.GetGrep()`)
- Test: `internal/server/server_test.go` (new grep test)

**Interfaces:**
- Produces: `FleetLogsHistoryRequest.Grep string` (via `GetGrep()`).

- [ ] **Step 1: Write the failing server test**

Add to `internal/server/server_test.go` (models the existing
`TestFleetLogsHistorySelectorMergeAndFilter`):

```go
func TestFleetLogsHistoryGrep(t *testing.T) {
	ls := newLogStores(t.TempDir())
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls, nil)

	srv.storeLogBatch("web-1", []*pb.LogShipLine{
		{TsMs: 1, Label: "api#0", Stderr: false, Text: "starting up"},
		{TsMs: 2, Label: "api#0", Stderr: true, Text: "ERROR: boom"},
		{TsMs: 3, Label: "api#0", Stderr: false, Text: "recovered"},
	})

	resp, err := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 10, Grep: "error",
	})
	if err != nil {
		t.Fatalf("FleetLogsHistory: %v", err)
	}
	if len(resp.GetLines()) != 1 || resp.GetLines()[0].GetLine() != "ERROR: boom" {
		t.Fatalf("grep = %+v; want [ERROR: boom]", resp.GetLines())
	}

	// Empty grep is unchanged (all 3).
	all, _ := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 10,
	})
	if len(all.GetLines()) != 3 {
		t.Fatalf("empty grep = %d lines; want 3", len(all.GetLines()))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestFleetLogsHistoryGrep -count=1`
Expected: FAIL — `Grep` is not a field of `FleetLogsHistoryRequest` (compile error).

- [ ] **Step 3: Add the proto field and regenerate**

In `proto/marshal/v1/fleet.proto`, add field 5 to `FleetLogsHistoryRequest`:

```proto
message FleetLogsHistoryRequest {
  string    agent_name = 1;
  string    selector   = 2; // app name or "name#instance" label
  int32     lines      = 3; // backfill count
  LogStream stream     = 4; // reuse daemon.proto enum; unspecified = merged
  string    grep       = 5; // only lines containing this substring (case-insensitive); empty = all
}
```

Regenerate the Go bindings (protoc + plugins are installed; the directive lives in
`internal/pb/doc.go`):

```bash
go generate ./internal/pb/
```

Expected: `internal/pb/fleet.pb.go` regenerates with a `Grep` field and `GetGrep()` accessor.
Run `git status` to confirm only `internal/pb/fleet.pb.go` (and possibly `fleet_grpc.pb.go`)
changed.

- [ ] **Step 4: Pass `req.GetGrep()` into the store query**

In `internal/server/server.go`, in `FleetLogsHistory`, change the `Tail` call (the `""` added
in Task 1):

```go
		lines, err := st.Tail(l, limit, filter, req.GetGrep())
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/server/ -run TestFleetLogsHistory -race -count=1`
Expected: PASS (grep test + the existing merge/filter test). Then `gofmt -l internal/server/`
(must print nothing) and `go vet ./internal/server/`.

- [ ] **Step 6: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/ internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): grep field on FleetLogsHistory for log search

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: CLI `fleet logs --grep`

**Files:**
- Modify: `cmd/marshal/fleet.go`
- Test: `cmd/marshal/fleet_test.go` (flag wiring)

**Interfaces:**
- Consumes: `FleetLogsHistoryRequest.Grep` (Task 4).

- [ ] **Step 1: Write the failing test**

Add to `cmd/marshal/fleet_test.go` (assert the flag is registered and its value lands in the
request; a pure wiring check that does not need a live server):

```go
func TestFleetLogsGrepFlag(t *testing.T) {
	cmd := fleetLogsCmd()
	f := cmd.Flags().Lookup("grep")
	if f == nil {
		t.Fatal("fleet logs has no --grep flag")
	}
	if f.DefValue != "" {
		t.Fatalf("--grep default = %q; want empty", f.DefValue)
	}
}
```

(If `cmd/marshal/fleet_test.go` does not exist, create it with `package main` and
`import "testing"`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestFleetLogsGrepFlag -count=1`
Expected: FAIL — `--grep` flag not found (`f == nil`).

- [ ] **Step 3: Add the flag and set the request field**

In `cmd/marshal/fleet.go`, `fleetLogsCmd`: add a `grep` variable, set it on the request, and
register the flag.

Add to the `var` block at the top of `fleetLogsCmd`:

```go
	var grepFlag string
```

In the `FleetLogsHistory` request literal, add the field:

```go
			resp, err := pb.NewFleetClient(conn).FleetLogsHistory(authCtx(ctx, token), &pb.FleetLogsHistoryRequest{
				AgentName: args[0],
				Selector:  args[1],
				Lines:     int32(lines),
				Stream:    streamSel,
				Grep:      grepFlag,
			})
```

Register the flag with the others:

```go
	cmd.Flags().StringVar(&grepFlag, "grep", "", "only lines containing this substring (case-insensitive)")
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/marshal/ -run TestFleetLogsGrepFlag -race -count=1`
Expected: PASS. Then `gofmt -l cmd/marshal/` (must print nothing).

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/fleet.go cmd/marshal/fleet_test.go
git commit -m "feat(cli): add 'fleet logs --grep' substring search

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Full gate, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m18-log-search.md`

- [ ] **Step 1: Run the full gate**

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .            # must print nothing
go vet ./...
make ui               # frontend builds clean
```
Expected: all packages PASS, `gofmt` silent, `vet` clean, `make ui` succeeds.

- [ ] **Step 2: Live demo (per CLAUDE.md convention)**

On a scratch data dir, start a server with `--http-listen` and at least one demo agent
shipping logs (so a store exists), or seed a store directly. Exercise **both** surfaces:
- CLI: `marshal fleet logs <agent> <selector> --grep <substring>` returns only matching lines;
  without `--grep` returns all; a `%` in the needle matches literally.
- Dashboard (in-browser, per the viewable-demo convention): open the logs tab, type a
  substring in the search box, confirm matching lines from full history appear (not just the
  in-buffer ones), and clearing the box restores the live tail.
Tear down (stop processes + server, remove scratch dir) and confirm `pgrep -fl marshal` shows
no demo orphans.

- [ ] **Step 3: Write the handoff**

Write `docs/handoffs/2026-06-18-m18-log-search.md` covering: current state + branch, what
changed and why (the shared `text` filter, the `LIKE ESCAPE` literal match, dashboard `q` +
lifted search box, the proto `grep` field + CLI flag), build/run/test (incl. `make ui` and the
proto-regen note), the live-demo result, deferred items (regex/FTS, highlighting, time-range,
multi-agent), and the concrete next step. Commit it.

- [ ] **Step 4: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to merge `m18-log-search` to `main`.

---

## Self-review

**Spec coverage:**
- `escapeLike` + `text` arg on `Tail`/`Since`, `LIKE ? ESCAPE '\'`, empty = unchanged → Task 1. ✅
- `logStores.Since` forwards `text` → Task 1 Step 5. ✅
- `LogsHistory.Since` gains `text`; `/api/logs?q=` reads & threads it → Task 1 (interface) + Task 2 (read `q`). ✅
- Frontend: `getLogs` sends `q`; search box lifted server-side + debounce; client-side filter removed → Task 3. ✅
- Proto `grep = 5`; regenerate; `FleetLogsHistory` uses `req.GetGrep()` → Task 4. ✅
- CLI `fleet logs --grep` → Task 5. ✅
- Wildcard-literal safety (`%`/`_` escaped) → Task 1 `TestTextFilterWildcardLiteral`. ✅
- Tests 1–6 from the spec → Task 1 (logstore 1–3 + wildcard), Task 4 (server/gRPC 4), Task 2 (dashboard 5), Task 5 (CLI 6). ✅
- Gate (incl. `make ui`) + live demo (both surfaces, in-browser) + handoff → Task 6. ✅

**Placeholder scan:** none — every step carries concrete code or commands.

**Type consistency:** `Tail(label, limit, filter, text)`, `Since(labels, afterRowID, limit, filter, text)`, `logStores.Since(..., text)`, `LogsHistory.Since(..., text)`, `getLogs(..., {..., q})`, `FleetLogsHistoryRequest.Grep` used identically across tasks. ✅
