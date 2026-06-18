# Marshal Dashboard M17 — Login-Attempt Audit Log — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist every dashboard login attempt (success / bad credentials / rate-limited) to a disk-bounded, append-only JSONL file, and add a `marshal server audit` CLI to view it.

**Architecture:** A new leaf package `internal/audit` owns the on-disk format, a size-rotating writer (`Log.Record`), and a tolerant reader (`Read`). The dashboard handler holds an `*audit.Log` (nil = disabled) and records one event per login exit path. The CLI reads via `audit.Read`. The package imports neither `dashboard` nor `server`, so both depend on it cycle-free.

**Tech Stack:** Go 1.26, standard library only (`encoding/json`, `os`, `bufio`, `sync`), cobra for the CLI (already used).

## Global Constraints

- Module path `marshal`; imports `marshal/internal/...`. Standard library only (cobra already vendored for CLI).
- TDD: failing test first, then minimal implementation.
- Gate before finishing: `go build -o marshal ./cmd/marshal`, `go test ./... -race -count=1`, `gofmt -l .` prints nothing, `go vet ./...` clean.
- Commit subject imperative + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work on branch `m17-login-audit`, not `main`.
- `NewHandler`'s exported signature must NOT change (it passes no audit path → auditing disabled).
- Passwords are NEVER recorded — only the submitted username. The audit file is mode `0600`.
- Touch only: `internal/audit` (new), `internal/dashboard`, `internal/server/server.go` (one call site), `cmd/marshal`.

---

## File structure

- `internal/audit/audit.go` (new) — `Event`, outcome constants, `DefaultMaxBytes`, `Log`, `New`, `Record`.
- `internal/audit/read.go` (new) — `ReadOptions`, `Read`, internal `readFile`.
- `internal/audit/audit_test.go` (new) — package unit tests.
- `internal/dashboard/handlers.go` — `handler.audit` field; `newHandler` gains `auditPath`; `login` records 3 outcomes; `NewHandler` passes `""`.
- `internal/dashboard/server.go` — `Serve` gains `auditPath`, forwards to `newHandler`.
- `internal/dashboard/audit_test.go` (new) — login-records-events tests.
- `internal/server/server.go` — pass `filepath.Join(dataDir, "login-audit.log")` to `dashboard.Serve`.
- `cmd/marshal/server_audit.go` (new) — `serverAuditCmd`.
- `cmd/marshal/server.go` — register `serverAuditCmd()` in the `AddCommand` list.
- `cmd/marshal/server_audit_test.go` (new) — CLI render/filter test.

---

## Task 1: `internal/audit` package (writer + reader)

**Files:**
- Create: `internal/audit/audit.go`, `internal/audit/read.go`
- Test: `internal/audit/audit_test.go`

**Interfaces:**
- Produces:
  - `type Event struct { Time time.Time; User, IP, Outcome string }` with json tags `time,user,ip,outcome`.
  - Consts `OutcomeSuccess = "success"`, `OutcomeInvalid = "invalid_credentials"`, `OutcomeRateLimited = "rate_limited"`, `DefaultMaxBytes int64 = 5 << 20`.
  - `func New(path string, maxBytes int64) *Log`
  - `func (l *Log) Record(ev Event)` — nil-safe no-op; all I/O errors logged+swallowed.
  - `type ReadOptions struct { Limit int; FailuresOnly bool }`
  - `func Read(path string, opts ReadOptions) ([]Event, error)`

- [ ] **Step 1: Write the failing tests**

Create `internal/audit/audit_test.go`:

```go
package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordReadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	l := New(p, DefaultMaxBytes)
	t0 := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)
	l.Record(Event{Time: t0, User: "admin", IP: "1.2.3.4", Outcome: OutcomeSuccess})
	l.Record(Event{Time: t0.Add(time.Minute), User: "bob", IP: "5.6.7.8", Outcome: OutcomeInvalid})
	got, err := Read(p, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d events; want 2", len(got))
	}
	if got[0].User != "admin" || got[0].Outcome != OutcomeSuccess {
		t.Errorf("e0 = %+v", got[0])
	}
	if got[1].User != "bob" || got[1].Outcome != OutcomeInvalid {
		t.Errorf("e1 = %+v", got[1])
	}
	if !got[0].Time.Equal(t0) {
		t.Errorf("time round-trip: %v != %v", got[0].Time, t0)
	}
}

func TestRotationAtCap(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	l := New(p, 200) // tiny cap forces rotation
	for i := 0; i < 20; i++ {
		l.Record(Event{Time: time.Unix(int64(i), 0).UTC(), User: "u", IP: "1.1.1.1", Outcome: OutcomeInvalid})
	}
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatalf(".1 not created (no rotation): %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() >= 2*int64(200) {
		t.Errorf("current file not bounded after rotation: %d bytes", fi.Size())
	}
	got, err := Read(p, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("read returned nothing after rotation")
	}
	// The most recent event must survive (rotation keeps current + one .1).
	last := got[len(got)-1]
	if !last.Time.Equal(time.Unix(19, 0).UTC()) {
		t.Errorf("most recent event lost; last time = %v", last.Time)
	}
}

func TestReadSkipsCorruptLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	good := `{"time":"2026-06-18T10:00:00Z","user":"a","ip":"1.1.1.1","outcome":"success"}`
	content := good + "\n{ this is not json\n\n" + good + "\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Read(p, ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d; want 2 (corrupt + blank lines skipped)", len(got))
	}
}

func TestReadFiltersAndLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "login-audit.log")
	l := New(p, DefaultMaxBytes)
	base := time.Unix(0, 0).UTC()
	l.Record(Event{Time: base, User: "a", IP: "i", Outcome: OutcomeSuccess})
	l.Record(Event{Time: base.Add(time.Second), User: "b", IP: "i", Outcome: OutcomeInvalid})
	l.Record(Event{Time: base.Add(2 * time.Second), User: "c", IP: "i", Outcome: OutcomeRateLimited})

	fails, err := Read(p, ReadOptions{FailuresOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fails) != 2 {
		t.Fatalf("FailuresOnly got %d; want 2", len(fails))
	}
	for _, e := range fails {
		if e.Outcome == OutcomeSuccess {
			t.Errorf("success leaked into failures: %+v", e)
		}
	}

	last1, err := Read(p, ReadOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(last1) != 1 || last1[0].User != "c" {
		t.Fatalf("Limit 1 = %+v; want only most recent (c)", last1)
	}
}

func TestReadMissingFile(t *testing.T) {
	got, err := Read(filepath.Join(t.TempDir(), "nope.log"), ReadOptions{})
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d; want 0", len(got))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audit/ -count=1`
Expected: FAIL — package/types undefined (`New`, `Read`, `Event`, etc.).

- [ ] **Step 3: Write the writer**

Create `internal/audit/audit.go`:

```go
// Package audit records dashboard login attempts to an append-only, size-rotating
// JSONL file, and reads them back. It is a leaf package: it imports neither the
// dashboard nor the server package, so both may depend on it without a cycle.
package audit

import (
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// DefaultMaxBytes is the rotation threshold the dashboard passes by default.
const DefaultMaxBytes int64 = 5 << 20 // 5 MiB

// Outcome values for Event.Outcome.
const (
	OutcomeSuccess     = "success"
	OutcomeInvalid     = "invalid_credentials"
	OutcomeRateLimited = "rate_limited"
)

// Event is one recorded login attempt. Passwords are never stored — only the
// submitted username, the source IP, and the outcome.
type Event struct {
	Time    time.Time `json:"time"`
	User    string    `json:"user"`
	IP      string    `json:"ip"`
	Outcome string    `json:"outcome"`
}

// Log is a concurrency-safe append-only writer that rotates at maxBytes.
type Log struct {
	path     string
	maxBytes int64
	mu       sync.Mutex
}

// New returns a writer appending to path. maxBytes <= 0 uses DefaultMaxBytes.
func New(path string, maxBytes int64) *Log {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Log{path: path, maxBytes: maxBytes}
}

// Record appends ev as one JSON line. A nil *Log is a no-op. Before writing it
// rotates path to path+".1" (overwriting any prior .1) once the file reaches
// maxBytes, bounding disk to ~2x maxBytes. All I/O errors are logged and
// swallowed: auditing must never break the login path.
func (l *Log) Record(ev Event) {
	if l == nil {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		log.Printf("audit: marshal event: %v", err)
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if fi, err := os.Stat(l.path); err == nil && fi.Size() >= l.maxBytes {
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			log.Printf("audit: rotate: %v", err)
		}
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit: open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		log.Printf("audit: write: %v", err)
	}
}
```

- [ ] **Step 4: Write the reader**

Create `internal/audit/read.go`:

```go
package audit

import (
	"bufio"
	"encoding/json"
	"os"
)

// ReadOptions filters the result of Read.
type ReadOptions struct {
	Limit        int  // 0 = all; otherwise the most recent N (after filtering)
	FailuresOnly bool // exclude OutcomeSuccess
}

// Read returns events from path and its rotated companion (path+".1") in
// chronological order, oldest first. The rotated file is read before the current
// one. Corrupt or blank lines are skipped. A missing file is not an error.
func Read(path string, opts ReadOptions) ([]Event, error) {
	var out []Event
	for _, p := range []string{path + ".1", path} {
		evs, err := readFile(p)
		if err != nil {
			return nil, err
		}
		out = append(out, evs...)
	}
	if opts.FailuresOnly {
		var kept []Event
		for _, e := range out {
			if e.Outcome != OutcomeSuccess {
				kept = append(kept, e)
			}
		}
		out = kept
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[len(out)-opts.Limit:]
	}
	return out, nil
}

// readFile parses one JSONL file, skipping corrupt/blank lines. A missing file
// yields a nil slice and no error.
func readFile(p string) ([]Event, error) {
	f, err := os.Open(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue // tolerate a corrupt/partial line
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/audit/ -race -count=1`
Expected: PASS (all 5 tests). Then `gofmt -l internal/audit/` (must print nothing).

- [ ] **Step 6: Commit**

```bash
git add internal/audit/
git commit -m "feat(audit): append-only rotating login-attempt log + reader

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Dashboard wiring (record on each login outcome)

This is one compilation unit: `dashboard.Serve`'s signature change breaks its sole
caller (`internal/server/server.go`), which is updated in the same commit.

**Files:**
- Modify: `internal/dashboard/handlers.go`, `internal/dashboard/server.go`, `internal/server/server.go`
- Test: `internal/dashboard/audit_test.go` (new)

**Interfaces:**
- Consumes: `audit.New`, `audit.Event`, `audit.Outcome*`, `audit.DefaultMaxBytes`, `audit.Read` (Task 1).
- Produces:
  - `handler.audit *audit.Log`
  - `newHandler(lister, metrics, logs, controller, auth, ttl, sessionsPath, auditPath string) *handler`
  - `Serve(ctx, addr, lister, metrics, logs, controller, auth, cert, sessionsPath, auditPath string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/dashboard/audit_test.go`:

```go
package dashboard

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/audit"
)

func postLogin(t *testing.T, c *http.Client, base, jsonBody string) {
	t.Helper()
	resp, err := c.Post(base+"/api/login", "application/json", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestLoginRecordsSuccessAndInvalid(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "login-audit.log")
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", auditPath)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()

	postLogin(t, c, srv.URL, `{"User":"admin","Pass":"pw"}`)    // success
	postLogin(t, c, srv.URL, `{"User":"admin","Pass":"nope"}`) // invalid

	evs, err := audit.Read(auditPath, audit.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events; want 2", len(evs))
	}
	if evs[0].Outcome != audit.OutcomeSuccess || evs[0].User != "admin" {
		t.Errorf("e0 = %+v; want success/admin", evs[0])
	}
	if evs[1].Outcome != audit.OutcomeInvalid {
		t.Errorf("e1 = %+v; want invalid_credentials", evs[1])
	}
	if evs[0].IP == "" {
		t.Errorf("event missing IP")
	}
}

func TestLoginRecordsRateLimited(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "login-audit.log")
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", auditPath)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()

	// 5 wrong attempts engage the lock; the 6th is rejected while locked.
	for i := 0; i < 6; i++ {
		postLogin(t, c, srv.URL, `{"User":"admin","Pass":"nope"}`)
	}
	evs, err := audit.Read(auditPath, audit.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("no events recorded")
	}
	last := evs[len(evs)-1]
	if last.Outcome != audit.OutcomeRateLimited {
		t.Fatalf("last outcome = %q; want rate_limited", last.Outcome)
	}
}

