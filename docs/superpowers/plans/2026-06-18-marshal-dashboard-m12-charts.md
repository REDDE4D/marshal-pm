# M12 Dashboard Metric Charts — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add live CPU/memory charts to the Marshal dashboard — inline SVG sparklines per process row plus a click-to-expand detail panel with a selectable time window.

**Architecture:** The central server already persists per-agent CPU/mem history in SQLite (`internal/metricstore`, managed by `internal/server/stores.go`). We extract the history-query logic onto `*stores`, add a session-guarded `GET /api/metrics` dashboard endpoint over it, and render the data as hand-rolled SVG in the React SPA. No new storage, no charting library.

**Tech Stack:** Go (stdlib `net/http`, `modernc.org/sqlite`), React 19 + TypeScript + Vite, hand-rolled SVG.

## Global Constraints

- Module path is `marshal`; imports are `marshal/internal/...`.
- TDD for all Go code: failing test first, then implementation. The web SPA has **no JS test harness** (none exists in the repo) — verify React changes by building (`make ui`) and the live demo; do not introduce a JS test framework.
- Go gate before finishing: `go test ./... -race -count=1`, `gofmt -l .` (must print nothing), `go vet ./...`.
- Dashboard interfaces are **defined in the `dashboard` package** and satisfied by `server` types — never import `server` from `dashboard` (import cycle).
- Commit messages: imperative subject; trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work on a branch `m12-charts`, not `main`.
- The committed `internal/dashboard/dist/` is the embedded SPA artifact; rebuild it with `make ui` after any `web/src/` change and commit it.

---

## File Structure

- `internal/server/stores.go` — add `History(agent, selector, sinceMs, bucketMs)`.
- `internal/server/server.go` — refactor `FleetMetricsHistory` to call `History`; pass `ss` to `dashboard.Serve`.
- `internal/dashboard/metrics.go` (new) — `MetricsHistory` interface, `metricsView`, `GET /api/metrics` handler.
- `internal/dashboard/handlers.go`, `internal/dashboard/server.go` — thread the `MetricsHistory` dependency through `newHandler` / `NewHandler` / `Serve`.
- `web/src/Sparkline.tsx` (new), `web/src/MetricChart.tsx` (new) — SVG components.
- `web/src/api.ts`, `web/src/Fleet.tsx` — fetch helpers + integration.
- `internal/dashboard/dist/` — rebuilt committed artifact.

---

## Task 1: `stores.History` + refactor `FleetMetricsHistory`

**Files:**
- Modify: `internal/server/stores.go`
- Modify: `internal/server/server.go:180-224` (`FleetMetricsHistory`)
- Test: `internal/server/stores_test.go`

**Interfaces:**
- Produces: `func (s *stores) History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error)` — merged CPU/mem buckets for an agent's selector (matches `selector` exactly or as a `selector#` prefix), oldest first. `sinceMs` is a window width in ms; `History` computes `lowerMs = time.Now().UnixMilli() - sinceMs` and passes `bucketMs` through `metricstore.AutoBucketMs(sinceMs, bucketMs)`. A missing agent (`!s.has(agent)`) returns `(nil, nil)`.
- Consumes (existing): `metricstore.AutoBucketMs`, `metricstore.MergeBuckets`, `(*metricstore.Store).Labels`, `(*metricstore.Store).Query`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/stores_test.go`:

```go
func TestStoresHistory(t *testing.T) {
	ss := newStores(t.TempDir())
	defer ss.closeAll()
	st, _ := ss.get("web-1")
	now := time.Now().UnixMilli()
	_ = st.Append(now-2000, []metricstore.Sample{{Label: "api#0", Cpu: 10, Mem: 100}, {Label: "api#1", Cpu: 5, Mem: 50}})
	_ = st.Append(now-1000, []metricstore.Sample{{Label: "api#0", Cpu: 30, Mem: 300}})

	// Missing agent → (nil, nil).
	bs, err := ss.History("ghost", "api", (time.Hour).Milliseconds(), 1000)
	if err != nil || bs != nil {
		t.Fatalf("missing agent = (%v, %v); want (nil, nil)", bs, err)
	}

	// Selector "api" matches api#0 and api#1, merged across instances.
	bs, err = ss.History("web-1", "api", (time.Hour).Milliseconds(), 1000)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(bs) == 0 {
		t.Fatal("expected merged buckets for api")
	}
}
```

This requires `time` and `metricstore` imports, already present in `stores_test.go`'s package; add `"time"` if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestStoresHistory -v`
Expected: FAIL — `ss.History undefined (type *stores has no field or method History)`.

