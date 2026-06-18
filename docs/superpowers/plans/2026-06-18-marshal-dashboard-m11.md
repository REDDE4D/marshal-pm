# M11 — Web Dashboard (thin vertical slice) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a username/password-authenticated web dashboard to the Marshal server that serves an embedded React SPA and exposes a live read-only process list across the fleet, over TLS on a separate port.

**Architecture:** A new `internal/dashboard/` Go package runs an `http.Server` over TLS on its own `--http-listen` port (reusing the server's existing cert). Its handler depends only on two small interfaces — `FleetLister` (satisfied by `*server.Registry`) and `Authenticator` (satisfied by `*server.AuthStore`) — so there is no import cycle. Sessions are server-side and in-memory; passwords are PBKDF2-hashed in `auth.json`. A Vite+React SPA is built into `internal/dashboard/dist/` and embedded via `go:embed`.

**Tech Stack:** Go 1.26 (stdlib `crypto/pbkdf2`, `net/http`, `embed`), `golang.org/x/term` (CLI password prompt), Vite + React + TypeScript (frontend), cobra (CLI).

## Global Constraints

- Module path is `marshal`; imports are `marshal/internal/...`. The proto package is imported as `marshal/internal/pb`.
- Go version floor: **1.26** (required for stdlib `crypto/pbkdf2`).
- TDD: write the failing test first, then the implementation. Run `go test ./... -race -count=1`, `go vet ./...`, and `gofmt -l .` (must print nothing) before declaring work done.
- Commit messages: imperative subject; co-author trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- The dashboard is **off by default** — only starts when `--http-listen` is non-empty. Standalone agent mode and the gRPC service on `:9000` must remain unaffected.
- Password hashing: PBKDF2-HMAC-SHA256, 600000 iterations, 16-byte random salt, 32-byte key, stdlib only (no bcrypt/x-crypto). Verification is constant-time.
- Session cookie: name `marshal_session`, `HttpOnly` + `Secure` + `SameSite=Strict`, 24h fixed lifetime.
- `auth.json` stores dashboard credentials under a `users` map keyed by username (forward-compatible with multi-user in M13+).

---

### Task 1: Dashboard credentials in AuthStore

Add PBKDF2-hashed dashboard users to the existing `AuthStore`, plus exported `dataDir` wrappers for the CLI.

**Files:**
- Modify: `internal/server/auth.go`
- Test: `internal/server/auth_test.go`

**Interfaces:**
- Consumes: existing `AuthStore`, `authData`, `(*AuthStore).save()`, `loadOrInitAuth`, `ensureDataDir`, `LoadOrInitAuth`.
- Produces:
  - `(*AuthStore).SetDashboardUser(user, password string) error`
  - `(*AuthStore).VerifyDashboardUser(user, password string) bool`
  - `(*AuthStore).HasDashboardUser() bool`
  - `server.SetDashboardPassword(dataDir, user, password string) error`
  - `server.HasDashboardUserDir(dataDir string) (bool, error)`

- [ ] **Step 1: Write the failing test**

Add to `internal/server/auth_test.go`:

```go
func TestDashboardUserSetVerify(t *testing.T) {
	dir := t.TempDir()
	a, _, err := LoadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.HasDashboardUser() {
		t.Fatal("expected no dashboard user initially")
	}
	if err := a.SetDashboardUser("admin", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if !a.HasDashboardUser() {
		t.Fatal("expected a dashboard user after set")
	}
	if !a.VerifyDashboardUser("admin", "s3cret") {
		t.Fatal("correct password rejected")
	}
	if a.VerifyDashboardUser("admin", "wrong") {
		t.Fatal("wrong password accepted")
	}
	if a.VerifyDashboardUser("nobody", "s3cret") {
		t.Fatal("unknown user accepted")
	}
}

func TestDashboardUserPersistsAndSaltsDiffer(t *testing.T) {
	dir := t.TempDir()
	a, _, _ := LoadOrInitAuth(dir)
	if err := a.SetDashboardUser("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	// Reload from disk: the credential must persist.
	b, _, err := LoadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !b.VerifyDashboardUser("admin", "pw") {
		t.Fatal("dashboard user not persisted across reload")
	}
	// Same password for two users must hash differently (per-user random salt).
	if err := a.SetDashboardUser("u1", "same"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDashboardUser("u2", "same"); err != nil {
		t.Fatal(err)
	}
	if a.data.Users["u1"].PBKDF2 == a.data.Users["u2"].PBKDF2 {
		t.Fatal("expected per-user random salt to produce different hashes")
	}
}

func TestSetDashboardPasswordDir(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	ok, err := HasDashboardUserDir(dir)
	if err != nil || !ok {
		t.Fatalf("HasDashboardUserDir = %v, %v", ok, err)
	}
	a, _, _ := LoadOrInitAuth(dir)
	if !a.VerifyDashboardUser("admin", "pw") {
		t.Fatal("password set via dir wrapper not verifiable")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/ -run TestDashboard -v`
Expected: FAIL — `a.HasDashboardUser` / `SetDashboardUser` / `SetDashboardPassword` undefined.

- [ ] **Step 3: Implement the credential storage**

In `internal/server/auth.go`, extend the imports and the `authData` struct, then add the methods.

Add these imports to the existing `import (...)` block:

```go
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
```

Add the user record type and extend `authData`:

```go
type dashboardUser struct {
	PBKDF2 string `json:"pbkdf2"` // base64 std of the derived key
	Salt   string `json:"salt"`   // base64 std of the random salt
	Iter   int    `json:"iter"`
}
```

In `authData`, add the field (after `Agents`):

```go
	Users map[string]dashboardUser `json:"users,omitempty"`
```

Add the constant and methods at the end of the file:

```go
const dashboardPBKDF2Iter = 600000

// SetDashboardUser creates or replaces the dashboard credential for user,
// storing a PBKDF2-HMAC-SHA256 hash with a fresh random salt. It persists
// atomically and rolls back the in-memory map on save failure.
func (a *AuthStore) SetDashboardUser(user, password string) error {
	if user == "" {
		return errors.New("dashboard user name required")
	}
	if password == "" {
		return errors.New("dashboard password required")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, dashboardPBKDF2Iter, 32)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.data.Users == nil {
		a.data.Users = map[string]dashboardUser{}
	}
	old, existed := a.data.Users[user]
	a.data.Users[user] = dashboardUser{
		PBKDF2: base64.StdEncoding.EncodeToString(dk),
		Salt:   base64.StdEncoding.EncodeToString(salt),
		Iter:   dashboardPBKDF2Iter,
	}
	if err := a.save(); err != nil {
		if existed {
			a.data.Users[user] = old
		} else {
			delete(a.data.Users, user)
		}
		return err
	}
	return nil
}

// VerifyDashboardUser reports whether password matches the stored credential
// for user (constant-time). Unknown user or malformed record returns false.
func (a *AuthStore) VerifyDashboardUser(user, password string) bool {
	a.mu.Lock()
	u, ok := a.data.Users[user]
	a.mu.Unlock()
	if !ok {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(u.Salt)
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(u.PBKDF2)
	if err != nil {
		return false
	}
	dk, err := pbkdf2.Key(sha256.New, password, salt, u.Iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(dk, want) == 1
}

// HasDashboardUser reports whether any dashboard user is configured.
func (a *AuthStore) HasDashboardUser() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.data.Users) > 0
}

// SetDashboardPassword sets (or replaces) the dashboard credential for the
// server rooted at dataDir.
func SetDashboardPassword(dataDir, user, password string) error {
	if err := ensureDataDir(dataDir); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return err
	}
	return a.SetDashboardUser(user, password)
}

// HasDashboardUserDir reports whether the server at dataDir has a dashboard
// user configured.
func HasDashboardUserDir(dataDir string) (bool, error) {
	if err := ensureDataDir(dataDir); err != nil {
		return false, fmt.Errorf("create data dir: %w", err)
	}
	a, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return false, err
	}
	return a.HasDashboardUser(), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ -run TestDashboard -v && go test ./internal/server/ -run TestSetDashboardPasswordDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/auth.go internal/server/auth_test.go
git commit -m "feat(server): PBKDF2 dashboard credentials in AuthStore

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: In-memory session store

A token→{user, expiry} store with an injectable clock for testing.

**Files:**
- Create: `internal/dashboard/session.go`
- Test: `internal/dashboard/session_test.go`

**Interfaces:**
- Produces:
  - `newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore`
  - `(*sessionStore).create(user string) (token string, err error)`
  - `(*sessionStore).validate(token string) (user string, ok bool)`
  - `(*sessionStore).delete(token string)`
  - `(*sessionStore).sweep()`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/session_test.go`:

```go
package dashboard

import (
	"testing"
	"time"
)

func TestSessionCreateValidate(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, err := s.create("admin")
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	user, ok := s.validate(tok)
	if !ok || user != "admin" {
		t.Fatalf("validate = %q, %v; want admin, true", user, ok)
	}
}

func TestSessionExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, _ := s.create("admin")
	now = now.Add(2 * time.Hour)
	if _, ok := s.validate(tok); ok {
		t.Fatal("expired session still valid")
	}
}

func TestSessionDelete(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, _ := s.create("a")
	s.delete(tok)
	if _, ok := s.validate(tok); ok {
		t.Fatal("deleted session still valid")
	}
}

func TestSessionSweep(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, _ := s.create("b")
	now = now.Add(2 * time.Hour)
	s.sweep()
	if _, present := s.m[tok]; present {
		t.Fatal("sweep did not remove expired session")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestSession -v`
Expected: FAIL — package/`newSessionStore` undefined (the package does not exist yet).

- [ ] **Step 3: Implement the session store**

Create `internal/dashboard/session.go`:

```go
package dashboard

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type session struct {
	user   string
	expiry time.Time
}

// sessionStore is an in-memory token→session map. Sessions are lost on process
// restart by design (v1).
type sessionStore struct {
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
	m   map[string]session
}

// newSessionStore returns a store with the given session lifetime. If now is
// nil, time.Now is used.
func newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore {
	if now == nil {
		now = time.Now
	}
	return &sessionStore{ttl: ttl, now: now, m: map[string]session{}}
}

// create mints a random 256-bit session token for user.
func (s *sessionStore) create(user string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.m[tok] = session{user: user, expiry: s.now().Add(s.ttl)}
	s.mu.Unlock()
	return tok, nil
}

// validate returns the user for a live token, or ok=false if the token is
// unknown or expired (expired tokens are removed).
func (s *sessionStore) validate(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tok]
	if !ok {
		return "", false
	}
	if !s.now().Before(sess.expiry) {
		delete(s.m, tok)
		return "", false
	}
	return sess.user, true
}

// delete removes a token (logout).
func (s *sessionStore) delete(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// sweep removes all expired sessions.
func (s *sessionStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for tok, sess := range s.m {
		if !now.Before(sess.expiry) {
			delete(s.m, tok)
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestSession -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/session.go internal/dashboard/session_test.go
git commit -m "feat(dashboard): in-memory server-side session store

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Fleet view mapping + FleetLister interface

A pure mapping from the registry's protobuf state to JSON-friendly view structs.

**Files:**
- Create: `internal/dashboard/fleet.go`
- Test: `internal/dashboard/fleet_test.go`

**Interfaces:**
- Consumes: `marshal/internal/pb` (`*pb.AgentState`, `*pb.ProcInfo`).
- Produces:
  - `FleetLister interface { List() []*pb.AgentState }`
  - `type agentView` / `type procView` (JSON-tagged)
  - `fleetView(l FleetLister) []agentView`

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/fleet_test.go`:

```go
package dashboard

import (
	"testing"

	"marshal/internal/pb"
)

type fakeLister struct{ agents []*pb.AgentState }

func (f fakeLister) List() []*pb.AgentState { return f.agents }

func TestFleetView(t *testing.T) {
	f := fakeLister{agents: []*pb.AgentState{{
		AgentName:    "dev-1",
		Connected:    true,
		LastSeenUnix: 42,
		Procs: []*pb.ProcInfo{{
			Name: "ticker", State: "running", Pid: 99, UptimeMs: 1000, Restarts: 2, Cpu: 1.5, Mem: 2048,
		}},
	}}}
	v := fleetView(f)
	if len(v) != 1 {
		t.Fatalf("len(v) = %d; want 1", len(v))
	}
	if v[0].Name != "dev-1" || !v[0].Connected || v[0].LastSeen != 42 {
		t.Fatalf("agent view = %+v", v[0])
	}
	if len(v[0].Procs) != 1 {
		t.Fatalf("len procs = %d; want 1", len(v[0].Procs))
	}
	p := v[0].Procs[0]
	if p.Name != "ticker" || p.State != "running" || p.PID != 99 || p.Restarts != 2 {
		t.Fatalf("proc view = %+v", p)
	}
}

func TestFleetViewEmpty(t *testing.T) {
	v := fleetView(fakeLister{})
	if v == nil {
		t.Fatal("fleetView should return a non-nil empty slice for JSON []")
	}
	if len(v) != 0 {
		t.Fatalf("len(v) = %d; want 0", len(v))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestFleetView -v`
Expected: FAIL — `fleetView` / `FleetLister` undefined.

- [ ] **Step 3: Implement the mapping**

Create `internal/dashboard/fleet.go`:

```go
package dashboard

import "marshal/internal/pb"

// FleetLister is the read side of the live registry the dashboard renders.
// *server.Registry satisfies it.
type FleetLister interface {
	List() []*pb.AgentState
}

type procView struct {
	Name     string  `json:"name"`
	State    string  `json:"state"`
	PID      int32   `json:"pid"`
	UptimeMs int64   `json:"uptime_ms"`
	Restarts int32   `json:"restarts"`
	CPU      float64 `json:"cpu"`
	Mem      int64   `json:"mem"`
}

type agentView struct {
	Name      string     `json:"name"`
	Connected bool       `json:"connected"`
	LastSeen  int64      `json:"last_seen_unix"`
	Procs     []procView `json:"procs"`
}

// fleetView maps the live registry state into JSON-friendly view structs.
func fleetView(l FleetLister) []agentView {
	agents := l.List()
	out := make([]agentView, 0, len(agents))
	for _, a := range agents {
		procs := make([]procView, 0, len(a.GetProcs()))
		for _, p := range a.GetProcs() {
			procs = append(procs, procView{
				Name:     p.GetName(),
				State:    p.GetState(),
				PID:      p.GetPid(),
				UptimeMs: p.GetUptimeMs(),
				Restarts: p.GetRestarts(),
				CPU:      p.GetCpu(),
				Mem:      p.GetMem(),
			})
		}
		out = append(out, agentView{
			Name:      a.GetAgentName(),
			Connected: a.GetConnected(),
			LastSeen:  a.GetLastSeenUnix(),
			Procs:     procs,
		})
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dashboard/ -run TestFleetView -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/fleet.go internal/dashboard/fleet_test.go
git commit -m "feat(dashboard): fleet view mapping and FleetLister interface

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: HTTP handler, server, and embedded SPA

The route mux, auth middleware, JSON handlers, SPA static serving, and the TLS `Serve` entrypoint. Includes a placeholder `dist/index.html` so `go:embed` compiles (Task 7 replaces it with the real build).

**Files:**
- Create: `internal/dashboard/embed.go`
- Create: `internal/dashboard/handlers.go`
- Create: `internal/dashboard/server.go`
- Create: `internal/dashboard/dist/index.html` (placeholder)
- Test: `internal/dashboard/server_test.go`

**Interfaces:**
- Consumes: `FleetLister`, `fleetView`, `sessionStore` (Tasks 2–3).
- Produces:
  - `Authenticator interface { VerifyDashboardUser(user, password string) bool }`
  - `NewHandler(lister FleetLister, auth Authenticator, ttl time.Duration) http.Handler`
  - `Serve(ctx context.Context, addr string, lister FleetLister, auth Authenticator, cert tls.Certificate) error`
  - const `sessionCookie = "marshal_session"`

- [ ] **Step 1: Create the placeholder SPA file**

Create `internal/dashboard/dist/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <title>Marshal</title>
  </head>
  <body>
    <div id="root">Marshal dashboard (placeholder — run `make ui`).</div>
  </body>
</html>
```

- [ ] **Step 2: Write the failing test**

Create `internal/dashboard/server_test.go`:

```go
package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/pb"
)

