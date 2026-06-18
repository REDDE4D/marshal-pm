# Marshal Dashboard M16 — Session Invalidation on Credential Change — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Invalidate a dashboard user's active sessions when that user's password changes, on the session's next authenticated request.

**Architecture:** Stamp each session with an opaque fingerprint of the credential generation that minted it (derived from the per-credential random salt). On every authenticated request, compare the session's stamp against the user's *current* stamp from the (hot-reloading) `AuthStore`; a mismatch — or a now-unknown user — invalidates the session. The session store stays credential-agnostic; the comparison lives in the handler.

**Tech Stack:** Go 1.26, standard library only (`crypto/sha256`, `encoding/hex`, `net/http`). No new dependencies.

## Global Constraints

- Module path `marshal`; imports are `marshal/internal/...`.
- TDD: failing test first, then minimal implementation.
- Gate before finishing: `go build -o marshal ./cmd/marshal`, `go test ./... -race -count=1`, `gofmt -l .` prints nothing, `go vet ./...` clean.
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work on a branch `m16-session-invalidation`, not `main`.
- No proto, agent, or manager changes. Touch only `internal/server` (auth) and `internal/dashboard`.
- `NewHandler`'s exported signature must not change.

---

## File structure

- `internal/server/auth.go` — add `DashboardCredentialStamp` method (new import `encoding/hex`).
- `internal/server/auth_test.go` — unit tests for the new method.
- `internal/dashboard/handlers.go` — extend `Authenticator` interface; wire `login` + `requireSession`.
- `internal/dashboard/session.go` — add `Stamp` field; change `create`/`validate` signatures.
- `internal/dashboard/server_test.go` — add `DashboardCredentialStamp` to `fakeAuth` and `countingAuth`; add a mutable `stampAuth` helper.
- `internal/dashboard/session_test.go` — update `create`/`validate` call sites to the new signatures.
- `internal/dashboard/invalidation_test.go` (new) — behavior tests for stamp enforcement.
- `internal/server/dashboard_serve_test.go` — add an integration test wiring a real `AuthStore` to the dashboard handler.

---

## Task 1: `AuthStore.DashboardCredentialStamp`

**Files:**
- Modify: `internal/server/auth.go`
- Test: `internal/server/auth_test.go`

**Interfaces:**
- Consumes: existing `AuthStore.data.Users` (`map[string]dashboardUser{PBKDF2, Salt, Iter}`), `a.mu`.
- Produces: `func (a *AuthStore) DashboardCredentialStamp(user string) (string, bool)` — opaque hex stamp, `ok=false` for an unknown user. Stamp changes whenever the password is (re)set (salt is regenerated each time).

- [ ] **Step 1: Write the failing test**

Add to `internal/server/auth_test.go`:

```go
func TestDashboardCredentialStamp(t *testing.T) {
	dir := t.TempDir()
	a, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.DashboardCredentialStamp("admin"); ok {
		t.Fatal("stamp ok=true for unknown user")
	}
	if err := a.SetDashboardUser("admin", "pw"); err != nil {
		t.Fatal(err)
	}
	s1, ok := a.DashboardCredentialStamp("admin")
	if !ok || s1 == "" {
		t.Fatalf("no stamp after SetDashboardUser (ok=%v, s=%q)", ok, s1)
	}
	if s1b, _ := a.DashboardCredentialStamp("admin"); s1b != s1 {
		t.Fatalf("stamp not stable: %q vs %q", s1, s1b)
	}
	// A new password (fresh random salt) must change the stamp.
	if err := a.SetDashboardUser("admin", "pw2"); err != nil {
		t.Fatal(err)
	}
	if s2, _ := a.DashboardCredentialStamp("admin"); s2 == s1 {
		t.Fatal("stamp unchanged after password change")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestDashboardCredentialStamp -count=1`
Expected: FAIL — `a.DashboardCredentialStamp undefined`.

- [ ] **Step 3: Add the `encoding/hex` import and the method**

In `internal/server/auth.go`, add `"encoding/hex"` to the import block (keep imports sorted: it goes between `encoding/base64` and `encoding/json`).

Add this method (place it next to `VerifyDashboardUser`):