- [ ] **Step 3: Implement `History` on `*stores`**

Add to `internal/server/stores.go` (add `"time"` to the import block; `metricstore` is already imported):

```go
// History returns merged CPU/mem buckets for an agent's selector (an app name or
// "app#instance"), matching the selector exactly or as a "selector#" prefix and
// merging across instances, oldest first. sinceMs is the window width in ms.
// A missing agent returns (nil, nil).
func (s *stores) History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error) {
	if !s.has(agent) {
		return nil, nil
	}
	st, err := s.get(agent)
	if err != nil {
		return nil, err
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, err
	}
	var matched []string
	for _, l := range labels {
		if l == selector || strings.HasPrefix(l, selector+"#") {
			matched = append(matched, l)
		}
	}
	bucketMs = metricstore.AutoBucketMs(sinceMs, bucketMs)
	lowerMs := time.Now().UnixMilli() - sinceMs
	var series [][]metricstore.Bucket
	for _, l := range matched {
		bs, err := st.Query(metricstore.QueryReq{Label: l, SinceMs: lowerMs, BucketMs: bucketMs})
		if err != nil {
			return nil, err
		}
		series = append(series, bs)
	}
	return metricstore.MergeBuckets(series), nil
}
```

`strings` is already imported in `stores.go`.

- [ ] **Step 4: Run the History test — verify it passes**

Run: `go test ./internal/server/ -run TestStoresHistory -v`
Expected: PASS.

- [ ] **Step 5: Refactor `FleetMetricsHistory` to call `History`**

Replace the body of `FleetMetricsHistory` in `internal/server/server.go` (currently lines ~180-224) with a thin wrapper that keeps the `NotFound` guard and proto mapping:

```go
// FleetMetricsHistory returns bucketed CPU/mem history for one agent's app/instance.
func (s *Server) FleetMetricsHistory(_ context.Context, req *pb.FleetMetricsHistoryRequest) (*pb.MetricsHistoryResponse, error) {
	if s.stores == nil || !s.stores.has(req.GetAgentName()) {
		return nil, status.Errorf(codes.NotFound, "no metric history for agent %q", req.GetAgentName())
	}
	sinceMs := req.GetSinceMs()
	if sinceMs <= 0 {
		sinceMs = defaultHistoryMs
	}
	buckets, err := s.stores.History(req.GetAgentName(), req.GetSelector(), sinceMs, req.GetBucketMs())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "history: %v", err)
	}
	resp := &pb.MetricsHistoryResponse{}
	for _, b := range buckets {
		resp.Buckets = append(resp.Buckets, &pb.MetricBucket{
			TsMs: b.TsMs, CpuAvg: b.CpuAvg, CpuMax: b.CpuMax, MemAvg: b.MemAvg, MemMax: b.MemMax,
		})
	}
	return resp, nil
}
```

Keep the `const defaultHistoryMs = int64(60 * 60 * 1000)` declaration. After removing the old per-label loop, run `go vet`/`gofmt`; if `strings`, `time`, or `metricstore` become unused **in server.go** (they may still be used elsewhere in the file — `storeBatch` uses `metricstore`, the prune goroutine uses `time`), only remove imports the compiler flags.

- [ ] **Step 6: Verify the existing proto test stays green + full server pkg + gate**

Run: `go test ./internal/server/ -run 'TestFleetMetricsHistory|TestStoresHistory' -v`
Expected: PASS (both).
Run: `go build ./... && gofmt -l internal/server/ && go vet ./internal/server/`
Expected: build ok, `gofmt` prints nothing, vet clean.

- [ ] **Step 7: Commit**

