# M15 Dashboard Auth Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make dashboard logins survive a server restart, rate-limit failed logins, and let a running server pick up `passwd`/`token --rotate` changes without a restart.

**Architecture:** Three independent changes over the existing `internal/dashboard` and `internal/server` packages. (1) The in-memory session map becomes disk-backed, keyed by token *hash*, persisted atomically to `sessions.json`. (2) A new per-(user,IP) lockout limiter guards `POST /api/login`. (3) `AuthStore` gains a mtime-gated `Reload()` plus a background poll goroutine started in `ServeDir`. The shared `*AuthStore` means both dashboard and gRPC interceptors benefit from reload.

**Tech Stack:** Go (stdlib only — `crypto/sha256`, `encoding/json`, `os`, `net`, `time`, `sync`). No new dependencies. Tests with the standard `testing` package + `net/http/httptest`.

## Global Constraints

- **Stdlib only** — no new third-party dependencies (project is stdlib + gRPC/protobuf).
- **TDD** — write the failing test first, watch it fail, then implement.
- **Gate before finishing each task:** `go test ./... -race -count=1`, `gofmt -l .` (must print nothing), `go vet ./...`, `go build -o marshal ./cmd/marshal`.
- **Secrets at rest are hashed, never plaintext** — session tokens stored as `sha256` hex, matching the password-hashing posture.
- **Backward compatibility:** an empty persistence path ⇒ pure in-memory (no file I/O), so existing tests that build handlers without a data dir keep working unchanged.
- **Commit message trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Branch:** do this work on a feature branch `m15-auth-hardening`, not `main`.
- **Parameters (single source of truth):** session token = 256-bit random base64url, stored as `sha256` hex; session file = `<dataDir>/sessions.json`, mode `0600`, atomic tmp+rename; session TTL = 24h; lockout threshold = 5 consecutive failures per (user, IP); backoff = 1 min, ×2 per repeat, cap 15 min; locked response = HTTP 429 + `Retry-After` seconds; hot-reload poll ≈ 3s, no-op when mtime unchanged.

---

### Task 0: Create the feature branch

- [ ] **Step 1: Branch off main**

```bash
git checkout -b m15-auth-hardening
git status
```

Expected: `On branch m15-auth-hardening`, clean tree (the spec + plan are already committed on main).

---

### Task 1: Disk-backed session store

Re-key the session map by token hash and persist it to disk. An empty path keeps the store purely in-memory.

**Files:**
- Modify: `internal/dashboard/session.go`
- Test: `internal/dashboard/session_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func newSessionStore(ttl time.Duration, now func() time.Time, path string) *sessionStore` — **path "" ⇒ in-memory only**.
  - `func hashSessionToken(tok string) string` — hex SHA-256 of a token (used by tests to look up `s.m`).
  - `session` struct fields are now exported: `User string`, `Expiry time.Time` (JSON-serialized).
  - `create`, `validate`, `delete`, `sweep`, `sweepLoop` keep their existing signatures and external behavior.

- [ ] **Step 1: Write the failing persistence tests**

Add to `internal/dashboard/session_test.go`:

```go
func TestSessionPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	now := time.Unix(1000, 0)

	s1 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	tok, err := s1.create("admin")
	if err != nil {
		t.Fatal(err)
	}

	// A brand-new store at the same path (simulating a restart) sees the session.
	s2 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	user, ok := s2.validate(tok)
	if !ok || user != "admin" {
		t.Fatalf("after reload validate = %q, %v; want admin, true", user, ok)
	}
}

func TestSessionLoadDropsExpired(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	now := time.Unix(1000, 0)

	s1 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	tok, _ := s1.create("admin")

	// Reload after the TTL has elapsed: the expired entry must be dropped.
	later := now.Add(2 * time.Hour)
	s2 := newSessionStore(time.Hour, func() time.Time { return later }, path)
	if _, ok := s2.validate(tok); ok {
		t.Fatal("expired session survived reload")
	}
}

func TestSessionDeletePersists(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	now := time.Unix(1000, 0)

	s1 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	tok, _ := s1.create("admin")
	s1.delete(tok)

	s2 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	if _, ok := s2.validate(tok); ok {
		t.Fatal("deleted session reappeared after reload")
	}
}

func TestSessionEmptyPathNoFile(t *testing.T) {
	dir := t.TempDir()
	s := newSessionStore(time.Hour, nil, "")
	if _, err := s.create("admin"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("in-memory store wrote files: %v", entries)
	}
}

func TestSessionCorruptFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSessionStore(time.Hour, nil, path) // must not panic
	if _, ok := s.validate("anything"); ok {
		t.Fatal("validated against a corrupt-loaded store")
	}
}
```