type fakeAuth struct{ user, pass string }

func (f fakeAuth) VerifyDashboardUser(u, p string) bool { return u == f.user && p == f.pass }

func sessionCookieFrom(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginFleetLogout(t *testing.T) {
	auth := fakeAuth{user: "admin", pass: "pw"}
	lister := fakeLister{agents: []*pb.AgentState{{AgentName: "dev-1", Connected: true}}}
	srv := httptest.NewServer(NewHandler(lister, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()

	// fleet without a cookie → 401
	resp, err := c.Get(srv.URL + "/api/fleet")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie fleet = %d; want 401", resp.StatusCode)
	}

	// bad login → 401
	resp, _ = c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login = %d; want 401", resp.StatusCode)
	}

	// good login → 200 + cookie
	resp, _ = c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good login = %d; want 200", resp.StatusCode)
	}
	cookie := sessionCookieFrom(resp)
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	// fleet with cookie → 200 + JSON
	req, _ := http.NewRequest("GET", srv.URL+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth fleet = %d; want 200", resp.StatusCode)
	}
	var got []agentView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "dev-1" {
		t.Fatalf("fleet json = %+v", got)
	}

	// logout → subsequent fleet → 401
	req, _ = http.NewRequest("POST", srv.URL+"/api/logout", nil)
	req.AddCookie(cookie)
	if _, err := c.Do(req); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout fleet = %d; want 401", resp.StatusCode)
	}
}

