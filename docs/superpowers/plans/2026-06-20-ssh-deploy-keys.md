# SSH Deploy Keys Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a dashboard user deploy, redeploy, and edit/push a git app from an SSH-authenticated repo using a Marshal-generated ed25519 deploy key, with server-pinned strict host-key verification.

**Architecture:** Second credential *type* alongside the M22 HTTPS token. The server generates the keypair (`ssh-keygen`), seals the private key at rest (existing AES-256-GCM credstore), and shows the public key to register as a deploy key. On first deploy the server scans the repo host (`ssh-keyscan`), pins the result into the credential, and pushes `{private_key, known_hosts, kind:SSH}` down per-op over the existing TLS fleet link. The agent writes the key + pin to short-lived `0600` temp files and routes every git op through `GIT_SSH_COMMAND` with `StrictHostKeyChecking=yes` — so clone, fetch, and M24 push all work with one mechanism and the agent never trusts a key the server did not pin.

**Tech Stack:** Go (stdlib + existing deps; no new Go module — generation shells out to `ssh-keygen`, scanning to `ssh-keyscan`), protobuf/gRPC, React/TypeScript dashboard.

## Global Constraints

- Go 1.26.4; module path `marshal`; imports `marshal/internal/...`.
- No new Go dependency — generation/scan shell out to `ssh-keygen`/`ssh-keyscan` (both on PATH), consistent with driving `git` via the `Runner`.
- TDD: failing test first, then minimal implementation. `go test ./... -race -count=1` green; `gofmt -l .` silent; `go vet ./...` clean before finishing.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- All proto changes are **additive** (new field numbers / new enum) so an M22/M24 agent still works.
- Secrets (private key) never appear in argv, the clone/push URL, the per-app log, `dump.json`, or the audit log. Logs record credential **name + kind** only.
- Work happens on branch `m25-ssh-deploy-keys` (already created; spec already committed there).

---

### Task 1: Proto — `GitCredential` SSH fields + `CredentialKind` enum

**Files:**
- Modify: `proto/marshal/v1/fleet.proto` (the `GitCredential` message, ~line 101)
- Regenerate: `internal/pb/fleet.pb.go` (via `go generate ./internal/pb`)
- Test: `internal/pb/gitsource_test.go` (add a pin test asserting the new accessors exist)

**Interfaces:**
- Produces: `pb.GitCredential` gains `GetPrivateKey() string`, `GetKnownHosts() string`, `GetKind() pb.CredentialKind`. New enum `pb.CredentialKind` with `pb.CredentialKind_CRED_HTTPS` (0) and `pb.CredentialKind_CRED_SSH` (1).

- [ ] **Step 1: Write the failing test**

Append to `internal/pb/gitsource_test.go`:

```go
func TestGitCredentialSSHFields(t *testing.T) {
	c := &pb.GitCredential{
		PrivateKey: "PRIV",
		KnownHosts: "github.com ssh-ed25519 AAAA",
		Kind:       pb.CredentialKind_CRED_SSH,
	}
	if c.GetPrivateKey() != "PRIV" || c.GetKnownHosts() == "" {
		t.Fatal("ssh fields not carried")
	}
	if c.GetKind() != pb.CredentialKind_CRED_SSH {
		t.Fatalf("kind = %v, want CRED_SSH", c.GetKind())
	}
	// default kind is HTTPS so legacy agents/messages keep working
	if (&pb.GitCredential{}).GetKind() != pb.CredentialKind_CRED_HTTPS {
		t.Fatal("zero-value kind must be CRED_HTTPS")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/pb/ -run TestGitCredentialSSHFields`
Expected: FAIL (compile error — `PrivateKey`/`KnownHosts`/`Kind`/`CredentialKind_CRED_SSH` undefined).

- [ ] **Step 3: Edit the proto**

In `proto/marshal/v1/fleet.proto`, replace the `GitCredential` message with:

```proto
enum CredentialKind {
  CRED_HTTPS = 0;
  CRED_SSH   = 1;
}

message GitCredential {
  string         username    = 1;
  string         token       = 2; // HTTPS only
  string         private_key = 3; // SSH only — sealed in transit by TLS; never persisted on the agent
  string         known_hosts = 4; // SSH only — the server-pinned host key line(s)
  CredentialKind kind        = 5; // default CRED_HTTPS keeps M22/M24 agents working
}
```

- [ ] **Step 4: Regenerate the bindings**

Run: `go generate ./internal/pb`
Expected: no output; `git status` shows `internal/pb/fleet.pb.go` modified.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/pb/ -run TestGitCredentialSSHFields && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 6: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/fleet.pb.go internal/pb/gitsource_test.go
git commit -m "feat(m25): add SSH fields + CredentialKind to GitCredential proto

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: credstore — `ssh-key` type (`Generate` / `GetKey` / `SetKnownHosts`, `Meta.PublicKey`)

**Files:**
- Modify: `internal/credstore/credstore.go`
- Test: `internal/credstore/credstore_test.go`