Then update the five **existing** `newSessionStore(...)` callers in this file to pass the new `path` argument (in-memory). Change every `newSessionStore(time.Hour, func() time.Time { return now })` to `newSessionStore(time.Hour, func() time.Time { return now }, "")`.

Also fix the two existing tests that index the map by the **raw token** — after re-keying, the map key is the hash:
- In `TestSessionSweep` change `s.m[tok]` to `s.m[hashSessionToken(tok)]`.
- In `TestSweepLoop` change both `_, present := s.m[tok]` lookups to `s.m[hashSessionToken(tok)]`.

Add `"os"` to the test file's import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dashboard/ -run 'TestSession|TestSweep' -count=1`
Expected: compile error first (`newSessionStore` takes 2 args, `hashSessionToken` undefined). That counts as the failing state — proceed to implement.

- [ ] **Step 3: Rewrite `internal/dashboard/session.go`**

Replace the entire file contents with:

```go
package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

type session struct {
	User   string    `json:"user"`
	Expiry time.Time `json:"expiry"`
}

// sessionStore maps a session-token *hash* to a session. When path is non-empty
// the map is persisted to that file (atomic write, 0600) and reloaded on
// construction, so sessions survive a server restart. An empty path keeps the
// store purely in-memory.
type sessionStore struct {
	ttl  time.Duration
	now  func() time.Time
	path string
	mu   sync.Mutex
	m    map[string]session
}

// hashSessionToken returns the hex SHA-256 of a session token. The plaintext
// token lives only in the user's cookie; memory and disk hold only the hash.
func hashSessionToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// newSessionStore returns a store with the given session lifetime. If now is
// nil, time.Now is used. If path is non-empty, persisted sessions are loaded
// from it (expired entries dropped) and every mutation is written back.
func newSessionStore(ttl time.Duration, now func() time.Time, path string) *sessionStore {
	if now == nil {
		now = time.Now
	}
	s := &sessionStore{ttl: ttl, now: now, path: path, m: map[string]session{}}
	if path != "" {
		s.load()
	}
	return s
}

// create mints a random 256-bit session token for user and returns the
// plaintext token; the store keeps only its hash.
func (s *sessionStore) create(user string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.m[hashSessionToken(tok)] = session{User: user, Expiry: s.now().Add(s.ttl)}
	s.persistLocked()
	s.mu.Unlock()
	return tok, nil
}

// validate returns the user for a live token, or ok=false if the token is
// unknown or expired (expired tokens are removed).
func (s *sessionStore) validate(tok string) (string, bool) {
	h := hashSessionToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[h]
	if !ok {
		return "", false
	}
	if !s.now().Before(sess.Expiry) {
		delete(s.m, h)
		s.persistLocked()
		return "", false
	}
	return sess.User, true
}

// delete removes a token (logout).
func (s *sessionStore) delete(tok string) {
	s.mu.Lock()
	delete(s.m, hashSessionToken(tok))
	s.persistLocked()
	s.mu.Unlock()
}

// sweep removes all expired sessions.
func (s *sessionStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	changed := false
	for h, sess := range s.m {
		if !now.Before(sess.Expiry) {
			delete(s.m, h)
			changed = true
		}
	}
	if changed {
		s.persistLocked()
	}
}

// sweepLoop periodically removes expired sessions until ctx is canceled.
func (s *sessionStore) sweepLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep()
		}
	}
}

// persistLocked atomically writes the session map to disk. Caller holds s.mu.
// No-op for an in-memory store (path == "").
func (s *sessionStore) persistLocked() {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.m)
	if err != nil {
		log.Printf("dashboard: marshal sessions: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("dashboard: write sessions: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("dashboard: rename sessions: %v", err)
	}
}

// load reads persisted sessions, dropping expired entries. A missing file is
// fine; a corrupt file logs and leaves the store empty.
func (s *sessionStore) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("dashboard: read sessions: %v", err)
		}
		return
	}
	var m map[string]session
	if err := json.Unmarshal(b, &m); err != nil {
		log.Printf("dashboard: parse sessions, starting empty: %v", err)
		return
	}
	now := s.now()
	for h, sess := range m {
		if now.Before(sess.Expiry) {
			s.m[h] = sess
		}
	}
}
```