```bash
git add internal/server/stores.go internal/server/stores_test.go internal/server/server.go
git commit -m "refactor(server): extract metric history query to stores.History

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: dashboard `GET /api/metrics` endpoint

**Files:**
- Create: `internal/dashboard/metrics.go`
- Create: `internal/dashboard/metrics_test.go`
- Modify: `internal/dashboard/handlers.go` (handler struct, `newHandler`, `NewHandler`, route registration)
- Modify: `internal/dashboard/server.go` (`Serve` signature)
- Modify: `internal/server/server.go` (`ServeDir` → pass `ss` to `dashboard.Serve`)
- Modify: `internal/dashboard/server_test.go`, `internal/dashboard/dashboard_serve_test.go` test references? (only `NewHandler` call sites need a new arg — see Step 5)

**Interfaces:**
- Produces:
  - `type MetricsHistory interface { History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error) }` — satisfied by `*server.stores`.
  - `func NewHandler(lister FleetLister, metrics MetricsHistory, auth Authenticator, ttl time.Duration) http.Handler`.
  - `func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, auth Authenticator, cert tls.Certificate) error`.
  - JSON response of `GET /api/metrics`: `[]agentMetricsView` where
    `agentMetricsView{Agent string; Procs []procMetricsView}`,
    `procMetricsView{Name string; Buckets []bucketView}`,
    `bucketView{Ts int64 (json "ts"); CpuAvg,CpuMax float64 (json "cpu_avg","cpu_max"); MemAvg,MemMax uint64 (json "mem_avg","mem_max")}`.
- Consumes: `FleetLister.List()` (existing), `MetricsHistory.History` (Task 1).

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/metrics_test.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/metricstore"
	"marshal/internal/pb"
)

type fakeMetrics struct{ calls []string }

func (f *fakeMetrics) History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error) {
	f.calls = append(f.calls, agent+"/"+selector)
	return []metricstore.Bucket{{TsMs: 1000, CpuAvg: 1, CpuMax: 2, MemAvg: 10, MemMax: 20}}, nil
}

func loginCookie(t *testing.T, c *http.Client, base string) *http.Cookie {
	t.Helper()
	resp, _ := c.Post(base+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	cookie := sessionCookieFrom(resp)
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}
	return cookie
}

func TestMetricsRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/metrics")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie metrics = %d; want 401", resp.StatusCode)
	}
}

func TestMetricsBatched(t *testing.T) {
	lister := fakeLister{agents: []*pb.AgentState{{
		AgentName: "dev-1",
		Procs:     []*pb.ProcInfo{{Name: "ticker"}, {Name: "web"}},
	}}}
	fm := &fakeMetrics{}
	srv := httptest.NewServer(NewHandler(lister, fm, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/metrics", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics = %d; want 200", resp.StatusCode)
	}
	var got []agentMetricsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Agent != "dev-1" || len(got[0].Procs) != 2 {
		t.Fatalf("batched metrics = %+v", got)
	}
	if got[0].Procs[0].Name != "ticker" || len(got[0].Procs[0].Buckets) != 1 {
		t.Fatalf("proc metrics = %+v", got[0].Procs[0])
	}
	if got[0].Procs[0].Buckets[0].Ts != 1000 || got[0].Procs[0].Buckets[0].CpuMax != 2 {
		t.Fatalf("bucket = %+v", got[0].Procs[0].Buckets[0])
	}
}

func TestMetricsSingleSeries(t *testing.T) {
	lister := fakeLister{agents: []*pb.AgentState{{AgentName: "dev-1", Procs: []*pb.ProcInfo{{Name: "ticker"}}}}}
	fm := &fakeMetrics{}
	srv := httptest.NewServer(NewHandler(lister, fm, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/metrics?agent=dev-1&selector=ticker&since=60000&bucket=1000", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("single metrics = %d; want 200", resp.StatusCode)
	}
	var got []agentMetricsView
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != 1 || len(got[0].Procs) != 1 || got[0].Procs[0].Name != "ticker" {
		t.Fatalf("single-series metrics = %+v", got)
	}
	if len(fm.calls) != 1 || fm.calls[0] != "dev-1/ticker" {
		t.Fatalf("History calls = %v; want one dev-1/ticker", fm.calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestMetrics -v`
Expected: FAIL — `NewHandler` arg count wrong / `agentMetricsView` undefined.

- [ ] **Step 3: Create `internal/dashboard/metrics.go`**