```go
// DashboardCredentialStamp returns an opaque fingerprint of user's current
// dashboard credential, or ok=false if user has no credential. The fingerprint
// changes whenever the password is (re)set, because SetDashboardUser draws a
// fresh random salt each time. It reveals nothing useful: a hash over an
// already-hashed secret plus its salt and iteration count.
func (a *AuthStore) DashboardCredentialStamp(user string) (string, bool) {
	a.mu.Lock()
	u, ok := a.data.Users[user]
	a.mu.Unlock()
	if !ok {
		return "", false
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s.%s.%d", u.PBKDF2, u.Salt, u.Iter)))
	return hex.EncodeToString(sum[:]), true
}
```

(`sha256` and `fmt` are already imported.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/server/ -run TestDashboardCredentialStamp -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/auth.go internal/server/auth_test.go
git commit -m "feat(server): add DashboardCredentialStamp for session invalidation

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Enforce the stamp in the dashboard

This task is one compilation unit: extending the `Authenticator` interface breaks
every test fake until updated, and changing the session-store signatures ripples
into the handler and session tests. All of it lands in one green commit.

**Files:**
- Modify: `internal/dashboard/handlers.go`, `internal/dashboard/session.go`
- Modify (tests): `internal/dashboard/server_test.go`, `internal/dashboard/session_test.go`
- Create (test): `internal/dashboard/invalidation_test.go`

**Interfaces:**
- Consumes: `Authenticator.DashboardCredentialStamp(user string) (string, bool)` from Task 1.
- Produces:
  - `session{User, Stamp string, Expiry time.Time}`
  - `(*sessionStore).create(user, stamp string) (string, error)`
  - `(*sessionStore).validate(tok string) (user, stamp string, ok bool)`
  - `Authenticator` interface with both `VerifyDashboardUser` and `DashboardCredentialStamp`.

- [ ] **Step 1: Write the failing behavior test**

Create `internal/dashboard/invalidation_test.go`:

```go
package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stampAuth is a mutable Authenticator: tests flip stamp/known to simulate a
// password change or a deleted user between requests.
type stampAuth struct {
	user, pass string
	stamp      string
	known      bool
}

func (s *stampAuth) VerifyDashboardUser(u, p string) bool { return u == s.user && p == s.pass }
func (s *stampAuth) DashboardCredentialStamp(u string) (string, bool) {
	if !s.known || u != s.user {
		return "", false
	}
	return s.stamp, true
}

func loginCookie(t *testing.T, c *http.Client, base string) *http.Cookie {
	t.Helper()
	resp, err := c.Post(base+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200", resp.StatusCode)
	}
	ck := sessionCookieFrom(resp)
	if ck == nil {
		t.Fatal("login set no session cookie")
	}
	return ck
}

func fleetStatus(t *testing.T, c *http.Client, base string, ck *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/fleet", nil)
	req.AddCookie(ck)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestSessionSurvivesUnchangedStamp(t *testing.T) {
	auth := &stampAuth{user: "admin", pass: "pw", stamp: "s1", known: true}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()
	ck := loginCookie(t, c, srv.URL)
	if got := fleetStatus(t, c, srv.URL, ck); got != http.StatusOK {
		t.Fatalf("fleet with unchanged stamp = %d; want 200", got)
	}
}

func TestSessionDiesOnStampChange(t *testing.T) {
	auth := &stampAuth{user: "admin", pass: "pw", stamp: "s1", known: true}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()
	ck := loginCookie(t, c, srv.URL)
	auth.stamp = "s2" // password changed under the session
	if got := fleetStatus(t, c, srv.URL, ck); got != http.StatusUnauthorized {
		t.Fatalf("fleet after stamp change = %d; want 401", got)
	}
}

func TestSessionDiesWhenUserGone(t *testing.T) {
	auth := &stampAuth{user: "admin", pass: "pw", stamp: "s1", known: true}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()
	ck := loginCookie(t, c, srv.URL)
	auth.known = false // user deleted under the session
	if got := fleetStatus(t, c, srv.URL, ck); got != http.StatusUnauthorized {
		t.Fatalf("fleet after user removed = %d; want 401", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails to compile**

Run: `go test ./internal/dashboard/ -run TestSession -count=1`
Expected: FAIL — `*stampAuth` does not implement `Authenticator` is not yet the error; instead the build fails because the interface still has one method and `NewHandler` accepts it, but `create`/`validate` mismatches appear once we change them. At this point the test simply fails to compile against the *current* interface only if `DashboardCredentialStamp` is unused — it compiles but the assertions fail (stamp not enforced). Accept either: a compile error or `TestSessionDiesOnStampChange` returning 200 instead of 401.

- [ ] **Step 3: Extend the `Authenticator` interface**

In `internal/dashboard/handlers.go`:

```go
// Authenticator verifies dashboard credentials. *server.AuthStore satisfies it.
type Authenticator interface {
	VerifyDashboardUser(user, password string) bool
	DashboardCredentialStamp(user string) (string, bool)
}
```

- [ ] **Step 4: Add `Stamp` and update the session store**

In `internal/dashboard/session.go`, change the struct:

```go
type session struct {
	User   string    `json:"user"`
	Stamp  string    `json:"stamp"`
	Expiry time.Time `json:"expiry"`
}
```

Change `create` to take a stamp:

```go
// create mints a random 256-bit session token for user, recording the
// credential stamp under which it was minted, and returns the plaintext token;
// the store keeps only its hash.
func (s *sessionStore) create(user, stamp string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.m[hashSessionToken(tok)] = session{User: user, Stamp: stamp, Expiry: s.now().Add(s.ttl)}
	s.persistLocked()
	s.mu.Unlock()
	return tok, nil
}
```

Change `validate` to also return the stamp:

```go
// validate returns the user and credential stamp for a live token, or ok=false
// if the token is unknown or expired (expired tokens are removed). The caller
// compares the stamp against the user's current credential.
func (s *sessionStore) validate(tok string) (string, string, bool) {
	h := hashSessionToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[h]
	if !ok {
		return "", "", false
	}
	if !s.now().Before(sess.Expiry) {
		delete(s.m, h)
		s.persistLocked()
		return "", "", false
	}
	return sess.User, sess.Stamp, true
}
```

- [ ] **Step 5: Wire the handler**

In `internal/dashboard/handlers.go`, in `login`, replace the session-create block:

```go
	h.limiter.reset(key)
	stamp, _ := h.auth.DashboardCredentialStamp(body.User)
	tok, err := h.sessions.create(body.User, stamp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
```

Replace `requireSession`:

```go
func (h *handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user, stamp, ok := h.sessions.validate(c.Value)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Invalidate if the credential changed (stamp mismatch) or the user is
		// gone. The session dies on its next request; no push needed.
		cur, exists := h.auth.DashboardCredentialStamp(user)
		if !exists || cur != stamp {
			h.sessions.delete(c.Value)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}
```

- [ ] **Step 6: Update the existing test fakes and call sites**

In `internal/dashboard/server_test.go`, add the method to both fakes:

```go
func (f fakeAuth) DashboardCredentialStamp(u string) (string, bool) {
	if u != f.user {
		return "", false
	}
	return "stamp-" + f.pass, true
}

func (c *countingAuth) DashboardCredentialStamp(u string) (string, bool) {
	if u != c.user {
		return "", false
	}
	return "stamp", true
}
```

In `internal/dashboard/session_test.go`, update every call site:
- `s.create("admin")` → `s.create("admin", "")` (same for `"a"`, `"b"`, `"c"`).
- `user, ok := s.validate(tok)` → `user, _, ok := s.validate(tok)`.
- `_, ok := s.validate(tok)` → `_, _, ok := s.validate(tok)`.
- `s2.validate(tok)` in `if _, ok :=` forms → `if _, _, ok :=`.

(13 call sites total per the grep; the change is purely the extra return value / extra `""` argument. No assertion logic changes.)

- [ ] **Step 7: Run the dashboard package tests**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS — new invalidation tests pass; all pre-existing session/login/limiter/control/logs/metrics tests still pass.

- [ ] **Step 8: Commit**

```bash
git add internal/dashboard/handlers.go internal/dashboard/session.go \
  internal/dashboard/server_test.go internal/dashboard/session_test.go \
  internal/dashboard/invalidation_test.go
git commit -m "feat(dashboard): invalidate sessions on credential-stamp change

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: End-to-end integration test against a real AuthStore

Proves the whole path with the real stamp implementation and a real `Reload`,
without waiting on the 3s poll: build the dashboard handler directly on a
`*server.AuthStore`, change the password on disk, `Reload()`, and confirm the
old cookie now 401s.

**Files:**
- Test: `internal/server/dashboard_serve_test.go` (add one test; the package already imports `dashboard` via `dashboard_serve.go`).

**Interfaces:**
- Consumes: `LoadOrInitAuth`, `SetDashboardPassword`, `(*AuthStore).Reload`, `dashboard.NewHandler`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/dashboard_serve_test.go` (ensure imports include
`net/http`, `net/http/httptest`, `strings`, `time`, and `marshal/internal/dashboard`):

```go
func TestDashboardSessionDiesAfterPasswordChange(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "old"); err != nil {
		t.Fatal(err)
	}
	a, _, err := LoadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := dashboard.NewHandler(nil, nil, nil, nil, a, time.Hour)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := srv.Client()

	resp, err := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200", resp.StatusCode)
	}
	var ck *http.Cookie
	for _, x := range resp.Cookies() {
		if x.Name == "marshal_session" {
			ck = x
		}
	}
	if ck == nil {
		t.Fatal("no session cookie")
	}

	status := func() int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/fleet", nil)
		req.AddCookie(ck)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode
	}

	if got := status(); got != http.StatusOK {
		t.Fatalf("fleet before change = %d; want 200", got)
	}

	// Change the password on disk (as `server passwd` would) and hot-reload.
	if err := SetDashboardPassword(dir, "admin", "new"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("fleet after password change = %d; want 401", got)
	}
}
```

Note: `dashboard.NewHandler` tolerates nil lister/metrics/logs/controller here because the only routes exercised are `/api/login` and `/api/fleet`; `/api/fleet`'s 401 path returns before touching the lister. If `fleetView(nil)` panics on the *200* branch, the "before change" call would fail loudly — that is itself a useful assertion. If it does panic, pass `fakeLister`-equivalent stubs; the server package has its own stubs in `dashboard_serve_test.go` / `stores_test.go` — reuse the lightest one that satisfies `dashboard.FleetLister`.

- [ ] **Step 2: Run test to verify it fails (or passes) meaningfully**

Run: `go test ./internal/server/ -run TestDashboardSessionDiesAfterPasswordChange -race -count=1`
Expected before Tasks 1–2 are present: compile/behavior failure. With Tasks 1–2 done: PASS. (If it panics on the 200 branch due to a nil lister, swap in a stub satisfying `dashboard.FleetLister` and re-run.)

- [ ] **Step 3: Commit**

```bash
git add internal/server/dashboard_serve_test.go
git commit -m "test(server): session dies after password change + reload (e2e)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Full gate, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m16-session-invalidation.md`