- [ ] **Step 4: Update the in-memory caller in `handlers.go`**

In `internal/dashboard/handlers.go`, change the session-store construction (line ~44) from:

```go
		sessions:    newSessionStore(ttl, nil),
```

to:

```go
		sessions:    newSessionStore(ttl, nil, ""),
```

(Task 2 replaces this `""` with a real path threaded through `newHandler`.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dashboard/ -run 'TestSession|TestSweep' -count=1`
Expected: PASS (all session tests).

- [ ] **Step 6: Full gate + commit**

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./... && go build -o marshal ./cmd/marshal
git add internal/dashboard/session.go internal/dashboard/session_test.go internal/dashboard/handlers.go
git commit -m "feat(dashboard): disk-backed session store keyed by token hash

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

Expected: tests PASS, `gofmt -l .` prints nothing.

---

### Task 2: Thread the session-persistence path through the server

Wire `<dataDir>/sessions.json` from `ServeDir` down to the session store so the real server persists, while keeping the test-facing `NewHandler` signature unchanged.

**Files:**
- Modify: `internal/dashboard/handlers.go` (`newHandler` gains a `sessionsPath` param; `NewHandler` unchanged)
- Modify: `internal/dashboard/server.go` (`Serve` gains a `sessionsPath` param)
- Modify: `internal/server/server.go` (`ServeDir` passes the path)
- Test: `internal/dashboard/server_test.go`

**Interfaces:**
- Consumes: `newSessionStore(ttl, now, path)` from Task 1.
- Produces:
  - `func newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration, sessionsPath string) *handler`
  - `func NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration) http.Handler` — **signature unchanged**; calls `newHandler(..., "")`.
  - `func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath string) error` — gains a trailing `sessionsPath`.

- [ ] **Step 1: Write the failing cross-restart handler test**

Add to `internal/dashboard/server_test.go`:

```go
func TestSessionSurvivesHandlerRestart(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	auth := fakeAuth{user: "admin", pass: "pw"}

	// First handler: log in, capture the cookie.
	h1 := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour, path)
	srv1 := httptest.NewServer(h1.mux)
	c1 := srv1.Client()
	resp, _ := c1.Post(srv1.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	cookie := sessionCookieFrom(resp)
	srv1.Close()
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	// Second handler at the same path (simulating a restart): the cookie still validates.
	h2 := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour, path)
	srv2 := httptest.NewServer(h2.mux)
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, _ = srv2.Client().Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-restart fleet = %d; want 200 (session not persisted)", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run TestSessionSurvivesHandlerRestart -count=1`
Expected: compile error (`newHandler` takes 6 args, not 7). Proceed.

- [ ] **Step 3: Add `sessionsPath` to `newHandler`**

In `internal/dashboard/handlers.go`:

Change the `newHandler` signature and the session-store line:

```go
// newHandler builds a *handler (with its mux) for the given session lifetime.
// sessionsPath persists sessions to disk; "" keeps them in-memory.
func newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration, sessionsPath string) *handler {
	files := staticFS()
	h := &handler{
		lister:      lister,
		metricsHist: metrics,
		logsHist:    logs,
		controller:  controller,
		auth:        auth,
		sessions:    newSessionStore(ttl, nil, sessionsPath),
		files:       files,
		static:      http.FileServer(http.FS(files)),
	}
```

Change `NewHandler` to forward an empty path (signature stays the same):

```go
func NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, metrics, logs, controller, auth, ttl, "").mux
}
```

- [ ] **Step 4: Add `sessionsPath` to `dashboard.Serve`**

In `internal/dashboard/server.go`, change the signature and the `newHandler` call:

```go
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath string) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour, sessionsPath)
```

(The rest of the function is unchanged.)