```go
package dashboard

import (
	"net/http"
	"strconv"

	"marshal/internal/metricstore"
)

// MetricsHistory is the read side of stored CPU/mem history. *server.stores satisfies it.
type MetricsHistory interface {
	History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error)
}

type bucketView struct {
	Ts     int64   `json:"ts"`
	CpuAvg float64 `json:"cpu_avg"`
	CpuMax float64 `json:"cpu_max"`
	MemAvg uint64  `json:"mem_avg"`
	MemMax uint64  `json:"mem_max"`
}

type procMetricsView struct {
	Name    string       `json:"name"`
	Buckets []bucketView `json:"buckets"`
}

type agentMetricsView struct {
	Agent string            `json:"agent"`
	Procs []procMetricsView `json:"procs"`
}

const (
	defaultSparkSinceMs  = int64(5 * 60 * 1000)      // 5m, batched sparklines
	defaultDetailSinceMs = int64(60 * 60 * 1000)     // 1h, single-series detail
)

func bucketViews(bs []metricstore.Bucket) []bucketView {
	out := make([]bucketView, 0, len(bs))
	for _, b := range bs {
		out = append(out, bucketView{Ts: b.TsMs, CpuAvg: b.CpuAvg, CpuMax: b.CpuMax, MemAvg: b.MemAvg, MemMax: b.MemMax})
	}
	return out
}

// metrics serves GET /api/metrics. With agent+selector query params it returns a
// single proc's series (detail panel); otherwise it returns recent history for
// every agent/proc in the live fleet (sparklines).
func (h *handler) metrics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	agent := q.Get("agent")
	selector := q.Get("selector")

	if agent != "" && selector != "" {
		sinceMs := parseMs(q.Get("since"), defaultDetailSinceMs)
		bucketMs := parseMs(q.Get("bucket"), 0)
		bs, err := h.metricsHist.History(agent, selector, sinceMs, bucketMs)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, []agentMetricsView{{
			Agent: agent,
			Procs: []procMetricsView{{Name: selector, Buckets: bucketViews(bs)}},
		}})
		return
	}

	sinceMs := parseMs(q.Get("since"), defaultSparkSinceMs)
	agents := h.lister.List()
	out := make([]agentMetricsView, 0, len(agents))
	for _, a := range agents {
		procs := make([]procMetricsView, 0, len(a.GetProcs()))
		for _, p := range a.GetProcs() {
			bs, err := h.metricsHist.History(a.GetAgentName(), p.GetName(), sinceMs, 0)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			procs = append(procs, procMetricsView{Name: p.GetName(), Buckets: bucketViews(bs)})
		}
		out = append(out, agentMetricsView{Agent: a.GetAgentName(), Procs: procs})
	}
	writeJSON(w, http.StatusOK, out)
}

// parseMs parses a positive int64 ms value, returning def for empty or invalid input.
func parseMs(s string, def int64) int64 {
	if s == "" {
		return def
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return def
	}
	return v
}
```

- [ ] **Step 4: Thread `MetricsHistory` through the handler**

In `internal/dashboard/handlers.go`:

Add the field to the struct:

```go
type handler struct {
	lister      FleetLister
	metricsHist MetricsHistory
	auth        Authenticator
	sessions    *sessionStore
	files       fs.FS
	static      http.Handler
	mux         http.Handler
}
```

Update `newHandler` signature, field assignment, and route registration:

```go
func newHandler(lister FleetLister, metrics MetricsHistory, auth Authenticator, ttl time.Duration) *handler {
	files := staticFS()
	h := &handler{
		lister:      lister,
		metricsHist: metrics,
		auth:        auth,
		sessions:    newSessionStore(ttl, nil),
		files:       files,
		static:      http.FileServer(http.FS(files)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.logout)
	mux.HandleFunc("GET /api/session", h.requireSession(h.session))
	mux.HandleFunc("GET /api/fleet", h.requireSession(h.fleet))
	mux.HandleFunc("GET /api/metrics", h.requireSession(h.metrics))
	mux.HandleFunc("/", h.spa)
	h.mux = mux
	return h
}
```

Update `NewHandler`:

```go
func NewHandler(lister FleetLister, metrics MetricsHistory, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, metrics, auth, ttl).mux
}
```

- [ ] **Step 5: Update `Serve` + existing call sites/tests**

In `internal/dashboard/server.go`, update `Serve`:

```go
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, auth Authenticator, cert tls.Certificate) error {
	h := newHandler(lister, metrics, auth, 24*time.Hour)
	// ... rest unchanged
}
```

In `internal/server/server.go` `ServeDir`, pass `ss`:

```go
go func() {
	if err := dashboard.Serve(ctx, httpAddr, reg, ss, auth, cert); err != nil {
		log.Printf("dashboard: %v", err)
	}
}()
```

Update the three existing `NewHandler(...)` call sites in `internal/dashboard/server_test.go` (`TestLoginFleetLogout`, `TestSPAFallback`, `TestUnknownAPIRouteNotFound`) to pass a metrics arg. Add a shared nil-safe fake at the top of `server_test.go` is unnecessary — reuse `&fakeMetrics{}` from `metrics_test.go` (same package). Change each call:
- `NewHandler(lister, auth, time.Hour)` → `NewHandler(lister, &fakeMetrics{}, auth, time.Hour)`
- `NewHandler(fakeLister{}, fakeAuth{}, time.Hour)` → `NewHandler(fakeLister{}, &fakeMetrics{}, fakeAuth{}, time.Hour)` (both `TestSPAFallback` and `TestUnknownAPIRouteNotFound`).

