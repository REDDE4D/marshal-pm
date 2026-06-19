# Marshal-Managed Git Credentials Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Store a git HTTPS personal-access token in Marshal (encrypted at rest on the server) and inject it into an agent's git clone/fetch at deploy time, so private-repo deploys work on hosts that are not themselves git-authed.

**Architecture:** A new `internal/credstore` package holds AES-256-GCM-encrypted credentials on the server. The dashboard resolves a credential by **name** and ships the secret to the chosen agent inside the existing `ControlOp_Deploy`/`Redeploy` op. The agent's deployer injects it into git via a throwaway `GIT_ASKPASS` helper (token never in argv/URL/log). The credential **name** (not the secret) persists in the app's `config.GitSource` so redeploy re-resolves automatically; it also rides the fleet snapshot so the dashboard can drive redeploy.

**Tech Stack:** Go 1.26 (stdlib only: `crypto/aes`, `crypto/cipher`, `crypto/rand`, `encoding/base64`, `os`, `os/exec`); protobuf via `go generate ./internal/pb` (protoc + `protoc-gen-go`/`protoc-gen-go-grpc`); React/TypeScript web client (Vite, `make ui`); `git` CLI on the agent host.

## Global Constraints

- TDD: write the failing test first; `go test ./... -race -count=1` green before finishing.
- `gofmt -l .` silent; `go vet ./...` clean before finishing.
- Feature work on branch `m22-managed-git-credentials` (already created); co-author trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- No git remote (local-only) — merge `--no-ff` to `main` at the end.
- **Never log secret values.** Token must never appear in argv, the clone URL, the per-app log, `dump.json`, or the audit log.
- Never hand-edit `*.pb.go`. Regenerate with `go generate ./internal/pb`. If `protoc` isn't found, run from `internal/pb`: `protoc --go_out=../.. --go_opt=module=marshal --go-grpc_out=../.. --go-grpc_opt=module=marshal -I ../../proto ../../proto/marshal/v1/daemon.proto ../../proto/marshal/v1/fleet.proto`.
- Credential name allowlist: `^[A-Za-z0-9][A-Za-z0-9._-]*$` (reuse the app-name style).
- Web client has no test framework — verify with `make ui` (`tsc -b`) + the live demo.

---

### Task 1: `internal/credstore` — encrypted credential store

**Files:**
- Create: `internal/credstore/credstore.go`
- Test: `internal/credstore/credstore_test.go`

**Interfaces:**
- Consumes: nothing (leaf package, stdlib only).
- Produces:
  - `type Meta struct { Name, Type, Username string; CreatedAt int64 }`
  - `func Open(dir string) (*Store, error)` — loads/creates `<dir>/credentials.json` (0600) and the master key.
  - `func (s *Store) Put(name, username, token string) error` — create or rotate (upsert by name); validates name + non-empty token.
  - `func (s *Store) Get(name string) (username, token string, ok bool, err error)` — decrypts.
  - `func (s *Store) List() []Meta` — metadata only, sorted by name; never plaintext token.
  - `func (s *Store) Delete(name string) bool`
- Master key resolution order: `MARSHAL_MASTER_KEY` (base64 std, exactly 32 bytes) → `<dir>/master.key` (0600, auto-generated). Invalid env key → `Open` error.

- [ ] **Step 1: Write the failing test**

```go
package credstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put("gh-ci", "octocat", "ghp_secret123"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	user, tok, ok, err := s.Get("gh-ci")
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if user != "octocat" || tok != "ghp_secret123" {
		t.Fatalf("got %q/%q", user, tok)
	}
}

func TestListHasNoSecret(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put("gh-ci", "octocat", "ghp_secret123")
	metas := s.List()
	if len(metas) != 1 || metas[0].Name != "gh-ci" || metas[0].Username != "octocat" {
		t.Fatalf("meta: %+v", metas)
	}
	// The on-disk file must not contain the plaintext token.
	b, _ := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if string(b) == "" || containsPlaintext(b, "ghp_secret123") {
		t.Fatalf("plaintext token leaked to disk: %s", b)
	}
}

func TestPutRotatesAndDelete(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put("gh-ci", "octocat", "old")
	_ = s.Put("gh-ci", "octocat", "new") // rotate
	_, tok, _, _ := s.Get("gh-ci")
	if tok != "new" {
		t.Fatalf("rotate failed, got %q", tok)
	}
	if !s.Delete("gh-ci") {
		t.Fatalf("Delete returned false")
	}
	if _, _, ok, _ := s.Get("gh-ci"); ok {
		t.Fatalf("still present after delete")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := Open(dir)
	_ = s1.Put("gh-ci", "octocat", "ghp_secret123")
	s2, err := Open(dir) // reuses master.key on disk
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_, tok, ok, _ := s2.Get("gh-ci")
	if !ok || tok != "ghp_secret123" {
		t.Fatalf("reopen lost data: ok=%v tok=%q", ok, tok)
	}
}

func TestMasterKeyFileMode(t *testing.T) {
	dir := t.TempDir()
	_, _ = Open(dir)
	fi, err := os.Stat(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("master.key mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestInvalidEnvKey(t *testing.T) {
	t.Setenv("MARSHAL_MASTER_KEY", "not-base64-or-wrong-len")
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatalf("expected error on invalid MARSHAL_MASTER_KEY")
	}
}

func TestBadName(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Put("../escape", "u", "t"); err == nil {
		t.Fatalf("expected name validation error")
	}
	if err := s.Put("ok", "u", ""); err == nil {
		t.Fatalf("expected empty-token error")
	}
}

// containsPlaintext reports whether b contains needle as a raw substring.
func containsPlaintext(b []byte, needle string) bool {
	return len(needle) > 0 && bytesContains(b, []byte(needle))
}
func bytesContains(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/credstore/ -run Test -v`
Expected: FAIL — `undefined: Open` (package has no implementation yet).

- [ ] **Step 3: Write minimal implementation**