**Interfaces:**
- Consumes: existing `entry`, `Store`, `flushLocked`, `nameRE`, `nowUnix`.
- Produces:
  - `(*Store).Generate(name string) (publicKey string, err error)` — mint ed25519 keypair, seal private key, store `Type:"ssh-key"`, `Username:"git"`, `PublicKey`, empty `KnownHosts`; upsert.
  - `(*Store).GetKey(name string) (privateKey, knownHosts string, ok bool, err error)`.
  - `(*Store).SetKnownHosts(name, line string) error`.
  - `Meta` gains `PublicKey string` (json `public_key`, empty for https-token rows).
  - Package var seam `genKeypair func() (priv, pub []byte, err error)` (default shells out to `ssh-keygen`) so tests can stub.

- [ ] **Step 1: Write the failing test**

Append to `internal/credstore/credstore_test.go`:

```go
func TestGenerateAndGetKey(t *testing.T) {
	dir := t.TempDir()
	s, err := credstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.Generate("deploykey")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("public key = %q, want ssh-ed25519 prefix", pub)
	}

	// Meta exposes the public key + type, never the private key.
	metas := s.List()
	if len(metas) != 1 || metas[0].Type != "ssh-key" || metas[0].PublicKey != pub {
		t.Fatalf("meta = %+v", metas)
	}

	// The private key round-trips via GetKey...
	priv, kh, ok, err := s.GetKey("deploykey")
	if err != nil || !ok {
		t.Fatalf("GetKey ok=%v err=%v", ok, err)
	}
	if !strings.Contains(priv, "PRIVATE KEY") {
		t.Fatalf("private key not returned: %q", priv)
	}
	if kh != "" {
		t.Fatalf("known_hosts should start empty, got %q", kh)
	}

	// ...but is NOT present in plaintext on disk.
	raw, _ := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if bytes.Contains(raw, []byte(priv)) {
		t.Fatal("private key leaked to credentials.json in plaintext")
	}

	// SetKnownHosts persists the pin.
	if err := s.SetKnownHosts("deploykey", "github.com ssh-ed25519 AAAA"); err != nil {
		t.Fatal(err)
	}
	_, kh2, _, _ := s.GetKey("deploykey")
	if kh2 != "github.com ssh-ed25519 AAAA" {
		t.Fatalf("pin not persisted: %q", kh2)
	}
}

func TestHTTPSEntriesStillWork(t *testing.T) {
	s, err := credstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("tok", "octocat", "ghp_xxx"); err != nil {
		t.Fatal(err)
	}
	u, tk, ok, err := s.Get("tok")
	if err != nil || !ok || u != "octocat" || tk != "ghp_xxx" {
		t.Fatalf("https get broke: u=%q tk=%q ok=%v err=%v", u, tk, ok, err)
	}
	if m := s.List(); m[0].Type != "https-token" || m[0].PublicKey != "" {
		t.Fatalf("https meta wrong: %+v", m[0])
	}
}
```

Ensure the test imports include `bytes`, `os`, `path/filepath`, `strings` (add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/credstore/ -run 'TestGenerateAndGetKey|TestHTTPSEntriesStillWork'`
Expected: FAIL (compile error — `Generate`/`GetKey`/`SetKnownHosts`/`Meta.PublicKey` undefined).

- [ ] **Step 3: Implement**

In `internal/credstore/credstore.go`:

Add `os/exec` to imports. Add `PublicKey` to both `Meta` and `entry`:

```go
// Meta is the non-secret view of a credential.
type Meta struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Username  string `json:"username"`
	PublicKey string `json:"public_key,omitempty"` // ssh-key only
	CreatedAt int64  `json:"created_at"`
}

type entry struct {
	Type      string `json:"type"`
	Username  string `json:"username"`
	PublicKey string `json:"public_key,omitempty"` // ssh-key only (not secret)
	Nonce     string `json:"nonce"`                 // base64 std
	Cipher    string `json:"cipher"`                // base64 std (token OR private key)
	CreatedAt int64  `json:"created_at"`
}
```

Update `List` to copy `PublicKey`:

```go
out = append(out, Meta{Name: name, Type: e.Type, Username: e.Username, PublicKey: e.PublicKey, CreatedAt: e.CreatedAt})
```

Add the seal/open helpers refactored out of `Put`/`Get` (reuse the existing AES-GCM code) plus the new methods:

```go
// seal encrypts plaintext under the master key, returning base64 nonce + cipher.
func (s *Store) seal(plaintext string) (nonceB64, cipherB64 string, err error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(nonce), base64.StdEncoding.EncodeToString(ct), nil
}

