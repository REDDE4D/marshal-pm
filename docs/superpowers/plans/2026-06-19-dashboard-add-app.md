# Dashboard "Add an App" Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a dashboard user create and launch a new app on a connected agent via a modal form, routed through the existing `ControlOp_Start` chain.

**Architecture:** A new session-guarded `POST /api/apps` endpoint decodes a `{agent, source}` body, switches on `source.type` (only `"command"` today), maps the command source to a `pb.AppSpec`, builds `ControlOp_Start`, and forwards it via the existing `FleetController.Control`. No proto, agent, manager, or supervisor changes — that chain is already wired and persists the app. The frontend adds an `addApp` API call and an `AddAppModal` opened from the overview header.

**Tech Stack:** Go (net/http, protobuf `internal/pb`), React 18 + TypeScript + Vite (`web/`), Signal CSS tokens in `web/src/styles.css`.

## Global Constraints

- Backend: TDD — failing Go test first, then implementation. `go test ./... -race -count=1` green; `gofmt -l .` silent; `go vet ./...` clean.
- Frontend: no web test runner exists and none is added this milestone. Correctness = TypeScript type-check via `make ui` (`tsc -b && vite build`) + the live in-browser demo. Use existing Signal CSS tokens (`--cyan`, `--panel`, `--border`, `--r`, etc.); do NOT introduce a new stylesheet or plain styles.
- `addApp` (like `control`) must never throw — server errors surface as `{ok:false,error}` values, so a failed add cannot trigger a logout.
- Forward-compat: the request body carries `source.type`; only `"command"` is accepted now. Any other type → `400 "unsupported source type"`. No git logic is built.
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. Work stays on branch `m20-dashboard-add-app`.
- The full AppSpec field set (for reference): `name, cmd, args, cwd, instances, env, restart, max_restarts, kill_timeout, logs`. The form covers all except `logs` (deferred).

---

### Task 1: Backend — `POST /api/apps` handler

**Files:**
- Create: `internal/dashboard/apps.go`
- Create: `internal/dashboard/apps_test.go`
- Modify: `internal/dashboard/handlers.go:71` (register route after the `/api/control` line)

**Interfaces:**
- Consumes: `FleetController.Control(ctx, agent string, op *pb.ControlOp) (*pb.ControlResult, error)` (defined `internal/dashboard/control.go:14`); `controlTimeout` (`control.go:18`); `writeJSON`, `userKey` (`handlers.go`); test helpers `fakeController`, `loginCookie`, `fakeLister`, `fakeAuth{}`, `fakeMetrics{}`, `fakeLogs{}` (existing package test files).
- Produces: `func (h *handler) apps(w http.ResponseWriter, r *http.Request)`; `func startOp(s commandSource) *pb.ControlOp`; types `addAppRequest`, `commandSource`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dashboard/apps_test.go`:

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

func postApps(t *testing.T, c *http.Client, base string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/apps", strings.NewReader(body))
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

func TestAddAppRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp := postApps(t, srv.Client(), srv.URL, nil, `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/bin/true"}}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie add = %d; want 401", resp.StatusCode)
	}
}

func TestAddAppHappyPath(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	body := `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/usr/bin/node","args":["server.js"],"cwd":"/srv","instances":2,"env":{"PORT":"3000"},"restart":"on-failure","max_restarts":5,"kill_timeout":"7s"}}`
	resp := postApps(t, c, srv.URL, cookie, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != true {
		t.Fatalf("add body = %+v; want ok:true", got)
	}
	if fc.gotAgent != "dev-1" {
		t.Fatalf("agent = %q; want dev-1", fc.gotAgent)
	}
	apps := fc.gotOp.GetStart().GetApps()
	if len(apps) != 1 {
		t.Fatalf("apps = %d; want 1", len(apps))
	}
	a := apps[0]
	if a.GetName() != "web" || a.GetCmd() != "/usr/bin/node" || a.GetCwd() != "/srv" ||
		a.GetInstances() != 2 || a.GetRestart() != "on-failure" || a.GetMaxRestarts() != 5 ||
		a.GetKillTimeout() != "7s" || len(a.GetArgs()) != 1 || a.GetArgs()[0] != "server.js" ||
		a.GetEnv()["PORT"] != "3000" {
		t.Fatalf("spec = %+v", a)
	}
}

func TestAddAppUnsupportedSource(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"git","name":"web","cmd":"x"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("git source = %d; want 400", resp.StatusCode)
	}
}