```go
// Package credstore is an encrypted-at-rest store for git HTTPS credentials
// (M22). Tokens are sealed with AES-256-GCM under a server master key; List
// and the on-disk file never expose a plaintext token.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Meta is the non-secret view of a credential.
type Meta struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Username  string `json:"username"`
	CreatedAt int64  `json:"created_at"`
}

type entry struct {
	Type      string `json:"type"`
	Username  string `json:"username"`
	Nonce     string `json:"nonce"`  // base64 std
	Cipher    string `json:"cipher"` // base64 std
	CreatedAt int64  `json:"created_at"`
}

// Store is a file-backed, encrypted credential store.
type Store struct {
	path string
	key  [32]byte
	mu   sync.Mutex
	data map[string]entry
}

// Open loads or creates the store under dir, resolving the master key.
func Open(dir string) (*Store, error) {
	key, err := loadMasterKey(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{path: filepath.Join(dir, "credentials.json"), key: key, data: map[string]entry{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if err := json.Unmarshal(b, &s.data); err != nil {
			return nil, fmt.Errorf("parse credentials.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return s, nil
}

func loadMasterKey(dir string) ([32]byte, error) {
	var key [32]byte
	if env := os.Getenv("MARSHAL_MASTER_KEY"); env != "" {
		raw, err := base64.StdEncoding.DecodeString(env)
		if err != nil || len(raw) != 32 {
			return key, fmt.Errorf("MARSHAL_MASTER_KEY must be base64 of exactly 32 bytes")
		}
		copy(key[:], raw)
		return key, nil
	}
	path := filepath.Join(dir, "master.key")
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != 32 {
			return key, fmt.Errorf("%s must be exactly 32 bytes", path)
		}
		copy(key[:], b)
		return key, nil
	} else if !os.IsNotExist(err) {
		return key, err
	}
	if _, err := rand.Read(key[:]); err != nil {
		return key, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return key, err
	}
	if err := os.WriteFile(path, key[:], 0o600); err != nil {
		return key, err
	}
	return key, nil
}

// Put creates or rotates the credential named name.
func (s *Store) Put(name, username, token string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid credential name %q", name)
	}
	if token == "" {
		return fmt.Errorf("token is required")
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := gcm.Seal(nil, nonce, []byte(token), nil)
	s.mu.Lock()
	created := int64(0)
	if old, ok := s.data[name]; ok {
		created = old.CreatedAt
	}
	if created == 0 {
		created = nowUnix()
	}
	s.data[name] = entry{
		Type:      "https-token",
		Username:  username,
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
		Cipher:    base64.StdEncoding.EncodeToString(ct),
		CreatedAt: created,
	}
	err = s.flushLocked()
	s.mu.Unlock()
	return err
}

// Get decrypts the credential named name.
func (s *Store) Get(name string) (username, token string, ok bool, err error) {
	s.mu.Lock()
	e, present := s.data[name]
	s.mu.Unlock()
	if !present {
		return "", "", false, nil
	}
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", "", false, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", false, err
	}
	nonce, err := base64.StdEncoding.DecodeString(e.Nonce)
	if err != nil {
		return "", "", false, err
	}
	ct, err := base64.StdEncoding.DecodeString(e.Cipher)
	if err != nil {
		return "", "", false, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", "", false, fmt.Errorf("decrypt %q: %w", name, err)
	}
	return e.Username, string(pt), true, nil
}

// List returns non-secret metadata for every credential, sorted by name.
func (s *Store) List() []Meta {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Meta, 0, len(s.data))
	for name, e := range s.data {
		out = append(out, Meta{Name: name, Type: e.Type, Username: e.Username, CreatedAt: e.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Delete removes the credential named name, reporting whether it existed.
func (s *Store) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[name]
	if ok {
		delete(s.data, name)
		_ = s.flushLocked()
	}
	return ok
}

func (s *Store) flushLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0o600)
}
```

Add a tiny clock helper (the codebase forbids `time.Now()` only inside Workflow scripts — plain Go may use it):

```go
// nowUnix is the creation timestamp source, in its own func for test seams.
import "time"
func nowUnix() int64 { return time.Now().Unix() }
```

