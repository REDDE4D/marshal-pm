# M14 — Dashboard Process Controls (Restart + Stop) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Restart and Stop buttons to the web dashboard that drive the already-existing fleet control path.

**Architecture:** Construct the `*server.Server` once in `ServeDir` and share it with both the gRPC `Serve` and the dashboard. The dashboard gains a one-method `FleetController` interface (satisfied by `*server.Server.Control`, a thin wrapper over the existing `FleetControl` RPC) and a session-guarded `POST /api/control` endpoint. The React UI adds per-row Restart/Stop buttons with an inline confirm step. No proto, agent, or manager changes.

**Tech Stack:** Go 1.26 (stdlib `net/http`, gRPC), React + TypeScript + Vite (built via `make ui`, embedded into the Go binary).

## Global Constraints

- Module path is `marshal`; imports are `marshal/internal/...`.
- Control acts at **app granularity**: the selector is the proc row's app name (`p.Name`); `manager.resolve` matches `"all"`, a numeric id, or an exact app name — never `name#idx`.
- The dashboard imports only leaf packages plus `marshal/internal/pb`; it reaches the server through structurally-satisfied interfaces (no import of `marshal/internal/server`).
- Frontend changes require `make ui` to regenerate the embedded `internal/dashboard/dist/`; `go build` then embeds it (no Node at build time).
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Feature work on branch `m14-controls`, not `main`.
- Gate before finishing: `go test ./... -race -count=1`, `gofmt -l .` (silent), `go vet ./...`, `make ui` clean, `go build -o marshal ./cmd/marshal`.

---

### Task 0: Create the feature branch

- [ ] **Step 1: Branch from main**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout main && git pull --ff-only 2>/dev/null; git checkout -b m14-controls
git branch --show-current   # expect: m14-controls
```

---

### Task 1: Server `Control` adapter + share `*Server` with `Serve`

**Files:**
- Modify: `internal/server/server.go` (add `Control` method; refactor `Serve` to take `*Server`; update `ServeDir` to build the server once)
- Test: `internal/server/control_test.go` (create)
- Modify (compile fix): `internal/server/server_test.go:64`, `internal/server/e2e_test.go:223`, `internal/server/e2e_test.go:395`, `internal/server/tls_serve_test.go:31`, `internal/server/tls_serve_test.go:56`

**Interfaces:**
- Produces: `func (s *Server) Control(ctx context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error)`
- Produces: `func Serve(ctx context.Context, lis net.Listener, srv *Server, cert tls.Certificate) error` (new signature)

- [ ] **Step 1: Write the failing test**

Create `internal/server/control_test.go`:

```go
package server

import (
	"context"
	"testing"

	"marshal/internal/pb"
)