- [ ] **Step 1: Run the full gate**

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .            # must print nothing
go vet ./...
```
Expected: all packages PASS, `gofmt` silent, `vet` clean.

- [ ] **Step 2: Live demo (per CLAUDE.md convention)**

On a scratch data dir (`XDG_DATA_HOME=/tmp/marshal-m16-demo/...`): set a password while the server is down, start with `--http-listen`, log in (capture the cookie), confirm an authenticated request returns 200; then run `marshal server passwd` to change the password on the *running* server, wait past the ~3s reload poll, and confirm the **same cookie now 401s** while a fresh login with the new password works. Tear down (stop server, remove scratch dir) and confirm `pgrep -fl marshal` shows no demo orphans.

- [ ] **Step 3: Write the handoff**

Write `docs/handoffs/2026-06-18-m16-session-invalidation.md` covering: current state + branch, what changed and why (the stamp mechanism, the back-compat empty-stamp behavior), build/run/test, the live-demo result, deferred items, and the concrete next step. Commit it.

- [ ] **Step 4: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to merge `m16-session-invalidation` to `main`.

---

## Self-review

**Spec coverage:**
- Stamp mechanism (`SHA-256(PBKDF2.Salt.Iter)`) → Task 1. ✅
- `Authenticator` gains `DashboardCredentialStamp` → Task 2 Step 3. ✅
- `session.Stamp` + `create`/`validate` signature change → Task 2 Steps 4. ✅
- `login` stamps the session; `requireSession` compares + deletes on mismatch/unknown → Task 2 Step 5. ✅
- Back-compat: pre-upgrade empty stamp forces re-login → covered by `cur != stamp` logic (empty ≠ real); integration covers the live path. ✅
- Unknown/deleted user invalidation → `TestSessionDiesWhenUserGone` (Task 2) + `!exists` branch. ✅
- Persistence of the new field → `Stamp` has a `json` tag; existing persist tests in `session_test.go` exercise the write/read path. ✅
- Server-side stamp unit behavior → `TestDashboardCredentialStamp` (Task 1). ✅
- End-to-end with real Reload → Task 3. ✅
- Gate + live demo + handoff → Task 4. ✅

**Placeholder scan:** none — all steps carry concrete code or commands.

**Type consistency:** `DashboardCredentialStamp(string) (string, bool)`, `create(user, stamp string)`, `validate(tok) (string, string, bool)`, `session{User, Stamp, Expiry}` used identically across Tasks 1–3. ✅