Also update the `dashboard.Serve(...)` call inside `internal/server/dashboard_serve_test.go` if it calls `dashboard.Serve` directly. Check with:
Run: `grep -rn "dashboard.Serve\|NewHandler(" internal/`
and update every call site to the new arity (server integration test passes the real `*stores` or `newStores(t.TempDir())`).

- [ ] **Step 6: Run the dashboard + server tests**

Run: `go test ./internal/dashboard/ ./internal/server/ -v -run 'Metrics|Login|SPA|UnknownAPI|ServeDir|FleetMetricsHistory'`
Expected: PASS.
Run: `go build ./...`
Expected: ok (confirms `*server.stores` satisfies `dashboard.MetricsHistory` and all call sites compile).

- [ ] **Step 7: Full gate + commit**

Run: `go test ./... -race -count=1 && gofmt -l . && go vet ./...`
Expected: all pass; `gofmt` prints nothing.

```bash
git add internal/dashboard/metrics.go internal/dashboard/metrics_test.go \
  internal/dashboard/handlers.go internal/dashboard/server.go internal/dashboard/server_test.go \
  internal/server/server.go internal/server/dashboard_serve_test.go
git commit -m "feat(dashboard): GET /api/metrics history endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: Sparklines in the fleet table

**Files:**
- Create: `web/src/Sparkline.tsx`
- Modify: `web/src/api.ts`
- Modify: `web/src/Fleet.tsx`
- Rebuild: `internal/dashboard/dist/` (via `make ui`)

**Interfaces:**
- Produces (api.ts): `Bucket`, `ProcMetrics`, `AgentMetrics` types; `getMetrics(sinceMs: number): Promise<AgentMetrics[]>`; `getMetricsForProc(agent, selector, sinceMs, bucketMs): Promise<AgentMetrics[]>`.
- Produces (Sparkline.tsx): `Sparkline({ points, width?, height?, color? })`.
- Consumes: `GET /api/metrics` (Task 2).

> No JS unit tests (no harness in repo). Verify by `make ui` building cleanly and by the live demo in Task 5.

- [ ] **Step 1: Add metrics fetch helpers to `web/src/api.ts`**

Append:

```ts
export type Bucket = {
  ts: number;
  cpu_avg: number;
  cpu_max: number;
  mem_avg: number;
  mem_max: number;
};

export type ProcMetrics = { name: string; buckets: Bucket[] };
export type AgentMetrics = { agent: string; procs: ProcMetrics[] };