// genKeypair mints an ed25519 keypair via ssh-keygen, returning OpenSSH-format
// private key bytes and the authorized_keys public-key line. A var seam so tests stub it.
var genKeypair = func() (priv, pub []byte, err error) {
	tmp, err := os.MkdirTemp("", "marshal-keygen-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	key := filepath.Join(tmp, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "marshal", "-f", key, "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, nil, fmt.Errorf("ssh-keygen: %v: %s", err, out)
	}
	priv, err = os.ReadFile(key)
	if err != nil {
		return nil, nil, err
	}
	pub, err = os.ReadFile(key + ".pub")
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

// Generate mints and stores an ssh-key credential, returning the public key line.
func (s *Store) Generate(name string) (string, error) {
	if !nameRE.MatchString(name) {
		return "", fmt.Errorf("invalid credential name %q", name)
	}
	priv, pub, err := genKeypair()
	if err != nil {
		return "", err
	}
	pubLine := strings.TrimSpace(string(pub))
	nonceB64, cipherB64, err := s.seal(string(priv))
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	created := int64(0)
	if old, ok := s.data[name]; ok {
		created = old.CreatedAt
	}
	if created == 0 {
		created = nowUnix()
	}
	s.data[name] = entry{
		Type:      "ssh-key",
		Username:  "git",
		PublicKey: pubLine,
		Nonce:     nonceB64,
		Cipher:    cipherB64,
		CreatedAt: created,
	}
	err = s.flushLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return pubLine, nil
}

// GetKey decrypts an ssh-key credential's private key and returns it with the pin.
func (s *Store) GetKey(name string) (privateKey, knownHosts string, ok bool, err error) {
	s.mu.Lock()
	e, present := s.data[name]
	s.mu.Unlock()
	if !present || e.Type != "ssh-key" {
		return "", "", false, nil
	}
	pt, err := s.openCipher(e.Nonce, e.Cipher)
	if err != nil {
		return "", "", false, err
	}
	return pt, e.knownHosts(), true, nil
}

// SetKnownHosts records the pinned host key for an ssh-key credential.
func (s *Store) SetKnownHosts(name, line string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[name]
	if !ok || e.Type != "ssh-key" {
		return fmt.Errorf("no ssh credential %q", name)
	}
	e.KnownHosts = line
	s.data[name] = e
	return s.flushLocked()
}
```

Add `KnownHosts string json:"known_hosts,omitempty"` to `entry`, a tiny `func (e entry) knownHosts() string { return e.KnownHosts }` accessor (or read `e.KnownHosts` directly — drop the accessor and use `e.KnownHosts` in `GetKey`), and refactor `Get`'s decrypt body into `openCipher(nonceB64, cipherB64 string) (string, error)` (lift the existing AES-GCM open code). Update `Put` to call `s.seal(token)` instead of inlining the cipher (optional cleanup; keep behavior identical).

> Note: store `entry.KnownHosts` directly; in `GetKey` return `e.KnownHosts`. Remove the `knownHosts()` accessor shown above if you inline it.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/credstore/ -count=1`
Expected: PASS (all, including the existing M22 tests). Requires `ssh-keygen` on PATH.

- [ ] **Step 5: Commit**

```bash
git add internal/credstore/credstore.go internal/credstore/credstore_test.go
git commit -m "feat(m25): credstore ssh-key type (Generate/GetKey/SetKnownHosts)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: deploy — `Credential` SSH fields, `gitCredEnv` SSH branch, redacting `String()`

**Files:**
- Modify: `internal/deploy/deployer.go` (`Credential` struct, `gitCredEnv`, `fetch`)
- Modify: `internal/deploy/mutate.go` (push `credActive`)
- Test: `internal/deploy/deployer_test.go`

**Interfaces:**
- Consumes: existing `Runner`, `gitCredEnv`, `gitArgs`, `withUsername`.
- Produces:
  - `deploy.Credential` gains `PrivateKey string`, `KnownHosts string`, `SSH bool`.
  - `(Credential).httpsActive() bool` → `!c.SSH && c.Token != ""`.
  - `(Credential).String() string` → redacting (`username`/kind only, never secrets).
  - `gitCredEnv` returns a `GIT_SSH_COMMAND` env for SSH credentials.

- [ ] **Step 1: Write the failing test**

Append to `internal/deploy/deployer_test.go`:

```go
func TestGitCredEnvSSH(t *testing.T) {
	d := deploy.New(nil, nil, t.TempDir())
	cred := deploy.Credential{
		SSH:        true,
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nzzz\n-----END OPENSSH PRIVATE KEY-----\n",
		KnownHosts: "github.com ssh-ed25519 AAAA",
	}
	env, cleanup, err := deploy.ExportGitCredEnv(d, cred)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	var sshCmd, keyPath, khPath string
	for _, e := range env {
		if strings.HasPrefix(e, "GIT_SSH_COMMAND=") {
			sshCmd = strings.TrimPrefix(e, "GIT_SSH_COMMAND=")
		}
	}
	if sshCmd == "" {
		t.Fatal("no GIT_SSH_COMMAND in env")
	}
	for _, want := range []string{"StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "UserKnownHostsFile=", "-i "} {
		if !strings.Contains(sshCmd, want) {
			t.Fatalf("GIT_SSH_COMMAND %q missing %q", sshCmd, want)
		}
	}
	// pull the -i <key> and UserKnownHostsFile=<kh> paths back out and assert 0600 + contents
	fields := strings.Fields(sshCmd)
	for i, f := range fields {
		if f == "-i" {
			keyPath = fields[i+1]
		}
		if strings.HasPrefix(f, "UserKnownHostsFile=") {
			khPath = strings.TrimPrefix(f, "UserKnownHostsFile=")
		}
	}
	fi, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o, want 600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(keyPath); !strings.Contains(string(b), "OPENSSH PRIVATE KEY") {
		t.Fatal("key file content wrong")
	}
	if b, _ := os.ReadFile(khPath); string(b) != cred.KnownHosts {
		t.Fatalf("known_hosts content = %q", b)
	}

	cleanup()
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatal("cleanup did not remove the key file")
	}
}