func TestSPAFallback(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, fakeAuth{}, time.Hour))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/some/client/route")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spa fallback = %d; want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(strings.ToLower(string(b)), "<html") {
		t.Fatalf("expected index.html, got %q", string(b))
	}
}

func TestUnknownAPIRouteNotFound(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, fakeAuth{}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api route = %d; want 404", resp.StatusCode)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run 'TestLogin|TestSPA|TestUnknownAPI' -v`
Expected: FAIL — `NewHandler` / `sessionCookie` undefined.

- [ ] **Step 4: Implement the embed**

Create `internal/dashboard/embed.go`:

```go
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed dist
var distFS embed.FS

// staticFS returns the embedded SPA build rooted at the dist directory.
func staticFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this cannot fail
	}
	return sub
}
```

- [ ] **Step 5: Implement the handlers**

Create `internal/dashboard/handlers.go`:

```go
package dashboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "marshal_session"

// Authenticator verifies dashboard credentials. *server.AuthStore satisfies it.
type Authenticator interface {
	VerifyDashboardUser(user, password string) bool
}

type ctxKey string

const userKey ctxKey = "user"

type handler struct {
	lister   FleetLister
	auth     Authenticator
	sessions *sessionStore
	files    fs.FS
	static   http.Handler
}

// NewHandler builds the dashboard HTTP handler with the given session lifetime.
func NewHandler(lister FleetLister, auth Authenticator, ttl time.Duration) http.Handler {
	files := staticFS()
	h := &handler{
		lister:   lister,
		auth:     auth,
		sessions: newSessionStore(ttl, nil),
		files:    files,
		static:   http.FileServer(http.FS(files)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.logout)
	mux.HandleFunc("GET /api/session", h.requireSession(h.session))
	mux.HandleFunc("GET /api/fleet", h.requireSession(h.fleet))
	mux.HandleFunc("/", h.spa)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *handler) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ User, Pass string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !h.auth.VerifyDashboardUser(body.User, body.Pass) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	tok, err := h.sessions.create(body.User)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.setSessionCookie(w, tok, 0)
	writeJSON(w, http.StatusOK, map[string]string{"user": body.User})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.sessions.delete(c.Value)
	}
	h.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user, ok := h.sessions.validate(c.Value)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(userKey).(string)
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

func (h *handler) fleet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, fleetView(h.lister))
}

// spa serves embedded static assets, falling back to index.html for client-side
// routes. Unknown /api/ paths 404 (real API routes are registered explicitly).
func (h *handler) spa(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := h.files.Open(p); err == nil {
		_ = f.Close()
		h.static.ServeHTTP(w, r)
		return
	}
	// SPA fallback: serve index.html for unknown (client-routed) paths.
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	h.static.ServeHTTP(w, r2)
}
```

- [ ] **Step 6: Implement the TLS server**

Create `internal/dashboard/server.go`:

```go
package dashboard

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// Serve runs the dashboard HTTP server over TLS on addr until ctx is canceled.
// cert is the server's TLS certificate (shared with the gRPC service).
func Serve(ctx context.Context, addr string, lister FleetLister, auth Authenticator, cert tls.Certificate) error {
	srv := &http.Server{
		Addr:      addr,
		Handler:   NewHandler(lister, auth, 24*time.Hour),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ServeTLS(lis, "", ""); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -v`
Expected: PASS (all of `TestSession*`, `TestFleetView*`, `TestLoginFleetLogout`, `TestSPAFallback`, `TestUnknownAPIRouteNotFound`).

- [ ] **Step 8: Format, vet, commit**

```bash
gofmt -w internal/dashboard/
go vet ./internal/dashboard/
git add internal/dashboard/
git commit -m "feat(dashboard): HTTP handler, TLS server, embedded SPA shell

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `marshal server passwd` CLI command

Set the dashboard password from the terminal (no echo) or piped stdin.

**Files:**
- Create: `cmd/marshal/server_dashboard.go`
- Test: `cmd/marshal/server_dashboard_test.go`
- Modify: `cmd/marshal/server.go` (register the command)
- Modify: `go.mod` / `go.sum` (add `golang.org/x/term`)

**Interfaces:**
- Consumes: `server.SetDashboardPassword`, `server.HasDashboardUserDir` (Task 1); `defaultServerDataDir` (existing in `cmd/marshal/server.go`).
- Produces: `serverPasswdCmd() *cobra.Command`, `readPassword(cmd *cobra.Command) (string, error)`.

- [ ] **Step 1: Add the x/term dependency**

Run:

```bash
go get golang.org/x/term@latest
```

Expected: `go.mod` gains `golang.org/x/term vX.Y.Z`.

- [ ] **Step 2: Write the failing test**

Create `cmd/marshal/server_dashboard_test.go`:

```go
package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"marshal/internal/server"
)