- [ ] **Step 5: Pass the path from `ServeDir`**

In `internal/server/server.go`, update the `dashboard.Serve` call (line ~373). The package already imports `path/filepath` (used elsewhere); if not, add it. Change:

```go
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, srv, auth, cert); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
```

to:

```go
		sessionsPath := filepath.Join(dataDir, "sessions.json")
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, srv, auth, cert, sessionsPath); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
```

Verify `path/filepath` is imported in `internal/server/server.go` (run `go build ./internal/server/` after editing; add the import if the compiler complains).

- [ ] **Step 6: Run the new test + the existing login test to verify they pass**

Run: `go test ./internal/dashboard/ -run 'TestSessionSurvivesHandlerRestart|TestLoginFleetLogout' -count=1`
Expected: PASS.

- [ ] **Step 7: Full gate + commit**

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./... && go build -o marshal ./cmd/marshal
git add internal/dashboard/handlers.go internal/dashboard/server.go internal/server/server.go internal/dashboard/server_test.go
git commit -m "feat(dashboard): persist sessions to <dataDir>/sessions.json

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

### Task 3: Login rate-limiter

A per-(user, IP) consecutive-failure lockout with exponential backoff.

**Files:**
- Create: `internal/dashboard/limiter.go`
- Test: `internal/dashboard/limiter_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func newLoginLimiter(now func() time.Time) *loginLimiter` — nil `now` ⇒ `time.Now`.
  - `func (l *loginLimiter) retryAfter(key string) (locked bool, wait time.Duration)` — is this key locked right now, and for how long.
  - `func (l *loginLimiter) fail(key string)` — record a failed attempt (may engage a lock).
  - `func (l *loginLimiter) reset(key string)` — clear a key after a success.
  - Constants: `lockoutThreshold = 5`, `lockoutBase = time.Minute`, `lockoutCap = 15 * time.Minute`.

- [ ] **Step 1: Write the failing limiter tests**

Create `internal/dashboard/limiter_test.go`:

```go
package dashboard

import (
	"testing"
	"time"
)

func TestLimiterLocksAfterThreshold(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	for i := 0; i < lockoutThreshold-1; i++ {
		l.fail("admin|1.2.3.4")
	}
	if locked, _ := l.retryAfter("admin|1.2.3.4"); locked {
		t.Fatal("locked before reaching threshold")
	}
	l.fail("admin|1.2.3.4") // crosses the threshold
	locked, wait := l.retryAfter("admin|1.2.3.4")
	if !locked {
		t.Fatal("not locked at threshold")
	}
	if wait != lockoutBase {
		t.Fatalf("wait = %v; want %v", wait, lockoutBase)
	}
}

func TestLimiterResetClearsState(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < lockoutThreshold; i++ {
		l.fail("admin|ip")
	}
	l.reset("admin|ip")
	if locked, _ := l.retryAfter("admin|ip"); locked {
		t.Fatal("still locked after reset")
	}
}

func TestLimiterLockExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < lockoutThreshold; i++ {
		l.fail("admin|ip")
	}
	now = now.Add(lockoutBase + time.Second)
	if locked, _ := l.retryAfter("admin|ip"); locked {
		t.Fatal("still locked after the backoff elapsed")
	}
}

func TestLimiterBackoffDoublesAndCaps(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	// Drive several lockout cycles; each adds `lockoutThreshold` fresh failures
	// after the previous lock has expired.
	want := []time.Duration{lockoutBase, 2 * lockoutBase, 4 * lockoutBase, 8 * lockoutBase, lockoutCap, lockoutCap}
	for _, w := range want {
		for i := 0; i < lockoutThreshold; i++ {
			l.fail("admin|ip")
		}
		_, wait := l.retryAfter("admin|ip")
		if wait != w {
			t.Fatalf("backoff = %v; want %v", wait, w)
		}
		now = now.Add(wait + time.Second) // let the lock expire before the next cycle
	}
}

func TestLimiterKeysIndependent(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < lockoutThreshold; i++ {
		l.fail("admin|1.1.1.1")
	}
	if locked, _ := l.retryAfter("admin|2.2.2.2"); locked {
		t.Fatal("a different IP was locked")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run TestLimiter -count=1`
