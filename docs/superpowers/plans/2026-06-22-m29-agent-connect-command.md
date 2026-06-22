# M29 — Agent Connect-Command Generator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an authenticated dashboard user generate a copy-paste shell command that enrolls a new agent host into the fleet.

**Architecture:** A new session-gated endpoint `POST /api/fleet/connect-token` mints a fresh enroll token through the running server's in-memory `AuthStore` (immediately effective), returns it once alongside the cert fingerprint and a default address; the frontend assembles a shell one-liner. The dashboard depends on a small `EnrollMinter` interface; a thin adapter in `internal/server` satisfies it from `AuthStore` + fingerprint + the fleet listen address.

**Tech Stack:** Go 1.26 (stdlib + existing `marshal/internal/*`), React + TypeScript (`web/`), embedded bundle via `make ui`.

## Global Constraints

- TDD: failing test first, then minimal implementation. (CLAUDE.md)
- `go test ./... -race -count=1` green; `go vet ./...` clean; `gofmt -l .` silent before finishing. (CLAUDE.md)
- Branch is `m29-agent-connect-command` (already created off `dev`); never commit to `main`. (CLAUDE.md)
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. (CLAUDE.md)
- Every change records a `CHANGELOG.md` `[Unreleased]` entry as part of the work. (CLAUDE.md)
- Generate = **rotate**: minting a token rotates the single shared enroll token (spec §"Key constraint").
- The minted token is returned **once** and **never written to logs** (spec §3).
- Session-gated, like `POST /api/control`; unauthenticated → `401` (spec §3).
- Rotation must not disrupt enrolled agents (they use per-agent tokens) — true by construction; do not touch `data.Agents` (spec §3).

---

### Task 1: `EnrollMinter` interface + `connectToken` endpoint (dashboard)

**Files:**
- Create: `internal/dashboard/connect.go`
- Modify: `internal/dashboard/handlers.go` (add `enroll EnrollMinter` field to the `handler` struct; register the route in the mux)
- Test: `internal/dashboard/connect_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: existing `handler`, `requireSession`, `writeJSON`, `userKey`; test fakes `fakeLister`, `fakeMetrics`, `fakeLogs`, `fakeController`, `fakeAuth`, helper `loginCookie` (all already in the `dashboard` test package).
- Produces: `EnrollMinter interface { RotateEnrollToken() (string, error); Fingerprint() string; FleetAddress() string }`; `handler.enroll EnrollMinter`; route `POST /api/fleet/connect-token` → `h.connectToken`; helper `defaultConnectAddress(reqHost, fleetAddr string) string`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dashboard/connect_test.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeMinter struct {
	tok   string
	fp    string
	addr  string
	err   error
	calls int
}

func (f *fakeMinter) RotateEnrollToken() (string, error) { f.calls++; return f.tok, f.err }
func (f *fakeMinter) Fingerprint() string                { return f.fp }
func (f *fakeMinter) FleetAddress() string               { return f.addr }

func newConnectHandler(t *testing.T, m EnrollMinter) *handler {
	t.Helper()
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", nil)
	h.enroll = m
	return h
}

func TestConnectTokenRequiresSession(t *testing.T) {
	h := newConnectHandler(t, &fakeMinter{tok: "T", fp: "FP", addr: ":9000"})
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/fleet/connect-token", strings.NewReader(`{}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-session = %d; want 401", resp.StatusCode)
	}
}

func TestConnectTokenReturnsMintedFields(t *testing.T) {
	m := &fakeMinter{tok: "enroll-XYZ", fp: "abc123", addr: ":9000"}
	h := newConnectHandler(t, m)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	req, _ := http.NewRequest("POST", srv.URL+"/api/fleet/connect-token", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d; want 200", resp.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["token"] != "enroll-XYZ" {
		t.Fatalf("token=%q", got["token"])
	}
	if got["fingerprint"] != "abc123" {
		t.Fatalf("fingerprint=%q", got["fingerprint"])
	}
	if !strings.HasSuffix(got["default_address"], ":9000") {
		t.Fatalf("default_address=%q; want host:9000", got["default_address"])
	}
	if m.calls != 1 {
		t.Fatalf("RotateEnrollToken calls=%d; want 1", m.calls)
	}
}

