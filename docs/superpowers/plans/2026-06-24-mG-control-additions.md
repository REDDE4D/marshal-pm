# M-G Control Additions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a graceful **reload** control op (rolling per-instance restart), a per-agent **restart-all** UI affordance over the existing op, and a **log download** endpoint — backend capability plus minimal transitional UI.

**Architecture:** Reload is a new `ControlOp` that maps to a new `manager.Reload` doing a rolling restart (stop one instance, wait for exit, start it, wait for it to come online, then the next). Restart-all needs no backend change — it is the existing `restart` op with selector `"all"`, exposed as a confirm button. Log download is a new `GET /api/logs/download` that reuses the existing `LogsHistory.Since` with no limit and streams plain text.

**Tech Stack:** Go (stdlib `net/http`, protobuf/gRPC via `protoc`), SQLite-backed log store, React/TypeScript SPA built with Vite and embedded into the Go binary.

## Global Constraints

- Module path is `marshal`; imports are `marshal/internal/...`.
- TDD: write the failing test first, run it red, then implement. Frequent commits.
- Go via Homebrew (`/opt/homebrew/bin/go`, 1.26.4). Run `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (must list nothing), `make build` before finishing.
- Proto changes regenerate `internal/pb` via `make proto` (`scripts/gen-proto.sh`); commit the regenerated `.pb.go`.
- Frontend has **no unit-test harness**; verify the SPA with `make ui` (runs `tsc -b && vite build`) and commit the rebuilt embedded bundle. "Show only real data" — transitional UI is minimal but functional.
- Commit messages: imperative subject; trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- All work on branch `mG-control-additions` (already created off `dev`).
- `CHANGELOG.md` gets an `[Unreleased]` → `Added` entry as part of the work.

---

## File Structure

- `internal/manager/manager.go` — add `Reload(sel)` + `waitInstanceOnline` helper + an unexported `onReloadStep` test seam field. (modify)
- `internal/manager/manager_test.go` — reload tests. (modify)
- `proto/marshal/v1/fleet.proto` — add `Selector reload = 10;` to `ControlOp`. (modify)
- `internal/pb/fleet.pb.go` — regenerated. (modify, via `make proto`)
- `internal/daemon/command.go` — add `ControlOp_Reload` dispatch case. (modify)
- `internal/daemon/command_test.go` — reload dispatch test. (modify)
- `internal/dashboard/control.go` — add `"reload"` to `controlOp`. (modify)
- `internal/dashboard/control_test.go` — reload action test. (modify)
- `internal/dashboard/logs.go` — add `logsDownload` handler. (modify)
- `internal/dashboard/handlers.go` — register `GET /api/logs/download`. (modify)
- `internal/dashboard/logs_test.go` — download endpoint test (+ extend `fakeLogs` to record `limit`). (modify)
- `web/src/api.ts` — extend `control` action union; add `logsDownloadURL`. (modify)
- `web/src/ControlButtons.tsx` — add reload button. (modify)
- `web/src/RestartAllButton.tsx` — new per-agent restart-all button. (create)
- `web/src/Overview.tsx` — render `RestartAllButton` in the agent header. (modify)
- `web/src/ProcessDetail.tsx` — add a download link in the log controls. (modify)
- `internal/dashboard/dist/**` — rebuilt SPA bundle. (modify, via `make ui`)
- `CHANGELOG.md` — `[Unreleased]` → `Added` entry. (modify)

---

## Task 1: `manager.Reload` — rolling graceful restart

**Files:**
- Modify: `internal/manager/manager.go`
- Test: `internal/manager/manager_test.go`

**Interfaces:**
- Consumes: existing `resolve`, `startInstance`, `managedInstance{cancel, done, inst}`, `supervisor.StateOnline`, `(*supervisor.Instance).Snapshot().State`.
- Produces: `func (m *Manager) Reload(sel string) ([]InstanceSnapshot, error)` — rolling restart of the selected apps; returns the post-reload snapshot like `Restart`. Unexported test seam `onReloadStep func()` fired after each instance is stopped and before its replacement starts.

Note (M-E consistency): `manager.Restart` does **not** record restart events (the M-E `WithOnRestart` sink fires only on supervisor-level crash/backoff restarts via `startInstance`, line ~116). `Reload` uses the same `startInstance`, so it likewise records no manual-reload event. This matches the spec's "treat reload exactly like a manual restart" — **add no event-recording code to `Reload`.**

- [ ] **Step 1: Write the failing tests**

Add to `internal/manager/manager_test.go`:

```go
func TestReloadRestartsAllInstances(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 2)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := waitOnline(m, 2); got != 2 {
		t.Fatalf("setup online = %d, want 2", got)
	}

	before := map[string]int{}
	for _, s := range m.List() {
		before[s.Label] = s.Pid
	}

	if _, err := m.Reload("a"); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := waitOnline(m, 2); got != 2 {
		t.Fatalf("after Reload online = %d, want 2", got)
	}
	for _, s := range m.List() {
		if s.State != supervisor.StateOnline {
			t.Fatalf("%s state = %s, want online", s.Label, s.State)
		}
		if s.Pid == before[s.Label] || s.Pid == 0 {
			t.Fatalf("%s pid = %d (before %d); want a fresh non-zero pid", s.Label, s.Pid, before[s.Label])
		}
	}
	m.StopAll()
}

func TestReloadIsRolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 2)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 2)

	minOnline := 2
	m.onReloadStep = func() {
		online := 0
		for _, s := range m.List() {
			if s.State == supervisor.StateOnline {
				online++
			}
		}
		if online < minOnline {
			minOnline = online
		}
	}

	if _, err := m.Reload("a"); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// A rolling reload takes at most one instance of a 2-instance app down at a
	// time, so at every step (one instance stopped, replacement not yet started)
	// at least one instance is still online.
	if minOnline < 1 {
		t.Fatalf("minOnline during reload = %d, want >= 1 (rolling)", minOnline)
	}
	m.StopAll()
}

func TestReloadUnknownSelector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Reload("nope"); err == nil {
		t.Fatal("Reload of unknown selector: want error, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/manager/ -run TestReload -v`
Expected: FAIL — `m.Reload` undefined and `m.onReloadStep` undefined.

- [ ] **Step 3: Implement `Reload` + helper + seam**

In `internal/manager/manager.go`, add the seam field to the `Manager` struct (after `restartSink RestartSink`):

```go
	mu          sync.Mutex
	apps        []*managedApp
	nextID      int
	logs        LogProvider
	restartSink RestartSink

	// onReloadStep, when set, fires during Reload after an instance is stopped
	// and before its replacement starts. Test seam only; nil in production.
	onReloadStep func()
```

Add, after `Restart` (around line 198):

```go
// reloadOnlineTimeout bounds the wait for a freshly started instance to come
// online before a rolling reload proceeds to the next instance.
const reloadOnlineTimeout = 10 * time.Second

// Reload performs a rolling graceful restart of the selected apps: each app's
// instances are restarted one at a time (stop, wait for exit, start, wait for
// online), so a multi-instance app keeps at most one instance down at any moment.
// A single-instance app degrades to an ordinary graceful restart.
func (m *Manager) Reload(sel string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	apps = append([]*managedApp(nil), apps...) // own the slice; we mutate per index below
	m.mu.Unlock()

	for _, a := range apps {
		for idx := 0; idx < a.spec.Instances; idx++ {
			m.mu.Lock()
			var old *managedInstance
			if idx < len(a.insts) {
				old = a.insts[idx]
			}
			m.mu.Unlock()

			if old != nil {
				old.cancel()
				<-old.done
			}

			if m.onReloadStep != nil {
				m.onReloadStep()
			}

			m.mu.Lock()
			fresh := m.startInstance(a.spec, idx)
			if idx < len(a.insts) {
				a.insts[idx] = fresh
			} else {
				a.insts = append(a.insts, fresh)
			}
			m.mu.Unlock()

			waitInstanceOnline(fresh, reloadOnlineTimeout)
		}
	}
	return m.Describe(sel)
}

// waitInstanceOnline polls until the instance reports Online or timeout elapses.
// Best-effort: a never-online instance simply ends the wait so reload can finish.
func waitInstanceOnline(in *managedInstance, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if in.inst.Snapshot().State == supervisor.StateOnline {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/manager/ -run TestReload -v`
Expected: PASS (all three).

- [ ] **Step 5: Run the full manager package with race**

Run: `go test ./internal/manager/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/manager/manager.go internal/manager/manager_test.go
git commit -m "feat(manager): add Reload (rolling graceful restart)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Proto `reload` op + daemon dispatch

**Files:**
- Modify: `proto/marshal/v1/fleet.proto`
- Modify (regenerated): `internal/pb/fleet.pb.go`
- Modify: `internal/daemon/command.go`
- Test: `internal/daemon/command_test.go`

**Interfaces:**
- Consumes: `manager.Reload` (Task 1); generated `*pb.ControlOp_Reload` with `GetReload() *pb.Selector`.
- Produces: a `ControlOp_Reload` dispatch in `handleFleetCommand` returning the affected procs.

- [ ] **Step 1: Add the proto field**

In `proto/marshal/v1/fleet.proto`, inside the `ControlOp` oneof, after `CommitRequest commit = 9;`:

```protobuf
    CommitRequest   commit    = 9;  // M24
    Selector        reload    = 10; // M-G, rolling graceful restart
```

- [ ] **Step 2: Regenerate pb**

Run: `make proto`
Expected: prints `regenerated internal/pb`; `git status` shows `internal/pb/fleet.pb.go` modified.

- [ ] **Step 3: Verify the generated symbols exist**

Run: `grep -n "ControlOp_Reload\|GetReload" internal/pb/fleet.pb.go | head`
Expected: shows the `ControlOp_Reload` wrapper type and `func (x *ControlOp) GetReload() *Selector`.

- [ ] **Step 4: Write the failing dispatch test**

Add to `internal/daemon/command_test.go`:

```go
func TestHandleFleetCommandReload(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	// Start an app so there is something to reload.
	start := &pb.Command{RequestId: 1, Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
		Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app1")}},
	}}}
	if res := s.handleFleetCommand(start); !res.GetOk() {
		t.Fatalf("start: %s", res.GetError())
	}

	reload := &pb.Command{RequestId: 2, Op: &pb.ControlOp{Op: &pb.ControlOp_Reload{
		Reload: &pb.Selector{Target: "app1"},
	}}}
	res := s.handleFleetCommand(reload)
	if !res.GetOk() {
		t.Fatalf("reload: expected Ok=true, got error: %s", res.GetError())
	}
	if len(res.GetProcs()) == 0 {
		t.Fatal("reload: expected procs in result, got none")
	}
}
```

- [ ] **Step 5: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommandReload -v`
Expected: FAIL — result `Ok=false`, error `unknown op type *pb.ControlOp_Reload`.

- [ ] **Step 6: Add the dispatch case**

In `internal/daemon/command.go`, in the `switch v := op.GetOp().(type)`, after the `ControlOp_Restart` case (around line 53):

```go
	case *pb.ControlOp_Restart:
		snaps, err = s.mgr.Restart(v.Restart.GetTarget())

	case *pb.ControlOp_Reload:
		snaps, err = s.mgr.Reload(v.Reload.GetTarget())
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommandReload -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/fleet.pb.go internal/daemon/command.go internal/daemon/command_test.go
git commit -m "feat(proto,daemon): add reload control op + dispatch

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Dashboard `reload` action

**Files:**
- Modify: `internal/dashboard/control.go`
- Test: `internal/dashboard/control_test.go`

**Interfaces:**
- Consumes: generated `pb.ControlOp_Reload`, `pb.Selector`.
- Produces: `controlOp("reload", sel)` returns a `*pb.ControlOp` wrapping `ControlOp_Reload`; `POST /api/control` with `action:"reload"` is accepted.

- [ ] **Step 1: Write the failing tests**

Add to `internal/dashboard/control_test.go`:

```go
func TestControlOpReload(t *testing.T) {
	op := controlOp("reload", "web")
	if op == nil {
		t.Fatal("controlOp(reload) = nil, want a ControlOp")
	}
	r, ok := op.GetOp().(*pb.ControlOp_Reload)
	if !ok {
		t.Fatalf("controlOp(reload) op type = %T, want *pb.ControlOp_Reload", op.GetOp())
	}
	if r.Reload.GetTarget() != "web" {
		t.Fatalf("reload target = %q, want web", r.Reload.GetTarget())
	}
}

func TestControlReloadHappyPath(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"reload"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload control = %d, want 200", resp.StatusCode)
	}
	if _, ok := fc.gotOp.GetOp().(*pb.ControlOp_Reload); !ok {
		t.Fatalf("forwarded op type = %T, want *pb.ControlOp_Reload", fc.gotOp.GetOp())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dashboard/ -run 'TestControlOpReload|TestControlReloadHappyPath' -v`
Expected: FAIL — `controlOp("reload", ...)` returns nil (unknown action).

- [ ] **Step 3: Add the action case**

In `internal/dashboard/control.go`, in `controlOp`, after the `restart` case:

```go
	case "restart":
		return &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: selector}}}
	case "reload":
		return &pb.ControlOp{Op: &pb.ControlOp_Reload{Reload: &pb.Selector{Target: selector}}}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -run 'TestControlOpReload|TestControlReloadHappyPath' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/control.go internal/dashboard/control_test.go
git commit -m "feat(dashboard): accept reload control action

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Log download endpoint

**Files:**
- Modify: `internal/dashboard/logs.go`
- Modify: `internal/dashboard/handlers.go`
- Test: `internal/dashboard/logs_test.go`

**Interfaces:**
- Consumes: `h.logsHist.Since(agent, selector, 0, 0, filter, text)` — `limit <= 0` means no limit (full retained history; see `logstore.Store.Since` doc); `streamFilterFor`; `logLineView`'s source `logstore.StoredLine{TsMs, Label, Stderr, Text}`; `splitLogLabel`.
- Produces: `GET /api/logs/download` → `text/plain; charset=utf-8`, `Content-Disposition: attachment; filename="<agent>-<selector>.log"`, one `RFC3339 ts <stream> name#idx | text` line per stored line, honoring `stream` and `q` filters, no line limit.

Note: `Since` returns a materialized slice (bounded by log retention/pruning), so the handler writes that slice out rather than truly streaming row-by-row. A row iterator is unnecessary at current retention and is out of scope.

- [ ] **Step 1: Extend `fakeLogs` to record limits, then write the failing test**

In `internal/dashboard/logs_test.go`, change the fake to also record `limit`:

```go
type fakeLogs struct {
	afters []int64
	limits []int
}

func (f *fakeLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error) {
	f.afters = append(f.afters, afterRowID)
	f.limits = append(f.limits, limit)
	return []logstore.StoredLine{
		{RowID: 7, TsMs: 1000, Label: "web#0", Stderr: false, Text: "hello"},
		{RowID: 8, TsMs: 1001, Label: "web#1", Stderr: true, Text: "oops"},
	}, 8, nil
}
```

Add the test:

```go
func TestLogsDownload(t *testing.T) {
	fl := &fakeLogs{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs/download?agent=dev-1&selector=web&stream=stderr&q=oops", nil)
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="dev-1-web.log"` {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), "oops") {
		t.Fatalf("body missing lines: %q", body)
	}
	// Full history: no limit passed to the store.
	if len(fl.limits) != 1 || fl.limits[0] != 0 {
		t.Fatalf("store limits = %v, want [0]", fl.limits)
	}
	if len(fl.afters) != 1 || fl.afters[0] != 0 {
		t.Fatalf("store afters = %v, want [0]", fl.afters)
	}
}

func TestLogsDownloadRequiresParams(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	req, _ := http.NewRequest("GET", srv.URL+"/api/logs/download?agent=dev-1", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing selector = %d, want 400", resp.StatusCode)
	}
}
```

Add the `io` import to the test file if not present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dashboard/ -run TestLogsDownload -v`
Expected: FAIL — 404 (route not registered) / handler undefined.

- [ ] **Step 3: Implement the handler**

In `internal/dashboard/logs.go`, add (after the `logs` handler, keeping `fmt` import — add it):

```go
// logsDownload serves GET /api/logs/download: the full retained log history for
// one agent/selector as a plain-text attachment, honoring the same stream/q
// filters as /api/logs (no line limit).
func (h *handler) logsDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	selector := q.Get("selector")
	if agent == "" || selector == "" {
		http.Error(w, "agent and selector required", http.StatusBadRequest)
		return
	}
	lines, _, err := h.logsHist.Since(agent, selector, 0, 0, streamFilterFor(q.Get("stream")), q.Get("q"))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", downloadName(agent, selector)))
	for _, ln := range lines {
		stream := "out"
		if ln.Stderr {
			stream = "err"
		}
		ts := time.UnixMilli(ln.TsMs).UTC().Format(time.RFC3339)
		fmt.Fprintf(w, "%s %s %s | %s\n", ts, stream, ln.Label, ln.Text)
	}
}

// downloadName builds a filesystem-safe "<agent>-<selector>.log" attachment name.
func downloadName(agent, selector string) string {
	repl := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == '"' {
				return '-'
			}
			return r
		}, s)
	}
	return repl(agent) + "-" + repl(selector) + ".log"
}
```

Add imports `"fmt"` to `internal/dashboard/logs.go` (it already imports `net/http`, `strconv`, `strings`, `time`, `logstore`).

- [ ] **Step 4: Register the route**

In `internal/dashboard/handlers.go`, after the `/api/logs` registration (line ~97):

```go
	mux.HandleFunc("GET /api/logs", h.requireSession(h.logs))
	mux.HandleFunc("GET /api/logs/download", h.requireSession(h.logsDownload))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -run TestLogsDownload -v`
Expected: PASS (both).

- [ ] **Step 6: Run the whole dashboard package**

Run: `go test ./internal/dashboard/ -count=1`
Expected: PASS (the `fakeLogs` change keeps existing `/api/logs` tests green).

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/logs.go internal/dashboard/handlers.go internal/dashboard/logs_test.go
git commit -m "feat(dashboard): add GET /api/logs/download (plain-text full history)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Frontend wiring + bundle + changelog

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/ControlButtons.tsx`
- Create: `web/src/RestartAllButton.tsx`
- Modify: `web/src/Overview.tsx`
- Modify: `web/src/ProcessDetail.tsx`
- Modify (rebuilt): `internal/dashboard/dist/**`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: backend `action:"reload"` (Task 3) and `GET /api/logs/download` (Task 4).
- Produces: `control(agent, selector, "restart"|"stop"|"delete"|"reload")`; `logsDownloadURL(agent, selector, {stream, q})`; a reload button (ControlButtons), a per-agent restart-all button, and a log download link.

- [ ] **Step 1: Extend the API client**

In `web/src/api.ts`, widen the `control` action union (line ~151):

```ts
export async function control(
  agent: string,
  selector: string,
  action: "restart" | "stop" | "delete" | "reload",
): Promise<ControlResult> {
```

Add a URL builder near `fileDownloadURL` (line ~321):

```ts
// logsDownloadURL builds the GET /api/logs/download link for a proc, honoring the
// current stream/search filters. Used as a plain <a href> (cookie auth applies).
export function logsDownloadURL(agent: string, selector: string, opts: { stream: string; q: string }): string {
  const q = new URLSearchParams({ agent, selector, stream: opts.stream, q: opts.q });
  return `/api/logs/download?${q.toString()}`;
}
```

- [ ] **Step 2: Add the reload button**

In `web/src/ControlButtons.tsx`, widen `Op` and add a reload button (with confirm, enabled when running):

```ts
type Op = "restart" | "stop" | "reload";
```

In the returned button row, between `restart` and `stop`:

```tsx
      <button className="ctl-btn" disabled={!connected || !running} onClick={() => ask("restart", "restart")}>restart</button>
      <button className="ctl-btn" disabled={!connected || !running} onClick={() => ask("reload", "reload")}>reload</button>
      <button className="ctl-btn danger" disabled={!connected || !running} onClick={() => ask("stop", "stop")}>stop</button>
```

(`fire`/`ask`/`pending` already accept any `Op`, so no other change.)

- [ ] **Step 3: Create the restart-all button**

Create `web/src/RestartAllButton.tsx`:

```tsx
import { useState } from "react";
import { control } from "./api";

// RestartAllButton restarts every app on one agent via the existing restart op
// with the "all" selector. Confirm-then-fire, mirroring ControlButtons.
export function RestartAllButton({ agent, connected }: { agent: string; connected: boolean }) {
  const [pending, setPending] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  async function fire() {
    setPending(false); setBusy(true); setMsg("");
    const res = await control(agent, "all", "restart");
    setBusy(false);
    setMsg(res.ok ? "✓" : res.error || "error");
    window.setTimeout(() => setMsg(""), 4000);
  }

  if (busy) return <span className="ctl"><span className="ctl-msg">…</span></span>;
  if (pending) {
    return (
      <span className="ctl">
        <button className="ctl-confirm" onClick={fire}>confirm restart all</button>
        <button className="ctl-btn" onClick={() => setPending(false)}>✕</button>
      </span>
    );
  }
  return (
    <span className="ctl">
      <button className="ctl-btn" disabled={!connected} onClick={() => setPending(true)}>restart all</button>
      {msg && <span className="ctl-msg">{msg}</span>}
    </span>
  );
}
```

- [ ] **Step 4: Render restart-all in the agent header**

In `web/src/Overview.tsx`, add the import:

```tsx
import { RestartAllButton } from "./RestartAllButton";
```

In the agent header block (the `agent-head` div, lines ~126-131), add the button after the meta spans:

```tsx
          <div className="agent-head">
            <span className={`dot ${a.connected ? "online" : "stopped"}`}></span>
            <span className="name">{a.name}</span>
            <span className="seen">{agentMeta(a)}</span>
            {hostMeta(a) && <span className="seen host-meta">{hostMeta(a)}</span>}
            {a.procs.length > 0 && <RestartAllButton agent={a.name} connected={a.connected} />}
          </div>
```

- [ ] **Step 5: Add the download link**

In `web/src/ProcessDetail.tsx`, update the import (line 2) to include `logsDownloadURL`:

```tsx
import { AgentMetrics, Bucket, LogLine, getFleet, getLogs, getLogStats, getMetricsForProc, logout, logsDownloadURL } from "./api";
```

In the `log-controls` div (lines ~143-148), add a download anchor after the search input:

```tsx
          <input className="log-search" placeholder="search…" value={search} onChange={(e) => setSearch(e.target.value)} />
          <a className="btn" href={logsDownloadURL(agent, proc, { stream, q: searchDeb })} download>download</a>
```

- [ ] **Step 6: Build the SPA bundle**

Run: `make ui`
Expected: `tsc -b && vite build` succeeds with no type errors; `internal/dashboard/dist/**` updated.

- [ ] **Step 7: Add the changelog entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added`, add:

```markdown
- **Control additions (M-G):** graceful **reload** (rolling per-instance restart, distinct from
  restart), a per-agent **restart all** action, and a **log download** endpoint
  (`GET /api/logs/download`, plain-text full history honoring the stream/search filters).
```

- [ ] **Step 8: Verify the whole project**

Run: `go build -o /dev/null ./... && go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: build ok; all tests PASS; vet clean; `gofmt -l .` prints nothing.

- [ ] **Step 9: Commit**

```bash
git add web/src internal/dashboard/dist CHANGELOG.md
git commit -m "feat(ui): reload + restart-all buttons and log download link

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed during planning)

**Spec coverage:**
- Graceful reload (rolling) → Tasks 1 (manager), 2 (proto+daemon), 3 (dashboard action), 5 (UI). ✓
- Restart-all (UI over existing op, no backend change) → Task 5 (RestartAllButton). ✓
- Log download (plain text, full history, stream/q filters) → Task 4 (endpoint), 5 (link). ✓
- M-E consistency (no extra reload event recording) → Task 1 note. ✓
- Proto regen, full-suite verification, changelog, bundle rebuild → Tasks 2, 4, 5. ✓

**Placeholder scan:** No TBD/TODO; every code step shows concrete code and exact commands. ✓

**Type consistency:** `Reload(sel string) ([]InstanceSnapshot, error)`, `ControlOp_Reload`/`GetReload()`, `controlOp("reload",...)`, `logsDownloadURL(agent, selector, {stream, q})`, `Since(...,0,0,...)` are used identically across tasks. ✓

## Out of scope (deferred to M-A)

Polished button styling/placement, multi-select bulk selector, a dedicated rolling restart-all button (restart-all uses the hard `restart all`; rolling-everything remains reachable via `reload all`).