Expected: compile error (`newLoginLimiter` / `loginLimiter` / constants undefined). Proceed.

- [ ] **Step 3: Implement `internal/dashboard/limiter.go`**

```go
package dashboard

import (
	"sync"
	"time"
)

const (
	lockoutThreshold = 5                // consecutive failures before a lock
	lockoutBase      = time.Minute      // first lock duration
	lockoutCap       = 15 * time.Minute // maximum lock duration
)

// limiterEntry tracks consecutive failures for one (user, IP) key.
type limiterEntry struct {
	fails      int
	lockUntil  time.Time
	lockedOnce int // number of locks engaged, for exponential backoff
	lastSeen   time.Time
}

// loginLimiter applies a per-key consecutive-failure lockout with exponential
// backoff. Keys are typically "user|ip". It is safe for concurrent use.
type loginLimiter struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*limiterEntry
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	if now == nil {
		now = time.Now
	}
	return &loginLimiter{now: now, m: map[string]*limiterEntry{}}
}

// retryAfter reports whether key is currently locked and, if so, how long until
// it unlocks.
func (l *loginLimiter) retryAfter(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok {
		return false, 0
	}
	now := l.now()
	if now.Before(e.lockUntil) {
		return true, e.lockUntil.Sub(now)
	}
	return false, 0
}

// fail records a failed attempt for key, engaging (or extending) a lock once the
// failure count reaches the threshold.
func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.pruneLocked(now)
	e, ok := l.m[key]
	if !ok {
		e = &limiterEntry{}
		l.m[key] = e
	}
	e.lastSeen = now
	e.fails++
	if e.fails >= lockoutThreshold {
		dur := lockoutBase << e.lockedOnce // base * 2^lockedOnce
		if dur > lockoutCap || dur <= 0 {  // <=0 guards against shift overflow
			dur = lockoutCap
		}
		e.lockUntil = now.Add(dur)
		e.lockedOnce++
		e.fails = 0 // reset the counter; the next threshold engages the next backoff step
	}
}

// reset clears all state for key (called after a successful login).
func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.m, key)
	l.mu.Unlock()
}

// pruneLocked drops entries that are unlocked and idle past the cap. Caller holds l.mu.
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, e := range l.m {
		if now.After(e.lockUntil) && now.Sub(e.lastSeen) > lockoutCap {
			delete(l.m, k)
		}
	}
}
```

Note on the backoff sequence: `lockoutBase << lockedOnce` gives 1, 2, 4, 8 min, then `16 min > cap` ⇒ `15 min` (cap) thereafter — matching `TestLimiterBackoffDoublesAndCaps`.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/dashboard/ -run TestLimiter -count=1`
Expected: PASS.

- [ ] **Step 5: Full gate + commit**

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./... && go build -o marshal ./cmd/marshal
git add internal/dashboard/limiter.go internal/dashboard/limiter_test.go
git commit -m "feat(dashboard): per-(user,IP) login lockout limiter

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

### Task 4: Enforce the limiter in the login handler

Check the limiter before verifying the password; return 429 + `Retry-After` while locked, and reset on success.

**Files:**
- Modify: `internal/dashboard/handlers.go` (add `limiter` field; build it in `newHandler`; guard `login`)
- Test: `internal/dashboard/server_test.go`

**Interfaces:**
- Consumes: `newLoginLimiter`, `retryAfter`, `fail`, `reset` from Task 3; `newHandler(..., sessionsPath)` from Task 2.
- Produces: `login` now returns **429** with a `Retry-After` header when the (user, IP) key is locked, and does **not** call `VerifyDashboardUser` in that case.

- [ ] **Step 1: Write the failing handler tests**

Add to `internal/dashboard/server_test.go`. The first test needs to observe whether `VerifyDashboardUser` was called, so add a counting fake near `fakeAuth`:

```go
type countingAuth struct {
	user, pass string
	calls      int
}