func TestServerPasswdSetsUser(t *testing.T) {
	dir := t.TempDir()

	// Feed the password via a pipe standing in for stdin (non-TTY path).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "hunter2\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	cmd := serverPasswdCmd()
	cmd.SetArgs([]string{"--data-dir", dir, "--user", "admin"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("passwd command: %v", err)
	}

	ok, err := server.HasDashboardUserDir(dir)
	if err != nil || !ok {
		t.Fatalf("HasDashboardUserDir = %v, %v", ok, err)
	}
	a, _, _ := server.LoadOrInitAuth(dir)
	if !a.VerifyDashboardUser("admin", "hunter2") {
		t.Fatal("password set by command not verifiable")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestServerPasswdSetsUser -v`
Expected: FAIL — `serverPasswdCmd` undefined.

- [ ] **Step 4: Implement the command**

Create `cmd/marshal/server_dashboard.go`:

```go
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"marshal/internal/server"
)

func serverPasswdCmd() *cobra.Command {
	var dataDir, user string
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Set the dashboard login password",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			pw, err := readPassword(cmd)
			if err != nil {
				return err
			}
			if err := server.SetDashboardPassword(dataDir, user, pw); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dashboard user %q set\n", user)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.Flags().StringVar(&user, "user", "admin", "dashboard username")
	return cmd
}

// readPassword reads a password with no echo when stdin is a terminal (with a
// confirmation), or a single line from stdin otherwise (piped/scripted/tests).
func readPassword(cmd *cobra.Command) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(cmd.OutOrStdout(), "New password: ")
		p1, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		fmt.Fprint(cmd.OutOrStdout(), "Confirm:      ")
		p2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		if string(p1) != string(p2) {
			return "", errors.New("passwords do not match")
		}
		if len(p1) == 0 {
			return "", errors.New("empty password")
		}
		return string(p1), nil
	}
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return "", errors.New("no password on stdin")
	}
	pw := strings.TrimRight(sc.Text(), "\r\n")
	if pw == "" {
		return "", errors.New("empty password")
	}
	return pw, nil
}
```

- [ ] **Step 5: Register the command**

In `cmd/marshal/server.go`, change the `AddCommand` line:

```go
	cmd.AddCommand(serverFingerprintCmd(), serverTokenCmd(), serverAgentCmd())