func TestCredentialStringRedacts(t *testing.T) {
	c := deploy.Credential{Username: "git", Token: "ghp_secret", PrivateKey: "PRIVDATA", SSH: true}
	s := fmt.Sprintf("%v %+v %s", c, c, c)
	for _, secret := range []string{"ghp_secret", "PRIVDATA"} {
		if strings.Contains(s, secret) {
			t.Fatalf("Credential String leaked %q: %s", secret, s)
		}
	}
}
```

Add a tiny export seam at the bottom of an existing test-only export file or in `deployer_test.go` via a same-package helper. Because the test is in package `deploy_test`, add this to a new file `internal/deploy/export_test.go`:

```go
package deploy

// ExportGitCredEnv exposes gitCredEnv for tests in the deploy_test package.
func ExportGitCredEnv(d *Deployer, c Credential) ([]string, func(), error) { return d.gitCredEnv(c) }
```

(If `deployer_test.go` is already `package deploy` rather than `deploy_test`, call `d.gitCredEnv` directly and skip the export shim — check the existing file's package clause first.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/deploy/ -run 'TestGitCredEnvSSH|TestCredentialStringRedacts'`
Expected: FAIL (compile error — `SSH`/`PrivateKey` fields and redacting `String` undefined).

- [ ] **Step 3: Implement**

In `internal/deploy/deployer.go`, replace the `Credential` struct and add helpers:

```go
// Credential is a git credential pushed per-deploy (M22/M25). For HTTPS, Token
// is set and SSH is false. For SSH, SSH is true and PrivateKey/KnownHosts are
// set. An empty Token with SSH false means "no managed credential".
type Credential struct {
	Username   string
	Token      string // HTTPS personal-access token
	PrivateKey string // SSH OpenSSH-format private key
	KnownHosts string // SSH server-pinned host key line(s)
	SSH        bool   // true → SSH key auth
}

func (c Credential) httpsActive() bool { return !c.SSH && c.Token != "" }

// String redacts secrets so a stray %v/%+v cannot leak the token or key.
func (c Credential) String() string {
	kind := "https"
	if c.SSH {
		kind = "ssh"
	}
	return fmt.Sprintf("Credential{user:%q kind:%s}", c.Username, kind)
}
```

Replace `gitCredEnv` with a kind-aware version (keep the existing HTTPS body verbatim under the `else`):

```go
func (d *Deployer) gitCredEnv(cred Credential) (env []string, cleanup func(), err error) {
	if cred.SSH {
		tmp, err := os.MkdirTemp("", "marshal-ssh-")
		if err != nil {
			return nil, func() {}, err
		}
		fail := func(e error) ([]string, func(), error) { _ = os.RemoveAll(tmp); return nil, func() {}, e }
		keyPath := filepath.Join(tmp, "id")
		key := cred.PrivateKey
		if !strings.HasSuffix(key, "\n") {
			key += "\n" // OpenSSH refuses a key without a trailing newline
		}
		if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
			return fail(err)
		}
		khPath := filepath.Join(tmp, "known_hosts")
		if err := os.WriteFile(khPath, []byte(cred.KnownHosts), 0o600); err != nil {
			return fail(err)
		}
		sshCmd := fmt.Sprintf(
			"ssh -i %s -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s",
			keyPath, khPath)
		return []string{"GIT_SSH_COMMAND=" + sshCmd, "GIT_TERMINAL_PROMPT=0"}, func() { _ = os.RemoveAll(tmp) }, nil
	}
	if cred.Token == "" {
		return nil, func() {}, nil
	}
	// --- existing HTTPS GIT_ASKPASS body unchanged below ---
	tmp, err := os.MkdirTemp("", "marshal-askpass-")
	// ... (leave the rest of the current implementation as-is) ...
}
```

In `fetch` (deployer.go), change the credential-active discriminator so SSH does **not** rewrite the URL or disable helpers:

```go
credActive := cred.httpsActive()
```

(replacing `credActive := cred.Token != ""`). The `env` from `gitCredEnv` already carries the SSH command, so SSH clone/fetch route through it while `gitArgs(false, …)` keeps the URL verbatim.

In `internal/deploy/mutate.go`, change the push discriminator the same way:

```go
credActive := cred.httpsActive()
```

(replacing `credActive := cred.Token != ""` at ~line 190). For SSH this keeps `pushURL = "origin"` and `gitArgs(false, "push", …)`, with auth driven by `GIT_SSH_COMMAND`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/deploy/ -count=1`
Expected: PASS (new SSH tests + all existing M21–M24 deploy tests).

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/deployer.go internal/deploy/mutate.go internal/deploy/deployer_test.go internal/deploy/export_test.go
git commit -m "feat(m25): deploy SSH credential env (GIT_SSH_COMMAND) + redacting String

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: daemon — map SSH fields from proto into `deploy.Credential`

**Files:**
- Modify: `internal/daemon/command.go` (three `deploy.Credential{...}` constructions, ~lines 59, 70, 110)
- Test: `internal/daemon/command_test.go` (add a mapping test; if no such file, create it)

**Interfaces:**
- Consumes: `pb.GitCredential` accessors (Task 1), `deploy.Credential` fields (Task 3).
- Produces: a shared `credFromProto(*pb.GitCredential) deploy.Credential` helper used by all three control ops.

- [ ] **Step 1: Write the failing test**

Add to `internal/daemon/command_test.go` (create the file with `package daemon` if absent):

```go
func TestCredFromProtoSSH(t *testing.T) {
	got := credFromProto(&pb.GitCredential{
		Kind:       pb.CredentialKind_CRED_SSH,
		PrivateKey: "PRIV",
		KnownHosts: "h ssh-ed25519 AAAA",
		Username:   "git",
	})
	if !got.SSH || got.PrivateKey != "PRIV" || got.KnownHosts == "" {
		t.Fatalf("ssh mapping wrong: %+v", got)
	}
}