func (c *countingAuth) VerifyDashboardUser(u, p string) bool {
	c.calls++
	return u == c.user && p == c.pass
}
```

Then the tests:

```go
func TestLoginLockoutReturns429(t *testing.T) {
	auth := &countingAuth{user: "admin", pass: "pw"}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()

	// Five bad logins from the same client → lockout.
	for i := 0; i < lockoutThreshold; i++ {
		resp, _ := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d; want 401", i, resp.StatusCode)
		}
	}
	callsBefore := auth.calls

	// The next attempt is locked: 429 + Retry-After, and no verify call.
	resp, _ := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked login = %d; want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("locked login missing Retry-After header")
	}
	if auth.calls != callsBefore {
		t.Fatalf("VerifyDashboardUser called while locked (%d -> %d)", callsBefore, auth.calls)
	}
}

func TestLoginSuccessResetsLimiter(t *testing.T) {
	auth := &countingAuth{user: "admin", pass: "pw"}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()

	// Four bad logins (one below the threshold), then a good one.
	for i := 0; i < lockoutThreshold-1; i++ {
		c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	}
	resp, _ := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good login = %d; want 200", resp.StatusCode)
	}
	// The counter is reset, so a fresh bad login returns 401 (not 429).
	resp, _ = c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-success bad login = %d; want 401", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run 'TestLoginLockoutReturns429|TestLoginSuccessResetsLimiter' -count=1`
Expected: FAIL — the lockout test gets 401 (not 429) on the 6th attempt because there's no limiter yet.

- [ ] **Step 3: Add the limiter to the handler**

In `internal/dashboard/handlers.go`:

Add the import `"net"` and `"strconv"` to the import block (alongside the existing imports).

Add a `limiter` field to the `handler` struct:

```go
type handler struct {
	lister      FleetLister
	metricsHist MetricsHistory
	logsHist    LogsHistory
	controller  FleetController
	auth        Authenticator
	sessions    *sessionStore
	limiter     *loginLimiter
	files       fs.FS
	static      http.Handler
	mux         http.Handler
}
```

Build it in `newHandler` (add the field to the struct literal):

```go
		sessions:    newSessionStore(ttl, nil, sessionsPath),
		limiter:     newLoginLimiter(nil),
```

- [ ] **Step 4: Guard the `login` handler**

Replace the `login` method body with:

```go
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ User, Pass string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	key := body.User + "|" + clientIP(r)
	if locked, wait := h.limiter.retryAfter(key); locked {
		secs := int(wait.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	if !h.auth.VerifyDashboardUser(body.User, body.Pass) {
		h.limiter.fail(key)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	h.limiter.reset(key)
	tok, err := h.sessions.create(body.User)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.setSessionCookie(w, tok, 0)
	writeJSON(w, http.StatusOK, map[string]string{"user": body.User})
}

// clientIP returns the source IP for r, stripping the port. It falls back to the
// raw RemoteAddr if there is no port (Marshal serves direct TLS, so RemoteAddr
// is the real client — no X-Forwarded-For to consult).
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/dashboard/ -run 'TestLogin|TestLoginLockoutReturns429|TestLoginSuccessResetsLimiter' -count=1`
Expected: PASS (including the existing `TestLoginFleetLogout`).

- [ ] **Step 6: Full gate + commit**

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./... && go build -o marshal ./cmd/marshal
git add internal/dashboard/handlers.go internal/dashboard/server_test.go
git commit -m "feat(dashboard): rate-limit failed logins with 429 + Retry-After

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

### Task 5: Auth hot-reload

Add a mtime-gated `Reload()` to `AuthStore`, a background poll loop, and wire it into `ServeDir`.

**Files:**
- Modify: `internal/server/auth.go` (track mtime in `LoadOrInitAuth`; add `Reload` + `ReloadLoop`)
- Modify: `internal/server/server.go` (start the poll goroutine in `ServeDir`)
- Test: `internal/server/auth_test.go`

**Interfaces:**
- Consumes: existing `AuthStore`, `SetDashboardPassword`, `loadOrInitAuth`.
- Produces:
  - `func (a *AuthStore) Reload() error` — re-reads `auth.json` if its mtime changed since the last load; a read/parse error keeps the current data and returns the error.
  - `func (a *AuthStore) ReloadLoop(ctx context.Context, interval time.Duration)` — polls `Reload` until ctx is canceled.
  - New unexported field `mtime time.Time` on `AuthStore`, set by `LoadOrInitAuth` and `Reload`.

- [ ] **Step 1: Write the failing reload tests**

Add to `internal/server/auth_test.go`:

```go
func TestReloadPicksUpNewPassword(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "old"); err != nil {
		t.Fatal(err)
	}
	a, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.VerifyDashboardUser("admin", "old") {
		t.Fatal("baseline password did not verify")
	}

	// A separate process (here, a separate call) changes the password on disk.
	if err := SetDashboardPassword(dir, "admin", "new"); err != nil {
		t.Fatal(err)
	}
	// Before reload, the running store still has the old password.
	if a.VerifyDashboardUser("admin", "new") {
		t.Fatal("store saw the new password without a reload")
	}
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if !a.VerifyDashboardUser("admin", "new") {
		t.Fatal("reload did not pick up the new password")
	}
	if a.VerifyDashboardUser("admin", "old") {
		t.Fatal("old password still valid after reload")
	}
}

func TestReloadNoopWhenUnchanged(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	a, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the in-memory data, then Reload with an unchanged file: because the
	// mtime is unchanged, Reload must NOT overwrite our in-memory change.
	a.mu.Lock()
	a.data.Users["sentinel"] = dashboardUser{}
	a.mu.Unlock()
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	_, present := a.data.Users["sentinel"]
	a.mu.Unlock()
	if !present {
		t.Fatal("Reload reparsed the file despite an unchanged mtime")
	}
}

func TestReloadCorruptKeepsOldData(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	a, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite auth.json with garbage (changes mtime).
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err == nil {
		t.Fatal("Reload of a corrupt file returned nil error")
	}
	// The previously-loaded password still verifies.
	if !a.VerifyDashboardUser("admin", "pw") {
		t.Fatal("corrupt reload dropped the good in-memory data")
	}
}
```

Ensure `auth_test.go` imports `"os"`, `"path/filepath"`, and `"time"` (add any that are missing).

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/server/ -run TestReload -count=1`
Expected: compile error (`Reload` / `a.mtime` undefined). Proceed.

- [ ] **Step 3: Track mtime in `LoadOrInitAuth` and add `Reload` / `ReloadLoop`**

In `internal/server/auth.go`:

Add the field to the struct:

```go
type AuthStore struct {
	path  string
	mu    sync.Mutex
	data  authData
	mtime time.Time
}
```

In `LoadOrInitAuth`, after a successful read+unmarshal on the reload path (inside the `if err == nil` block, just before `return a, nil, nil`), record the mtime:

```go
		if a.data.Users == nil {
			a.data.Users = map[string]dashboardUser{}
		}
		if fi, statErr := os.Stat(path); statErr == nil {
			a.mtime = fi.ModTime()
		}
		return a, nil, nil
```

And after the first-init `a.save()` succeeds (just before `return a, &InitSecrets{...}`), likewise record it:

```go
	if err := a.save(); err != nil {
		return nil, nil, err
	}
	if fi, statErr := os.Stat(path); statErr == nil {
		a.mtime = fi.ModTime()
	}
	return a, &InitSecrets{EnrollToken: enroll, AdminToken: admin}, nil
```

Add the `context` import to `auth.go` (for `ReloadLoop`), then add the methods (e.g. after `save`):

```go
// Reload re-reads auth.json if its modification time changed since the last
// successful load, swapping in the new data under the lock. A read or parse
// error leaves the current in-memory data intact and is returned to the caller.
// Atomic-rename writes guarantee we never observe a half-written file.
func (a *AuthStore) Reload() error {
	fi, err := os.Stat(a.path)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if fi.ModTime().Equal(a.mtime) {
		return nil // unchanged — cheap no-op
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return err
	}
	var fresh authData
	if err := json.Unmarshal(b, &fresh); err != nil {
		return fmt.Errorf("parse auth.json: %w", err)
	}
	if fresh.Agents == nil {
		fresh.Agents = map[string]authAgentEntry{}
	}
	if fresh.Users == nil {
		fresh.Users = map[string]dashboardUser{}
	}
	a.data = fresh
	a.mtime = fi.ModTime()
	return nil
}

