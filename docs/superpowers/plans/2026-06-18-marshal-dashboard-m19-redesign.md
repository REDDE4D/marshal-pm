# Marshal Dashboard M19 — Redesign + Drill-Down — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reskin the dashboard in the "Signal" identity and restructure it into an overview (summary cards + full-width process cards) that drills into a per-process detail page, plus a `start` control and a recent-error count.

**Architecture:** Three small Go additions (a `start` control case, a `logstore.ErrorCounts` query, and a `/api/logstats` endpoint), then a frontend rebuild: a tokenized Signal stylesheet + bundled JetBrains Mono, a tiny hash router, and `Fleet.tsx` split into `Overview`/`SummaryCards`/`ProcessCard`/`ProcessDetail`/`ControlButtons`/`Logo`.

**Tech Stack:** Go 1.26 (stdlib + modernc sqlite), React 18 + TypeScript + Vite, `@fontsource/jetbrains-mono`.

## Global Constraints

- Module `marshal`; Go imports `marshal/internal/...`. No proto, agent, or manager changes.
- Backend TDD: failing test first. Frontend: no unit harness — each frontend task ends by building with `make ui` (runs `npm install && npm run build`) with **zero TypeScript errors**, and the milestone is verified by a live in-browser pass.
- Gate before finishing: `go build -o marshal ./cmd/marshal`, `go test ./... -race -count=1`, `gofmt -l .` silent, `go vet ./...` clean, `make ui` builds.
- Commit subject imperative + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Branch `m19-dashboard-redesign`, not `main`.
- Identity tokens (verbatim): bg `#0A0A0C`, panel `#121216`, panel-2 `#16161B`, border `#26262C`, border-soft `#1C1C22`, text `#C7CAD2`, text-dim `#7A7E8C`, text-faint `#4D5160`, cyan `#2DD4BF`, lime `#A3E635`, danger `#F87171`, mem `#5B6BD8`. Font `"JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace`. Dark-only. Lowercase labels, never ALL-CAPS. Flat (no gradients/shadows). Prominent numbers (summary ~30px).
- Process states: `starting · online · stopping · stopped · restarting · errored`. Errored = `state === "errored"`. The **start** button is shown/enabled when `state ∈ {stopped, errored}`; restart/stop enabled otherwise; all gated on agent `connected`. **The start button issues a `Restart` control op** — `manager.Restart` stops-then-recreates the instances, which revives a stopped/errored managed process. There is NO separate `start` control op in M19 (the proto's `ControlOp_Start` launches *new* apps from an app-spec, which is a planned future "add app via dashboard" milestone, out of scope here).
- `NewHandler`'s exported signature must not change.

---

## File structure

Backend:
- `internal/dashboard/control.go` — `controlOp` gains `start`.
- `internal/logstore/store.go` — `ErrorCounts`.
- `internal/server/logstores.go` — `logStores.ErrorCounts`.
- `internal/dashboard/logs.go` — `LogsHistory` gains `ErrorCounts`; new `logstats` handler + route.
- tests: `control_test.go`, `internal/logstore/store_test.go`, `internal/server/logstores_test.go`, `internal/dashboard/logs_test.go`.

Frontend (`web/`):
- `package.json` — add `@fontsource/jetbrains-mono`.
- `web/src/styles.css` — replaced by the Signal system.
- `web/src/main.tsx` — import the font weights.
- `web/src/Logo.tsx`, `web/src/router.ts`, `web/src/ControlButtons.tsx`, `web/src/Overview.tsx`, `web/src/SummaryCards.tsx`, `web/src/ProcessCard.tsx`, `web/src/ProcessDetail.tsx` — new.
- `web/src/App.tsx`, `web/src/Login.tsx`, `web/src/api.ts`, `web/src/MetricChart.tsx`, `web/src/Sparkline.tsx` — modified.
- `web/src/Fleet.tsx` — deleted in Task 6.

---

## Task 1: (DROPPED)

Originally a backend `start` control case. Dropped during execution: the proto's
`ControlOp_Start` carries a `StartRequest` (an app-spec list to launch *new* apps), not a
by-name selector, so it cannot "start a stopped process by name". `manager.Restart` already
revives a stopped/errored managed process (stop-then-recreate). Decision: the **start button
issues a `Restart` op** (handled entirely in the frontend `ControlButtons`, Task 5) — no
backend change. A true "add app via dashboard" (app-spec → `StartRequest`) is a planned future
milestone. No work in this task; proceed to Task 2.

---

## Task 2: `ErrorCounts` query (logstore + server)

**Files:**
- Modify: `internal/logstore/store.go`, `internal/server/logstores.go`
- Test: `internal/logstore/store_test.go`, `internal/server/logstores_test.go`

**Interfaces:**
- Produces:
  - `func (s *Store) ErrorCounts(labels []string, sinceMs int64) (map[string]int64, error)` — per-label count of `stderr=1` rows with `ts >= sinceMs`. Labels with no matching rows are absent from the map.
  - `func (s *logStores) ErrorCounts(agent string, sinceMs int64) (map[string]int64, error)` — resolves the agent's labels and delegates; unknown agent ⇒ empty map, nil error.

- [ ] **Step 1: Write the failing logstore test**

Add to `internal/logstore/store_test.go`:

```go
func TestErrorCounts(t *testing.T) {
	st := open(t)
	if err := st.Append([]Line{
		{TsMs: 100, Label: "web#0", Stderr: true, Text: "boom"},
		{TsMs: 150, Label: "web#0", Stderr: false, Text: "ok"},
		{TsMs: 200, Label: "web#0", Stderr: true, Text: "boom2"},
		{TsMs: 50, Label: "web#0", Stderr: true, Text: "old"},
		{TsMs: 300, Label: "api#0", Stderr: true, Text: "err"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ErrorCounts([]string{"web#0", "api#0", "ghost#0"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got["web#0"] != 2 {
		t.Fatalf("web#0 = %d; want 2 (stderr only, ts>=100)", got["web#0"])
	}
	if got["api#0"] != 1 {
		t.Fatalf("api#0 = %d; want 1", got["api#0"])
	}
	if _, present := got["ghost#0"]; present {
		t.Fatalf("ghost#0 should be absent, got %d", got["ghost#0"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/logstore/ -run TestErrorCounts -count=1`
Expected: FAIL — `ErrorCounts` undefined.

- [ ] **Step 3: Implement `Store.ErrorCounts`**

In `internal/logstore/store.go`, add (after `Since`):

```go
// ErrorCounts returns, per label, the number of stderr lines with ts >= sinceMs.
// Labels with no matching rows are omitted from the map.
func (s *Store) ErrorCounts(labels []string, sinceMs int64) (map[string]int64, error) {
	out := map[string]int64{}
	if len(labels) == 0 {
		return out, nil
	}
	ph := make([]string, len(labels))
	args := make([]any, 0, len(labels)+1)
	for i, l := range labels {
		ph[i] = "?"
		args = append(args, l)
	}
	args = append(args, sinceMs)
	q := `SELECT label, count(*) FROM log_line WHERE label IN (` + strings.Join(ph, ",") +
		`) AND stderr = 1 AND ts >= ? GROUP BY label`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var label string
		var n int64
		if err := rows.Scan(&label, &n); err != nil {
			return nil, err
		}
		out[label] = n
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the logstore test**

Run: `go test ./internal/logstore/ -run TestErrorCounts -race -count=1`
Expected: PASS.

- [ ] **Step 5: Write the failing server test**

Add to `internal/server/logstores_test.go`:

```go
func TestLogStoresErrorCounts(t *testing.T) {
	ls := newLogStores(t.TempDir())
	defer ls.closeAll()
	st, err := ls.get("dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append([]logstore.Line{
		{TsMs: 200, Label: "web#0", Stderr: true, Text: "e"},
		{TsMs: 200, Label: "web#0", Stderr: false, Text: "o"},
		{TsMs: 200, Label: "api#0", Stderr: true, Text: "e"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := ls.ErrorCounts("dev-1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if got["web#0"] != 1 || got["api#0"] != 1 {
		t.Fatalf("counts = %v; want web#0:1 api#0:1", got)
	}
	// Unknown agent -> empty, no error.
	g2, err := ls.ErrorCounts("ghost", 0)
	if err != nil || len(g2) != 0 {
		t.Fatalf("unknown agent = (%v, %v); want ({}, nil)", g2, err)
	}
}
```

- [ ] **Step 6: Implement `logStores.ErrorCounts`**

In `internal/server/logstores.go`, add:

```go
// ErrorCounts returns per-label recent stderr counts (ts >= sinceMs) for one
// agent's store. Unknown agent yields an empty map.
func (s *logStores) ErrorCounts(agent string, sinceMs int64) (map[string]int64, error) {
	if !s.has(agent) {
		return map[string]int64{}, nil
	}
	st, err := s.get(agent)
	if err != nil {
		return nil, err
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, err
	}
	return st.ErrorCounts(labels, sinceMs)
}
```

- [ ] **Step 7: Run both packages**

Run: `go test ./internal/logstore/ ./internal/server/ -race -count=1`
Expected: PASS. Then `gofmt -l internal/logstore/ internal/server/`.

- [ ] **Step 8: Commit**

```bash
git add internal/logstore/store.go internal/logstore/store_test.go \
  internal/server/logstores.go internal/server/logstores_test.go
git commit -m "feat(logstore): per-label recent stderr ErrorCounts query

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `/api/logstats` endpoint

**Files:**
- Modify: `internal/dashboard/logs.go`, `internal/dashboard/handlers.go` (route), `internal/dashboard/logs_test.go` (fake + test)

**Interfaces:**
- Consumes: `logStores.ErrorCounts` (Task 2).
- Produces: `LogsHistory` interface gains `ErrorCounts(agent string, sinceMs int64) (map[string]int64, error)`; `GET /api/logstats?agent=<a>` ⇒ `{"counts":{"web#0":3,…}}`. Window = last 5 min, computed server-side.

- [ ] **Step 1: Write the failing test**

Add to `internal/dashboard/logs_test.go` (the `recordingLogs`/`fakeLogs` already in the dashboard tests must gain an `ErrorCounts` method — see Step 3; this test uses a small fake):

```go
type statLogs struct{ counts map[string]int64 }

func (s statLogs) Since(string, string, int64, int, logstore.StreamFilter, string) ([]logstore.StoredLine, int64, error) {
	return nil, 0, nil
}
func (s statLogs) ErrorCounts(string, int64) (map[string]int64, error) { return s.counts, nil }

func TestLogStatsEndpoint(t *testing.T) {
	sl := statLogs{counts: map[string]int64{"web#0": 4, "api#0": 1}}
	h := newHandler(fakeLister{}, &fakeMetrics{}, sl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "")
	req := httptest.NewRequest("GET", "/api/logstats?agent=dev-1", nil)
	rec := httptest.NewRecorder()
	h.logstats(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var body struct{ Counts map[string]int64 `json:"counts"` }
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Counts["web#0"] != 4 || body.Counts["api#0"] != 1 {
		t.Fatalf("counts = %v; want web#0:4 api#0:1", body.Counts)
	}
}
```

Ensure `logs_test.go` imports `encoding/json` and `net/http/httptest` (add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestLogStatsEndpoint -count=1`
Expected: FAIL — `h.logstats` undefined and `statLogs` does not satisfy `LogsHistory` (interface lacks `ErrorCounts`), or compile error.

- [ ] **Step 3: Extend the interface, the existing fake, and add the handler + route**

In `internal/dashboard/logs.go`, extend the interface:

```go
type LogsHistory interface {
	Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error)
	ErrorCounts(agent string, sinceMs int64) (map[string]int64, error)
}
```

Add the handler in `internal/dashboard/logs.go`:

```go
const errorWindowMs = 5 * 60 * 1000

// logstats serves GET /api/logstats?agent=<a>: per-label recent stderr counts
// (last 5 min). Best-effort; an empty/unknown agent returns {"counts":{}}.
func (h *handler) logstats(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("agent")
	if agent == "" {
		http.Error(w, "agent required", http.StatusBadRequest)
		return
	}
	since := nowMs() - errorWindowMs
	counts, err := h.logsHist.ErrorCounts(agent, since)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if counts == nil {
		counts = map[string]int64{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}
```

Add a `nowMs` helper at the bottom of `logs.go` (so it's injectable-free but isolated):

```go
func nowMs() int64 { return time.Now().UnixMilli() }
```

and add `"time"` to the `logs.go` imports.

Register the route in `internal/dashboard/handlers.go` `newHandler` (next to the other `GET` routes):

```go
	mux.HandleFunc("GET /api/logstats", h.requireSession(h.logstats))
```

Update the existing `fakeLogs` (in `internal/dashboard/logs_test.go`) and the `recordingLogs` (in the same file, from M18) to satisfy the extended interface — add to each:

```go
func (f *fakeLogs) ErrorCounts(string, int64) (map[string]int64, error) { return nil, nil }
```
```go
func (r *recordingLogs) ErrorCounts(string, int64) (map[string]int64, error) { return nil, nil }
```

(`*server.logStores` already satisfies the new method from Task 2 — no production wiring change beyond the route.)

- [ ] **Step 4: Run the dashboard package**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS — new logstats test + all existing dashboard tests (now compiling against the extended interface). Then `gofmt -l internal/dashboard/`.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/logs.go internal/dashboard/handlers.go internal/dashboard/logs_test.go
git commit -m "feat(dashboard): /api/logstats recent-error counts endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Signal foundation — font, tokens, stylesheet, logo, login

Delivers the complete Signal stylesheet and shared visual primitives. The existing
`Fleet.tsx` keeps rendering (its classes are restyled), so the app stays coherent until
Task 6 replaces the list.

**Files:**
- Modify: `web/package.json` (dependency), `web/src/main.tsx`, `web/src/styles.css`, `web/src/Login.tsx`
- Create: `web/src/Logo.tsx`

**Interfaces:**
- Produces: `Logo` component; the full token/class system in `styles.css`.

- [ ] **Step 1: Add the bundled font**

Run: `cd web && npm install @fontsource/jetbrains-mono` (adds it to `package.json`/lockfile; Vite bundles the woff2 into `dist` — offline at runtime).

In `web/src/main.tsx`, add these imports above `import "./styles.css";`:

```ts
import "@fontsource/jetbrains-mono/400.css";
import "@fontsource/jetbrains-mono/500.css";
import "@fontsource/jetbrains-mono/700.css";
```

- [ ] **Step 2: Write the Signal stylesheet**

Replace `web/src/styles.css` entirely with:

```css
:root {
  --bg: #0A0A0C; --panel: #121216; --panel-2: #16161B;
  --border: #26262C; --border-soft: #1C1C22;
  --text: #C7CAD2; --dim: #7A7E8C; --faint: #4D5160;
  --cyan: #2DD4BF; --lime: #A3E635; --danger: #F87171; --mem: #5B6BD8;
  --r: 10px; --r-lg: 12px; --r-sm: 7px;
  --mono: "JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace;
  font-family: var(--mono); color: var(--text); background: var(--bg);
  color-scheme: dark;
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); }
.loading { padding: 2rem; color: var(--dim); font-family: var(--mono); }
.error { color: var(--danger); font-size: 0.85rem; }
.empty { color: var(--faint); font-size: 0.85rem; }

.brand { display: flex; align-items: center; gap: 9px; }
.brand .word { font-weight: 700; font-size: 18px; color: #F2F3F7; letter-spacing: -0.01em; }
.brand .word .cur { color: var(--lime); }

button { font-family: var(--mono); cursor: pointer; }
.btn, .seg button, .win button, .ctl-btn {
  font-family: var(--mono); font-size: 11px; color: var(--text);
  background: transparent; border: 1px solid var(--border); border-radius: var(--r-sm);
  padding: 5px 11px;
}
.btn:hover, .seg button:hover, .win button:hover, .ctl-btn:hover:not(:disabled) { background: var(--panel-2); }
.seg button.active, .win button.active { background: var(--cyan); color: var(--bg); border-color: var(--cyan); }
.ctl-btn:disabled { color: #3F4350; border-color: var(--border-soft); cursor: default; }
.ctl-btn.danger { color: #F4A0A0; border-color: #4A2626; }
.ctl-confirm { font-family: var(--mono); font-size: 11px; color: var(--bg); background: var(--danger); border: 0; border-radius: var(--r-sm); padding: 5px 11px; }
.ctl { display: inline-flex; gap: 6px; align-items: center; }
.ctl-msg { font-size: 11px; color: var(--dim); }

.login { display: flex; min-height: 100vh; align-items: center; justify-content: center; }
.login form { background: var(--panel); border: 1px solid var(--border); border-radius: var(--r-lg); padding: 1.75rem; width: 320px; display: flex; flex-direction: column; gap: 0.8rem; }
.login .brand { margin-bottom: 0.5rem; }
.login label { display: flex; flex-direction: column; font-size: 11px; color: var(--dim); gap: 0.3rem; }
.login input { font-family: var(--mono); background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: var(--r-sm); padding: 0.5rem; font-size: 0.9rem; }
.login input:focus { outline: none; border-color: var(--cyan); }
.login button[type="submit"] { background: var(--cyan); color: var(--bg); border: 0; border-radius: var(--r-sm); padding: 0.55rem; font-size: 0.9rem; font-weight: 500; }

.app { max-width: 960px; margin: 0 auto; padding: 1.5rem 1.25rem 3rem; }
.topbar { display: flex; align-items: center; justify-content: space-between; margin-bottom: 1.25rem; }

.summary { display: grid; grid-template-columns: repeat(5, 1fr); gap: 10px; margin-bottom: 1.5rem; }
.stat-card { background: var(--panel); border: 1px solid var(--border); border-radius: var(--r); padding: 12px 13px; }
.stat-label { font-size: 10px; color: var(--dim); margin-bottom: 7px; }
.stat-value { font-size: 30px; font-weight: 500; color: #F2F3F7; line-height: 1; }
.stat-value small { font-size: 13px; color: var(--faint); font-weight: 400; }
.stat-value.cyan { color: var(--cyan); }
.stat-value.danger { color: var(--danger); }

.agent-head { display: flex; align-items: center; gap: 9px; margin: 1.25rem 0 0.7rem; }
.agent-head .name { font-weight: 500; font-size: 13px; color: #F2F3F7; }
.agent-head .seen { font-size: 10.5px; color: var(--faint); }
.dot { width: 7px; height: 7px; border-radius: 50%; display: inline-block; background: var(--dim); }
.dot.online { background: var(--lime); }
.dot.errored { background: var(--danger); }
.dot.stopped { background: var(--faint); }

.pcard { background: var(--panel); border: 1px solid var(--border); border-radius: var(--r-lg); padding: 14px 16px; margin-bottom: 10px; cursor: pointer; text-decoration: none; color: inherit; display: block; }
.pcard:hover { border-color: #2A2F3A; }
.pcard.errored { border-left: 2px solid var(--danger); }
.pcard-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.pcard-id { display: flex; align-items: center; gap: 9px; }
.pname { font-weight: 500; font-size: 15px; color: #F2F3F7; }
.pstate { font-size: 10px; color: var(--lime); }
.pstate.errored { color: var(--danger); }
.pstate.stopped { color: var(--dim); }
.pcard-meta { font-size: 10.5px; color: var(--faint); margin: 9px 0 11px; }
.pcard-metrics { display: flex; align-items: center; gap: 22px; flex-wrap: wrap; }
.metric { display: flex; align-items: center; gap: 9px; }
.metric .mlabel { font-size: 10px; color: var(--dim); }
.metric .mval { font-size: 13px; color: #F2F3F7; }
.err-badge { font-size: 11px; color: var(--danger); }
.pcard-link { margin-left: auto; font-size: 10.5px; color: var(--cyan); }

.crumb { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; font-size: 11px; }
.crumb a { color: var(--cyan); text-decoration: none; }
.crumb .sep { color: #3A3D47; }
.crumb .cur { color: #F2F3F7; }

.card { background: var(--panel); border: 1px solid var(--border); border-radius: var(--r-lg); padding: 15px 16px; margin-bottom: 12px; }
.dhead { display: flex; align-items: center; justify-content: space-between; gap: 10px; flex-wrap: wrap; }
.dtitle { display: flex; align-items: center; gap: 10px; }
.dtitle .pname { font-weight: 700; font-size: 20px; }
.stat-tiles { display: grid; grid-template-columns: repeat(4, 1fr); gap: 10px; margin-top: 14px; }
.tile { background: #0E0E12; border-radius: 8px; padding: 10px 12px; }
.tile .stat-label { margin-bottom: 5px; }
.tile .v { font-size: 19px; font-weight: 500; color: #F2F3F7; }
.tile .v.cyan { color: var(--cyan); }
.tile .v.danger { color: var(--danger); }
.tile .v small { font-size: 11px; color: var(--faint); }
.dmeta { font-size: 10px; color: var(--faint); margin-top: 11px; }

.card-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 12px; }
.card-head .lbl { font-size: 11px; color: var(--dim); }
.charts2 { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.chart-cap { font-size: 10px; color: var(--dim); margin-bottom: 6px; }
.metric-chart { width: 100%; height: auto; }
.metric-chart .grid { stroke: #1F1F26; stroke-width: 1; }
.metric-chart .axis { fill: var(--faint); font-size: 9px; font-family: var(--mono); }
.chart-empty { color: var(--faint); font-size: 11px; font-style: italic; }

.log-controls { display: flex; gap: 10px; align-items: center; margin-bottom: 10px; flex-wrap: wrap; }
.seg { display: inline-flex; gap: 4px; }
.log-search { font-family: var(--mono); font-size: 11px; background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: var(--r-sm); padding: 4px 10px; min-width: 110px; }
.log-search:focus { outline: none; border-color: var(--cyan); }
.logview-wrap { position: relative; }
.logview { max-height: 340px; overflow-y: auto; background: #060608; border: 1px solid var(--border-soft); border-radius: 8px; padding: 9px 11px; font-family: var(--mono); font-size: 10.5px; line-height: 1.8; }
.logline { display: flex; gap: 8px; white-space: pre-wrap; word-break: break-word; }
.logline .logts { color: var(--faint); flex: 0 0 auto; }
.logline .logtext { color: #9AA0AD; flex: 1 1 auto; }
.logline.err .logtext { color: var(--danger); }
.jump { position: absolute; right: 12px; bottom: 12px; font-size: 10px; }
.sparkline { display: block; }
```

- [ ] **Step 3: Create `Logo.tsx`**

```tsx
export function Logo() {
  return (
    <span className="brand">
      <svg width="22" height="22" viewBox="0 0 30 30" aria-hidden="true">
        <rect x="3" y="3" width="24" height="24" rx="6" fill="none" stroke="#2DD4BF" strokeWidth="2" />
        <path d="M9 11 l4 4 -4 4" fill="none" stroke="#A3E635" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" />
        <rect x="16" y="17" width="6" height="2.4" rx="1" fill="#A3E635" />
      </svg>
      <span className="word">marshal<span className="cur">_</span></span>
    </span>
  );
}
```

- [ ] **Step 4: Restyle `Login.tsx`**

Replace the `<h1>Marshal</h1>` with the logo (import it) and lowercase the copy:

```tsx
import { useState } from "react";
import { login } from "./api";
import { Logo } from "./Logo";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [user, setUser] = useState("admin");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (await login(user, pass)) onLogin();
    else setError("invalid username or password");
  }

  return (
    <div className="login">
      <form onSubmit={submit}>
        <Logo />
        <label>username<input value={user} onChange={(e) => setUser(e.target.value)} autoFocus /></label>
        <label>password<input type="password" value={pass} onChange={(e) => setPass(e.target.value)} /></label>
        {error && <p className="error">{error}</p>}
        <button type="submit">sign in</button>
      </form>
    </div>
  );
}
```

- [ ] **Step 5: Build**

Run: `make ui`
Expected: builds clean (no TS errors); the SPA bundles the font. The login page renders in Signal; the existing fleet view picks up the dark tokens.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/src/main.tsx web/src/styles.css web/src/Logo.tsx web/src/Login.tsx internal/dashboard/dist
git commit -m "feat(web): Signal design tokens, bundled mono font, logo, login

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Router, API additions, control buttons, detail page

Adds the hash router and the final process detail page, and converts the existing list
rows from inline-expand to links into the detail page. After this task the drill-down works
end to end (the overview is still the old table, restyled — Task 6 replaces it).

**Files:**
- Create: `web/src/router.ts`, `web/src/ControlButtons.tsx`, `web/src/ProcessDetail.tsx`
- Modify: `web/src/api.ts`, `web/src/App.tsx`, `web/src/Fleet.tsx`, `web/src/MetricChart.tsx`

**Interfaces:**
- Produces:
  - `router.ts`: `type Route`, `useRoute(): Route`, `navigate(hash: string)`, `procHref(agent, proc): string`.
  - `api.ts`: `getLogStats(agent: string): Promise<Record<string, number>>`; `control(...)` action type `"start" | "restart" | "stop"`.
  - `ControlButtons({ agent, proc, state, connected })`.
  - `ProcessDetail({ agent, proc, onLogout })`.

- [ ] **Step 1: Create `router.ts`**

```ts
import { useEffect, useState } from "react";

export type Route = { name: "overview" } | { name: "detail"; agent: string; proc: string };

export function parseHash(hash: string): Route {
  const m = hash.match(/^#\/a\/([^/]+)\/p\/([^/]+)$/);
  if (m) return { name: "detail", agent: decodeURIComponent(m[1]), proc: decodeURIComponent(m[2]) };
  return { name: "overview" };
}

export function useRoute(): Route {
  const [route, setRoute] = useState<Route>(() => parseHash(window.location.hash));
  useEffect(() => {
    const on = () => setRoute(parseHash(window.location.hash));
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);
  return route;
}

export function navigate(hash: string) { window.location.hash = hash; }
export function procHref(agent: string, proc: string) {
  return `#/a/${encodeURIComponent(agent)}/p/${encodeURIComponent(proc)}`;
}
```

- [ ] **Step 2: Extend `api.ts`**

The `control` action type stays `"restart" | "stop"` (the UI "start" maps to a `restart` op —
see `ControlButtons`). Only add `getLogStats` at the end of `api.ts`:

```ts
export async function getLogStats(agent: string): Promise<Record<string, number>> {
  const r = await fetch(`/api/logstats?agent=${encodeURIComponent(agent)}`);
  if (r.status === 401) throw new Error("unauthorized");
  const j = (await r.json()) as { counts: Record<string, number> };
  return j.counts ?? {};
}
```

- [ ] **Step 3: Create `ControlButtons.tsx`**

```tsx
import { useState } from "react";
import { control } from "./api";

// Backend ops are restart/stop. The UI "start" (shown when a process is
// stopped/errored) issues a restart, which revives the managed process.
type Op = "restart" | "stop";

export function ControlButtons({ agent, proc, state, connected }: { agent: string; proc: string; state: string; connected: boolean }) {
  const [pending, setPending] = useState<{ op: Op; label: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");
  const running = !["stopped", "errored"].includes(state);

  async function fire(op: Op) {
    setPending(null); setBusy(true); setMsg("");
    const res = await control(agent, proc, op);
    setBusy(false);
    setMsg(res.ok ? "✓" : res.error || "error");
    window.setTimeout(() => setMsg(""), 4000);
  }
  function ask(op: Op, label: string) {
    setMsg(""); setPending({ op, label });
    window.setTimeout(() => setPending((p) => (p?.op === op ? null : p)), 3000);
  }

  if (busy) return <span className="ctl"><span className="ctl-msg">…</span></span>;
  if (pending) {
    return (
      <span className="ctl" onClick={(e) => e.stopPropagation()}>
        <button className="ctl-confirm" onClick={() => fire(pending.op)}>confirm {pending.label}</button>
        <button className="ctl-btn" onClick={() => setPending(null)}>✕</button>
      </span>
    );
  }
  return (
    <span className="ctl" onClick={(e) => e.stopPropagation()}>
      {/* start: revive a stopped/errored proc via restart; no confirm (it's already down) */}
      <button className="ctl-btn" disabled={!connected || running} onClick={() => fire("restart")}>start</button>
      <button className="ctl-btn" disabled={!connected || !running} onClick={() => ask("restart", "restart")}>restart</button>
      <button className="ctl-btn danger" disabled={!connected || !running} onClick={() => ask("stop", "stop")}>stop</button>
      {msg && <span className="ctl-msg">{msg}</span>}
    </span>
  );
}
```

- [ ] **Step 4: Recolor `MetricChart.tsx`**

Change the two color literals to the Signal palette: cpu `#2DD4BF`, mem `#5B6BD8`:

```tsx
  const color = metric === "cpu" ? "#2DD4BF" : "#5B6BD8";
```

- [ ] **Step 5: Create `ProcessDetail.tsx`**

```tsx
import { useEffect, useState } from "react";
import { AgentMetrics, Bucket, LogLine, getFleet, getLogs, getLogStats, getMetricsForProc, logout } from "./api";
import { MetricChart } from "./MetricChart";
import { LogView } from "./LogView";
import { ControlButtons } from "./ControlButtons";
import { Logo } from "./Logo";
import { navigate } from "./router";

const WINDOWS = [
  { label: "15m", ms: 15 * 60 * 1000 },
  { label: "1h", ms: 60 * 60 * 1000 },
  { label: "6h", ms: 6 * 60 * 60 * 1000 },
];
const STREAMS = ["all", "stdout", "stderr"];
const LOG_LIMITS = [100, 500, 1000];
const LOG_CAP = 5000;

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
function mib(b: number): string { return b <= 0 ? "—" : `${(b / 1048576).toFixed(1)}`; }

export function ProcessDetail({ agent, proc, onLogout }: { agent: string; proc: string; onLogout: () => void }) {
  const [p, setP] = useState<{ state: string; pid: number; uptime_ms: number; restarts: number; cpu: number; mem: number } | null>(null);
  const [connected, setConnected] = useState(true);
  const [errCount, setErrCount] = useState(0);
  const [windowMs, setWindowMs] = useState(WINDOWS[0].ms);
  const [detail, setDetail] = useState<Bucket[]>([]);
  const [stream, setStream] = useState("all");
  const [limit, setLimit] = useState(500);
  const [lines, setLines] = useState<LogLine[]>([]);
  const [search, setSearch] = useState("");
  const [searchDeb, setSearchDeb] = useState("");
  useEffect(() => { const id = setTimeout(() => setSearchDeb(search), 250); return () => clearTimeout(id); }, [search]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const agents = await getFleet();
        if (stop) return;
        const a = agents.find((x) => x.name === agent);
        setConnected(a?.connected ?? false);
        const pr = a?.procs.find((x) => x.name === proc) ?? null;
        setP(pr);
        const counts = await getLogStats(agent);
        if (!stop) {
          let n = 0;
          for (const [label, c] of Object.entries(counts)) if (label === proc || label.startsWith(proc + "#")) n += c;
          setErrCount(n);
        }
      } catch { if (!stop) onLogout(); }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => { stop = true; clearInterval(id); };
  }, [agent, proc, onLogout]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const data: AgentMetrics[] = await getMetricsForProc(agent, proc, windowMs, 0);
        if (!stop) setDetail(data[0]?.procs[0]?.buckets ?? []);
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => { stop = true; clearInterval(id); };
  }, [agent, proc, windowMs]);

  useEffect(() => {
    let stop = false, cursor = 0, first = true;
    setLines([]);
    async function tick() {
      try {
        const res = await getLogs(agent, proc, { stream, limit, after: first ? 0 : cursor, q: searchDeb });
        if (stop) return;
        cursor = res.cursor || cursor; first = false;
        if (res.lines.length > 0) setLines((prev) => { const next = prev.concat(res.lines); return next.length > LOG_CAP ? next.slice(next.length - LOG_CAP) : next; });
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 1500);
    return () => { stop = true; clearInterval(id); };
  }, [agent, proc, stream, limit, searchDeb]);

  const state = p?.state ?? "—";
  const started = p && p.uptime_ms > 0 ? new Date(Date.now() - p.uptime_ms).toLocaleTimeString() : "—";

  return (
    <div className="app">
      <div className="topbar"><Logo /><button className="btn" onClick={async () => { await logout(); onLogout(); }}>sign out</button></div>
      <div className="crumb">
        <a href="#/" onClick={(e) => { e.preventDefault(); navigate("#/"); }}>← fleet</a>
        <span className="sep">/</span><span>{agent}</span><span className="sep">/</span><span className="cur">{proc}</span>
      </div>

      <div className="card">
        <div className="dhead">
          <div className="dtitle">
            <span className={`dot ${state === "online" ? "online" : state === "errored" ? "errored" : "stopped"}`}></span>
            <span className="pname">{proc}</span>
            <span className={`pstate ${state === "errored" ? "errored" : state === "online" ? "" : "stopped"}`}>{state}</span>
          </div>
          <ControlButtons agent={agent} proc={proc} state={state} connected={connected} />
        </div>
        <div className="stat-tiles">
          <div className="tile"><div className="stat-label">cpu</div><div className="v cyan">{p ? (p.cpu * 100).toFixed(1) : "—"}<small>%</small></div></div>
          <div className="tile"><div className="stat-label">memory</div><div className="v">{p ? mib(p.mem) : "—"}<small> mb</small></div></div>
          <div className="tile"><div className="stat-label">uptime</div><div className="v">{p ? uptime(p.uptime_ms) : "—"}</div></div>
          <div className="tile"><div className="stat-label">errors · 5m</div><div className={`v ${errCount > 0 ? "danger" : ""}`}>{errCount}</div></div>
        </div>
        <div className="dmeta">pid {p?.pid || "—"} · {p?.restarts ?? 0} restarts · started {started}</div>
      </div>

      <div className="card">
        <div className="card-head">
          <span className="lbl">metrics</span>
          <span className="seg win">{WINDOWS.map((w) => <button key={w.label} className={`win ${windowMs === w.ms ? "active" : ""}`} onClick={() => setWindowMs(w.ms)}>{w.label}</button>)}</span>
        </div>
        <div className="charts2">
          <div><div className="chart-cap">cpu %</div><MetricChart buckets={detail} metric="cpu" /></div>
          <div><div className="chart-cap">memory mb</div><MetricChart buckets={detail} metric="mem" /></div>
        </div>
      </div>

      <div className="card">
        <div className="log-controls">
          <span className="lbl" style={{ marginRight: "auto", fontSize: 11, color: "var(--dim)" }}>logs</span>
          <span className="seg">{STREAMS.map((s) => <button key={s} className={stream === s ? "active" : ""} onClick={() => setStream(s)}>{s}</button>)}</span>
          <span className="seg">{LOG_LIMITS.map((n) => <button key={n} className={limit === n ? "active" : ""} onClick={() => setLimit(n)}>{n}</button>)}</span>
          <input className="log-search" placeholder="search…" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
        <LogView lines={lines} />
      </div>
    </div>
  );
}
```

- [ ] **Step 6: Route in `App.tsx`**

```tsx
import { useEffect, useState } from "react";
import { getSession } from "./api";
import { Login } from "./Login";
import { Fleet } from "./Fleet";
import { ProcessDetail } from "./ProcessDetail";
import { useRoute } from "./router";

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);
  const route = useRoute();
  useEffect(() => { getSession().then((u) => setAuthed(u !== null)); }, []);
  if (authed === null) return <div className="loading">loading…</div>;
  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  const onLogout = () => setAuthed(false);
  if (route.name === "detail") return <ProcessDetail agent={route.agent} proc={route.proc} onLogout={onLogout} />;
  return <Fleet onLogout={onLogout} />;
}
```

- [ ] **Step 7: Convert `Fleet.tsx` rows to links (remove inline-expand)**

In `web/src/Fleet.tsx`: delete the `expanded`/`detail`/`tab`/log state and the three effects that depend on `expanded` (metrics-for-proc, logs) plus the inline `{isOpen && (...)}` detail `<tr>` and the `ProcActions`/charts/logs JSX. Replace the proc `<tr onClick=…>` with navigation to the detail page, and import `navigate, procHref` from `./router`. The row becomes:

```tsx
<tr className="proc" style={{ cursor: "pointer" }} onClick={() => navigate(procHref(a.name, p.name))}>
  <td>{p.name}</td>
  <td>{p.state}</td>
  <td>{p.pid || "—"}</td>
  <td>{uptime(p.uptime_ms)}</td>
  <td>{p.restarts}</td>
  <td>{(p.cpu * 100).toFixed(1)}%<Sparkline points={metrics[a.name]?.[p.name]?.cpu ?? []} color="#2DD4BF" /></td>
  <td>{mib(p.mem)}<Sparkline points={metrics[a.name]?.[p.name]?.mem ?? []} color="#5B6BD8" /></td>
</tr>
```

Drop the `Actions` column header and the now-unused imports (`getLogs`, `getMetricsForProc`, `MetricChart`, `LogView`, `Fragment`, `ProcActions`). Keep the fleet/agents/metrics(5m) polling and the header (use `<Logo />` + `sign out`). This is a transitional state — Task 6 replaces this whole component.

- [ ] **Step 8: Build**

Run: `make ui`
Expected: builds clean. Clicking a process now navigates to `#/a/<agent>/p/<proc>` and renders the new detail page; the back link returns to the list.

- [ ] **Step 9: Commit**

```bash
git add web/src/router.ts web/src/ControlButtons.tsx web/src/ProcessDetail.tsx web/src/api.ts web/src/App.tsx web/src/Fleet.tsx web/src/MetricChart.tsx internal/dashboard/dist
git commit -m "feat(web): hash router + process detail page + start control

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Overview — summary cards + full-width process cards

Replaces the transitional `Fleet.tsx` table with the final overview.

**Files:**
- Create: `web/src/Overview.tsx`, `web/src/SummaryCards.tsx`, `web/src/ProcessCard.tsx`
- Modify: `web/src/App.tsx` (use `Overview`)
- Delete: `web/src/Fleet.tsx`

**Interfaces:**
- Consumes: `getFleet`, `getMetrics`, `getLogStats`, `procHref`, `ControlButtons`, `Sparkline`, `Logo`.

- [ ] **Step 1: Create `SummaryCards.tsx`**

```tsx
import { Agent } from "./api";

function mib(b: number): string { return `${(b / 1048576).toFixed(1)}`; }

export function SummaryCards({ agents }: { agents: Agent[] }) {
  const online = agents.filter((a) => a.connected).length;
  const procs = agents.flatMap((a) => a.procs);
  const up = procs.filter((p) => p.state === "online").length;
  const errored = procs.filter((p) => p.state === "errored").length;
  const cpu = procs.reduce((s, p) => s + p.cpu, 0) * 100;
  const mem = procs.reduce((s, p) => s + p.mem, 0);
  return (
    <div className="summary">
      <div className="stat-card"><div className="stat-label">agents online</div><div className="stat-value">{online}<small> / {agents.length}</small></div></div>
      <div className="stat-card"><div className="stat-label">processes</div><div className="stat-value">{up}<small> / {procs.length} up</small></div></div>
      <div className="stat-card"><div className="stat-label">total cpu</div><div className="stat-value cyan">{cpu.toFixed(0)}<small>%</small></div></div>
      <div className="stat-card"><div className="stat-label">total memory</div><div className="stat-value">{mib(mem)}<small> mb</small></div></div>
      <div className="stat-card"><div className="stat-label">errors</div><div className={`stat-value ${errored > 0 ? "danger" : ""}`}>{errored}</div></div>
    </div>
  );
}
```

- [ ] **Step 2: Create `ProcessCard.tsx`**

```tsx
import { Proc } from "./api";
import { Sparkline } from "./Sparkline";
import { ControlButtons } from "./ControlButtons";
import { navigate, procHref } from "./router";

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000), h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}
function mib(b: number): string { return b <= 0 ? "—" : `${(b / 1048576).toFixed(1)}mb`; }

export function ProcessCard({ agent, proc, connected, cpuSeries, memSeries, errors }:
  { agent: string; proc: Proc; connected: boolean; cpuSeries: number[]; memSeries: number[]; errors: number }) {
  const state = proc.state;
  const dot = state === "online" ? "online" : state === "errored" ? "errored" : "stopped";
  const meta = state === "online"
    ? `${agent} · pid ${proc.pid || "—"} · up ${uptime(proc.uptime_ms)} · ${proc.restarts} restarts`
    : `${agent} · ${state} · ${proc.restarts} restarts`;
  return (
    <a className={`pcard ${state === "errored" ? "errored" : ""}`} href={procHref(agent, proc.name)}
       onClick={(e) => { e.preventDefault(); navigate(procHref(agent, proc.name)); }}>
      <div className="pcard-head">
        <div className="pcard-id">
          <span className={`dot ${dot}`}></span>
          <span className="pname">{proc.name}</span>
          <span className={`pstate ${state === "errored" ? "errored" : state === "online" ? "" : "stopped"}`}>{state}</span>
        </div>
        <ControlButtons agent={agent} proc={proc.name} state={state} connected={connected} />
      </div>
      <div className="pcard-meta">{meta}</div>
      <div className="pcard-metrics">
        <span className="metric"><span className="mlabel">cpu</span><Sparkline points={cpuSeries} color="#2DD4BF" /><span className="mval">{(proc.cpu * 100).toFixed(0)}%</span></span>
        <span className="metric"><span className="mlabel">mem</span><Sparkline points={memSeries} color="#5B6BD8" /><span className="mval">{mib(proc.mem)}</span></span>
        {errors > 0 && <span className="err-badge">⚠ {errors} errors</span>}
        <span className="pcard-link">view details →</span>
      </div>
    </a>
  );
}
```

- [ ] **Step 3: Create `Overview.tsx`**

```tsx
import { useEffect, useState } from "react";
import { Agent, AgentMetrics, getFleet, getLogStats, getMetrics, logout } from "./api";
import { SummaryCards } from "./SummaryCards";
import { ProcessCard } from "./ProcessCard";
import { Logo } from "./Logo";

type Series = Record<string, Record<string, { cpu: number[]; mem: number[] }>>;

export function Overview({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [metrics, setMetrics] = useState<Series>({});
  const [errors, setErrors] = useState<Record<string, Record<string, number>>>({});

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const f = await getFleet();
        if (stop) return;
        setAgents(f);
        const errMap: Record<string, Record<string, number>> = {};
        for (const a of f.filter((x) => x.connected)) {
          try {
            const counts = await getLogStats(a.name);
            const per: Record<string, number> = {};
            for (const [label, c] of Object.entries(counts)) {
              const name = label.includes("#") ? label.slice(0, label.lastIndexOf("#")) : label;
              per[name] = (per[name] ?? 0) + c;
            }
            errMap[a.name] = per;
          } catch { /* best-effort */ }
        }
        if (!stop) setErrors(errMap);
      } catch { if (!stop) onLogout(); }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => { stop = true; clearInterval(id); };
  }, [onLogout]);

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const data: AgentMetrics[] = await getMetrics(5 * 60 * 1000);
        if (stop) return;
        const next: Series = {};
        for (const a of data) {
          next[a.agent] = {};
          for (const p of a.procs) next[a.agent][p.name] = { cpu: p.buckets.map((b) => b.cpu_avg), mem: p.buckets.map((b) => b.mem_avg) };
        }
        setMetrics(next);
      } catch { /* best-effort */ }
    }
    tick();
    const id = setInterval(tick, 10000);
    return () => { stop = true; clearInterval(id); };
  }, []);

  return (
    <div className="app">
      <div className="topbar"><Logo /><button className="btn" onClick={async () => { await logout(); onLogout(); }}>sign out</button></div>
      <SummaryCards agents={agents} />
      {agents.length === 0 && <p className="empty">no agents connected.</p>}
      {agents.map((a) => (
        <div key={a.name}>
          <div className="agent-head">
            <span className={`dot ${a.connected ? "online" : "stopped"}`}></span>
            <span className="name">{a.name}</span>
            {!a.connected && <span className="seen">offline</span>}
          </div>
          {a.procs.length === 0 && <p className="empty">no processes.</p>}
          {a.procs.map((p) => (
            <ProcessCard key={`${p.name}-${p.pid}`} agent={a.name} proc={p} connected={a.connected}
              cpuSeries={metrics[a.name]?.[p.name]?.cpu ?? []} memSeries={metrics[a.name]?.[p.name]?.mem ?? []}
              errors={errors[a.name]?.[p.name] ?? 0} />
          ))}
        </div>
      ))}
    </div>
  );
}
```

- [ ] **Step 4: Point `App.tsx` at `Overview`, delete `Fleet.tsx`**

In `App.tsx`, replace the `Fleet` import + use with `Overview`:

```tsx
import { Overview } from "./Overview";
...
  return <Overview onLogout={onLogout} />;
```

Then `git rm web/src/Fleet.tsx`.

- [ ] **Step 5: Build**

Run: `make ui`
Expected: builds clean. The overview shows summary cards + full-width process cards with working start/restart/stop, error badges, and links into the detail page.

- [ ] **Step 6: Commit**

```bash
git add web/src/Overview.tsx web/src/SummaryCards.tsx web/src/ProcessCard.tsx web/src/App.tsx internal/dashboard/dist
git rm web/src/Fleet.tsx
git commit -m "feat(web): overview with summary cards + process cards

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Full gate, live in-browser demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m19-redesign.md`

- [ ] **Step 1: Full gate**

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .            # silent
go vet ./...
make ui               # clean
```
Expected: all PASS, silent gofmt, clean vet, `make ui` builds.

- [ ] **Step 2: Live in-browser demo (per CLAUDE.md + the viewable-demo convention)**

Stand up a scratch server on `:9001` (so the Vite proxy target matches) with a connected agent
running at least two apps — one healthy, one that exits non-zero (to exercise the errored card,
the red accent, and the errors summary) and one writing to stderr (to exercise the recent-error
badge). Run `npm --prefix web run dev` (or a `.claude/launch.json` + the Preview tool), open
`http://localhost:5173`, log in, and confirm: summary cards (incl. errors), full-width process
cards with sparklines + error badge + state-aware start/restart/stop, clicking a card → detail
page with stat tiles (incl. `errors · 5m`), side-by-side cpu/mem charts with the window selector,
and the log panel + server-side search; then the back link returns to the overview. Screenshot
the overview and detail. Tear down (stop agent + server + Vite, remove scratch + any
`.claude/launch.json`); confirm `pgrep -fl marshal` shows no demo orphans and the tree is clean.

- [ ] **Step 3: Write the handoff**

Write `docs/handoffs/2026-06-18-m19-redesign.md`: state + branch, what changed (Signal identity,
overview→detail IA, start-as-restart, error counts), build/run/test (incl. `make ui`), the
live-demo result with screenshots noted, deferred items (**add an app via the dashboard / true
start = app-spec → `StartRequest`, a planned next milestone**; one-click port open; light mode;
per-instance error attribution; process command on the detail page needs a proto/agent change),
and the next step. Commit it.

- [ ] **Step 4: Finish the branch**

Use `superpowers:finishing-a-development-branch` to merge `m19-dashboard-redesign` to `main`.

---

## Self-review

**Spec coverage:**
- Signal tokens, bundled mono font, prominent numbers, logo, flat → Task 4. ✅
- Hash router (overview ↔ detail) → Task 5 (`router.ts`). ✅
- Overview: summary cards (incl. errors) + full-width process cards (state, meta, sparklines, recent-error badge, state-aware start/restart/stop, link) → Task 6. ✅
- Process detail: back/breadcrumb, header + controls, stat tiles incl `errors·5m`, side-by-side cpu/mem charts + window selector, log panel + search → Task 5 (`ProcessDetail`). ✅
- `start` button → revives via a `Restart` op in `ControlButtons` (Task 5); no backend `start` op (Task 1 dropped — proto `Start` launches new apps). ✅
- Recent-error count (`stderr`, last 5 min): logstore + server + `/api/logstats` → Tasks 2–3; consumed by overview badges + detail tile → Tasks 5–6. ✅
- `Fleet.tsx` split into focused components → Tasks 5–6. ✅
- No proto/agent/manager changes; `NewHandler` unchanged → confirmed (only the `LogsHistory` interface grew, satisfied by `*server.logStores` + test fakes). ✅
- Command intentionally absent from detail (data not shipped) → noted in Task 7 deferred. ✅
- Gate + in-browser demo + handoff → Task 7. ✅

**Placeholder scan:** none — every step carries concrete code or commands.

**Type consistency:** `Route`/`useRoute`/`navigate`/`procHref`, `getLogStats(agent): Promise<Record<string,number>>`, `control(...,"start"|"restart"|"stop")`, `ControlButtons({agent,proc,state,connected})`, `ProcessCard({agent,proc,connected,cpuSeries,memSeries,errors})`, `Store.ErrorCounts(labels,sinceMs)`, `logStores.ErrorCounts(agent,sinceMs)`, `LogsHistory.ErrorCounts` used identically across tasks. ✅