func TestControlUnknownAgentErrors(t *testing.T) {
	srv := NewServer(NewRegistry(), nil, nil, nil)
	op := &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: "web"}}}
	if _, err := srv.Control(context.Background(), "ghost", op); err == nil {
		t.Fatal("Control on an unconnected agent = nil err; want error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestControlUnknownAgentErrors`
Expected: FAIL — `srv.Control undefined`.

- [ ] **Step 3: Add the `Control` method**

In `internal/server/server.go`, add directly below `FleetControl`:

```go
// Control routes one control op to a connected agent and returns the agent's
// result. It is the write-side adapter the dashboard depends on. A nil error
// means the op reached the agent; the *ControlResult carries the agent's Ok/Error.
func (s *Server) Control(ctx context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error) {
	resp, err := s.FleetControl(ctx, &pb.FleetControlRequest{AgentName: agent, Op: op})
	if err != nil {
		return nil, err
	}
	return resp.GetResult(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestControlUnknownAgentErrors`
Expected: PASS.

- [ ] **Step 5: Refactor `Serve` to take a pre-built `*Server`**

In `internal/server/server.go`, replace the whole `Serve` function with:

```go
// Serve registers the Fleet service backed by srv on lis (TLS) and serves until
// ctx is canceled. srv.auth must not be nil: unary and stream interceptors
// enforce admin/enroll tokens. srv's stores (if any) are closed on shutdown.
func Serve(ctx context.Context, lis net.Listener, srv *Server, cert tls.Certificate) error {
	if srv.auth == nil {
		return errors.New("server: Serve requires a non-nil AuthStore")
	}
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	gs := grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(srv.auth.unaryAuth),
		grpc.StreamInterceptor(srv.auth.streamAuth),
	)
	pb.RegisterFleetServer(gs, srv)
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
		if srv.stores != nil {
			_ = srv.stores.closeAll()
		}
		if srv.logs != nil {
			_ = srv.logs.closeAll()
		}
	}()
	return gs.Serve(lis)
}
```

- [ ] **Step 6: Update `ServeDir` to build the server once**

In `internal/server/server.go`, in `ServeDir`, replace the tail (from `reg := NewRegistry(opts...)` to the final `return Serve(...)`) with:

```go
	reg := NewRegistry(opts...)
	srv := NewServer(reg, ss, ls, auth)
	if httpAddr != "" {
		if !auth.HasDashboardUser() {
			log.Printf("dashboard: no user set — run 'marshal server passwd'")
		}
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, auth, cert); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
		log.Printf("dashboard: serving on %s", httpAddr)
	}
	return Serve(ctx, lis, srv, cert)
```

(The `dashboard.Serve` call still uses its old signature here — Task 2 adds the `srv` argument. This keeps Task 1 self-contained and compilable.)

- [ ] **Step 7: Fix the five `Serve` test call sites**

Each currently calls `Serve(ctx, lis, <reg>, nil, nil, cert, <auth>)`. Replace with a two-line form that builds the server first. Apply at each site:

`internal/server/server_test.go:64`
```go
	srv := NewServer(reg, nil, nil, auth)
	go func() { _ = Serve(ctx, lis, srv, cert) }()
```

`internal/server/e2e_test.go:223`
```go
	srv := NewServer(reg, nil, nil, auth)
	go func() { _ = Serve(ctx, lis, srv, cert) }()
```

`internal/server/e2e_test.go:395`
```go
	srv := NewServer(NewRegistry(), nil, nil, auth)
	go func() { _ = Serve(ctx, lis, srv, cert) }()
```

`internal/server/tls_serve_test.go:31` (this one asserts the nil-auth error path)
```go
	srv := NewServer(NewRegistry(), nil, nil, nil)
	serveErr := Serve(ctx, lis, srv, cert)
```

`internal/server/tls_serve_test.go:56`
```go
	srv := NewServer(NewRegistry(), nil, nil, auth)
	go Serve(ctx, lis, srv, cert)
```

- [ ] **Step 8: Run the full server package tests**

Run: `go test ./internal/server/ -race -count=1`
Expected: PASS (all existing tests + the new `Control` test).

- [ ] **Step 9: Verify the whole tree still builds**

Run: `go build ./... && gofmt -l internal/server/`
Expected: build succeeds; `gofmt` prints nothing.

- [ ] **Step 10: Commit**

```bash
git add internal/server/server.go internal/server/control_test.go \
        internal/server/server_test.go internal/server/e2e_test.go internal/server/tls_serve_test.go
git commit -m "feat(server): Control adapter; Serve takes a shared *Server

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Dashboard `FleetController` + `POST /api/control`

**Files:**
- Create: `internal/dashboard/control.go`
- Create: `internal/dashboard/control_test.go`
- Modify: `internal/dashboard/handlers.go` (handler struct field, `newHandler`/`NewHandler` signatures, route)
- Modify: `internal/dashboard/server.go` (`Serve` signature + `newHandler` call)
- Modify: `internal/server/server.go` (pass `srv` into `dashboard.Serve`)
- Modify (compile fix — insert the controller arg): all `NewHandler(...)` call sites in `internal/dashboard/metrics_test.go`, `internal/dashboard/logs_test.go`, `internal/dashboard/server_test.go`

**Interfaces:**
- Consumes: `*server.Server.Control` (from Task 1) — structurally satisfies `FleetController`.
- Produces: `FleetController` interface; `POST /api/control`.
- Produces: new `newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration)` and matching `NewHandler` / `dashboard.Serve` signatures (controller inserted **after** `logs`).

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/control_test.go`:

```go
package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/pb"
)

type fakeController struct {
	gotAgent string
	gotOp    *pb.ControlOp
	res      *pb.ControlResult
	err      error
}

func (f *fakeController) Control(_ context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error) {
	f.gotAgent = agent
	f.gotOp = op
	return f.res, f.err
}

func postControl(t *testing.T, c *http.Client, base string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/control", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestControlRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp := postControl(t, srv.Client(), srv.URL, nil, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie control = %d; want 401", resp.StatusCode)
	}
}

func TestControlRestartHappyPath(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"restart"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restart = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != true {
		t.Fatalf("restart body = %+v; want ok:true", got)
	}
	if fc.gotAgent != "dev-1" || fc.gotOp.GetRestart().GetTarget() != "web" {
		t.Fatalf("forwarded agent=%q op=%+v; want dev-1/restart web", fc.gotAgent, fc.gotOp)
	}
}

func TestControlStopForwardsStopOp(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d; want 200", resp.StatusCode)
	}
	if fc.gotOp.GetStop().GetTarget() != "web" {
		t.Fatalf("forwarded op=%+v; want stop web", fc.gotOp)
	}
}

func TestControlBadAction(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"delete"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad action = %d; want 400", resp.StatusCode)
	}
}

func TestControlMissingFields(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","action":"stop"}`) // no selector
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing selector = %d; want 400", resp.StatusCode)
	}
}