// ReloadLoop polls Reload every interval until ctx is canceled, so a running
// server picks up `server passwd` / `token --rotate` changes without a restart.
func (a *AuthStore) ReloadLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.Reload(); err != nil {
				log.Printf("auth: reload failed: %v", err)
			}
		}
	}
}
```

- [ ] **Step 4: Run the unit tests to verify they pass**

Run: `go test ./internal/server/ -run TestReload -count=1`
Expected: PASS.

- [ ] **Step 5: Wire the poll loop into `ServeDir`**

In `internal/server/server.go`, inside `ServeDir`, after `auth` is loaded (after line ~348, the `loadOrInitAuth` block) and before `return Serve(...)`, start the loop bound to ctx. A clean spot is right after `srv := NewServer(reg, ss, ls, auth)`:

```go
	srv := NewServer(reg, ss, ls, auth)
	go auth.ReloadLoop(ctx, 3*time.Second)
```

- [ ] **Step 6: Full gate + commit**

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./... && go build -o marshal ./cmd/marshal
git add internal/server/auth.go internal/server/auth_test.go internal/server/server.go
git commit -m "feat(server): hot-reload auth.json via mtime-gated poll

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

### Task 6: Update the handoff + finish

**Files:**
- Create: `docs/handoffs/2026-06-18-m15-auth-hardening.md`

- [ ] **Step 1: Run the full gate one more time**

```bash
go test ./... -race -count=1 && gofmt -l . && go vet ./... && go build -o marshal ./cmd/marshal
```

Expected: all PASS, `gofmt -l .` silent.

- [ ] **Step 2: Write the handoff**

Follow the convention in `CLAUDE.md`: current state, what changed this session and why, build/run/test, deferred/known issues, concrete next step. Note the carried-over out-of-scope items from the spec (session invalidation on password change, cert warning, multi-user roles).

- [ ] **Step 3: Commit the handoff**

```bash
git add docs/handoffs/2026-06-18-m15-auth-hardening.md
git commit -m "docs: M15 auth-hardening handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 4: Live demo (per CLAUDE.md live-demo convention)**

Spin up a scratch server (`XDG_DATA_HOME=/tmp/marshal-m15-demo/...`), set the password while the server is down, start with `--http-listen`, enroll an agent, then verify end-to-end:
1. Log in; restart the server; confirm the **session cookie still works** (no re-login) — the persistence guarantee.
2. Hammer 5 bad logins; confirm the 6th returns **429** with `Retry-After`.
3. With the server **running**, run `marshal server passwd` to change the password; within ~3s confirm a new login with the new password succeeds **without** a restart — the hot-reload guarantee.
4. Tear down (stop agent + server, remove scratch dir); confirm no orphan `marshal` processes (`pgrep -fl marshal`).

- [ ] **Step 5: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to merge `m15-auth-hardening` to `main` (after a code review pass).

---

## Self-Review

**Spec coverage:**
- Session persistence (spec §1) → Task 1 (store) + Task 2 (wiring). ✓ token-hash keying, `sessions.json` 0600 atomic, load-drops-expired, empty-path in-memory, corrupt-file-empty all covered.
- Login rate-limiting (spec §2) → Task 3 (limiter) + Task 4 (enforcement). ✓ (user,IP) key, threshold 5, backoff 1→2→4→8→cap 15m, 429 + Retry-After, verify-skipped-while-locked, success reset, (user,IP) independence, opportunistic prune.
- Auth hot-reload (spec §3) → Task 5. ✓ mtime-gated `Reload`, keeps old data on error, poll loop in `ServeDir`, shared `*AuthStore` ⇒ gRPC + dashboard both benefit.
- Testing (spec §4) → every implementation step is preceded by a failing test; integration cross-restart test in Task 2; live demo in Task 6.
- Out-of-scope (spec §5) → not implemented (correct); reiterated in the handoff step.

**Placeholder scan:** No TBD/TODO/"add error handling"/"similar to". Every code step shows complete code.

**Type consistency:** `newSessionStore(ttl, now, path)` consistent across Tasks 1–2; `hashSessionToken` used in store + tests; `newHandler(..., sessionsPath)` and unchanged `NewHandler` consistent across Tasks 2–4; `loginLimiter`/`newLoginLimiter`/`retryAfter`/`fail`/`reset` + constants consistent across Tasks 3–4; `Reload`/`ReloadLoop`/`a.mtime` consistent across Task 5. `clientIP` defined once in Task 4. No forward references to undefined symbols.