func TestCredFromProtoHTTPS(t *testing.T) {
	got := credFromProto(&pb.GitCredential{Username: "octocat", Token: "ghp_x"})
	if got.SSH || got.Username != "octocat" || got.Token != "ghp_x" {
		t.Fatalf("https mapping wrong: %+v", got)
	}
	if (credFromProto(nil)) != (deploy.Credential{}) {
		t.Fatal("nil credential must map to zero value")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run 'TestCredFromProto'`
Expected: FAIL (compile error — `credFromProto` undefined).

- [ ] **Step 3: Implement**

In `internal/daemon/command.go`, add the helper:

```go
// credFromProto maps a wire credential to the deployer's credential, branching
// on kind. A nil credential maps to the zero value (no managed credential).
func credFromProto(c *pb.GitCredential) deploy.Credential {
	if c == nil {
		return deploy.Credential{}
	}
	if c.GetKind() == pb.CredentialKind_CRED_SSH {
		return deploy.Credential{
			Username:   c.GetUsername(),
			PrivateKey: c.GetPrivateKey(),
			KnownHosts: c.GetKnownHosts(),
			SSH:        true,
		}
	}
	return deploy.Credential{Username: c.GetUsername(), Token: c.GetToken()}
}
```

Replace the three inline constructions:
- `cred := deploy.Credential{Username: c.GetUsername(), Token: c.GetToken()}` → `cred := credFromProto(c)`
- `cred := deploy.Credential{Username: rc.GetUsername(), Token: rc.GetToken()}` → `cred := credFromProto(rc)`
- `cred := deploy.Credential{Username: cc.GetUsername(), Token: cc.GetToken()}` → `cred := credFromProto(cc)`

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/command.go internal/daemon/command_test.go
git commit -m "feat(m25): map SSH credential fields proto->deploy in daemon

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: dashboard — type-aware credential create (generate SSH) + `Credentials` interface

**Files:**
- Modify: `internal/dashboard/credentials.go`
- Test: `internal/dashboard/credentials_test.go` (add cases; create if absent)

**Interfaces:**
- Consumes: `credstore.Store` now satisfies the wider interface (Tasks 2).
- Produces: the `Credentials` interface gains `Generate(name) (string, error)`, `GetKey(name) (string, string, bool, error)`, `SetKnownHosts(name, line string) error`. `POST /api/credentials` with `{"type":"ssh-key","name":...}` returns `{"public_key": "..."}`.

- [ ] **Step 1: Write the failing test**

Add to `internal/dashboard/credentials_test.go` a fake store implementing the new interface and a test (follow the existing M22 dashboard test pattern — reuse any existing `fakeCreds`/helper in that file; extend it with the new methods). Minimum new assertions:

```go
func TestCreateSSHCredentialReturnsPublicKey(t *testing.T) {
	h := newTestHandlerWithCreds(t, &fakeCreds{pub: "ssh-ed25519 AAAA marshal"}) // helper per existing tests
	body := `{"name":"deploykey","type":"ssh-key"}`
	rr := doSessionPost(t, h, "/api/credentials", body) // existing helper that attaches a session
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		PublicKey string `json:"public_key"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	if got.PublicKey != "ssh-ed25519 AAAA marshal" {
		t.Fatalf("public_key = %q", got.PublicKey)
	}
}

func TestCreateSSHCredentialRejectsEmptyName(t *testing.T) {
	h := newTestHandlerWithCreds(t, &fakeCreds{})
	rr := doSessionPost(t, h, "/api/credentials", `{"type":"ssh-key"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
```

Extend the file's `fakeCreds` with:

```go
func (f *fakeCreds) Generate(name string) (string, error) { f.genName = name; return f.pub, nil }
func (f *fakeCreds) GetKey(string) (string, string, bool, error) { return f.priv, f.kh, f.priv != "", nil }
func (f *fakeCreds) SetKnownHosts(name, line string) error { f.setKH = line; return nil }
```

(add `pub, priv, kh, genName, setKH string` fields to the struct).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run 'TestCreateSSHCredential'`
Expected: FAIL (compile error — interface lacks `Generate`; `type` field unhandled).

- [ ] **Step 3: Implement**

In `internal/dashboard/credentials.go`, widen the interface and add a `Type` to the request:

```go
type Credentials interface {
	List() []credstore.Meta
	Get(name string) (username, token string, ok bool, err error)
	Put(name, username, token string) error
	Generate(name string) (publicKey string, err error)
	GetKey(name string) (privateKey, knownHosts string, ok bool, err error)
	SetKnownHosts(name, line string) error
	Delete(name string) bool
}

type credentialReq struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "" or "https-token" → HTTPS; "ssh-key" → generate a key
	Username string `json:"username"`
	Token    string `json:"token"`
}
```

Branch in `createCredential` (replace the body after decode):

```go
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	user, _ := r.Context().Value(userKey).(string)

	if body.Type == "ssh-key" {
		pub, err := h.creds.Generate(body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("dashboard: credential.generate %s (ssh-key) by %s", body.Name, user) // never log the key
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "public_key": pub})
		return
	}

	if body.Token == "" {
		http.Error(w, "name and token required", http.StatusBadRequest)
		return
	}
	if err := h.creds.Put(body.Name, body.Username, body.Token); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("dashboard: credential.put %s (user=%s) by %s", body.Name, body.Username, user) // never log the token
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -run 'Credential' -count=1`
Expected: PASS (new SSH cases + existing M22 credential tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/credentials.go internal/dashboard/credentials_test.go
git commit -m "feat(m25): dashboard generate-ssh-key credential endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: dashboard — SSH resolution + server-side `ssh-keyscan` pin (the trust root)

**Files:**
- Modify: `internal/dashboard/apps.go` (`resolveCredential`, callers at ~lines 102, 127)
- Modify: `internal/dashboard/files.go` (3 `resolveCredential` callers at ~155, 194, 222)
- Modify: `internal/dashboard/handlers.go` (add the scanner seam to `handler` / `newHandler`)
- Test: `internal/dashboard/apps_test.go` (add resolution + scan-pin tests)

**Interfaces:**
- Consumes: `Credentials.List/GetKey/SetKnownHosts` (Task 5), `credstore.Meta.Type`.
- Produces:
  - `resolveCredential(name, repoURL string) (*pb.GitCredential, error)` — new second parameter (repo URL, used only to derive the scan host; pass `""` where unavailable).
  - `handler.scanHost func(hostport string) (string, error)` — injectable; default runs `ssh-keyscan`.
  - `sshHostPort(repo string) (host, port string)` helper.

- [ ] **Step 1: Write the failing test**

Add to `internal/dashboard/apps_test.go`:

```go
func TestSSHHostPort(t *testing.T) {
	cases := []struct{ repo, host, port string }{
		{"git@github.com:o/r.git", "github.com", ""},
		{"ssh://git@ssh.github.com:443/o/r.git", "ssh.github.com", "443"},
		{"ssh://git@example.com/o/r.git", "example.com", ""},
	}
	for _, c := range cases {
		h, p := dashboard.SSHHostPort(c.repo) // exported test shim
		if h != c.host || p != c.port {
			t.Fatalf("%s -> (%q,%q), want (%q,%q)", c.repo, h, p, c.host, c.port)
		}
	}
}

func TestResolveSSHScansAndPins(t *testing.T) {
	fc := &fakeCreds{
		metas: []credstore.Meta{{Name: "dk", Type: "ssh-key", PublicKey: "ssh-ed25519 AAAA"}},
		priv:  "PRIVKEY",
		kh:    "", // no pin yet → must scan
	}
	h := newTestHandlerWithCreds(t, fc)
	dashboard.SetScanHost(h, func(hp string) (string, error) {
		if hp != "github.com" {
			t.Fatalf("scanned %q", hp)
		}
		return "github.com ssh-ed25519 SCANNED", nil
	})

	cred, err := dashboard.ResolveCredential(h, "dk", "git@github.com:o/r.git")
	if err != nil {
		t.Fatal(err)
	}
	if cred.GetKind() != pb.CredentialKind_CRED_SSH || cred.GetPrivateKey() != "PRIVKEY" {
		t.Fatalf("cred = %+v", cred)
	}
	if cred.GetKnownHosts() != "github.com ssh-ed25519 SCANNED" {
		t.Fatalf("known_hosts = %q", cred.GetKnownHosts())
	}
	if fc.setKH != "github.com ssh-ed25519 SCANNED" {
		t.Fatal("pin was not persisted via SetKnownHosts")
	}
}

func TestResolveSSHAlreadyPinnedSkipsScan(t *testing.T) {
	fc := &fakeCreds{
		metas: []credstore.Meta{{Name: "dk", Type: "ssh-key"}},
		priv:  "PRIVKEY",
		kh:    "github.com ssh-ed25519 PINNED",
	}
	h := newTestHandlerWithCreds(t, fc)
	dashboard.SetScanHost(h, func(string) (string, error) { t.Fatal("must not scan when already pinned"); return "", nil })
	cred, err := dashboard.ResolveCredential(h, "dk", "git@github.com:o/r.git")
	if err != nil || cred.GetKnownHosts() != "github.com ssh-ed25519 PINNED" {
		t.Fatalf("cred=%+v err=%v", cred, err)
	}
}
```

Add to a `internal/dashboard/export_test.go` (create if absent, `package dashboard`):

```go
package dashboard

func SSHHostPort(repo string) (string, string) { return sshHostPort(repo) }
func SetScanHost(h *handler, f func(string) (string, error)) { h.scanHost = f }
func ResolveCredential(h *handler, name, repoURL string) (*pb.GitCredential, error) {
	return h.resolveCredential(name, repoURL)
}
```

Extend `fakeCreds` with a `metas []credstore.Meta` field and make its `List()` return it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run 'TestSSHHostPort|TestResolveSSH'`
Expected: FAIL (compile error — `sshHostPort`/`scanHost`/2-arg `resolveCredential` undefined).

- [ ] **Step 3: Implement**

In `internal/dashboard/handlers.go`, add a `scanHost func(string) (string, error)` field to `handler` and default it in `newHandler`:

```go
import "os/exec"

// in newHandler, after constructing h:
h.scanHost = func(hostport string) (string, error) {
	host, port := hostport, ""
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		host, port = hostport[:i], hostport[i+1:]
	}
	args := []string{}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, host)
	out, err := exec.Command("ssh-keyscan", args...).Output()
	if err != nil {
		return "", fmt.Errorf("ssh-keyscan %s: %w", host, err)
	}
	return strings.TrimSpace(string(out)), nil
}
```

(Adjust imports: `os/exec`, `strings`, `fmt` as needed. The `scanHost` receives the host with an optional `:port`; for `git@host` form there is no port so it gets just the host.)

In `internal/dashboard/apps.go`, replace `resolveCredential` and add `sshHostPort`:

```go
// resolveCredential turns a credential name into the secret to attach. Empty
// name → (nil, nil). repoURL supplies the host for the one-time SSH host-key
// scan; pass "" when the URL is not available (redeploy/commit — the pin is
// already set from the first deploy).
func (h *handler) resolveCredential(name, repoURL string) (*pb.GitCredential, error) {
	if name == "" {
		return nil, nil
	}
	if h.creds == nil {
		return nil, fmt.Errorf("credentials unavailable")
	}
	kind := ""
	for _, m := range h.creds.List() {
		if m.Name == name {
			kind = m.Type
			break
		}
	}
	if kind == "ssh-key" {
		priv, kh, ok, err := h.creds.GetKey(name)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %v", name, err)
		}
		if !ok {
			return nil, fmt.Errorf("unknown credential %q", name)
		}
		if kh == "" && repoURL != "" {
			host, port := sshHostPort(repoURL)
			if host != "" {
				hostport := host
				if port != "" {
					hostport = host + ":" + port
				}
				scanned, serr := h.scanHost(hostport)
				if serr != nil {
					return nil, fmt.Errorf("host-key scan failed: %v", serr)
				}
				if err := h.creds.SetKnownHosts(name, scanned); err != nil {
					return nil, err
				}
				kh = scanned
			}
		}
		return &pb.GitCredential{Username: "git", PrivateKey: priv, KnownHosts: kh, Kind: pb.CredentialKind_CRED_SSH}, nil
	}

	user, tok, ok, err := h.creds.Get(name)
	if err != nil {
		return nil, fmt.Errorf("credential %q: %v", name, err)
	}
	if !ok {
		return nil, fmt.Errorf("unknown credential %q", name)
	}
	return &pb.GitCredential{Username: user, Token: tok, Kind: pb.CredentialKind_CRED_HTTPS}, nil
}

// sshHostPort extracts host and optional port from an SSH git URL. Handles the
// scp-like form (git@host:path) and the ssh:// URL form. Returns ("","") if not
// recognizably SSH.
func sshHostPort(repo string) (host, port string) {
	if strings.HasPrefix(repo, "ssh://") {
		if u, err := url.Parse(repo); err == nil {
			return u.Hostname(), u.Port()
		}
		return "", ""
	}
	// scp-like: [user@]host:path  — host is between an optional "@" and the first ":"
	s := repo
	if at := strings.Index(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	if colon := strings.Index(s, ":"); colon >= 0 {
		return s[:colon], ""
	}
	return "", ""
}
```

Add the `net/url` import to apps.go if not present (it already imports it for `withUsername` usage elsewhere — verify).

Update the five callers to pass the repo URL where available, `""` otherwise:
- `apps.go:102` (deploy): `h.resolveCredential(g.Credential, g.Repo)` — use the git source's repo field (confirm the field name on `g`; it is the deploy request's repo URL).
- `apps.go:127` (redeploy): `h.resolveCredential(body.Credential, "")`.
- `files.go:155/194/222` (edit/delete/rename commit): `h.resolveCredential(body.Credential, "")`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/dashboard/ -count=1`
Expected: PASS (new SSH resolution/scan tests + all existing dashboard tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/apps.go internal/dashboard/files.go internal/dashboard/handlers.go internal/dashboard/apps_test.go internal/dashboard/export_test.go
git commit -m "feat(m25): server-side ssh-keyscan pin + SSH credential resolution

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: web — credential type toggle + show generated public key; rebuild embedded bundle

**Files:**
- Modify: `web/src/api.ts` (`CredentialMeta`, `createCredential`)
- Modify: `web/src/Credentials.tsx` (type toggle + public-key display)
- Rebuild: `internal/dashboard/dist` via `make ui`
- Test: manual (this repo has no JS test harness); type-check via the `make ui` build.

**Interfaces:**
- Consumes: `POST /api/credentials` `{name,type}` → `{public_key}` (Task 5); `GET /api/credentials` rows now carry `type` + `public_key` (Task 5/2).

- [ ] **Step 1: Extend the API client**

In `web/src/api.ts`, add `type` and `public_key` to the credential meta type and a generate call. Locate `CredentialMeta` (M22) and extend:

```ts
export interface CredentialMeta {
  name: string;
  type: string;          // "https-token" | "ssh-key"
  username: string;
  public_key?: string;   // ssh-key only
  created_at: number;
}

export async function createSSHCredential(name: string): Promise<{ public_key: string }> {
  const res = await fetch("/api/credentials", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, type: "ssh-key" }),
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}
```

(Keep the existing `createCredential` for HTTPS.)

- [ ] **Step 2: Add the type toggle + public-key display to `Credentials.tsx`**

In `web/src/Credentials.tsx`:
- Add a credential-type toggle to the add form: `https-token` (username + token, the existing fields) vs `ssh-key` (name only).
- On submit with `ssh-key`, call `createSSHCredential(name)` and show the returned `public_key` in a read-only `<textarea>` (or `<pre>`) with a "Copy" button and the helper text: *"Add this as a deploy key on your repo (e.g. GitHub → Settings → Deploy keys → Add deploy key)."*
- In the credential list, render the row `type`; for `ssh-key` rows show the stored `public_key` with a copy affordance (it is not secret).

Follow the existing component's styling/handlers — reuse its fetch-refresh and error-banner patterns. Keep the token field write-only (`type=password`) on the HTTPS path.

- [ ] **Step 3: Rebuild the embedded bundle**

Run: `make ui`
Expected: `web/` builds without type errors; `internal/dashboard/dist` updates.

- [ ] **Step 4: Verify the build embeds**

Run: `go build -o marshal ./cmd/marshal`
Expected: clean build (the embedded `dist` compiles in).

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/Credentials.tsx internal/dashboard/dist
git commit -m "feat(m25): dashboard SSH key credential UI (generate + show public key)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Whole-branch verification + handoff

**Files:**
- Create: `docs/handoffs/2026-06-20-m25-ssh-deploy-keys.md`

- [ ] **Step 1: Full test + lint sweep**

Run: `go test ./... -race -count=1 && gofmt -l . && go vet ./...`
Expected: all packages PASS; `gofmt` lists nothing; `go vet` clean.

- [ ] **Step 2: Live demo (per CLAUDE.md live-demo convention)**

Scratch dir `XDG_DATA_HOME=/tmp/marshal-m25-demo/...`, server on `:9000`/`:9001` (per memory). Against an **SSH git remote** — either a local bare repo over `ssh://localhost` with a throwaway `sshd`/`authorized_keys`, or a throwaway private GitHub repo with the generated public key registered as a deploy key:
- Generate an `ssh-key` credential via the dashboard; capture the returned public key; register it on the remote.
- Deploy the repo over SSH → `cloning → online`; confirm `credentials.json` holds the **sealed** private key (no plaintext) and a `known_hosts` pin appeared after first deploy.
- Confirm the private key is **absent** from the agent data dir and the per-app log.
- Redeploy (fetch) and perform an M24 edit→push, both over SSH; confirm the push lands on origin.
- **Negative:** tamper the pinned `known_hosts` (or point at a host whose key differs) → the agent op fails strict host-key verification with an honest error, working tree rolled back.
- Tear down by data dir only (preserve the user's standing launchd daemon); confirm `pgrep -fl marshal` shows no demo orphans.

- [ ] **Step 3: Write the handoff** documenting state, what changed, build/run/test, deferred items (paste-key, passphrase, rotation UI, RSA/ECDSA), and the concrete next step (merge `--no-ff` to `main`).

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-20-m25-ssh-deploy-keys.md
git commit -m "docs: M25 SSH deploy keys handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review notes

- **Spec coverage:** §3 data model → Task 2; §4 proto → Task 1; §5 server scan/pin → Task 6; §6 agent SSH env → Task 3 (+ Task 4 wiring); §7 dashboard UX → Tasks 5 & 7; §8 security (redacting `String`, sealed-at-rest, strict verify, nil build env) → Tasks 2/3/6; §9 testing → each task's tests + Task 8 live demo. All spec sections map to a task.
- **No new Go dep:** generation/scan shell out (`ssh-keygen`/`ssh-keyscan`), per Global Constraints.
- **Additive proto:** `kind` defaults to `CRED_HTTPS`, so M22/M24 agents keep working (Task 1 asserts the zero-value default).
- **Type consistency:** `deploy.Credential{Username,Token,PrivateKey,KnownHosts,SSH}`, `httpsActive()`, `pb.CredentialKind_CRED_SSH`, `Generate/GetKey/SetKnownHosts`, `resolveCredential(name, repoURL)`, `sshHostPort`, `scanHost`, `credFromProto` are used identically across Tasks 1–7.
- **Note for the implementer:** before Tasks 3/5/6, check whether `*_test.go` in that package uses `package deploy`/`dashboard` (internal) or `_test` (external) — it determines whether the `export_test.go` shims are needed or you can call the unexported symbols directly. Verify the deploy request's repo-URL field name on `g` in `apps.go` (Task 6 caller) and `body.Credential` shapes in `files.go` before wiring.