```

to:

```go
	cmd.AddCommand(serverFingerprintCmd(), serverTokenCmd(), serverAgentCmd(), serverPasswdCmd())
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./cmd/marshal/ -run TestServerPasswdSetsUser -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add cmd/marshal/server_dashboard.go cmd/marshal/server_dashboard_test.go cmd/marshal/server.go go.mod go.sum
git commit -m "feat(cli): marshal server passwd to set the dashboard password

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Wire `--http-listen` into the server start path

Start the dashboard alongside gRPC when `--http-listen` is set, sharing the registry, auth store, and cert.

**Files:**
- Modify: `internal/server/server.go` (`ServeDir` signature + dashboard start)
- Modify: `internal/server/e2e_test.go` (update 4 `ServeDir` calls)
- Modify: `cmd/marshal/server.go` (`--http-listen` flag, pass-through, hint)
- Test: `internal/server/dashboard_serve_test.go`

**Interfaces:**
- Consumes: `dashboard.Serve` (Task 4); `*Registry` satisfies `dashboard.FleetLister`; `*AuthStore` satisfies `dashboard.Authenticator`.
- Produces: new `ServeDir` signature `ServeDir(ctx context.Context, lis net.Listener, dataDir, certPath, keyPath, httpAddr string, opts ...RegOption) error`.

- [ ] **Step 1: Write the failing integration test**

Create `internal/server/dashboard_serve_test.go`:

```go
package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServeDirStartsDashboard(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "pw"); err != nil {
		t.Fatal(err)
	}

	gl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Reserve a port for the dashboard, then release it for ServeDir to bind.
	hl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := hl.Addr().String()
	_ = hl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeDir(ctx, gl, dir, "", "", httpAddr) }()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	base := "https://" + httpAddr

	// Wait for the dashboard to come up.
	var resp *http.Response
	for i := 0; i < 100; i++ {
		resp, err = client.Get(base + "/api/fleet")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dashboard never came up: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("fleet without cookie = %d; want 401", resp.StatusCode)
	}

	resp, err = client.Post(base+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "marshal_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	req, _ := http.NewRequest("GET", base+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated fleet = %d; want 200", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestServeDirStartsDashboard -v`
Expected: FAIL — `ServeDir` does not accept an `httpAddr` argument (too many arguments).

- [ ] **Step 3: Update `ServeDir`**

In `internal/server/server.go`, add the dashboard import to the `import (...)` block:

```go
	"marshal/internal/dashboard"
```

Change the `ServeDir` signature from:

```go
func ServeDir(ctx context.Context, lis net.Listener, dataDir, certPath, keyPath string, opts ...RegOption) error {
```

to:

```go
func ServeDir(ctx context.Context, lis net.Listener, dataDir, certPath, keyPath, httpAddr string, opts ...RegOption) error {
```

Then change the final line of `ServeDir` from:

```go
	return Serve(ctx, lis, NewRegistry(opts...), ss, ls, cert, auth)
```

to:

```go
	reg := NewRegistry(opts...)
	if httpAddr != "" {
		if !auth.HasDashboardUser() {
			log.Printf("dashboard: no user set — run 'marshal server passwd'")
		}
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, auth, cert); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
		log.Printf("dashboard: serving on %s", httpAddr)
	}
	return Serve(ctx, lis, reg, ss, ls, cert, auth)
```

- [ ] **Step 4: Update the existing `ServeDir` callers in tests**

In `internal/server/e2e_test.go`, update all four calls to add the `httpAddr` argument (empty string) before the (absent) options. Change each occurrence of:

```go
ServeDir(ctx1, lis1, dataDir, "", "")
```
and
```go
ServeDir(ctx2, lis2, dataDir, "", "")
```

to add a fifth `""` argument:

```go
ServeDir(ctx1, lis1, dataDir, "", "", "")
```
```go
ServeDir(ctx2, lis2, dataDir, "", "", "")
```

(There are four total — two with `ctx1/lis1`, two with `ctx2/lis2`. Apply to all.)

- [ ] **Step 5: Add the `--http-listen` flag and pass it through**

In `cmd/marshal/server.go`, in `serverCmd()`:

Change the var declaration line:

```go
	var listen, dataDir, tlsCert, tlsKey string
```

to:

```go
	var listen, dataDir, tlsCert, tlsKey, httpListen string
```

Change the final `return server.ServeDir(...)` line:

```go
			return server.ServeDir(ctx, lis, dataDir, tlsCert, tlsKey)
```

to:

```go
			if httpListen != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "marshal server: dashboard on %s\n", httpListen)
			}
			return server.ServeDir(ctx, lis, dataDir, tlsCert, tlsKey, httpListen)
```

Add the flag registration after the existing `--tls-key` flag line:

```go
	cmd.Flags().StringVar(&httpListen, "http-listen", "", "address for the web dashboard (e.g. :9001); disabled if empty")
```

- [ ] **Step 6: Run the targeted test and the full server suite**

Run: `go test ./internal/server/ -run TestServeDirStartsDashboard -v && go test ./internal/server/ ./cmd/marshal/ -count=1`
Expected: PASS (including the previously-existing e2e tests with the updated signature).

- [ ] **Step 7: Format, vet, commit**

```bash
gofmt -w internal/server/ cmd/marshal/
go vet ./...
git add internal/server/server.go internal/server/e2e_test.go internal/server/dashboard_serve_test.go cmd/marshal/server.go
git commit -m "feat(server): start web dashboard when --http-listen is set

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: React + Vite SPA, build into `dist`, build tooling

Build the actual login + fleet-table UI, output it to `internal/dashboard/dist/`, and commit the build so `go build` stays Node-free.

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/tsconfig.node.json`, `web/index.html`
- Create: `web/src/main.tsx`, `web/src/api.ts`, `web/src/App.tsx`, `web/src/Login.tsx`, `web/src/Fleet.tsx`, `web/src/styles.css`
- Create: `Makefile` (with `ui` target)
- Modify: `.gitignore`
- Regenerate (commit): `internal/dashboard/dist/**`

**Interfaces:**
- Consumes: the HTTP API from Task 4 (`/api/login`, `/api/logout`, `/api/session`, `/api/fleet`).
- Produces: the built SPA under `internal/dashboard/dist/`, embedded by Task 4's `embed.go`.

- [ ] **Step 1: Fix `.gitignore` so the built `dist` is tracked**

In `.gitignore`, change the line:

```
dist/
```

to:

```
/dist/
web/node_modules/
```

(This keeps a stray top-level `dist/` ignored but allows `internal/dashboard/dist/` to be committed, and ignores frontend deps.)

- [ ] **Step 2: Create the frontend project files**

Create `web/package.json`:

```json
{
  "name": "marshal-dashboard",
  "private": true,
  "version": "0.0.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview"
  },
  "dependencies": {
    "react": "^18.3.1",
    "react-dom": "^18.3.1"
  },
  "devDependencies": {
    "@types/react": "^18.3.12",
    "@types/react-dom": "^18.3.1",
    "@vitejs/plugin-react": "^4.3.4",
    "typescript": "^5.6.3",
    "vite": "^5.4.11"
  }
}
```

Create `web/vite.config.ts`:

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build output lands in the Go package's dist dir, which is go:embed-ed.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/dashboard/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      // For `npm run dev` against a locally-running server (self-signed TLS).
      "/api": { target: "https://localhost:9001", changeOrigin: true, secure: false },
    },
  },
});
```

Create `web/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "resolveJsonModule": true,
    "isolatedModules": true,
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true
  },
  "include": ["src"],
  "references": [{ "path": "./tsconfig.node.json" }]
}
```

Create `web/tsconfig.node.json`:

```json
{
  "compilerOptions": {
    "composite": true,
    "skipLibCheck": true,
    "module": "ESNext",
    "moduleResolution": "bundler",
    "allowSyntheticDefaultImports": true,
    "strict": true
  },
  "include": ["vite.config.ts"]
}
```

Create `web/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Marshal</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 3: Create the React source**