func TestControlTransportErrorIs502(t *testing.T) {
	fc := &fakeController{err: errors.New("agent \"dev-1\" not connected")}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("transport error = %d; want 502", resp.StatusCode)
	}
}

func TestControlAgentErrorPassthrough(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: false, Error: "no app matching \"web\""}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent-error = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != false || got["error"] != "no app matching \"web\"" {
		t.Fatalf("agent-error body = %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestControl`
Expected: FAIL to compile — `NewHandler` arg count mismatch / `h.control` undefined.

- [ ] **Step 3: Create the control handler**

Create `internal/dashboard/control.go`:

```go
package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"marshal/internal/pb"
)

// FleetController is the write side of the fleet. *server.Server satisfies it.
type FleetController interface {
	Control(ctx context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error)
}

const controlTimeout = 10 * time.Second

type controlRequest struct {
	Agent    string `json:"agent"`
	Selector string `json:"selector"`
	Action   string `json:"action"`
}

// control serves POST /api/control: routes a Restart/Stop to one agent's app.
// 400 on bad input; 502 when the op never reached the agent; 200 with
// {"ok":bool,"error"?} when the agent executed (or rejected) it.
func (h *handler) control(w http.ResponseWriter, r *http.Request) {
	var body controlRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" || body.Selector == "" {
		http.Error(w, "agent and selector required", http.StatusBadRequest)
		return
	}
	op := controlOp(body.Action, body.Selector)
	if op == nil {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, body.Agent, op)
	user, _ := r.Context().Value(userKey).(string)
	if err != nil {
		log.Printf("dashboard: control %s %s/%s by %s: %v", body.Action, body.Agent, body.Selector, user, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("dashboard: control %s %s/%s by %s: ok=%v", body.Action, body.Agent, body.Selector, user, res.GetOk())
	if !res.GetOk() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": res.GetError()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// controlOp builds the ControlOp for an action, or nil if the action is unknown.
func controlOp(action, selector string) *pb.ControlOp {
	switch action {
	case "restart":
		return &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: selector}}}
	case "stop":
		return &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: selector}}}
	default:
		return nil
	}
}
```

- [ ] **Step 4: Wire the field, signatures, and route in `handlers.go`**

In `internal/dashboard/handlers.go`:

Add the field to the `handler` struct (after `logsHist`):
```go
	logsHist    LogsHistory
	controller  FleetController
```

Change `newHandler` to accept and store the controller:
```go
func newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration) *handler {
	files := staticFS()
	h := &handler{
		lister:      lister,
		metricsHist: metrics,
		logsHist:    logs,
		controller:  controller,
		auth:        auth,
		sessions:    newSessionStore(ttl, nil),
		files:       files,
		static:      http.FileServer(http.FS(files)),
	}
```

Add the route (after the `/api/logs` line):
```go
	mux.HandleFunc("POST /api/control", h.requireSession(h.control))
```

Change `NewHandler`:
```go
func NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, metrics, logs, controller, auth, ttl).mux
}
```

- [ ] **Step 5: Update `dashboard.Serve` signature**

In `internal/dashboard/server.go`, change `Serve` to thread the controller:
```go
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour)
```
(Leave the rest of the function unchanged.)

- [ ] **Step 6: Pass `srv` from `ServeDir`**

In `internal/server/server.go`, in `ServeDir`, update the dashboard call to pass `srv`:
```go
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, srv, auth, cert); err != nil {
```

- [ ] **Step 7: Fix every existing `NewHandler` call site**

Insert the controller argument (a `nil` FleetController — these tests don't exercise control) **after the `logs` argument** in each call. The sites are:

- `internal/dashboard/metrics_test.go`: lines 33, 47, 76
- `internal/dashboard/logs_test.go`: lines 24, 34, 65, 82
- `internal/dashboard/server_test.go`: lines 31, 90, 107

Pattern — change `NewHandler(<lister>, <metrics>, <logs>, <auth>, time.Hour)` to `NewHandler(<lister>, <metrics>, <logs>, nil, <auth>, time.Hour)`. Example for `logs_test.go:24`:
```go
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
```

- [ ] **Step 8: Run the dashboard tests**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS (existing tests + all `TestControl*`).

- [ ] **Step 9: Build the whole tree + lint**

Run: `go build ./... && go vet ./... && gofmt -l internal/`
Expected: build + vet clean; `gofmt` prints nothing.

- [ ] **Step 10: Commit**

```bash
git add internal/dashboard/control.go internal/dashboard/control_test.go \
        internal/dashboard/handlers.go internal/dashboard/server.go \
        internal/dashboard/metrics_test.go internal/dashboard/logs_test.go internal/dashboard/server_test.go \
        internal/server/server.go
git commit -m "feat(dashboard): POST /api/control endpoint (restart/stop)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Frontend — Restart/Stop buttons with inline confirm

**Files:**
- Modify: `web/src/api.ts` (add `control` helper + `ControlResult` type)
- Modify: `web/src/Fleet.tsx` (Actions column, `ProcActions` component, colSpan fix)
- Modify: `web/src/styles.css` (button/confirm/status styling)
- Regenerate: `internal/dashboard/dist/` (via `make ui`)

**Interfaces:**
- Consumes: `POST /api/control` from Task 2.
- Produces: `control(agent, selector, action): Promise<ControlResult>`; a `ProcActions` row component.

- [ ] **Step 1: Add the `control` API helper**

Append to `web/src/api.ts`:

```ts
export type ControlResult = { ok: boolean; error?: string };

// control posts a Restart/Stop and surfaces server errors as values — it never
// throws, so a failed control call cannot trigger a logout (only the fleet poll
// owns auth). 200 -> the agent's result; 400/502 -> {ok:false,error}.
export async function control(
  agent: string,
  selector: string,
  action: "restart" | "stop",
): Promise<ControlResult> {
  const r = await fetch("/api/control", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, selector, action }),
  });
  if (r.status === 200) return (await r.json()) as ControlResult;
  try {
    const j = await r.json();
    return { ok: false, error: (j.error as string) ?? `error ${r.status}` };
  } catch {
    return { ok: false, error: `error ${r.status}` };
  }
}
```

- [ ] **Step 2: Add the `ProcActions` component to `Fleet.tsx`**

In `web/src/Fleet.tsx`, update the import to include `control`:
```ts
import {
  Agent,
  AgentMetrics,
  Bucket,
  LogLine,
  control,
  getFleet,
  getLogs,
  getMetrics,
  getMetricsForProc,
  logout,
} from "./api";
```

Add this component near the top of the file, just below the `mib` helper:
```tsx
function ProcActions({ agent, proc, disabled }: { agent: string; proc: string; disabled: boolean }) {
  const [pending, setPending] = useState<"restart" | "stop" | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState("");

  function ask(action: "restart" | "stop") {
    setMsg("");
    setPending(action);
    window.setTimeout(() => setPending((p) => (p === action ? null : p)), 3000);
  }

  async function fire(action: "restart" | "stop") {
    setPending(null);
    setBusy(true);
    setMsg("");
    const res = await control(agent, proc, action);
    setBusy(false);
    setMsg(res.ok ? "✓" : res.error || "error");
    window.setTimeout(() => setMsg(""), 4000);
  }

  if (disabled) return <span className="muted">—</span>;
  if (busy) return <span className="muted">…</span>;

  return (
    <span className="actions" onClick={(e) => e.stopPropagation()}>
      {pending ? (
        <>
          <button className="confirm" onClick={() => fire(pending)}>
            Confirm {pending}?
          </button>
          <button onClick={() => setPending(null)}>✕</button>
        </>
      ) : (
        <>
          <button onClick={() => ask("restart")}>Restart</button>
          <button onClick={() => ask("stop")}>Stop</button>
        </>
      )}
      {msg && <span className="action-msg">{msg}</span>}
    </span>
  );
}
```

- [ ] **Step 3: Add the Actions column header**

In `web/src/Fleet.tsx`, in the `<thead>` row, add a final header after `<th>Mem</th>`:
```tsx
                <th>Mem</th>
                <th>Actions</th>