func TestConnectTokenUnavailableWhenNilMinter(t *testing.T) {
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", nil)
	// h.enroll left nil
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	req, _ := http.NewRequest("POST", srv.URL+"/api/fleet/connect-token", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil minter = %d; want 503", resp.StatusCode)
	}
}

func TestDefaultConnectAddress(t *testing.T) {
	cases := []struct{ reqHost, fleet, want string }{
		{"127.0.0.1:9001", ":9000", "127.0.0.1:9000"},
		{"example.com:9001", "0.0.0.0:9000", "example.com:9000"},
		{"host-no-port", ":9000", "host-no-port:9000"},
	}
	for _, c := range cases {
		if got := defaultConnectAddress(c.reqHost, c.fleet); got != c.want {
			t.Errorf("defaultConnectAddress(%q,%q)=%q; want %q", c.reqHost, c.fleet, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dashboard/ -run 'TestConnectToken|TestDefaultConnectAddress'`
Expected: FAIL to compile — `EnrollMinter` / `h.enroll` / `defaultConnectAddress` / route undefined.

- [ ] **Step 3: Add the `enroll` field and register the route**

In `internal/dashboard/handlers.go`, add a field to the `handler` struct (next to `notifs`/`notifBuild`):

```go
	notifs      Notifications
	notifBuild  notify.BuildFunc
	enroll      EnrollMinter
```

In the same file, in the mux-registration block (next to the other `mux.HandleFunc("POST /api/...` lines, e.g. right after the `/api/control` registration), add:

```go
	mux.HandleFunc("POST /api/fleet/connect-token", h.requireSession(h.connectToken))
```

- [ ] **Step 4: Implement the handler**

Create `internal/dashboard/connect.go`:

```go
package dashboard

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
)

// EnrollMinter mints a fresh enroll token and exposes the data the dashboard
// needs to build an agent connect command: the server cert fingerprint and the
// fleet (agent-facing) listen address. *server.enrollMinter satisfies it.
type EnrollMinter interface {
	RotateEnrollToken() (string, error)
	Fingerprint() string
	FleetAddress() string // e.g. ":9000" or "0.0.0.0:9000"
}

type connectTokenReq struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

// connectToken serves POST /api/fleet/connect-token: mints a fresh enroll token
// (rotating the single shared one), returning it ONCE with the cert fingerprint
// and a default address (request host + fleet port) for assembling an agent
// connect command. Session-gated. The token is never written to logs.
func (h *handler) connectToken(w http.ResponseWriter, r *http.Request) {
	if h.enroll == nil {
		http.Error(w, "fleet enrollment unavailable", http.StatusServiceUnavailable)
		return
	}
	// Body is optional: address/name are non-secret hints the frontend may also
	// fill locally, so a missing/!valid body is not an error.
	var body connectTokenReq
	_ = json.NewDecoder(r.Body).Decode(&body)

	tok, err := h.enroll.RotateEnrollToken()
	if err != nil {
		http.Error(w, "could not mint token", http.StatusInternalServerError)
		return
	}
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: minted agent enroll token by %s", user) // never log the token
	writeJSON(w, http.StatusOK, map[string]string{
		"token":           tok,
		"fingerprint":     h.enroll.Fingerprint(),
		"default_address": defaultConnectAddress(r.Host, h.enroll.FleetAddress()),
	})
}

// defaultConnectAddress combines the request host (sans port) with the fleet
// listen port, e.g. ("127.0.0.1:9001", ":9000") -> "127.0.0.1:9000". If the
// request host has no port it is used as-is; if the fleet port is unknown the
// bare host is returned.
func defaultConnectAddress(reqHost, fleetAddr string) string {
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}
	_, port, err := net.SplitHostPort(fleetAddr)
	if err != nil || port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -run 'TestConnectToken|TestDefaultConnectAddress'`
Expected: PASS (all four). Then `go test ./internal/dashboard/` to confirm nothing else broke.

- [ ] **Step 6: Add the CHANGELOG entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added` (create the subsection if absent), add:

```markdown
- Dashboard "Connect an agent": generates a ready-to-run command (with a freshly minted enroll token, the server fingerprint, and address) to enroll a new agent host.
```

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/connect.go internal/dashboard/handlers.go internal/dashboard/connect_test.go CHANGELOG.md
git commit -m "feat(dashboard): POST /api/fleet/connect-token mints an agent enroll command

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `enrollMinter` adapter + wire through `dashboard.Serve`

**Files:**
- Create: `internal/server/enroll.go`
- Modify: `internal/dashboard/server.go` (add `enroll EnrollMinter` param to `Serve`, set `h.enroll`)
- Modify: `internal/server/server.go` (build the adapter, pass it at the `dashboard.Serve(...)` call site in `ServeDir`)
- Test: `internal/server/enroll_test.go`

**Interfaces:**
- Consumes: `dashboard.EnrollMinter` (Task 1); `*AuthStore` with unexported `rotate(which string) (string, error)` and `verifyEnroll(token string) bool`; `loadOrInitAuth(dir string) (*AuthStore, *InitSecrets, error)`; the in-scope `auth`, `fp`, and `lis net.Listener` inside `ServeDir`.
- Produces: `type enrollMinter struct { auth *AuthStore; fp string; fleetAddr string }` implementing the three `EnrollMinter` methods.

- [ ] **Step 1: Write the failing adapter test**

Create `internal/server/enroll_test.go`:

```go
package server

import "testing"

func TestEnrollMinterAdapter(t *testing.T) {
	a, _, err := loadOrInitAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := enrollMinter{auth: a, fp: "deadbeef", fleetAddr: "[::]:9000"}
	if m.Fingerprint() != "deadbeef" {
		t.Fatalf("Fingerprint=%q", m.Fingerprint())
	}
	if m.FleetAddress() != "[::]:9000" {
		t.Fatalf("FleetAddress=%q", m.FleetAddress())
	}
	t1, err := m.RotateEnrollToken()
	if err != nil || t1 == "" {
		t.Fatalf("rotate1 token=%q err=%v", t1, err)
	}
	t2, err := m.RotateEnrollToken()
	if err != nil {
		t.Fatal(err)
	}
	if t1 == t2 {
		t.Fatal("expected a fresh token on re-rotate")
	}
	if !a.verifyEnroll(t2) {
		t.Fatal("latest enroll token must verify")
	}
	if a.verifyEnroll(t1) {
		t.Fatal("rotated-out token must be rejected")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestEnrollMinterAdapter`
Expected: FAIL — `enrollMinter` undefined.

- [ ] **Step 3: Implement the adapter**

Create `internal/server/enroll.go`:

```go
package server

// enrollMinter adapts the running server's AuthStore, cert fingerprint, and
// fleet listen address to the dashboard.EnrollMinter interface. Rotating goes
// through the in-memory AuthStore, so a minted token is immediately effective
// for enrollment (no restart needed, unlike the CLI on-disk rotate path).
type enrollMinter struct {
	auth      *AuthStore
	fp        string
	fleetAddr string
}

func (m enrollMinter) RotateEnrollToken() (string, error) { return m.auth.rotate("enroll") }
func (m enrollMinter) Fingerprint() string                { return m.fp }
func (m enrollMinter) FleetAddress() string               { return m.fleetAddr }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestEnrollMinterAdapter`
Expected: PASS.

- [ ] **Step 5: Add the `enroll` param to `dashboard.Serve`**

In `internal/dashboard/server.go`, change the `Serve` signature to add a trailing `enroll EnrollMinter` parameter, and set the field after `newHandler` (alongside `h.notifs`):

```go
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath, auditPath string, creds Credentials, notifs Notifications, notifBuild notify.BuildFunc, enroll EnrollMinter) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour, sessionsPath, auditPath, creds)
	h.notifs = notifs
	h.notifBuild = notifBuild
	h.enroll = enroll
```

(Leave the rest of `Serve` unchanged.)

- [ ] **Step 6: Pass the adapter at the call site**

In `internal/server/server.go`, find the `dashboard.Serve(...)` call inside `ServeDir` (currently the last argument is `channels.New`). Immediately before the `go func() { ... dashboard.Serve(...)` block, the variables `auth`, `fp`, and `lis` are in scope. Add the adapter argument:

```go
		em := enrollMinter{auth: auth, fp: fp, fleetAddr: lis.Addr().String()}
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, srv, auth, cert, sessionsPath, auditPath, cw, nw, channels.New, em); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
```

- [ ] **Step 7: Build and run the full server + dashboard suites**

Run: `go build ./... && go test ./internal/server/ ./internal/dashboard/`
Expected: PASS (signature change compiles; adapter test green; dashboard tests still green).

- [ ] **Step 8: Commit**

```bash
git add internal/server/enroll.go internal/server/enroll_test.go internal/dashboard/server.go internal/server/server.go
git commit -m "feat(server): wire enrollMinter adapter into the dashboard

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Frontend — "Connect an agent" modal + `make ui`

**Files:**
- Modify: `web/src/api.ts` (add `connectToken` + `ConnectInfo` type)
- Create: `web/src/ConnectAgentModal.tsx`
- Modify: `web/src/Overview.tsx` (topbar button + modal wiring)
- Regenerate: `internal/dashboard/dist` via `make ui` (committed)

**Interfaces:**
- Consumes: the `POST /api/fleet/connect-token` endpoint (Task 1) returning `{token, fingerprint, default_address}`.
- Produces: `api.connectToken(address?, name?): Promise<ConnectInfo>`, `type ConnectInfo = { token: string; fingerprint: string; default_address: string }`; `<ConnectAgentModal onClose />`.

- [ ] **Step 1: Add the API call**

In `web/src/api.ts`, add (near the other fleet calls):

```ts
export type ConnectInfo = { token: string; fingerprint: string; default_address: string };

export async function connectToken(address?: string, name?: string): Promise<ConnectInfo> {
  const r = await fetch("/api/fleet/connect-token", {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ address: address ?? "", name: name ?? "" }),
  });
  if (!r.ok) throw new Error(`connect-token failed: ${r.status}`);
  return (await r.json()) as ConnectInfo;
}
```

- [ ] **Step 2: Create the modal component**

Create `web/src/ConnectAgentModal.tsx`:

```tsx
import { useState } from "react";
import { connectToken } from "./api";

export function ConnectAgentModal({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("agent");
  const [address, setAddress] = useState("");
  const [cmd, setCmd] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState(false);

  async function generate() {
    setBusy(true);
    setError("");
    setCopied(false);
    try {
      const info = await connectToken(address, name);
      const addr = address.trim() || info.default_address;
      setAddress(addr);
      setCmd(
        `cat > marshal.yaml <<'EOF'\n` +
          `server:\n` +
          `  address: ${addr}\n` +
          `  name: ${name.trim() || "agent"}\n` +
          `  token: ${info.token}\n` +
          `  fingerprint: ${info.fingerprint}\n` +
          `EOF\n` +
          `marshal start marshal.yaml`,
      );
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>Connect an agent</h3>
        <label>agent name<input value={name} onChange={(e) => setName(e.target.value)} /></label>
        <label>server address<input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="auto (host:fleet-port)" /></label>
        <button className="btn" disabled={busy} onClick={generate}>Generate connect command</button>
        {error && <p className="error">{error}</p>}
        {cmd && (
          <>
            <p className="warn">Shown once. Generating rotated the enroll token — any previously generated, unused command no longer works. Already-connected agents are unaffected.</p>
            <pre className="connect-cmd">{cmd}</pre>
            <button className="btn" onClick={async () => { await navigator.clipboard.writeText(cmd); setCopied(true); }}>{copied ? "copied" : "copy"}</button>
          </>
        )}
        <button className="btn" onClick={onClose}>close</button>
      </div>
    </div>
  );
}
```

(The CSS classes `modal-backdrop`/`modal`/`btn`/`error` already exist from `AddAppModal`; `warn` and `connect-cmd` are optional unstyled extras — the Signal UI pass will style them. Do not add CSS here.)

- [ ] **Step 3: Wire the modal into the Overview topbar**

In `web/src/Overview.tsx`:

Add the import near the other component imports:
```tsx
import { ConnectAgentModal } from "./ConnectAgentModal";
```

Add state next to `const [showAdd, setShowAdd] = useState(false);`:
```tsx
  const [showConnect, setShowConnect] = useState(false);
```

In the `topbar-actions` div, add a button right after the `+ add app` button:
```tsx
          <button className="btn" onClick={() => setShowConnect(true)}>+ connect agent</button>
```

Render the modal next to the `{showAdd && ( ... )}` block:
```tsx
      {showConnect && <ConnectAgentModal onClose={() => setShowConnect(false)} />}
```

- [ ] **Step 4: Type-check / build the frontend**

Run: `cd web && npm run build`
Expected: build succeeds, no TypeScript errors.

- [ ] **Step 5: Regenerate the embedded bundle**

Run (from repo root): `make ui`
Expected: `internal/dashboard/dist` updated. Confirm the new strings ship:

Run: `grep -rl "Connect an agent" internal/dashboard/dist`
Expected: at least one `assets/index-*.js` matches.

- [ ] **Step 6: Commit**

```bash
git add web/src/api.ts web/src/ConnectAgentModal.tsx web/src/Overview.tsx internal/dashboard/dist
git commit -m "feat(dashboard): Connect-an-agent modal generating the enroll command

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Final verification (run before declaring the branch done)

- [ ] `go test ./... -race -count=1` — all packages green.
- [ ] `go vet ./...` — clean.
- [ ] `gofmt -l .` — prints nothing.
- [ ] `make build` — version-stamped binary builds.
- [ ] Spec coverage re-check against `docs/superpowers/specs/2026-06-22-m29-agent-connect-command-design.md`.

Then: requesting-code-review (whole branch), address findings, live demo (spec §5 — run a scratch server, log in, click **Connect an agent**, copy the one-liner, **actually enroll a second scratch agent** with it, confirm it appears in `server agent ls` / the Overview; confirm a previously-generated command fails after a re-generate; teardown by data-dir + PID, preserve the standing launchd daemon, `pgrep -fl marshal` clean), write the handoff to `docs/handoffs/`, finish the branch (merge `--no-ff` into `dev`).

---

## Self-Review

**Spec coverage:**
- §1 backend endpoint (mint via rotate, fingerprint, default_address, 200 shape, 401, never-log token, nil→503) → Task 1. ✓
- §1 plumbing (`EnrollMinter`, adapter in `internal/server`, fleet addr through `Serve`) → Tasks 1 + 2. ✓
- §2 frontend (Overview affordance, editable name/address, Generate → one-liner, copy, show-once/rotation warning, function-first) → Task 3. ✓
- §3 security (session-gated, token once + not logged, fingerprint/address not secret, enrolled agents safe) → Task 1 (handler + 401 + nil-guard tests) + by-construction (adapter never touches `data.Agents`). ✓
- §4 testing (200 fields, minted-token-verifies/old-rejected, 401) → Task 1 (handler/401) + Task 2 (verify/reject at adapter). ✓
- CHANGELOG Added → Task 1. ✓
- §5 live demo / final verification → Final sections. ✓

**Placeholder scan:** No TBD/TODO/"add error handling"/"similar to Task N"; every code step shows full code. ✓

**Type consistency:** `EnrollMinter{RotateEnrollToken()(string,error); Fingerprint()string; FleetAddress()string}` is defined in Task 1 and implemented verbatim by `enrollMinter` in Task 2; `handler.enroll EnrollMinter`; `Serve(..., enroll EnrollMinter)`; `connectToken`/`defaultConnectAddress`; TS `ConnectInfo{token,fingerprint,default_address}` matches the handler's JSON keys; `connectToken(address?,name?)`. Names align across Tasks 1→2→3. ✓