(Place the `time` import with the others; do not create a second import block.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/credstore/ -run Test -v`
Expected: PASS (all 7 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/credstore/
git commit -m "feat(credstore): encrypted-at-rest git credential store (AES-256-GCM)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Proto wire contract for credentials

**Files:**
- Modify: `proto/marshal/v1/daemon.proto` (GitSource, ProcInfo)
- Modify: `proto/marshal/v1/fleet.proto` (GitCredential, DeployRequest, RedeployRequest)
- Regenerate: `internal/pb/*.pb.go`
- Test: `internal/pb/gitsource_test.go` (extend)

**Interfaces:**
- Produces (generated Go accessors used by later tasks):
  - `GitSource.GetCredential() string`
  - `ProcInfo.GetCredential() string`
  - `type GitCredential struct{...}` with `GetUsername()/GetToken() string`
  - `DeployRequest.GetCredential() *GitCredential`
  - `RedeployRequest.GetCredential() *GitCredential`

- [ ] **Step 1: Write the failing test**

Add to `internal/pb/gitsource_test.go`:

```go
func TestGitCredentialWire(t *testing.T) {
	d := &DeployRequest{
		App:        &AppSpec{Name: "x", Source: &GitSource{Repo: "r", Credential: "gh-ci"}},
		Credential: &GitCredential{Username: "octocat", Token: "ghp_x"},
	}
	if d.GetApp().GetSource().GetCredential() != "gh-ci" {
		t.Fatalf("GitSource.Credential not wired")
	}
	if d.GetCredential().GetToken() != "ghp_x" {
		t.Fatalf("DeployRequest.Credential not wired")
	}
	rd := &RedeployRequest{Target: "x", Credential: &GitCredential{Token: "ghp_y"}}
	if rd.GetCredential().GetToken() != "ghp_y" {
		t.Fatalf("RedeployRequest.Credential not wired")
	}
	if (&ProcInfo{Credential: "gh-ci"}).GetCredential() != "gh-ci" {
		t.Fatalf("ProcInfo.Credential not wired")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pb/ -run TestGitCredentialWire -v`
Expected: FAIL — `unknown field Credential` / `undefined: GitCredential` (proto not yet regenerated).

- [ ] **Step 3: Edit the .proto files, then regenerate**

In `proto/marshal/v1/daemon.proto`, add field 5 to `GitSource` and field 12 to `ProcInfo` (keep existing fields; use the exact next free numbers):

```proto
message GitSource {
  // ... existing repo=1, ref=2, build=3, subdir=4 ...
  string credential = 5; // M22: credstore name (non-secret), persisted for redeploy
}

message ProcInfo {
  // ... existing fields 1..11 (incl. source=10, detail=11) ...
  string credential = 12; // M22: credential name (non-secret) → drives redeploy resolution
}
```

In `proto/marshal/v1/fleet.proto`, add `GitCredential` and the new fields on the requests (confirm the existing field numbers in the file first; `DeployRequest.app` and `RedeployRequest.target` keep their numbers):

```proto
message GitCredential {
  string username = 1;
  string token    = 2;
}

message DeployRequest {
  AppSpec app          = 1;
  GitCredential credential = 2; // M22: secret attached per-op, never persisted on the agent
}

message RedeployRequest {
  string target            = 1;
  GitCredential credential = 2; // M22
}
```

Regenerate:

Run: `go generate ./internal/pb`
(If `protoc` isn't found, use the direct command from Global Constraints.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/pb/ -run TestGitCredentialWire -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add proto/ internal/pb/
git commit -m "feat(proto): GitCredential + GitSource/ProcInfo credential fields (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Persist credential name in `config.GitSource`

**Files:**
- Modify: `internal/config/config.go` (GitSource struct)
- Test: `internal/config/config_test.go` (add a round-trip test, or the nearest existing config test file)

**Interfaces:**
- Produces: `config.GitSource.Credential string` (yaml `credential`, json `credential,omitempty`).

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go`:

```go
func TestGitSourceCredentialRoundTrip(t *testing.T) {
	src := GitSource{Repo: "https://x/y.git", Credential: "gh-ci"}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var got GitSource
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Credential != "gh-ci" {
		t.Fatalf("credential lost: %q", got.Credential)
	}
}
```

(Ensure `encoding/json` is imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestGitSourceCredentialRoundTrip -v`
Expected: FAIL — `unknown field Credential` / compile error.

- [ ] **Step 3: Add the field**

In `internal/config/config.go`, extend `GitSource`:

```go
type GitSource struct {
	Repo       string `yaml:"repo" json:"repo"`
	Ref        string `yaml:"ref" json:"ref,omitempty"`
	Build      string `yaml:"build" json:"build,omitempty"`
	Subdir     string `yaml:"subdir" json:"subdir,omitempty"`
	Credential string `yaml:"credential" json:"credential,omitempty"` // M22 credstore name
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestGitSourceCredentialRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): persist GitSource.Credential name (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Add `env` parameter to `deploy.Runner`

**Files:**
- Modify: `internal/deploy/deployer.go` (Runner interface + all `d.runner.Run(...)` call sites)
- Modify: `internal/deploy/exec_runner.go` (ExecRunner.Run signature + `cmd.Env`)
- Modify: `internal/deploy/deployer_test.go`, `internal/deploy/exec_runner_test.go` (fake runner + assertions)

**Interfaces:**
- Produces (consumed by Task 5):
  - `Runner.Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error`
  - `ExecRunner` sets `cmd.Env = append(os.Environ(), env...)` when `env != nil`; `nil` env → inherit only.

- [ ] **Step 1: Write the failing test**

Add to `internal/deploy/exec_runner_test.go`:

```go
func TestExecRunnerEnv(t *testing.T) {
	var buf bytes.Buffer
	err := ExecRunner{}.Run(context.Background(), "", []string{"MARSHAL_TEST_VAR=hello"}, &buf, &buf, "sh", "-c", "echo $MARSHAL_TEST_VAR")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "hello" {
		t.Fatalf("env not applied: %q", buf.String())
	}
}
```

(Imports: `bytes`, `context`, `strings`, `testing`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deploy/ -run TestExecRunnerEnv -v`
Expected: FAIL — too many arguments to `Run` (signature mismatch).

- [ ] **Step 3: Update the interface, ExecRunner, and call sites**

In `internal/deploy/deployer.go` change the interface:

```go
type Runner interface {
	Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error
}
```

Update every `d.runner.Run(ctx, dir, stdout, stderr, ...)` call in `deployer.go` to pass `nil` for env, e.g.:

```go
if err := d.runner.Run(ctx, "", nil, stdout, stderr, "git", "clone", src.Repo, dir); err != nil {
```

Do this for **all** call sites in `fetch` (clone, checkout, fetch, reset) and the build step `d.runner.Run(ctx, buildDir, nil, stdout, stderr, "sh", "-c", build)`.

In `internal/deploy/exec_runner.go`:

```go
func (ExecRunner) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
```

(Add `"os"` to the imports.)

In `internal/deploy/deployer_test.go`, update the fake runner to match the new signature and record env. Find the existing fake (a struct with a `Run` method and a slice of recorded calls) and change its method signature to `Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error`, storing `env` on each recorded call (add an `env []string` field to the recorded-call struct).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/deploy/ -count=1 -v`
Expected: PASS (the new env test + all existing deployer tests, now compiling against the new signature).

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/
git commit -m "refactor(deploy): thread env []string through Runner.Run (M22 seam)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Deployer credential injection via GIT_ASKPASS

**Files:**
- Modify: `internal/deploy/deployer.go` (`Credential` type, `Start`/`Redeploy` signatures, `runDeploy`, `fetch`, new `askpass` helper)
- Modify: `internal/daemon/command.go` (pass `GetCredential()` to deployer)
- Modify: `internal/deploy/deployer_test.go`

**Interfaces:**
- Consumes: `Runner.Run(..., env, ...)` (Task 4); `DeployRequest.GetCredential()` / `RedeployRequest.GetCredential()` (Task 2).
- Produces:
  - `type Credential struct { Username, Token string }`
  - `func (d *Deployer) Start(app config.App, cred Credential) error`
  - `func (d *Deployer) Redeploy(name string, cred Credential) error`
  - When `cred.Token != ""`, clone/fetch run with env `GIT_ASKPASS`, `MARSHAL_GIT_USER`, `MARSHAL_GIT_TOKEN`, `GIT_TERMINAL_PROMPT=0`, and the clone URL is rewritten to embed the username. Build step gets **no** credential env. Token never in argv.

- [ ] **Step 1: Write the failing test**

Add to `internal/deploy/deployer_test.go` (adapt the fake-runner field names to those already in the file):

```go
func TestCloneUsesAskpassAndHidesToken(t *testing.T) {
	fr := &fakeRunner{} // existing fake; records dir, env, name, args per call
	host := &fakeHost{} // existing fake host
	d := New(host, fr, t.TempDir())

	app := config.App{Name: "priv", Source: &config.GitSource{Repo: "https://github.com/me/priv.git"}}
	if err := d.Start(app, Credential{Username: "octocat", Token: "ghp_SECRET"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()

	clone := fr.find("clone") // helper: first recorded call whose args include "clone"
	if clone == nil {
		t.Fatal("no clone call recorded")
	}
	// Token must never appear in argv.
	for _, a := range clone.args {
		if strings.Contains(a, "ghp_SECRET") {
			t.Fatalf("token leaked into argv: %v", clone.args)
		}
	}
	// URL carries the username only.
	if !argvHas(clone.args, "https://octocat@github.com/me/priv.git") {
		t.Fatalf("username not embedded in clone URL: %v", clone.args)
	}
	// Credential env is present on the clone, token only in env.
	if !envHas(clone.env, "GIT_ASKPASS") || !envHas(clone.env, "MARSHAL_GIT_TOKEN=ghp_SECRET") ||
		!envHas(clone.env, "MARSHAL_GIT_USER=octocat") || !envHas(clone.env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("credential env missing/incomplete: %v", clone.env)
	}
}

func TestNoCredentialNoAskpass(t *testing.T) {
	fr := &fakeRunner{}
	d := New(&fakeHost{}, fr, t.TempDir())
	app := config.App{Name: "pub", Source: &config.GitSource{Repo: "https://github.com/me/pub.git"}}
	if err := d.Start(app, Credential{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()
	clone := fr.find("clone")
	if envHas(clone.env, "GIT_ASKPASS") {
		t.Fatalf("askpass set without a credential")
	}
	if !argvHas(clone.args, "https://github.com/me/pub.git") {
		t.Fatalf("URL should be unmodified without a credential: %v", clone.args)
	}
}
```

Add the small test helpers (next to the test, if not already present):

```go
func envHas(env []string, want string) bool {
	// "GIT_ASKPASS" matches any "GIT_ASKPASS=..."; "K=V" matches exactly.
	for _, e := range env {
		if e == want || strings.HasPrefix(e, want+"=") {
			return true
		}
	}
	return false
}
func argvHas(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
```

(And on the fake runner, add `func (f *fakeRunner) find(arg string) *call { for i := range f.calls { for _, a := range f.calls[i].args { if a == arg { return &f.calls[i] } } }; return nil }` — match the existing recorded-call type name.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/deploy/ -run 'TestCloneUsesAskpass|TestNoCredential' -v`
Expected: FAIL — `Start` takes 1 arg, not 2 (signature mismatch) / `undefined: Credential`.

- [ ] **Step 3: Implement the credential + askpass injection**

In `internal/deploy/deployer.go`:

```go
import (
	// ...existing...
	"net/url"
)

// Credential is an HTTPS git credential pushed per-deploy (M22). Empty Token
// means "no managed credential" — use the host's own git auth.
type Credential struct {
	Username string
	Token    string
}
```

Change the signatures and thread `cred` through:

```go
func (d *Deployer) Start(app config.App, cred Credential) error {
	// ...existing validation/state...
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runDeploy(app, cred, false)
	}()
	return nil
}

func (d *Deployer) Redeploy(name string, cred Credential) error {
	// ...existing lookup/state...
	app := config.App{Name: name, Source: &src}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runDeploy(app, cred, true)
	}()
	return nil
}
```

Update `runDeploy` to accept and forward `cred`, passing it only to `fetch` (NOT the build step):

```go
func (d *Deployer) runDeploy(app config.App, cred Credential, redeploy bool) {
	ctx := context.Background()
	dir := d.dir(app.Name)
	stdout, stderr := d.host.Writers(app.Name + "#0")
	src := *app.Source

	d.setState(app.Name, phaseCloning, "")
	if err := d.fetch(ctx, dir, src, cred, redeploy, stdout, stderr); err != nil {
		d.setState(app.Name, phaseFailed, summarize("clone", err))
		return
	}
	// ...build step unchanged: keep `nil` env (no credential during build)...
	// ...launch/restart unchanged...
}
```

Rewrite `fetch` to set up the askpass env and rewrite the clone URL when a token is present:

```go
func (d *Deployer) fetch(ctx context.Context, dir string, src config.GitSource, cred Credential, redeploy bool, stdout, stderr io.Writer) error {
	env, cleanup, err := d.gitCredEnv(cred)
	if err != nil {
		return err
	}
	defer cleanup()

	if !redeploy {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		cloneURL := src.Repo
		if cred.Token != "" {
			cloneURL = withUsername(src.Repo, cred.Username)
		}
		if err := d.runner.Run(ctx, "", env, stdout, stderr, "git", "clone", cloneURL, dir); err != nil {
			return err
		}
		if src.Ref != "" {
			return d.runner.Run(ctx, dir, env, stdout, stderr, "git", "checkout", src.Ref)
		}
		return nil
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if err := d.runner.Run(ctx, dir, env, stdout, stderr, "git", "fetch", "origin", ref); err != nil {
		return err
	}
	return d.runner.Run(ctx, dir, env, stdout, stderr, "git", "reset", "--hard", "FETCH_HEAD")
}

// gitCredEnv writes a throwaway GIT_ASKPASS helper and returns the env that
// makes git read the token from the environment (never argv/URL). cleanup
// removes the helper. With no token it returns (nil, noop, nil).
func (d *Deployer) gitCredEnv(cred Credential) (env []string, cleanup func(), err error) {
	if cred.Token == "" {
		return nil, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "marshal-askpass-")
	if err != nil {
		return nil, func() {}, err
	}
	script := filepath.Join(tmp, "askpass.sh")
	// $1 contains git's prompt text; "Username" → user, else → token.
	body := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s' \"$MARSHAL_GIT_USER\";; *) printf '%s' \"$MARSHAL_GIT_TOKEN\";; esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, func() {}, err
	}
	env = []string{
		"GIT_ASKPASS=" + script,
		"MARSHAL_GIT_USER=" + cred.Username,
		"MARSHAL_GIT_TOKEN=" + cred.Token,
		"GIT_TERMINAL_PROMPT=0",
	}
	return env, func() { _ = os.RemoveAll(tmp) }, nil
}

// withUsername injects a (non-secret) username into an https URL's authority.
func withUsername(raw, user string) string {
	if user == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.User = url.User(user)
	return u.String()
}
```

In `internal/daemon/command.go`, pass the credential from the proto:

```go
case *pb.ControlOp_Deploy:
	if s.deployer == nil {
		return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
	}
	app, cerr := appSpecToConfig(v.Deploy.GetApp())
	if cerr != nil {
		return &pb.ControlResult{Ok: false, Error: cerr.Error()}
	}
	c := v.Deploy.GetCredential()
	cred := deploy.Credential{Username: c.GetUsername(), Token: c.GetToken()}
	if derr := s.deployer.Start(app, cred); derr != nil {
		return &pb.ControlResult{Ok: false, Error: derr.Error()}
	}
	return &pb.ControlResult{Ok: true}

case *pb.ControlOp_Redeploy:
	if s.deployer == nil {
		return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
	}
	rc := v.Redeploy.GetCredential()
	cred := deploy.Credential{Username: rc.GetUsername(), Token: rc.GetToken()}
	if derr := s.deployer.Redeploy(v.Redeploy.GetTarget(), cred); derr != nil {
		return &pb.ControlResult{Ok: false, Error: derr.Error()}
	}
	return &pb.ControlResult{Ok: true}
```

(Ensure `internal/daemon/command.go` imports `marshal/internal/deploy`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/deploy/ ./internal/daemon/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/ internal/daemon/command.go
git commit -m "feat(deploy): inject managed git credential via GIT_ASKPASS (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Stamp credential name onto the fleet snapshot

**Files:**
- Modify: `internal/daemon/convert.go` (map `GitSource.Credential` into `config.App`)
- Modify: `internal/manager/manager.go` (`InstanceSnapshot.Credential` + `snapshotApp` stamping)
- Modify: `internal/daemon/fleet.go` (`snapshotToProc` maps `Credential`)
- Test: `internal/daemon/fleet_test.go` (or the nearest snapshot test)

**Interfaces:**
- Consumes: `config.GitSource.Credential` (Task 3); `ProcInfo.Credential` (Task 2).
- Produces: `manager.InstanceSnapshot.Credential string`; real git instances report `ProcInfo.Credential = <name>`.

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/fleet_test.go`:

```go
func TestSnapshotToProcCredential(t *testing.T) {
	p := snapshotToProc(manager.InstanceSnapshot{
		Name:       "priv",
		Source:     "git",
		Credential: "gh-ci",
	}, 0, 0)
	if p.GetCredential() != "gh-ci" {
		t.Fatalf("credential not stamped: %q", p.GetCredential())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestSnapshotToProcCredential -v`
Expected: FAIL — `unknown field Credential in struct literal` (manager.InstanceSnapshot has no Credential).

- [ ] **Step 3: Add the field and stamping**

In `internal/manager/manager.go`, add to `InstanceSnapshot`:

```go
Credential string // M22 credstore name, from the app spec's GitSource
```

In `snapshotApp`, derive it next to `src`:

```go
src := "command"
cred := ""
if a.spec.Source != nil {
	src = "git"
	cred = a.spec.Source.Credential
}
```

and set `Credential: cred` in the `InstanceSnapshot{...}` literal.

In `internal/daemon/convert.go`, map the field where `app.Source` is built:

```go
if gs := s.GetSource(); gs != nil {
	app.Source = &config.GitSource{
		Repo:       gs.GetRepo(),
		Ref:        gs.GetRef(),
		Build:      gs.GetBuild(),
		Subdir:     gs.GetSubdir(),
		Credential: gs.GetCredential(),
	}
}
```

In `internal/daemon/fleet.go` `snapshotToProc`, add to the returned `pb.ProcInfo{...}`:

```go
Credential: s.Credential,
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ ./internal/manager/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/ internal/manager/
git commit -m "feat(fleet): stamp credential name onto git proc snapshots (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Dashboard credentials CRUD endpoints + server wiring

**Files:**
- Create: `internal/dashboard/credentials.go`
- Create: `internal/dashboard/credentials_test.go`
- Modify: `internal/dashboard/handlers.go` (handler field + newHandler param + routes)
- Modify: `internal/dashboard/server.go` (Serve param)
- Modify: `internal/server/server.go` (`credstore.Open` in ServeDir, pass through)
- Modify: `internal/dashboard/handlers_test.go` / helpers as needed for the new newHandler arg

**Interfaces:**
- Consumes: `credstore` (Task 1).
- Produces:
  - `type Credentials interface { List() []credstore.Meta; Put(name, username, token string) error; Get(name string) (string, string, bool, error); Delete(name string) bool }` (satisfied by `*credstore.Store`).
  - Routes: `GET /api/credentials`, `POST /api/credentials`, `DELETE /api/credentials/{name}` — all behind `requireSession`.
  - `newHandler(..., creds Credentials)` and `dashboard.Serve(..., creds Credentials)`; nil `creds` ⇒ feature disabled (503).

- [ ] **Step 1: Write the failing test**

Create `internal/dashboard/credentials_test.go`:

```go
package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marshal/internal/credstore"
)

func TestCredentialsCRUD(t *testing.T) {
	cs, err := credstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHandlerWithCreds(t, cs) // helper defined below

	// Create.
	rec := httptest.NewRecorder()
	h.createCredential(rec, httptest.NewRequest("POST", "/api/credentials",
		strings.NewReader(`{"name":"gh-ci","username":"octocat","token":"ghp_x"}`)))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create: %d", rec.Code)
	}

	// List has no token.
	rec = httptest.NewRecorder()
	h.listCredentials(rec, httptest.NewRequest("GET", "/api/credentials", nil))
	if rec.Code != 200 {
		t.Fatalf("list: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "ghp_x") {
		t.Fatalf("token leaked into list response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gh-ci") {
		t.Fatalf("created credential missing from list: %s", rec.Body.String())
	}
}

func TestCredentialsDisabledWhenNil(t *testing.T) {
	h := newTestHandlerWithCreds(t, nil)
	rec := httptest.NewRecorder()
	h.listCredentials(rec, httptest.NewRequest("GET", "/api/credentials", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when disabled, got %d", rec.Code)
	}
}
```

Add a small constructor helper to `internal/dashboard/credentials_test.go` (mirror the existing test-handler construction in `handlers_test.go`; pass nil for the fleet/metrics/logs/controller/auth deps that these endpoints don't touch):

```go
func newTestHandlerWithCreds(t *testing.T, creds Credentials) *handler {
	t.Helper()
	return newHandler(nil, nil, nil, nil, nil, time.Hour, "", "", creds)
}
```

(Add `time` import. If existing tests already have a richer helper, reuse it and just thread `creds`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestCredentials -v`
Expected: FAIL — `too many arguments in call to newHandler` / `undefined: h.createCredential`.

- [ ] **Step 3: Implement endpoints + wiring**

Create `internal/dashboard/credentials.go`:

```go
package dashboard

import (
	"encoding/json"
	"net/http"

	"marshal/internal/credstore"
)

// Credentials is the subset of credstore.Store the dashboard needs.
type Credentials interface {
	List() []credstore.Meta
	Get(name string) (username, token string, ok bool, err error)
	Put(name, username, token string) error
	Delete(name string) bool
}

type credentialReq struct {
	Name     string `json:"name"`
	Username string `json:"username"`
	Token    string `json:"token"`
}

func (h *handler) listCredentials(w http.ResponseWriter, r *http.Request) {
	if h.creds == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, h.creds.List())
}

func (h *handler) createCredential(w http.ResponseWriter, r *http.Request) {
	if h.creds == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	var body credentialReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.Token == "" {
		http.Error(w, "name and token required", http.StatusBadRequest)
		return
	}
	if err := h.creds.Put(body.Name, body.Username, body.Token); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: credential.put %s (user=%s) by %s", body.Name, body.Username, user) // never log the token
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (h *handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if h.creds == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	if !h.creds.Delete(name) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: credential.delete %s by %s", name, user)
	w.WriteHeader(http.StatusNoContent)
}
```

Add `"log"` to the `credentials.go` imports. We deliberately use `log.Printf` (not `h.audit`) for credential operations: the `audit.Log`/`audit.Event` type is login-specific (Time/User/IP/Outcome, no action or resource field), and M21's deploy/redeploy actions are already recorded the same way via `log.Printf` in `dispatchApp`. This matches the existing codebase pattern and satisfies the spec's intent (record action + name + username, **never** the token).

In `internal/dashboard/handlers.go`:
- add `creds Credentials` to the `handler` struct;
- add `creds Credentials` as the final param of `newHandler` and set `h.creds = creds`;
- update the package-level `NewHandler` shim to pass `nil`;
- register routes:

```go
mux.HandleFunc("GET /api/credentials", h.requireSession(h.listCredentials))
mux.HandleFunc("POST /api/credentials", h.requireSession(h.createCredential))
mux.HandleFunc("DELETE /api/credentials/{name}", h.requireSession(h.deleteCredential))
```

In `internal/dashboard/server.go`, add `creds Credentials` as the final param of `Serve` and pass it to `newHandler`.

In `internal/server/server.go` `ServeDir`, open the store and pass it through (around the existing `sessionsPath`/`auditPath` block):

```go
creds, cerr := credstore.Open(dataDir)
if cerr != nil {
	log.Printf("server: credentials disabled: %v", cerr)
	creds = nil // ordinary deploys still work; credential endpoints return 503
}
// ... in the dashboard.Serve(...) call, append creds as the final argument.
```

(Import `marshal/internal/credstore` in `internal/server/server.go`. `credstore.Open` returns `*credstore.Store`, which satisfies `dashboard.Credentials`; when `cerr != nil`, pass `nil` — but note a typed-nil pitfall: assign to a `dashboard.Credentials` variable as nil, or pass a literal `nil` of interface type. Easiest: `var cw dashboard.Credentials; if cerr == nil { cw = creds }` and pass `cw`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ ./internal/server/ -count=1`
Expected: PASS. Fix any other `newHandler`/`Serve` call sites the compiler flags (pass `nil`/`cw`).

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/ internal/server/server.go
git commit -m "feat(dashboard): credentials CRUD endpoints + credstore wiring (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Resolve + attach credential on deploy/redeploy; expose in procView

**Files:**
- Modify: `internal/dashboard/apps.go` (`gitSource.Credential`, `redeployRequest.Credential`, resolve+attach, `deployOp`)
- Modify: `internal/dashboard/fleet.go` (`procView.Credential`)
- Test: `internal/dashboard/apps_test.go`, `internal/dashboard/fleet_test.go`

**Interfaces:**
- Consumes: `h.creds.Get` (Task 7); `pb.GitCredential`, `pb.DeployRequest.Credential`, `pb.RedeployRequest.Credential`, `ProcInfo.GetCredential()` (Task 2).
- Produces: deploy/redeploy attach `*pb.GitCredential` when a credential name is given; `procView.Credential` JSON field.

- [ ] **Step 1: Write the failing tests**

Add to `internal/dashboard/fleet_test.go`:

```go
func TestProcViewCredential(t *testing.T) {
	// Build a fleet lister fake whose ProcInfo carries Credential, then assert
	// fleetView surfaces it. (Mirror the existing procView test setup.)
	views := fleetView(fakeListerWithProc(&pb.ProcInfo{Name: "priv", Source: "git", Credential: "gh-ci"}))
	if views[0].Procs[0].Credential != "gh-ci" {
		t.Fatalf("credential dropped by procView: %+v", views[0].Procs[0])
	}
}
```

Add to `internal/dashboard/apps_test.go` (mirror the existing deploy-mapping test; use a fake controller that captures the op and a credstore seeded with one credential):

```go
func TestDeployAttachesResolvedCredential(t *testing.T) {
	cs, _ := credstore.Open(t.TempDir())
	_ = cs.Put("gh-ci", "octocat", "ghp_SECRET")
	fc := &captureController{} // existing fake; records the last op
	h := newHandler(nil, nil, nil, fc, stubAuth{}, time.Hour, "", "", cs)

	body := `{"agent":"a1","source":{"type":"git","name":"priv","repo":"https://x/y.git","credential":"gh-ci"}}`
	rec := httptest.NewRecorder()
	req := authedRequest(t, "POST", "/api/apps", body) // helper that injects a session user
	h.apps(rec, req)

	dep := fc.last.GetDeploy()
	if dep == nil || dep.GetCredential().GetToken() != "ghp_SECRET" {
		t.Fatalf("token not resolved+attached: %+v", fc.last)
	}
	if dep.GetApp().GetSource().GetCredential() != "gh-ci" {
		t.Fatalf("credential name not set on GitSource")
	}
}

func TestDeployUnknownCredential(t *testing.T) {
	cs, _ := credstore.Open(t.TempDir())
	h := newHandler(nil, nil, nil, &captureController{}, stubAuth{}, time.Hour, "", "", cs)
	body := `{"agent":"a1","source":{"type":"git","name":"priv","repo":"https://x/y.git","credential":"nope"}}`
	rec := httptest.NewRecorder()
	h.apps(rec, authedRequest(t, "POST", "/api/apps", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown credential, got %d", rec.Code)
	}
}
```

(Reuse the controller/auth fakes already present in `apps_test.go`/`handlers_test.go`. If `authedRequest` doesn't exist, set the user via the same context key the existing apps tests use.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/dashboard/ -run 'TestProcViewCredential|TestDeploy(Attaches|Unknown)' -v`
Expected: FAIL — `procView` has no `Credential` field; deploy doesn't attach a credential.

- [ ] **Step 3: Implement**

In `internal/dashboard/fleet.go`, add to `procView`:

```go
Credential string `json:"credential,omitempty"` // M22 credential name (drives redeploy)
```

and in the `procView{...}` literal:

```go
Credential: p.GetCredential(),
```

In `internal/dashboard/apps.go`:
- add `Credential string \`json:"credential"\`` to `gitSource` and `redeployRequest`;
- in the `"git"` case of `apps`, after validation, resolve and attach:

```go
case "git":
	var g gitSource
	if err := json.Unmarshal(body.Source, &g); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if g.Name == "" || g.Repo == "" {
		http.Error(w, "name and repo required", http.StatusBadRequest)
		return
	}
	cred, cerr := h.resolveCredential(g.Credential)
	if cerr != nil {
		http.Error(w, cerr.Error(), http.StatusBadRequest)
		return
	}
	op, name = deployOp(g, cred), g.Name
```

- change `deployOp` to take and attach the resolved credential and set the name on the GitSource:

```go
func deployOp(g gitSource, cred *pb.GitCredential) *pb.ControlOp {
	spec := &pb.AppSpec{
		Name:      g.Name,
		Cmd:       g.Cmd,
		Args:      g.Args,
		Instances: g.Instances,
		Env:       g.Env,
		Restart:   g.Restart,
		Source:    &pb.GitSource{Repo: g.Repo, Ref: g.Ref, Build: g.Build, Subdir: g.Subdir, Credential: g.Credential},
	}
	return &pb.ControlOp{Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: spec, Credential: cred}}}
}
```

- add the resolver helper (returns nil credential when name is empty; error on unknown / disabled):

```go
// resolveCredential turns a credential name into the secret to attach. Empty
// name → (nil, nil) = no managed credential.
func (h *handler) resolveCredential(name string) (*pb.GitCredential, error) {
	if name == "" {
		return nil, nil
	}
	if h.creds == nil {
		return nil, fmt.Errorf("credentials unavailable")
	}
	user, tok, ok, err := h.creds.Get(name)
	if err != nil {
		return nil, fmt.Errorf("credential %q: %v", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("unknown credential %q", name)
	}
	return &pb.GitCredential{Username: user, Token: tok}, nil
}
```

- update `redeploy` to resolve+attach too:

```go
func (h *handler) redeploy(w http.ResponseWriter, r *http.Request) {
	var body redeployRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" || body.Name == "" {
		http.Error(w, "agent and name required", http.StatusBadRequest)
		return
	}
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		http.Error(w, cerr.Error(), http.StatusBadRequest)
		return
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Redeploy{Redeploy: &pb.RedeployRequest{Target: body.Name, Credential: cred}}}
	h.dispatchApp(w, r, body.Agent, body.Name, op, "redeploy")
}
```

(Add `"fmt"` to `apps.go` imports.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/apps.go internal/dashboard/fleet.go internal/dashboard/*_test.go
git commit -m "feat(dashboard): resolve+attach credential on deploy/redeploy; expose in procView (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Web client — credentials UI + deploy selector

**Files:**
- Modify: `web/src/api.ts` (types + `listCredentials`/`createCredential`/`deleteCredential`, widen `GitSource`/`redeploy`)
- Modify: `web/src/AddAppModal.tsx` (credential dropdown in git mode)
- Modify: `web/src/ProcessCard.tsx` (pass the proc's credential to `redeploy`)
- Create: `web/src/Credentials.tsx` (list/add/delete view)
- Modify: `web/src/App.tsx` + `web/src/router.ts` (route/section for Credentials)
- Modify: `web/src/styles.css` (reuse existing form/list classes; add only what's missing)
- Build: `make ui`

**Interfaces:**
- Consumes: `GET/POST/DELETE /api/credentials`; `procView.credential` (Task 8).
- Produces: a Credentials management view; a credential `<select>` in the add-app git form; redeploy sends the persisted credential name.

- [ ] **Step 1: Add API client functions and types**

In `web/src/api.ts`:

```ts
export interface CredentialMeta {
  name: string;
  type: string;
  username: string;
  created_at: number;
}

export async function listCredentials(): Promise<CredentialMeta[]> {
  const r = await fetch("/api/credentials");
  if (r.status !== 200) return [];
  return (await r.json()) as CredentialMeta[];
}

export async function createCredential(
  name: string,
  username: string,
  token: string,
): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch("/api/credentials", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, username, token }),
  });
  if (r.status === 201 || r.status === 200) return { ok: true };
  try {
    const j = await r.json();
    return { ok: false, error: (j.error as string) ?? `error ${r.status}` };
  } catch {
    return { ok: false, error: `error ${r.status}` };
  }
}

export async function deleteCredential(name: string): Promise<{ ok: boolean; error?: string }> {
  const r = await fetch(`/api/credentials/${encodeURIComponent(name)}`, { method: "DELETE" });
  if (r.status === 204) return { ok: true };
  return { ok: false, error: `error ${r.status}` };
}
```

Widen `GitSource` with `credential?: string;` and change `redeploy` to accept an optional credential name:

```ts
export async function redeploy(
  agent: string,
  name: string,
  credential?: string,
): Promise<{ ok: boolean; error?: string }> {
  const res = await fetch("/api/apps/redeploy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, name, credential }),
  });
  if (res.status === 401) throw new Error("error 401");
  try {
    return (await res.json()) as { ok: boolean; error?: string };
  } catch {
    return { ok: false, error: `error ${res.status}` };
  }
}
```

Also add `credential?: string` to the `Proc` type (wherever `source`/`detail` were added in M21) so `ProcessCard` can read it.

- [ ] **Step 2: Add the credential dropdown to the add-app git form**

In `web/src/AddAppModal.tsx`: load credentials on mount (`listCredentials()` into state) and, inside the git-mode fields, render a `<select>` whose value flows into the submitted git source's `credential`:

```tsx
// state: const [creds, setCreds] = useState<CredentialMeta[]>([]);
//        const [credential, setCredential] = useState("");
// effect: useEffect(() => { listCredentials().then(setCreds); }, []);

<label>
  Credential
  <select value={credential} onChange={(e) => setCredential(e.target.value)}>
    <option value="">None (public repo / host auth)</option>
    {creds.map((c) => (
      <option key={c.name} value={c.name}>{c.name} ({c.username})</option>
    ))}
  </select>
</label>
```

Include `credential: credential || undefined` in the git source object passed to `addApp`.

- [ ] **Step 3: Pass the credential on redeploy**

In `web/src/ProcessCard.tsx`, where it calls `redeploy(agent, name)`, pass the proc's credential: `redeploy(agent, proc.name, proc.credential)`.

- [ ] **Step 4: Create the Credentials management view**

Create `web/src/Credentials.tsx` following the existing component style (functional component, `styles.css` classes used elsewhere). It lists `CredentialMeta[]` (name / username / created), has an add form (name, username, token — `<input type="password">` for the token), a delete button per row, and refreshes via `listCredentials()` after create/delete. Wire it into `web/src/App.tsx` + `web/src/router.ts` as a new section/route alongside the existing views (e.g. a "Credentials" nav entry). Keep the token field write-only — never render an existing token (the API never returns one).

- [ ] **Step 5: Build the web client**

Run: `make ui`
Expected: `tsc -b` passes with no type errors; `web/ → internal/dashboard/dist` rebuilt (tracked, embedded).

- [ ] **Step 6: Commit**

```bash
git add web/ internal/dashboard/dist/
git commit -m "feat(web): credentials management UI + deploy credential selector (M22)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Final verification (before handoff/merge)

- [ ] `go test ./... -race -count=1` — all packages green.
- [ ] `gofmt -l .` — silent; `go vet ./...` — clean.
- [ ] `make ui` — `tsc -b` clean.
- [ ] **Live demo** (per CLAUDE.md), scratch `XDG_DATA_HOME=/tmp/marshal-m22-demo`, server `:9000`/`:9001`, auth set while server is **down**: create a credential for a **private** repo; deploy it selecting the credential → `cloning → building → online`; **grep the per-app log and confirm the token does not appear**; rotate the token + redeploy; delete the credential. Tear down (agent + server + Vite, scratch dir, `.claude/launch.json`) and confirm `pgrep -fl marshal` shows no orphans.
- [ ] Write the handoff `docs/handoffs/2026-06-19-m22-managed-git-credentials.md`.
- [ ] Merge `m22-managed-git-credentials` to `main` with `--no-ff` (no remote).

## Self-review (spec coverage map)

- Spec §`internal/credstore` (AES-GCM, master key, CRUD, List-no-secret) → **Task 1**. ✓
- Spec §Wire contract (GitCredential, GitSource/ProcInfo.credential, request fields) → **Task 2**. ✓
- Spec §config (persist credential name) → **Task 3**. ✓
- Spec §Agent deployer + runner (env param) → **Task 4**; (askpass injection, build gets no cred, URL username) → **Task 5**. ✓
- Spec §Redeploy symmetric + ProcInfo.credential through procView → **Task 6** (snapshot stamp) + **Task 8** (procView + redeploy attach). ✓
- Spec §Server wiring (resolve+attach) → **Task 8**; (credstore.Open, feature-disabled 503) → **Task 7**. ✓
- Spec §Dashboard HTTP (CRUD, 401, no-token-in-list) → **Task 7**; (deploy/redeploy resolve, unknown→400) → **Task 8**. ✓
- Spec §Dashboard web (credentials view, dropdown, redeploy reuse) → **Task 9**. ✓
- Spec §Error handling (unknown cred 400, disabled 503, GCM decrypt error) → Tasks 1/7/8. ✓
- Spec §Audit (name+username, never token) → **Task 7** (auditCredEvent / log fallback). ✓
- Spec §Testing & live demo → per-task tests + Final verification. ✓