export async function getMetrics(sinceMs: number): Promise<AgentMetrics[]> {
  const r = await fetch(`/api/metrics?since=${sinceMs}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as AgentMetrics[];
}

export async function getMetricsForProc(
  agent: string,
  selector: string,
  sinceMs: number,
  bucketMs: number,
): Promise<AgentMetrics[]> {
  const q = new URLSearchParams({
    agent,
    selector,
    since: String(sinceMs),
    bucket: String(bucketMs),
  });
  const r = await fetch(`/api/metrics?${q.toString()}`);
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as AgentMetrics[];
}
```

- [ ] **Step 2: Create `web/src/Sparkline.tsx`**

```tsx
type SparklineProps = {
  points: number[];
  width?: number;
  height?: number;
  color?: string;
};

export function Sparkline({
  points,
  width = 80,
  height = 20,
  color = "#4ade80",
}: SparklineProps) {
  if (points.length === 0) {
    return <svg width={width} height={height} className="sparkline" aria-label="no data" />;
  }
  const min = Math.min(...points);
  const max = Math.max(...points);
  const span = max - min || 1;
  const stepX = points.length > 1 ? width / (points.length - 1) : 0;
  const coords = points.map((v, i) => {
    const x = i * stepX;
    const y = height - ((v - min) / span) * height;
    return `${x.toFixed(1)},${y.toFixed(1)}`;
  });
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      className="sparkline"
      preserveAspectRatio="none"
      role="img"
    >
      <polyline points={coords.join(" ")} fill="none" stroke={color} strokeWidth={1.5} />
    </svg>
  );
}
```

- [ ] **Step 3: Add CPU/Mem columns + sparklines to `web/src/Fleet.tsx`**

Add imports at the top:

```tsx
import { Agent, AgentMetrics, getFleet, getMetrics, logout } from "./api";
import { Sparkline } from "./Sparkline";
```

Add a memory formatter beside `uptime`:

```tsx
function mib(bytes: number): string {
  if (bytes <= 0) return "—";
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
```

Inside the `Fleet` component, add metrics state and a second poll loop (keep the existing 2s fleet `useEffect` unchanged):

```tsx
const [metrics, setMetrics] = useState<Record<string, Record<string, number[]>>>({});

useEffect(() => {
  let stop = false;
  async function tick() {
    try {
      const data: AgentMetrics[] = await getMetrics(5 * 60 * 1000);
      if (stop) return;
      const next: Record<string, Record<string, number[]>> = {};
      for (const a of data) {
        next[a.agent] = {};
        for (const p of a.procs) {
          next[a.agent][p.name] = p.buckets.map((b) => b.cpu_avg);
        }
      }
      setMetrics(next);
    } catch {
      // metrics are best-effort; the fleet poll owns auth/logout.
    }
  }
  tick();
  const id = setInterval(tick, 10000);
  return () => {
    stop = true;
    clearInterval(id);
  };
}, []);
```

> This task plots CPU in the sparkline; a mem sparkline column is added the same way driven by `b.mem_avg`. Store both series: change the inner map value to `{ cpu: number[]; mem: number[] }`. Use this shape:

```tsx
const [metrics, setMetrics] = useState<Record<string, Record<string, { cpu: number[]; mem: number[] }>>>({});
// in tick():
next[a.agent][p.name] = {
  cpu: p.buckets.map((b) => b.cpu_avg),
  mem: p.buckets.map((b) => b.mem_avg),
};
```

Extend the table header (add CPU and Mem columns):

```tsx
<tr>
  <th>Process</th>
  <th>State</th>
  <th>PID</th>
  <th>Uptime</th>
  <th>Restarts</th>
  <th>CPU</th>
  <th>Mem</th>
</tr>
```

Extend each process row (`a.procs.map`) to render the numbers plus sparklines:

```tsx
<tr key={`${p.name}-${p.pid}`}>
  <td>{p.name}</td>
  <td>{p.state}</td>
  <td>{p.pid || "—"}</td>
  <td>{uptime(p.uptime_ms)}</td>
  <td>{p.restarts}</td>
  <td>
    {(p.cpu * 100).toFixed(1)}%
    <Sparkline points={metrics[a.name]?.[p.name]?.cpu ?? []} color="#4ade80" />
  </td>
  <td>
    {mib(p.mem)}
    <Sparkline points={metrics[a.name]?.[p.name]?.mem ?? []} color="#60a5fa" />
  </td>
</tr>
```

Update the empty-row `colSpan={5}` to `colSpan={7}`.

- [ ] **Step 4: Build the SPA**

Run: `make ui`
Expected: `vite build` succeeds, writes hashed assets into `internal/dashboard/dist/`. If `tsc` reports a type error, fix it before proceeding.

- [ ] **Step 5: Confirm Go still embeds + builds**

Run: `go build -o marshal ./cmd/marshal && ./marshal --help >/dev/null && echo ok`
Expected: `ok`.

- [ ] **Step 6: Commit (including rebuilt dist)**

```bash
git add web/src/Sparkline.tsx web/src/api.ts web/src/Fleet.tsx internal/dashboard/dist/
git commit -m "feat(dashboard): inline CPU/mem sparklines in fleet table

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Expandable detail panel with time-window selector

**Files:**
- Create: `web/src/MetricChart.tsx`
- Modify: `web/src/Fleet.tsx`
- Rebuild: `internal/dashboard/dist/` (via `make ui`)

**Interfaces:**
- Produces (MetricChart.tsx): `MetricChart({ buckets, metric })` where `metric: "cpu" | "mem"`.
- Consumes: `getMetricsForProc` (Task 3), `Bucket` (Task 3).

- [ ] **Step 1: Create `web/src/MetricChart.tsx`**

```tsx
import { Bucket } from "./api";

type MetricChartProps = {
  buckets: Bucket[];
  metric: "cpu" | "mem";
};

const W = 480;
const H = 140;
const PAD = 28;

function fmt(metric: "cpu" | "mem", v: number): string {
  return metric === "cpu" ? `${(v * 100).toFixed(0)}%` : `${(v / (1024 * 1024)).toFixed(0)} MB`;
}

export function MetricChart({ buckets, metric }: MetricChartProps) {
  if (buckets.length === 0) {
    return <p className="chart-empty">No history yet.</p>;
  }
  const avg = buckets.map((b) => (metric === "cpu" ? b.cpu_avg : b.mem_avg));
  const max = buckets.map((b) => (metric === "cpu" ? b.cpu_max : b.mem_max));
  const lo = 0;
  const hi = Math.max(...max) || 1;
  const span = hi - lo || 1;
  const n = buckets.length;
  const x = (i: number) => PAD + (n > 1 ? (i * (W - 2 * PAD)) / (n - 1) : 0);
  const y = (v: number) => H - PAD - ((v - lo) / span) * (H - 2 * PAD);
  const line = (series: number[]) =>
    series.map((v, i) => `${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
  const color = metric === "cpu" ? "#4ade80" : "#60a5fa";

  return (
    <svg width={W} height={H} viewBox={`0 0 ${W} ${H}`} className="metric-chart" role="img">
      {/* Y gridlines + labels at lo and hi */}
      <line x1={PAD} y1={y(hi)} x2={W - PAD} y2={y(hi)} className="grid" />
      <line x1={PAD} y1={y(lo)} x2={W - PAD} y2={y(lo)} className="grid" />
      <text x={4} y={y(hi) + 4} className="axis">{fmt(metric, hi)}</text>
      <text x={4} y={y(lo) + 4} className="axis">{fmt(metric, lo)}</text>
      {/* max series (faint), then avg series */}
      <polyline points={line(max)} fill="none" stroke={color} strokeWidth={1} opacity={0.35} />
      <polyline points={line(avg)} fill="none" stroke={color} strokeWidth={1.75} />
    </svg>
  );
}
```

- [ ] **Step 2: Add expand state + window selector to `web/src/Fleet.tsx`**

Update imports:

```tsx
import {
  Agent,
  AgentMetrics,
  Bucket,
  getFleet,
  getMetrics,
  getMetricsForProc,
  logout,
} from "./api";
import { Sparkline } from "./Sparkline";
import { MetricChart } from "./MetricChart";
```

Add window options and detail state inside the component:

```tsx
const WINDOWS: { label: string; ms: number }[] = [
  { label: "5m", ms: 5 * 60 * 1000 },
  { label: "1h", ms: 60 * 60 * 1000 },
  { label: "6h", ms: 6 * 60 * 60 * 1000 },
  { label: "24h", ms: 24 * 60 * 60 * 1000 },
];

const [expanded, setExpanded] = useState<{ agent: string; proc: string } | null>(null);
const [windowMs, setWindowMs] = useState(WINDOWS[1].ms); // default 1h
const [detail, setDetail] = useState<Bucket[]>([]);
```

Add a detail poll loop that runs only while a panel is open (changing `expanded` or `windowMs` refetches immediately):

```tsx
useEffect(() => {
  if (!expanded) {
    setDetail([]);
    return;
  }
  let stop = false;
  async function tick() {
    try {
      const data = await getMetricsForProc(expanded!.agent, expanded!.proc, windowMs, 0);
      if (!stop) setDetail(data[0]?.procs[0]?.buckets ?? []);
    } catch {
      // best-effort; fleet poll owns auth.
    }
  }
  tick();
  const id = setInterval(tick, 10000);
  return () => {
    stop = true;
    clearInterval(id);
  };
}, [expanded, windowMs]);
```

Make each process row clickable and render the panel. Replace the row + add a following panel row. The `<tr>` gets an `onClick` that toggles `expanded` for that proc:

```tsx
{a.procs.map((p) => {
  const isOpen = expanded?.agent === a.name && expanded?.proc === p.name;
  return (
    <Fragment key={`${p.name}-${p.pid}`}>
      <tr
        className={isOpen ? "proc open" : "proc"}
        onClick={() => setExpanded(isOpen ? null : { agent: a.name, proc: p.name })}
      >
        <td>{p.name}</td>
        <td>{p.state}</td>
        <td>{p.pid || "—"}</td>
        <td>{uptime(p.uptime_ms)}</td>
        <td>{p.restarts}</td>
        <td>
          {(p.cpu * 100).toFixed(1)}%
          <Sparkline points={metrics[a.name]?.[p.name]?.cpu ?? []} color="#4ade80" />
        </td>
        <td>
          {mib(p.mem)}
          <Sparkline points={metrics[a.name]?.[p.name]?.mem ?? []} color="#60a5fa" />
        </td>
      </tr>
      {isOpen && (
        <tr className="detail">
          <td colSpan={7}>
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
          </td>
        </tr>
      )}
    </Fragment>
  );
})}
```

Add `Fragment` to the React import at the top of the file:

```tsx
import { Fragment, useEffect, useState } from "react";
```

- [ ] **Step 3: Add minimal styles**

Append to the existing global stylesheet (find it: `grep -rl "\.fleet" web/src`; it is `web/src/index.css` or `App.css` — add to whichever holds `.fleet` / `.badge` rules):

```css
.sparkline { display: block; margin-top: 2px; }
tr.proc { cursor: pointer; }
tr.proc.open { background: rgba(96, 165, 250, 0.12); }
tr.detail td { background: rgba(0, 0, 0, 0.15); }
.windows { display: flex; gap: 4px; margin: 6px 0; }
.windows button { padding: 2px 8px; cursor: pointer; }
.windows button.active { font-weight: 700; }
.charts { display: flex; gap: 24px; flex-wrap: wrap; }
.metric-chart .grid { stroke: rgba(255, 255, 255, 0.15); }
.metric-chart .axis { fill: rgba(255, 255, 255, 0.6); font-size: 10px; }
.chart-empty { color: rgba(255, 255, 255, 0.5); font-style: italic; }
```

If the existing theme is light, adjust the rgba colors to match (check the surrounding CSS first).

- [ ] **Step 4: Build the SPA**

Run: `make ui`
Expected: build succeeds. Fix any `tsc` type errors (commonly: `Fragment` import, `Bucket` import).

- [ ] **Step 5: Confirm Go build embeds the new dist**

Run: `go build -o marshal ./cmd/marshal && echo ok`
Expected: `ok`.

- [ ] **Step 6: Commit (including rebuilt dist)**

```bash
git add web/src/MetricChart.tsx web/src/Fleet.tsx web/src/*.css internal/dashboard/dist/
git commit -m "feat(dashboard): expandable metric detail panel with window selector

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Full gate, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m12-charts.md`

- [ ] **Step 1: Full Go gate**

Run: `go test ./... -race -count=1 && gofmt -l . && go vet ./...`
Expected: all packages ok, no races, `gofmt` silent, vet clean.

- [ ] **Step 2: Live demo (per CLAUDE.md convention)**

Use a scratch data dir and the M11 sequence (password set while server is **down**, then start, then enroll). Then:
- Open `https://localhost:<http-port>/`, log in.
- Enroll a demo agent running 1-2 busy processes (e.g. a CPU-spinning loop) so CPU/mem history accrues.
- Wait ~30-60s, confirm: inline CPU and mem sparklines populate in the table; clicking a process row expands the detail panel; the 5m/1h/6h/24h buttons re-fetch and redraw; both CPU and Mem charts render.
- Report what was observed.
- Tear down: stop processes + daemon + server, remove the scratch dir, confirm `pgrep -fl marshal` shows no orphans.

```bash
# Scratch dir + build
export XDG_DATA_HOME=/tmp/marshal-m12-demo
rm -rf "$XDG_DATA_HOME" && mkdir -p "$XDG_DATA_HOME"
go build -o marshal ./cmd/marshal
# 1) init tokens (server up briefly), capture enroll token + fingerprint, then stop
./marshal server --listen :9200 --http-listen :9201 &  # copy token + fingerprint from stdout
sleep 1; kill %1
# 2) set dashboard password while server is down
printf 'demo-pw\n' | ./marshal server passwd --user admin
# 3) start server (loads credentials from auth.json)
./marshal server --listen :9200 --http-listen :9201 &
# 4) enroll a demo agent with a busy process (see app.yaml from M11 handoff; use a CPU-spinning cmd)
./marshal start app.yaml
# ... observe in browser at https://localhost:9201/ ...
# teardown
./marshal delete all 2>/dev/null; kill %1 2>/dev/null
pgrep -fl marshal
rm -rf "$XDG_DATA_HOME"
```

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-18-m12-charts.md` covering: current state (branch `m12-charts`, gate status), what was built (the History refactor, `/api/metrics`, Sparkline/MetricChart, Fleet integration), build/run/test instructions, the live-demo result, deferred items (hover tooltips, zoom/pan, persisted expanded state — from the spec), and the concrete next step (final whole-branch review → merge via `finishing-a-development-branch`; then M13).

- [ ] **Step 4: Commit handoff**

```bash
git add docs/handoffs/2026-06-18-m12-charts.md
git commit -m "docs: M12 metric-charts handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review notes (coverage check)

- **Server history reuse** → Task 1 (`stores.History` + proto wrapper refactor; existing `TestFleetMetricsHistory` is the regression guard).
- **`GET /api/metrics` (batched + single-series, defaults, session guard)** → Task 2.
- **Wiring (`ss` into `dashboard.Serve`; `NewHandler`/`Serve` arity; updated existing tests)** → Task 2 Steps 4-5.
- **Sparklines (CPU+mem, 10s batched poll, hand-rolled SVG)** → Task 3.
- **Detail panel (expand, MetricChart, 5m/1h/6h/24h, 10s poll while open)** → Task 4.
- **Gate + live demo + handoff** → Task 5.
- **Deferred items** (tooltips, zoom/pan, unit formatting beyond MB, persisted expand) explicitly out of scope per the spec — no tasks, intentional.