func TestAddAppMissingFields(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	// missing cmd
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"command","name":"web"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing cmd = %d; want 400", resp.StatusCode)
	}
	// missing agent
	resp = postApps(t, c, srv.URL, cookie, `{"source":{"type":"command","name":"web","cmd":"x"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing agent = %d; want 400", resp.StatusCode)
	}
}

func TestAddAppValidationErrorPassthrough(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: false, Error: "app \"web\" already exists"}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/bin/true"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dup-name = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != false || got["error"] != "app \"web\" already exists" {
		t.Fatalf("dup-name body = %+v", got)
	}
}

func TestAddAppTransportErrorIs502(t *testing.T) {
	fc := &fakeController{err: errors.New("agent \"dev-1\" not connected")}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/bin/true"}}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("transport error = %d; want 502", resp.StatusCode)
	}
}

var _ = context.Background
```

(The `var _ = context.Background` line is a placeholder so the `context` import compiles before the handler exists; delete it in Step 3 once `apps.go` uses nothing from the test that needs it. If `context` ends up unused in the test after Step 3, remove the import instead.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd "/Users/sebastiankuprat/process manager" && go test ./internal/dashboard/ -run TestAddApp -v`
Expected: FAIL — compile error `h.apps undefined` / route not registered (404 → status mismatches).

- [ ] **Step 3: Implement the handler**

Create `internal/dashboard/apps.go`:

```go
package dashboard

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"marshal/internal/pb"
)

// commandSource is the "command" variant of an app-creation source. It maps
// 1:1 to pb.AppSpec. Only name and cmd are required; the rest fall through to
// backend defaults (instances→1, restart→always, max_restarts→16, kill_timeout→5s)
// applied by config.Prepare on the agent.
type commandSource struct {
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Cmd         string            `json:"cmd"`
	Args        []string          `json:"args"`
	Cwd         string            `json:"cwd"`
	Instances   int32             `json:"instances"`
	Env         map[string]string `json:"env"`
	Restart     string            `json:"restart"`
	MaxRestarts int32             `json:"max_restarts"`
	KillTimeout string            `json:"kill_timeout"`
}

type addAppRequest struct {
	Agent  string        `json:"agent"`
	Source commandSource `json:"source"`
}

// apps serves POST /api/apps: creates and launches a new app on one agent via
// ControlOp_Start. 400 on bad input / unsupported source / validation error;
// 502 when the op never reached the agent; 200 {"ok":bool,"error"?} when the
// agent executed (or rejected) it. The authoritative validation (restart mode,
// kill_timeout parse, instances >= 0, duplicate name) happens in the agent's
// start chain and is surfaced verbatim.
func (h *handler) apps(w http.ResponseWriter, r *http.Request) {
	var body addAppRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" {
		http.Error(w, "agent required", http.StatusBadRequest)
		return
	}
	if body.Source.Type != "command" {
		http.Error(w, "unsupported source type", http.StatusBadRequest)
		return
	}
	if body.Source.Name == "" || body.Source.Cmd == "" {
		http.Error(w, "name and cmd required", http.StatusBadRequest)
		return
	}
	op := startOp(body.Source)
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	res, err := h.controller.Control(ctx, body.Agent, op)
	user, _ := r.Context().Value(userKey).(string)
	if err != nil {
		log.Printf("dashboard: add app %s -> %s by %s: %v", body.Source.Name, body.Agent, user, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("dashboard: add app %s -> %s by %s: ok=%v", body.Source.Name, body.Agent, user, res.GetOk())
	if !res.GetOk() {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": res.GetError()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// startOp builds a ControlOp_Start carrying one AppSpec from a command source.
func startOp(s commandSource) *pb.ControlOp {
	spec := &pb.AppSpec{
		Name:        s.Name,
		Cmd:         s.Cmd,
		Args:        s.Args,
		Cwd:         s.Cwd,
		Instances:   s.Instances,
		Env:         s.Env,
		Restart:     s.Restart,
		MaxRestarts: s.MaxRestarts,
		KillTimeout: s.KillTimeout,
	}
	return &pb.ControlOp{Op: &pb.ControlOp_Start{Start: &pb.StartRequest{Apps: []*pb.AppSpec{spec}}}}
}
```

Register the route — in `internal/dashboard/handlers.go`, immediately after the `POST /api/control` line (`handlers.go:71`), add:

```go
	mux.HandleFunc("POST /api/apps", h.requireSession(h.apps))
```

Then in `apps_test.go` remove the `var _ = context.Background` placeholder line; if `context` is now unused in the test, drop it from the import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd "/Users/sebastiankuprat/process manager" && go test ./internal/dashboard/ -run TestAddApp -v && gofmt -l internal/dashboard/apps.go internal/dashboard/apps_test.go && go vet ./internal/dashboard/`
Expected: all `TestAddApp*` PASS; `gofmt` prints nothing; vet clean.

- [ ] **Step 5: Commit**

```bash
cd "/Users/sebastiankuprat/process manager"
git add internal/dashboard/apps.go internal/dashboard/apps_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): add POST /api/apps to create an app via ControlOp_Start

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Frontend — `addApp` API client

**Files:**
- Modify: `web/src/api.ts` (append after the `control` function, end of file)

**Interfaces:**
- Consumes: existing `ControlResult` type (`api.ts:115`).
- Produces: `type CommandSource`; `async function addApp(agent: string, source: CommandSource): Promise<ControlResult>`.

- [ ] **Step 1: Add the type and function**

Append to `web/src/api.ts`:

```ts
// CommandSource mirrors the backend "command" app source (maps 1:1 to AppSpec).
// Only name and cmd are required; omitted fields use backend defaults.
export type CommandSource = {
  type: "command";
  name: string;
  cmd: string;
  args?: string[];
  cwd?: string;
  instances?: number;
  env?: Record<string, string>;
  restart?: string;
  max_restarts?: number;
  kill_timeout?: string;
};

// addApp creates a new app on an agent via POST /api/apps. Like control() it
// never throws — server errors surface as {ok:false,error}, so a failed add
// cannot trigger a logout (only the fleet poll owns auth).
export async function addApp(agent: string, source: CommandSource): Promise<ControlResult> {
  const r = await fetch("/api/apps", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, source }),
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

- [ ] **Step 2: Type-check**

Run: `cd "/Users/sebastiankuprat/process manager/web" && npx tsc -b`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
cd "/Users/sebastiankuprat/process manager"
git add web/src/api.ts
git commit -m "feat(web): add addApp API client + CommandSource type

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Frontend — `AddAppModal` component

**Files:**
- Create: `web/src/AddAppModal.tsx`

**Interfaces:**
- Consumes: `Agent` type, `CommandSource` type, `addApp` (from `./api`).
- Produces: `export function AddAppModal({ agents, onClose, onAdded }: { agents: Agent[]; onClose: () => void; onAdded: () => void }): JSX.Element`. Calls `onAdded()` then `onClose()` on success; keeps the modal open and shows an error banner on failure.

- [ ] **Step 1: Create the component**

Create `web/src/AddAppModal.tsx`:

```tsx
import { useState, type FormEvent } from "react";
import { Agent, CommandSource, addApp } from "./api";

type EnvRow = { key: string; value: string };

export function AddAppModal({
  agents,
  onClose,
  onAdded,
}: {
  agents: Agent[];
  onClose: () => void;
  onAdded: () => void;
}) {
  const connected = agents.filter((a) => a.connected);
  const [agent, setAgent] = useState(connected.length === 1 ? connected[0].name : "");
  const [name, setName] = useState("");
  const [cmd, setCmd] = useState("");
  const [args, setArgs] = useState("");
  const [cwd, setCwd] = useState("");
  const [instances, setInstances] = useState("");
  const [showAdv, setShowAdv] = useState(false);
  const [env, setEnv] = useState<EnvRow[]>([]);
  const [restart, setRestart] = useState("always");
  const [maxRestarts, setMaxRestarts] = useState("");
  const [killTimeout, setKillTimeout] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const canSubmit = agent !== "" && name.trim() !== "" && cmd.trim() !== "" && !busy;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!canSubmit) return;
    setBusy(true);
    setError("");
    const source: CommandSource = { type: "command", name: name.trim(), cmd: cmd.trim() };
    const argList = args.split(/\s+/).map((s) => s.trim()).filter(Boolean);
    if (argList.length) source.args = argList;
    if (cwd.trim()) source.cwd = cwd.trim();
    if (instances.trim()) source.instances = Number(instances);
    const envMap: Record<string, string> = {};
    for (const row of env) if (row.key.trim()) envMap[row.key.trim()] = row.value;
    if (Object.keys(envMap).length) source.env = envMap;
    if (restart !== "always") source.restart = restart;
    if (maxRestarts.trim()) source.max_restarts = Number(maxRestarts);
    if (killTimeout.trim()) source.kill_timeout = killTimeout.trim();
    const res = await addApp(agent, source);
    setBusy(false);
    if (res.ok) {
      onAdded();
      onClose();
    } else {
      setError(res.error || "error");
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <form className="modal" onClick={(e) => e.stopPropagation()} onSubmit={submit}>
        <div className="modal-head">
          <span className="modal-title">add app</span>
          <button type="button" className="ctl-btn" onClick={onClose}>✕</button>
        </div>

        <label className="field">
          target agent
          {connected.length === 0 ? (
            <span className="hint">no agents connected</span>
          ) : (
            <select value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="" disabled>
                select…
              </option>
              {connected.map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name}
                </option>
              ))}
            </select>
          )}
        </label>

        <label className="field">
          name
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder="web" />
        </label>
        <label className="field">
          command
          <input value={cmd} onChange={(e) => setCmd(e.target.value)} placeholder="/usr/bin/node" />
        </label>
        <label className="field">
          args
          <input value={args} onChange={(e) => setArgs(e.target.value)} placeholder="server.js --port 3000" />
        </label>
        <label className="field">
          working dir
          <input value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/srv/app" />
        </label>
        <label className="field">
          instances
          <input value={instances} onChange={(e) => setInstances(e.target.value)} placeholder="1" inputMode="numeric" />
        </label>

        <button type="button" className="adv-toggle" onClick={() => setShowAdv((v) => !v)}>
          {showAdv ? "▾" : "▸"} advanced
        </button>
        {showAdv && (
          <div className="adv">
            <div className="field">
              env
              {env.map((row, i) => (
                <div className="env-row" key={i}>
                  <input
                    placeholder="KEY"
                    value={row.key}
                    onChange={(e) =>
                      setEnv((rs) => rs.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))
                    }
                  />
                  <input
                    placeholder="value"
                    value={row.value}
                    onChange={(e) =>
                      setEnv((rs) => rs.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))
                    }
                  />
                  <button type="button" className="ctl-btn" onClick={() => setEnv((rs) => rs.filter((_, j) => j !== i))}>
                    ✕
                  </button>
                </div>
              ))}
              <button type="button" className="ctl-btn" onClick={() => setEnv((rs) => [...rs, { key: "", value: "" }])}>
                + env var
              </button>
            </div>
            <label className="field">
              restart
              <select value={restart} onChange={(e) => setRestart(e.target.value)}>
                <option value="always">always</option>
                <option value="on-failure">on-failure</option>
                <option value="no">no</option>
              </select>
            </label>
            <label className="field">
              max restarts
              <input value={maxRestarts} onChange={(e) => setMaxRestarts(e.target.value)} placeholder="16" inputMode="numeric" />
            </label>
            <label className="field">
              kill timeout
              <input value={killTimeout} onChange={(e) => setKillTimeout(e.target.value)} placeholder="5s" />
            </label>
          </div>
        )}

        {error && <div className="modal-error">{error}</div>}
        <div className="modal-foot">
          <button type="button" className="btn" onClick={onClose}>
            cancel
          </button>
          <button type="submit" className="btn primary" disabled={!canSubmit}>
            {busy ? "adding…" : "add app"}
          </button>
        </div>
      </form>
    </div>
  );
}
```

- [ ] **Step 2: Type-check**

Run: `cd "/Users/sebastiankuprat/process manager/web" && npx tsc -b`
Expected: no errors. (The component is not yet rendered anywhere — that's Task 4. `tsc -b` still type-checks the file.)

- [ ] **Step 3: Commit**

```bash
cd "/Users/sebastiankuprat/process manager"
git add web/src/AddAppModal.tsx
git commit -m "feat(web): add AddAppModal form component

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Frontend — wire the modal into the overview + styles

**Files:**
- Modify: `web/src/Overview.tsx` (import, state, header button, render modal)
- Modify: `web/src/styles.css` (append modal styles)

**Interfaces:**
- Consumes: `AddAppModal` (Task 3), existing `agents` state in `Overview`.
- Produces: a `+ add app` button in the topbar that opens the modal; on success the next 2s fleet poll surfaces the new card.

- [ ] **Step 1: Wire the modal into `Overview.tsx`**

In `web/src/Overview.tsx`, change the import line (`:2`) to add `AddAppModal` and (already present) `Agent`:

```tsx
import { AddAppModal } from "./AddAppModal";
```

Add modal state inside the component, next to the other `useState` hooks (after `:12`):

```tsx
  const [showAdd, setShowAdd] = useState(false);
```

Replace the topbar line (`:62`) with a version that adds the button:

```tsx
      <div className="topbar">
        <Logo />
        <div className="topbar-actions">
          <button className="btn" onClick={() => setShowAdd(true)}>+ add app</button>
          <button className="btn" onClick={async () => { await logout(); onLogout(); }}>sign out</button>
        </div>
      </div>
      {showAdd && (
        <AddAppModal
          agents={agents}
          onClose={() => setShowAdd(false)}
          onAdded={() => {}}
        />
      )}
```

(`onAdded` is a no-op: the 2s `getFleet` poll already refreshes the card list. The modal closes itself on success.)

- [ ] **Step 2: Append modal styles to `styles.css`**

Append to `web/src/styles.css` (uses existing Signal tokens):

```css
.topbar-actions { display: flex; gap: 8px; align-items: center; }
.btn.primary { background: var(--cyan); color: var(--bg); border-color: var(--cyan); }
.btn.primary:hover:not(:disabled) { background: var(--cyan); }
.btn.primary:disabled { background: var(--border); color: var(--faint); border-color: var(--border); cursor: default; }

.modal-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.6); display: flex; align-items: flex-start; justify-content: center; padding: 4rem 1rem; z-index: 50; overflow-y: auto; }
.modal { background: var(--panel); border: 1px solid var(--border); border-radius: var(--r-lg); padding: 1.25rem 1.4rem; width: 420px; max-width: 100%; display: flex; flex-direction: column; gap: 0.7rem; }
.modal-head { display: flex; align-items: center; justify-content: space-between; }
.modal-title { font-weight: 700; font-size: 16px; color: #F2F3F7; }
.field { display: flex; flex-direction: column; font-size: 11px; color: var(--dim); gap: 0.3rem; }
.field input, .field select { font-family: var(--mono); background: var(--bg); color: var(--text); border: 1px solid var(--border); border-radius: var(--r-sm); padding: 0.45rem 0.55rem; font-size: 0.85rem; }
.field input:focus, .field select:focus { outline: none; border-color: var(--cyan); }
.field .hint { color: var(--faint); font-size: 11px; font-style: italic; }
.adv-toggle { align-self: flex-start; font-family: var(--mono); font-size: 11px; color: var(--dim); background: transparent; border: 0; padding: 2px 0; }
.adv { display: flex; flex-direction: column; gap: 0.7rem; border-left: 1px solid var(--border-soft); padding-left: 0.7rem; }
.env-row { display: flex; gap: 6px; align-items: center; }
.env-row input { flex: 1 1 auto; }
.modal-error { color: var(--danger); font-size: 0.8rem; word-break: break-word; }
.modal-foot { display: flex; justify-content: flex-end; gap: 8px; margin-top: 0.3rem; }
```

- [ ] **Step 3: Build the embedded UI**

Run: `cd "/Users/sebastiankuprat/process manager" && make ui`
Expected: `tsc -b && vite build` succeeds; output written into `internal/dashboard/dist`.

- [ ] **Step 4: Commit**

```bash
cd "/Users/sebastiankuprat/process manager"
git add web/src/Overview.tsx web/src/styles.css internal/dashboard/dist
git commit -m "feat(web): open AddAppModal from overview + Signal modal styles

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

(If `internal/dashboard/dist` is gitignored in this repo, drop it from `git add` — `make ui` regenerates it. Check with `git check-ignore internal/dashboard/dist` before committing.)

---

### Task 5: Full gate, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-19-m20-add-app.md`

- [ ] **Step 1: Full verification gate**

Run:
```bash
cd "/Users/sebastiankuprat/process manager"
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l . ; go vet ./...
make ui
```
Expected: build OK; all packages green; `gofmt` silent; vet clean; UI builds.

- [ ] **Step 2: Live in-browser demo (per CLAUDE.md)**

Follow the live-demo dance from `docs/handoffs/2026-06-18-current-state-after-m19.md` ("How to run / demo the dashboard in-browser"):
1. Scratch `XDG_DATA_HOME=/tmp/marshal-demo/...`. While the server is **down**: set the dashboard password, rotate an enroll token, capture the fingerprint.
2. `marshal start <yaml>` with a `server:` block + at least one app so an agent connects on :9001.
3. `npm --prefix web run dev` (Vite :5173), open `http://localhost:5173`, log in.
4. Click **+ add app**, fill name + cmd (e.g. name `demo2`, cmd `/bin/sh`, args `-c "while true; do echo hi; sleep 2; done"`), pick the agent, submit.
5. Confirm: modal closes, a new card for `demo2` appears within ~2s, state goes `online`. Then test an error path: add an app with the same name → in-modal error `app "demo2" already exists`, modal stays open.
6. Tear down: stop agent + server + Vite, remove the scratch dir and any `.claude/launch.json`. Confirm `pgrep -fl marshal` shows only the user's own daemon (no demo orphans).

Report what was observed.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-19-m20-add-app.md` covering: current state (branch `m20-dashboard-add-app`, what merged), what changed this session and why (new `POST /api/apps`, `source` discriminator for future git, modal), how to build/run/test, deferred items (git source, log-retention fields, edit-app, immediate refetch instead of 2s poll), and the concrete next step. Per CLAUDE.md handoff convention.

- [ ] **Step 4: Commit + finish the branch**

```bash
cd "/Users/sebastiankuprat/process manager"
git add docs/handoffs/2026-06-19-m20-add-app.md
git commit -m "docs: M20 handoff — add an app via the dashboard

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```
Then use the `superpowers:finishing-a-development-branch` skill to decide the merge (local `--no-ff` to `main` per project convention, since there is no git remote).

---

## Self-Review

**Spec coverage:**
- `POST /api/apps` endpoint + body shape + `source.type` discriminator → Task 1. ✓
- Backend mapping to `ControlOp_Start`/`AppSpec`, reuse of `Control` + error mapping (400/401/502/200) → Task 1 (`apps.go`, `startOp`). ✓
- Audit log line → Task 1 (`log.Printf("dashboard: add app …")`). ✓
- Route registration under `requireSession` → Task 1 Step 3. ✓
- Frontend `addApp` + `CommandSource` → Task 2. ✓
- Modal with target-agent dropdown (smart default / empty hint), core + advanced fields, env rows, in-modal errors, Signal tokens → Tasks 3 & 4. ✓
- `+ add app` button on overview header; success → close + card appears via poll → Task 4. ✓
- Backend TDD; frontend via type-check + live demo (no test runner added) → Tasks 1–5, Global Constraints. ✓
- Build/verify gate + live demo + handoff → Task 5. ✓
- Out of scope (git logic, log-retention fields, edit-app) → not implemented; noted in handoff. ✓

**Placeholder scan:** The only "placeholder" is the deliberate `var _ = context.Background` compile-guard in Task 1 Step 1, explicitly removed in Step 3 — not a plan gap. No TBD/TODO/"add error handling"-style hand-waving remains.

**Type consistency:** `commandSource`/`addAppRequest`/`startOp` (Go) field names and `AppSpec` getters (`GetName`, `GetCmd`, `GetArgs`, `GetCwd`, `GetInstances`, `GetEnv`, `GetRestart`, `GetMaxRestarts`, `GetKillTimeout`, `GetStart().GetApps()`) match the verified `internal/pb` struct. `CommandSource` (TS) fields match the JSON tags on `commandSource` (Go). `AddAppModal` prop names (`agents`, `onClose`, `onAdded`) are consistent between Tasks 3 and 4. `addApp(agent, source)` signature consistent across Tasks 2–4.