```

- [ ] **Step 4: Add the Actions cell to each proc row**

In the proc `<tr>`, after the Mem `<td>` (the one containing the mem Sparkline), add:
```tsx
                      <td>
                        <ProcActions agent={a.name} proc={p.name} disabled={!a.connected} />
                      </td>
```

- [ ] **Step 5: Fix the detail + empty colSpans (7 → 8)**

In `web/src/Fleet.tsx`, change the detail row `<td colSpan={7}>` to `<td colSpan={8}>`, and the "No processes." `<td colSpan={7} className="empty">` to `<td colSpan={8} className="empty">`.

- [ ] **Step 6: Add styles**

Append to `web/src/styles.css`:
```css
.actions {
  display: inline-flex;
  gap: 0.3rem;
  align-items: center;
}
.actions button {
  padding: 0.15rem 0.5rem;
  font-size: 0.8rem;
}
.actions button.confirm {
  background: #b91c1c;
  color: #fff;
}
.action-msg {
  font-size: 0.8rem;
  color: #9ca3af;
}
.muted {
  color: #6b7280;
}
```

- [ ] **Step 7: Build the SPA and the binary**

Run:
```bash
make ui
go build -o marshal ./cmd/marshal
```
Expected: `make ui` writes a fresh `internal/dashboard/dist/assets/index-*.js`; `go build` succeeds. Confirm the working tree shows the regenerated dist:
```bash
git status --short internal/dashboard/dist
```
Expected: the hashed asset(s) under `internal/dashboard/dist/assets/` show as modified.

- [ ] **Step 8: Quick type/lint check**

Run: `gofmt -l . && go vet ./...`
Expected: silent / clean.

- [ ] **Step 9: Commit**

```bash
git add web/src/api.ts web/src/Fleet.tsx web/src/styles.css internal/dashboard/dist
git commit -m "feat(dashboard): restart/stop buttons with inline confirm

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Full gate, live demo, and handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m14-process-controls.md`

- [ ] **Step 1: Run the full gate**

Run:
```bash
go test ./... -race -count=1
gofmt -l .
go vet ./...
go build -o marshal ./cmd/marshal
```
Expected: all tests PASS; `gofmt` prints nothing; vet clean; binary builds.

- [ ] **Step 2: Live demo (per project convention)**

Use a scratch data dir so real state is untouched. Follow the M11–M13 demo flow:

```bash
export XDG_DATA_HOME=/tmp/marshal-m14-demo
rm -rf "$XDG_DATA_HOME"; mkdir -p "$XDG_DATA_HOME"
# 1) server DOWN: set dashboard password, rotate a fresh enroll token, capture fingerprint
./marshal server passwd            # set a known password
./marshal server token --rotate    # capture the enroll token
# 2) start the server with the dashboard
./marshal server --listen :9300 --http-listen :9301 &   # capture fingerprint from logs
# 3) enroll an agent running a couple of demo apps (e.g. a chatty stdout/stderr loop)
#    via the daemon + an enroll, as in the M13 handoff demo
```

Then, against `https://localhost:9301`:
1. `POST /api/control` without a cookie → **401**.
2. Log in; **Restart** a running app → **200 {"ok":true}**; confirm the proc's PID/uptime/restart count changes on the next poll.
3. **Stop** the app → state goes to `stopped`/`errored` on the next poll.
4. Stop on a bogus selector (or use the inline path) → agent error surfaces inline, **200 {"ok":false}**.
5. With the agent disconnected, buttons render disabled (`—`).
6. Inline confirm: click Restart, see `Confirm restart?`, wait ~3s, confirm it reverts.