Create `web/src/api.ts`:

```ts
export type Proc = {
  name: string;
  state: string;
  pid: number;
  uptime_ms: number;
  restarts: number;
  cpu: number;
  mem: number;
};

export type Agent = {
  name: string;
  connected: boolean;
  last_seen_unix: number;
  procs: Proc[];
};

export async function getSession(): Promise<string | null> {
  const r = await fetch("/api/session");
  if (r.status === 200) {
    const j = await r.json();
    return j.user as string;
  }
  return null;
}

export async function login(user: string, pass: string): Promise<boolean> {
  const r = await fetch("/api/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ User: user, Pass: pass }),
  });
  return r.status === 200;
}

export async function logout(): Promise<void> {
  await fetch("/api/logout", { method: "POST" });
}

export async function getFleet(): Promise<Agent[]> {
  const r = await fetch("/api/fleet");
  if (r.status === 401) throw new Error("unauthorized");
  return (await r.json()) as Agent[];
}
```

Create `web/src/Login.tsx`:

```tsx
import { useState } from "react";
import { login } from "./api";

export function Login({ onLogin }: { onLogin: () => void }) {
  const [user, setUser] = useState("admin");
  const [pass, setPass] = useState("");
  const [error, setError] = useState("");

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError("");
    if (await login(user, pass)) {
      onLogin();
    } else {
      setError("Invalid username or password.");
    }
  }

  return (
    <div className="login">
      <form onSubmit={submit}>
        <h1>Marshal</h1>
        <label>
          Username
          <input value={user} onChange={(e) => setUser(e.target.value)} autoFocus />
        </label>
        <label>
          Password
          <input type="password" value={pass} onChange={(e) => setPass(e.target.value)} />
        </label>
        {error && <p className="error">{error}</p>}
        <button type="submit">Sign in</button>
      </form>
    </div>
  );
}
```

Create `web/src/Fleet.tsx`:

```tsx
import { useEffect, useState } from "react";
import { Agent, getFleet, logout } from "./api";

function uptime(ms: number): string {
  if (ms <= 0) return "—";
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s % 60}s`;
  return `${s}s`;
}