func TestLoginNoAuditWhenDisabled(t *testing.T) {
	// NewHandler passes no audit path → h.audit is nil → Record is a no-op.
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200 (nil audit must not break login)", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dashboard/ -run TestLogin -count=1`
Expected: FAIL — `newHandler` takes 7 args, not 8 (compile error).

- [ ] **Step 3: Add the `audit` field and thread `auditPath` through `newHandler`**

In `internal/dashboard/handlers.go`:

- Add import `"marshal/internal/audit"`.
- Add the field to `handler`:

```go
	limiter     *loginLimiter
	audit       *audit.Log
	files       fs.FS
```

- Change `newHandler` to accept `auditPath` and build the writer:

```go
func newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration, sessionsPath, auditPath string) *handler {
	files := staticFS()
	var al *audit.Log
	if auditPath != "" {
		al = audit.New(auditPath, audit.DefaultMaxBytes)
	}
	h := &handler{
		lister:      lister,
		metricsHist: metrics,
		logsHist:    logs,
		controller:  controller,
		auth:        auth,
		sessions:    newSessionStore(ttl, nil, sessionsPath),
		limiter:     newLoginLimiter(nil),
		audit:       al,
		files:       files,
		static:      http.FileServer(http.FS(files)),
	}
```

- Update `NewHandler` to pass an empty audit path (signature unchanged):

```go
func NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, metrics, logs, controller, auth, ttl, "", "").mux
}
```

- [ ] **Step 4: Record on each login exit path**

In `internal/dashboard/handlers.go`, rewrite `login` to capture the IP once and record on all three paths:

```go
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ User, Pass string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	key := body.User + "|" + ip
	if locked, wait := h.limiter.retryAfter(key); locked {
		h.audit.Record(audit.Event{Time: time.Now().UTC(), User: body.User, IP: ip, Outcome: audit.OutcomeRateLimited})
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
		h.audit.Record(audit.Event{Time: time.Now().UTC(), User: body.User, IP: ip, Outcome: audit.OutcomeInvalid})
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	h.limiter.reset(key)
	stamp, _ := h.auth.DashboardCredentialStamp(body.User)
	tok, err := h.sessions.create(body.User, stamp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit.Record(audit.Event{Time: time.Now().UTC(), User: body.User, IP: ip, Outcome: audit.OutcomeSuccess})
	h.setSessionCookie(w, tok, 0)
	writeJSON(w, http.StatusOK, map[string]string{"user": body.User})
}
```

(`h.audit.Record` is safe when `h.audit` is nil.)

- [ ] **Step 5: Thread `auditPath` through `Serve` and its caller**

In `internal/dashboard/server.go`, change `Serve`:

```go
// sessionsPath persists sessions to disk; "" keeps them in-memory. auditPath
// enables the login audit log; "" disables it.
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath, auditPath string) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour, sessionsPath, auditPath)
```

In `internal/server/server.go` (around the existing `sessionsPath` block near line 374), add the audit path and pass it:

```go
		sessionsPath := filepath.Join(dataDir, "sessions.json")
		auditPath := filepath.Join(dataDir, "login-audit.log")
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, srv, auth, cert, sessionsPath, auditPath); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
```

- [ ] **Step 6: Run the dashboard and server packages**

Run: `go test ./internal/dashboard/ ./internal/server/ -race -count=1`
Expected: PASS — new audit tests pass; existing dashboard/server tests still pass (NewHandler unchanged; server.go compiles with the new Serve arity).

- [ ] **Step 7: Commit**

```bash
git add internal/dashboard/handlers.go internal/dashboard/server.go \
  internal/dashboard/audit_test.go internal/server/server.go
git commit -m "feat(dashboard): record login attempts to the audit log

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `marshal server audit` CLI

**Files:**
- Create: `cmd/marshal/server_audit.go`
- Modify: `cmd/marshal/server.go` (register the subcommand)
- Test: `cmd/marshal/server_audit_test.go` (new)

**Interfaces:**
- Consumes: `audit.Read`, `audit.ReadOptions`, `audit.Event`, `audit.New`/`audit.Record` (tests), `defaultServerDataDir()`.
- Produces: `func serverAuditCmd() *cobra.Command`.

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/server_audit_test.go`:

```go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/audit"
)

func runAudit(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := serverAuditCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("audit cmd: %v", err)
	}
	return out.String()
}

func TestServerAuditRendersAndFilters(t *testing.T) {
	dir := t.TempDir()
	l := audit.New(filepath.Join(dir, "login-audit.log"), audit.DefaultMaxBytes)
	base := time.Unix(0, 0).UTC()
	l.Record(audit.Event{Time: base, User: "admin", IP: "1.1.1.1", Outcome: audit.OutcomeSuccess})
	l.Record(audit.Event{Time: base.Add(time.Minute), User: "eve", IP: "2.2.2.2", Outcome: audit.OutcomeInvalid})

	all := runAudit(t, "--data-dir", dir)
	if !strings.Contains(all, "admin") || !strings.Contains(all, "eve") {
		t.Fatalf("output missing users:\n%s", all)
	}

	fails := runAudit(t, "--data-dir", dir, "--failures")
	if strings.Contains(fails, "admin") {
		t.Errorf("success leaked with --failures:\n%s", fails)
	}
	if !strings.Contains(fails, "eve") {
		t.Errorf("failure missing with --failures:\n%s", fails)
	}
}

func TestServerAuditEmpty(t *testing.T) {
	out := runAudit(t, "--data-dir", t.TempDir())
	if !strings.Contains(out, "no login attempts") {
		t.Fatalf("empty log should report no attempts; got:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestServerAudit -count=1`
Expected: FAIL — `serverAuditCmd` undefined.

- [ ] **Step 3: Implement the command**

Create `cmd/marshal/server_audit.go`:

```go
package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/audit"
)

func serverAuditCmd() *cobra.Command {
	var dataDir string
	var limit int
	var failures bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show recent dashboard login attempts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			path := filepath.Join(dataDir, "login-audit.log")
			events, err := audit.Read(path, audit.ReadOptions{Limit: limit, FailuresOnly: failures})
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no login attempts recorded")
				return nil
			}
			for _, e := range events {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%-19s\t%s\t%s\n",
					e.Time.Local().Format(time.RFC3339), e.Outcome, e.User, e.IP)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.Flags().IntVar(&limit, "limit", 50, "show at most the most recent N attempts (0 = all)")
	cmd.Flags().BoolVar(&failures, "failures", false, "show only failed/locked attempts")
	return cmd
}
```

- [ ] **Step 4: Register the subcommand**

In `cmd/marshal/server.go`, add `serverAuditCmd()` to the existing `AddCommand` call:

```go
	cmd.AddCommand(serverFingerprintCmd(), serverTokenCmd(), serverAgentCmd(), serverPasswdCmd(), serverAuditCmd())
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/marshal/ -run TestServerAudit -race -count=1`
Expected: PASS. Then `gofmt -l cmd/marshal/` (must print nothing).

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/server_audit.go cmd/marshal/server.go cmd/marshal/server_audit_test.go
git commit -m "feat(cli): add 'marshal server audit' to view login attempts

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Full gate, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-18-m17-login-audit-log.md`

- [ ] **Step 1: Run the full gate**

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .            # must print nothing
go vet ./...
```
Expected: all packages PASS, `gofmt` silent, `vet` clean.

- [ ] **Step 2: Live demo (per CLAUDE.md convention)**

On a scratch data dir (`XDG_DATA_HOME=/tmp/marshal-m17-demo/...`): set a password while the server is down, start with `--http-listen`. Then: one successful login; a few wrong-password logins; enough wrong attempts to trigger a lockout (5 → 6th is rate-limited). Stop the server. Run `marshal server audit --data-dir <dir>` and confirm the success, `invalid_credentials`, and `rate_limited` rows appear with the right users/IPs; run `--failures` and confirm the success row is excluded; run `--limit 2` and confirm only the two most recent rows print. Confirm the file is mode `0600`. Tear down (stop server, remove scratch dir) and confirm `pgrep -fl marshal` shows no demo orphans.

- [ ] **Step 3: Write the handoff**

Write `docs/handoffs/2026-06-18-m17-login-audit-log.md` covering: current state + branch, what changed and why (the leaf `audit` package, the three recorded outcomes, rotation, the CLI), build/run/test, the live-demo result, deferred items (dashboard `/api/audit`, gRPC agent-auth audit), and the concrete next step (M18 server-side log search). Commit it.

- [ ] **Step 4: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to merge `m17-login-audit` to `main`.

---

## Self-review

**Spec coverage:**
- Leaf `internal/audit` package (writer + reader + schema) → Task 1. ✅
- Event schema, outcome constants, 0600, passwords-never-recorded → Task 1 (`audit.go`) + Task 2 (login records only user/ip/outcome). ✅
- Size-rotation at 5 MiB, keep one `.1`, errors swallowed, nil = disabled → Task 1 `Record`. ✅
- Reader: read `.1` then current, skip corrupt, missing = empty, Limit/FailuresOnly → Task 1 `Read`. ✅
- Dashboard records one event per outcome (rate_limited/invalid/success) → Task 2 Step 4. ✅
- `newHandler` threads auditPath; `NewHandler` unchanged → Task 2 Steps 3. ✅
- `Serve` + server.go threading `<dataDir>/login-audit.log` → Task 2 Step 5. ✅
- CLI `server audit` with `--limit`/`--failures`, aligned columns, empty message → Task 3. ✅
- Tests 1–7 from the spec → Task 1 (1–5), Task 2 (6), Task 3 (7). ✅
- Gate + live demo + handoff → Task 4. ✅

**Placeholder scan:** none — every step carries concrete code or commands.

**Type consistency:** `Event{Time,User,IP,Outcome}`, `New(path,maxBytes)`, `Record(Event)`, `Read(path,ReadOptions)`, `ReadOptions{Limit,FailuresOnly}`, `newHandler(...,sessionsPath,auditPath)`, `Serve(...,sessionsPath,auditPath)` used identically across Tasks 1–3. ✅