- [ ] **Step 3: Tear down and check for orphans**

```bash
# stop the scratch daemon + server, remove the scratch dir
kill %1 2>/dev/null
rm -rf /tmp/marshal-m14-demo
pgrep -fl marshal      # expect only the user's own pre-existing daemon, if any — no scratch procs
```

- [ ] **Step 4: Write the handoff**

Create `docs/handoffs/2026-06-18-m14-process-controls.md` covering: current state (branch `m14-controls`, what's built per task), key decisions (app-granular selector, Approach A shared `*Server`, best-effort control), build/run/test, the live-demo result, deferred items (per-instance targeting, Start/Delete, audit store, optimistic UI), and the concrete next step (merge `m14-controls` via `finishing-a-development-branch`).

- [ ] **Step 5: Commit the handoff**

```bash
git add docs/handoffs/2026-06-18-m14-process-controls.md
git commit -m "docs: M14 process-controls handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-review notes

- **Spec coverage:** Approach-A wiring → Task 1; `Control` adapter → Task 1; `POST /api/control` contract (400/502/200-ok/200-ok-false) + audit log → Task 2; `FleetController` interface → Task 2; inline-confirm buttons + best-effort + disconnected-disable → Task 3; full gate + live demo + out-of-scope handoff → Task 4. All spec sections map to a task.
- **Selector granularity:** every layer uses the app name (`p.Name` / `Selector.Target`), consistent with `manager.resolve`.
- **Signature consistency:** the controller arg is inserted **after `logs`** uniformly across `newHandler`, `NewHandler`, and `dashboard.Serve`; `Control` has the same signature in the produced interface (Task 2) and the implementation (Task 1).