export function Fleet({ onLogout }: { onLogout: () => void }) {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [err, setErr] = useState("");

  useEffect(() => {
    let stop = false;
    async function tick() {
      try {
        const f = await getFleet();
        if (!stop) {
          setAgents(f);
          setErr("");
        }
      } catch {
        if (!stop) onLogout();
      }
    }
    tick();
    const id = setInterval(tick, 2000);
    return () => {
      stop = true;
      clearInterval(id);
    };
  }, [onLogout]);

  async function doLogout() {
    await logout();
    onLogout();
  }

  return (
    <div className="fleet">
      <header>
        <h1>Fleet</h1>
        <button onClick={doLogout}>Sign out</button>
      </header>
      {err && <p className="error">{err}</p>}
      {agents.length === 0 && <p className="empty">No agents connected.</p>}
      {agents.map((a) => (
        <section key={a.name} className="agent">
          <h2>
            {a.name}{" "}
            <span className={a.connected ? "badge online" : "badge offline"}>
              {a.connected ? "online" : "offline"}
            </span>
          </h2>
          <table>
            <thead>
              <tr>
                <th>Process</th>
                <th>State</th>
                <th>PID</th>
                <th>Uptime</th>
                <th>Restarts</th>
              </tr>
            </thead>
            <tbody>
              {a.procs.map((p) => (
                <tr key={`${p.name}-${p.pid}`}>
                  <td>{p.name}</td>
                  <td>{p.state}</td>
                  <td>{p.pid || "—"}</td>
                  <td>{uptime(p.uptime_ms)}</td>
                  <td>{p.restarts}</td>
                </tr>
              ))}
              {a.procs.length === 0 && (
                <tr>
                  <td colSpan={5} className="empty">
                    No processes.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      ))}
    </div>
  );
}
```

Create `web/src/App.tsx`:

```tsx
import { useEffect, useState } from "react";
import { getSession } from "./api";
import { Login } from "./Login";
import { Fleet } from "./Fleet";

export function App() {
  const [authed, setAuthed] = useState<boolean | null>(null);

  useEffect(() => {
    getSession().then((u) => setAuthed(u !== null));
  }, []);

  if (authed === null) return <div className="loading">Loading…</div>;
  if (!authed) return <Login onLogin={() => setAuthed(true)} />;
  return <Fleet onLogout={() => setAuthed(false)} />;
}
```

Create `web/src/main.tsx`:

```tsx
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
```

Create `web/src/styles.css`:

```css
:root {
  font-family: system-ui, -apple-system, sans-serif;
  color: #1a1a2e;
  background: #f4f5f7;
}
* { box-sizing: border-box; }
body { margin: 0; }
.loading { padding: 2rem; color: #666; }
.error { color: #c0392b; }
.empty { color: #888; font-style: italic; }

.login {
  display: flex;
  min-height: 100vh;
  align-items: center;
  justify-content: center;
}
.login form {
  background: #fff;
  padding: 2rem;
  border-radius: 10px;
  box-shadow: 0 8px 30px rgba(0, 0, 0, 0.08);
  width: 320px;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.login h1 { margin: 0 0 0.5rem; }
.login label { display: flex; flex-direction: column; font-size: 0.85rem; gap: 0.25rem; }
.login input {
  padding: 0.5rem;
  border: 1px solid #ccc;
  border-radius: 6px;
  font-size: 1rem;
}
button {
  background: #2d6cdf;
  color: #fff;
  border: 0;
  padding: 0.55rem 1rem;
  border-radius: 6px;
  font-size: 0.95rem;
  cursor: pointer;
}
button:hover { background: #245bc0; }

.fleet { max-width: 920px; margin: 0 auto; padding: 1.5rem; }
.fleet header { display: flex; align-items: center; justify-content: space-between; }
.agent { background: #fff; border-radius: 10px; padding: 1rem 1.25rem; margin: 1rem 0; box-shadow: 0 2px 10px rgba(0,0,0,0.05); }
.agent h2 { font-size: 1.1rem; display: flex; align-items: center; gap: 0.5rem; }
.badge { font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 999px; text-transform: uppercase; letter-spacing: 0.04em; }
.badge.online { background: #d7f5e3; color: #1d8a4f; }
.badge.offline { background: #f5d7d7; color: #b03030; }
table { width: 100%; border-collapse: collapse; margin-top: 0.5rem; }
th, td { text-align: left; padding: 0.4rem 0.5rem; border-bottom: 1px solid #eee; font-size: 0.9rem; }
th { color: #666; font-weight: 600; }
```

- [ ] **Step 4: Create the Makefile target**

Create `Makefile` (at repo root):

```makefile
.PHONY: ui build test

# Build the web dashboard SPA into internal/dashboard/dist (embedded by Go).
ui:
	cd web && npm install && npm run build

# Build the marshal binary.
build:
	go build -o marshal ./cmd/marshal

test:
	go test ./... -race -count=1
```

- [ ] **Step 5: Install deps and build the SPA**

Run:

```bash
make ui
```

Expected: `npm install` resolves, `vite build` writes hashed assets and `index.html` into `internal/dashboard/dist/` (replacing the Task 4 placeholder). Verify:

```bash
ls internal/dashboard/dist
```
Expected: `index.html` and an `assets/` directory.

- [ ] **Step 6: Verify the Go build embeds the real SPA and tests still pass**

Run: `go build ./... && go test ./internal/dashboard/ -count=1`
Expected: build succeeds; `TestSPAFallback` still passes (real `index.html` contains `<html`).

- [ ] **Step 7: Commit (including the built dist)**

```bash
git add web/ Makefile .gitignore internal/dashboard/dist
git commit -m "feat(dashboard): React+Vite SPA (login + live fleet table)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Full verification + handoff

Run the complete gate, perform a manual smoke test, and write the milestone handoff.

**Files:**
- Create: `docs/handoffs/2026-06-18-m11-dashboard.md`

- [ ] **Step 1: Run the full gate**

Run:

```bash
gofmt -l .
go vet ./...
go test ./... -race -count=1
go build -o marshal ./cmd/marshal
```

Expected: `gofmt -l .` prints nothing; vet clean; all tests pass; binary builds.

- [ ] **Step 2: Manual smoke test**

Run (in a scratch data dir):

```bash
export XDG_DATA_HOME=/tmp/m11smoke
rm -rf "$XDG_DATA_HOME"
printf 'smokepw\n' | ./marshal server passwd --user admin
./marshal server --listen :9100 --http-listen :9101 &
```

Then:
1. Open `https://localhost:9101/` in a browser (accept the self-signed cert warning).
2. Log in with `admin` / `smokepw` — the fleet view should load (empty fleet initially).
3. In another terminal, start an agent that enrolls against `:9100` (per the M10 handoff's run instructions) and confirm it appears in the dashboard table within ~2s with an `online` badge and its processes.
4. Click **Sign out** — confirm you return to the login screen and a refresh stays logged out.

Record the result in the handoff. Stop the server (`kill %1`) when done.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-18-m11-dashboard.md` documenting: current state (M11 complete, branch name, gate green), what was built (the dashboard package, auth credentials, CLI `passwd`, `--http-listen`, the SPA), build/run/test instructions (including `make ui`), the smoke-test result, deferred items (charts M12 / log tailing M13 / multi-user M13+ / controls M14 / in-memory sessions / self-signed cert warning / no login rate-limiting), and the concrete next step (final whole-branch review + merge, then M12 metric charts).

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-18-m11-dashboard.md
git commit -m "docs: M11 web-dashboard handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review notes (for the executor)

- **Spec coverage:** Section 3 (package layout) → Tasks 2–4, 7. §4 (API/auth/sessions) → Tasks 1, 2, 4. §5 (data wiring & CLI) → Tasks 5, 6. §6 (frontend & build) → Task 7. §7 (testing) → tests in Tasks 1–6 + manual smoke in Task 8. §8 (deferred) → recorded in Task 8 handoff.
- **Branch:** do this work on a branch (e.g. `m11-dashboard`), not `main`, per project conventions. Create it before Task 1.
- **Embed ordering:** Task 4 commits a placeholder `dist/index.html` so `go:embed dist` compiles; Task 7's `vite build` (`emptyOutDir: true`) replaces it with the real SPA. Do not skip the placeholder — the package will not compile without at least one file under `dist/`.
