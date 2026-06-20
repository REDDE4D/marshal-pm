# M24 Commit & Push Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a dashboard user edit/create/delete/rename a deployed app's files and have each change committed and pushed to origin with the app's M22 credential.

**Architecture:** Reuse the M23 read seam end to end. One additive proto op (`ControlOp_Commit`) carries a CRUD mutation dashboard → server → agent. The agent performs the git work in the app's clone root with a security-critical core (`internal/deploy/mutate.go`): path confinement, targeted staging, inline-identity commit, push to a `withUsername` URL via the M22 `GIT_ASKPASS` helper, and transactional rollback on failure. Per-operation atomic commit+push; redeploy stays a separate action.

**Tech Stack:** Go 1.26 (stdlib + protobuf/gRPC), React + TypeScript + CodeMirror 6 (`web/`), `protoc` for codegen.

## Global Constraints

- Module path `marshal`; imports `marshal/internal/...`.
- TDD: failing test first, then implementation. `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (silent) before finishing.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Per-operation atomic commit+push (no batching). Commit+push only — never auto-redeploy.
- **Never force-push.** Never `git add -A`/`add .` — stage only the affected path(s).
- Token never in argv/URL/log/`dump.json`/audit — only via `GIT_ASKPASS` env (reuse M22 `gitCredEnv`/`withUsername`/`gitArgs`).
- Author identity inline (`-c user.name= -c user.email=`); never mutate the clone's git config.
- Generic, non-path-leaking errors (M23 `confine` discipline).
- All proto changes additive (an older agent must still work).
- Spec: `docs/superpowers/specs/2026-06-20-commit-push-design.md`.

---

### Task 1: Proto — Commit op + result, regenerated

**Files:**
- Modify: `proto/marshal/v1/fleet.proto` (after `RedeployRequest`, line ~134; `ControlOp` oneof line ~148; `ControlResult` line ~156)
- Test: `internal/pb/gitsource_test.go` (append a test)
- Regenerated (do not hand-edit): `internal/pb/fleet.pb.go`

**Interfaces:**
- Produces: `pb.CommitKind` (`COMMIT_EDIT=0`, `COMMIT_CREATE=1`, `COMMIT_DELETE=2`, `COMMIT_RENAME=3`); `pb.CommitRequest{App, Kind, Path, NewPath, Content, Message, Credential}`; `pb.CommitResult{Sha, Branch}`; oneof wrapper `pb.ControlOp_Commit{Commit *CommitRequest}`; `pb.ControlResult.Commit *CommitResult`. Getters `GetApp/GetKind/GetPath/GetNewPath/GetContent/GetMessage/GetCredential` and `GetCommit/GetSha/GetBranch`.

- [ ] **Step 1: Write the failing test** — append to `internal/pb/gitsource_test.go`:

```go
func TestCommitOpWire(t *testing.T) {
	op := &ControlOp{Op: &ControlOp_Commit{Commit: &CommitRequest{
		App:        "app1",
		Kind:       CommitKind_COMMIT_RENAME,
		Path:       "a.txt",
		NewPath:    "b.txt",
		Content:    []byte("hi"),
		Message:    "Rename a.txt → b.txt",
		Credential: &GitCredential{Username: "octocat", Token: "ghp_x"},
	}}}
	c := op.GetCommit()
	if c.GetApp() != "app1" || c.GetKind() != CommitKind_COMMIT_RENAME ||
		c.GetPath() != "a.txt" || c.GetNewPath() != "b.txt" ||
		string(c.GetContent()) != "hi" || c.GetCredential().GetToken() != "ghp_x" {
		t.Fatalf("CommitRequest not wired: %+v", c)
	}
	res := &ControlResult{Ok: true, Commit: &CommitResult{Sha: "abc123", Branch: "main"}}
	if res.GetCommit().GetSha() != "abc123" || res.GetCommit().GetBranch() != "main" {
		t.Fatalf("CommitResult not wired: %+v", res.GetCommit())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go build ./internal/pb/...`
Expected: FAIL — `CommitRequest`, `CommitKind_COMMIT_RENAME`, etc. undefined.

- [ ] **Step 3: Edit the proto.** In `proto/marshal/v1/fleet.proto`, after the `RedeployRequest` message (~line 134) add:

```proto
enum CommitKind {
  COMMIT_EDIT   = 0;
  COMMIT_CREATE = 1;
  COMMIT_DELETE = 2;
  COMMIT_RENAME = 3;
}

// M24 — a single file mutation (edit/create/delete/rename) committed and pushed.
message CommitRequest {
  string        app        = 1;
  CommitKind    kind       = 2;
  string        path       = 3; // target (edit/create/delete) or rename source
  string        new_path   = 4; // rename destination only
  bytes         content    = 5; // edit/create only
  string        message    = 6; // commit message (server fills a default)
  GitCredential credential = 7; // M22 secret, attached per-op, never persisted
}
message CommitResult { string sha = 1; string branch = 2; }
```

In the `ControlOp` oneof, after `read_file = 8;` add:

```proto
    CommitRequest   commit    = 9; // M24
```

In `ControlResult`, after `FileContent file = 5;` add:

```proto
  CommitResult commit = 6; // M24, set on commit success
```

- [ ] **Step 4: Regenerate**

Run: `go generate ./internal/pb && go build ./internal/pb/...`
Expected: clean build (protoc + plugins on PATH; if missing, install per repo env notes).

- [ ] **Step 5: Run the test**

Run: `go test ./internal/pb/ -run TestCommitOpWire -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/fleet.pb.go internal/pb/gitsource_test.go
git commit -m "feat(m24): add Commit op/result to fleet proto

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Write-path confinement — `confineNew` + `.git` guard

**Files:**
- Create: `internal/deploy/mutate.go`
- Test: `internal/deploy/mutate_test.go`

**Interfaces:**
- Consumes: `confine` (existing, `browse.go`).
- Produces: `confineNew(root, rel string) (string, error)` — confines a path whose leaf (and intermediate dirs) may not yet exist, defeating symlinked-parent escape; `isGitInternal(rel string) bool` — true when the cleaned relative path's first component is `.git`.

- [ ] **Step 1: Write the failing test** — create `internal/deploy/mutate_test.go`:

```go
package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfineNew(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlinked parent escaping the root.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}

	ok := []string{"new.txt", "sub/new.txt", "deep/dir/new.txt"} // leaf/dirs may not exist
	for _, rel := range ok {
		got, err := confineNew(root, rel)
		if err != nil {
			t.Errorf("confineNew(%q) unexpected error: %v", rel, err)
			continue
		}
		if !strings.HasSuffix(got, filepath.FromSlash(rel)) {
			t.Errorf("confineNew(%q) = %q, want suffix %q", rel, got, rel)
		}
	}

	bad := []string{"..", "../escape.txt", "sub/../../escape.txt", "/etc/x", "out/escape.txt"}
	for _, rel := range bad {
		if got, err := confineNew(root, rel); err == nil {
			t.Errorf("confineNew(%q) = %q, want error", rel, got)
		}
	}
}

func TestIsGitInternal(t *testing.T) {
	for _, rel := range []string{".git", ".git/config", ".git/refs/heads/main"} {
		if !isGitInternal(rel) {
			t.Errorf("isGitInternal(%q) = false, want true", rel)
		}
	}
	for _, rel := range []string{"src/.git.go", "agitator", "sub/file.txt"} {
		if isGitInternal(rel) {
			t.Errorf("isGitInternal(%q) = true, want false", rel)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/deploy/ -run 'TestConfineNew|TestIsGitInternal' -v`
Expected: FAIL — `confineNew`/`isGitInternal` undefined.

- [ ] **Step 3: Implement** — create `internal/deploy/mutate.go`:

```go
package deploy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/config"
	"marshal/internal/pb"
)

// confineNew is confine's sibling for paths that may not exist yet (create /
// rename destination). It does the same lexical containment check, then resolves
// the deepest *existing* ancestor via EvalSymlinks and verifies that stays inside
// the real root, defeating a symlinked parent directory. Returns generic errors.
func confineNew(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	full := filepath.Clean(filepath.Join(root, rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("deploy root unavailable")
	}
	// Walk up to the deepest existing ancestor and resolve it.
	anc := full
	var tail []string
	for {
		if anc == root {
			break
		}
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		tail = append([]string{filepath.Base(anc)}, tail...)
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	realAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", fmt.Errorf("path escapes deploy root")
	}
	if realAnc != realRoot && !strings.HasPrefix(realAnc, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root via symlink")
	}
	return filepath.Join(append([]string{realAnc}, tail...)...), nil
}

// isGitInternal reports whether rel addresses the clone's own .git directory.
func isGitInternal(rel string) bool {
	clean := filepath.Clean(filepath.FromSlash(rel))
	parts := strings.Split(clean, string(filepath.Separator))
	return len(parts) > 0 && parts[0] == ".git"
}
```

(The unused imports `bytes`, `context`, `config`, `pb` are consumed in Task 3 — add them now so the file compiles; if your linter rejects unused imports between tasks, add them in Task 3 instead. To be safe, import only what this step uses: drop `bytes`, `context`, `config`, `pb` for now and re-add in Task 3.)

Use this minimal import block for Task 2:

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/deploy/ -run 'TestConfineNew|TestIsGitInternal' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/mutate.go internal/deploy/mutate_test.go
git commit -m "feat(m24): write-path confinement (confineNew + .git guard)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Mutation core — commit+push for all four kinds (happy paths)

**Files:**
- Modify: `internal/deploy/mutate.go`
- Test: `internal/deploy/mutate_test.go`

**Interfaces:**
- Consumes: `confine`, `confineNew`, `isGitInternal`; `*Deployer.runner`, `*Deployer.gitCredEnv`, `withUsername`, `gitArgs` (existing, `deployer.go`); `config.GitSource`; `pb.CommitKind`, `pb.CommitResult`.
- Produces: `(*Deployer).mutateAndPush(dir string, src config.GitSource, cred Credential, kind pb.CommitKind, rel, newRel string, content []byte, message string) (*pb.CommitResult, error)`. Helper `gitOut(r Runner, dir string, env []string, args ...string) (string, error)`.

- [ ] **Step 1: Write the failing test** — append to `internal/deploy/mutate_test.go`. This sets up a bare remote + clone and exercises each kind against real git:

```go
import (
	"context"
	"os/exec"
)

// gitT runs a git command in dir for test setup, failing the test on error.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{
		"-c", "user.email=t@e", "-c", "user.name=t",
		"-c", "init.defaultBranch=main",
	}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// newRepoWithRemote builds a bare remote with one commit, clones it into a work
// dir on branch main, and returns (workDir, remoteDir).
func newRepoWithRemote(t *testing.T) (string, string) {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	gitT(t, base, "init", "--bare", "--initial-branch=main", remote)

	seed := filepath.Join(base, "seed")
	gitT(t, base, "clone", remote, seed)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, seed, "add", "README.md")
	gitT(t, seed, "commit", "-m", "seed")
	gitT(t, seed, "push", "origin", "main")

	work := filepath.Join(base, "work")
	gitT(t, base, "clone", remote, work)
	return work, remote
}

func remoteHead(t *testing.T, remote, path string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "show", "main:"+path)
	cmd.Dir = remote
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func TestMutateAndPush_AllKinds(t *testing.T) {
	d := New(nil, ExecRunner{}, t.TempDir())
	src := config.GitSource{Repo: "ignored-no-cred"} // no credential: pushes to origin URL

	// edit
	work, remote := newRepoWithRemote(t)
	if _, err := d.mutateAndPush(work, src, Credential{}, pb.CommitKind_COMMIT_EDIT, "README.md", "", []byte("edited\n"), "Update README.md"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got, _ := remoteHead(t, remote, "README.md"); got != "edited\n" {
		t.Fatalf("edit not on origin: %q", got)
	}

	// create (with intermediate dir)
	work, remote = newRepoWithRemote(t)
	if _, err := d.mutateAndPush(work, src, Credential{}, pb.CommitKind_COMMIT_CREATE, "cfg/app.yaml", "", []byte("k: v\n"), "Create cfg/app.yaml"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got, ok := remoteHead(t, remote, "cfg/app.yaml"); !ok || got != "k: v\n" {
		t.Fatalf("create not on origin: %q ok=%v", got, ok)
	}

	// delete
	work, remote = newRepoWithRemote(t)
	if _, err := d.mutateAndPush(work, src, Credential{}, pb.CommitKind_COMMIT_DELETE, "README.md", "", nil, "Delete README.md"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := remoteHead(t, remote, "README.md"); ok {
		t.Fatalf("delete: README.md still on origin")
	}

	// rename
	work, remote = newRepoWithRemote(t)
	res, err := d.mutateAndPush(work, src, Credential{}, pb.CommitKind_COMMIT_RENAME, "README.md", "DOCS.md", nil, "Rename README.md → DOCS.md")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, ok := remoteHead(t, remote, "README.md"); ok {
		t.Fatalf("rename: old path still on origin")
	}
	if got, ok := remoteHead(t, remote, "DOCS.md"); !ok || got != "seed\n" {
		t.Fatalf("rename: new path = %q ok=%v", got, ok)
	}
	if res.GetBranch() != "main" || res.GetSha() == "" {
		t.Fatalf("rename result: %+v", res)
	}
}

func TestMutateAndPush_TargetedStaging(t *testing.T) {
	// An untracked build artifact must NOT be swept into the commit.
	d := New(nil, ExecRunner{}, t.TempDir())
	work, _ := newRepoWithRemote(t)
	if err := os.WriteFile(filepath.Join(work, "app.bin"), []byte("ARTIFACT"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := d.mutateAndPush(work, config.GitSource{}, Credential{}, pb.CommitKind_COMMIT_EDIT, "README.md", "", []byte("x\n"), "Update README.md"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	files := gitT(t, work, "show", "--name-only", "--pretty=format:", "HEAD")
	if strings.Contains(files, "app.bin") {
		t.Fatalf("build artifact leaked into commit: %q", files)
	}
	st := gitT(t, work, "status", "--porcelain")
	if !strings.Contains(st, "app.bin") {
		t.Fatalf("artifact should still be untracked: %q", st)
	}
}

func TestMutateAndPush_GitGuard(t *testing.T) {
	d := New(nil, ExecRunner{}, t.TempDir())
	work, _ := newRepoWithRemote(t)
	if _, err := d.mutateAndPush(work, config.GitSource{}, Credential{}, pb.CommitKind_COMMIT_EDIT, ".git/config", "", []byte("x"), "m"); err == nil {
		t.Fatalf(".git write must be rejected")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/deploy/ -run TestMutateAndPush -v`
Expected: FAIL — `mutateAndPush` undefined.

- [ ] **Step 3: Implement** — in `internal/deploy/mutate.go`, set the import block to:

```go
import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"marshal/internal/config"
	"marshal/internal/pb"
)
```

and add:

```go
// gitOut runs a git command capturing stdout; on failure it returns a generic,
// non-path-leaking error keyed on the subcommand.
func gitOut(r Runner, dir string, env []string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	if err := r.Run(context.Background(), dir, env, &out, &errb, "git", args...); err != nil {
		verb := "git"
		if len(args) > 0 {
			verb = args[len(args)-1] // last non-flag is unreliable; use first real verb below
		}
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				verb = a
				break
			}
		}
		return "", fmt.Errorf("git %s failed", verb)
	}
	return strings.TrimSpace(out.String()), nil
}

// gitIdentity derives the commit author from the credential, with a fixed
// fallback when the app has no managed credential.
func gitIdentity(cred Credential) (name, email string) {
	if cred.Username != "" {
		return cred.Username, cred.Username + "@marshal.local"
	}
	return "Marshal", "marshal@localhost"
}

// mutateAndPush applies one file mutation in dir's clone, commits it with an
// inline identity, and pushes to origin. On any failure after HEAD is captured
// it rolls the working tree back to the pre-op commit. Never force-pushes,
// never stages anything but the affected path(s).
func (d *Deployer) mutateAndPush(dir string, src config.GitSource, cred Credential, kind pb.CommitKind, rel, newRel string, content []byte, message string) (*pb.CommitResult, error) {
	// Branch gate: commit+push only makes sense onto a branch.
	branch, err := gitOut(d.runner, dir, nil, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil || branch == "" {
		return nil, fmt.Errorf("deployment is not on a branch (read-only)")
	}
	if message == "" {
		message = "Update via Marshal"
	}

	// Resolve + guard target path(s).
	if isGitInternal(rel) || (kind == pb.CommitKind_COMMIT_RENAME && isGitInternal(newRel)) {
		return nil, fmt.Errorf("cannot modify .git")
	}
	var leftover string // untracked path to remove on rollback (create/rename dest)

	preSHA, err := gitOut(d.runner, dir, nil, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse failed")
	}

	apply := func() error {
		switch kind {
		case pb.CommitKind_COMMIT_EDIT:
			full, err := confine(dir, rel)
			if err != nil {
				return err
			}
			if err := os.WriteFile(full, content, 0o644); err != nil {
				return fmt.Errorf("write failed")
			}
			_, err = gitOut(d.runner, dir, nil, "add", "--", rel)
			return err
		case pb.CommitKind_COMMIT_CREATE:
			full, err := confineNew(dir, rel)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return fmt.Errorf("mkdir failed")
			}
			if err := os.WriteFile(full, content, 0o644); err != nil {
				return fmt.Errorf("write failed")
			}
			leftover = full
			_, err = gitOut(d.runner, dir, nil, "add", "--", rel)
			return err
		case pb.CommitKind_COMMIT_DELETE:
			if _, err := confine(dir, rel); err != nil {
				return err
			}
			_, err := gitOut(d.runner, dir, nil, "rm", "--", rel)
			return err
		case pb.CommitKind_COMMIT_RENAME:
			if _, err := confine(dir, rel); err != nil {
				return err
			}
			full, err := confineNew(dir, newRel)
			if err != nil {
				return err
			}
			leftover = full
			_, err = gitOut(d.runner, dir, nil, "mv", "--", rel, newRel)
			return err
		default:
			return fmt.Errorf("unknown commit kind")
		}
	}

	rollback := func() {
		_, _ = gitOut(d.runner, dir, nil, "reset", "--hard", preSHA)
		if leftover != "" {
			_ = os.Remove(leftover)
		}
	}

	if err := apply(); err != nil {
		rollback()
		return nil, err
	}

	name, email := gitIdentity(cred)
	if _, err := gitOut(d.runner, dir, nil,
		"-c", "credential.helper=", "-c", "user.name="+name, "-c", "user.email="+email,
		"commit", "-m", message); err != nil {
		rollback()
		return nil, fmt.Errorf("git commit failed")
	}

	env, cleanup, err := d.gitCredEnv(cred)
	if err != nil {
		rollback()
		return nil, fmt.Errorf("credential setup failed")
	}
	defer cleanup()
	credActive := cred.Token != ""
	pushURL := withUsername(src.Repo, cred.Username)
	if pushURL == "" {
		pushURL = "origin"
	}
	if _, err := gitOut(d.runner, dir, env,
		gitArgs(credActive, "push", pushURL, "HEAD:refs/heads/"+branch)...); err != nil {
		rollback()
		return nil, fmt.Errorf("push rejected (origin moved or credential lacks write access)")
	}

	sha, _ := gitOut(d.runner, dir, nil, "rev-parse", "HEAD")
	return &pb.CommitResult{Sha: sha, Branch: branch}, nil
}
```

Note: `gitArgs` takes `(credActive bool, args ...string)`. The `gitOut(..., gitArgs(...)...)` call expands the slice; that is valid Go (`gitOut` is variadic on the trailing `args`).

In the no-credential test cases `src.Repo` is non-empty but not a real URL — `withUsername` returns it unchanged, and the clone's `origin` already points at the bare remote, so `git push <repo> HEAD:...` would fail. **Fix:** when there is no credential, push to `origin` (the clone's configured remote). Adjust the `pushURL` logic:

```go
	pushURL := "origin"
	if credActive {
		pushURL = withUsername(src.Repo, cred.Username)
	}
```

Use this `credActive`/`pushURL` form (it makes the no-cred tests push to the real `origin`, and the credentialed live path push to the username-embedded URL).

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/deploy/ -run TestMutateAndPush -v`
Expected: PASS (requires `git` on PATH).

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/mutate.go internal/deploy/mutate_test.go
git commit -m "feat(m24): mutation core (edit/create/delete/rename commit+push)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Rollback on push failure + detached-HEAD gate

**Files:**
- Test: `internal/deploy/mutate_test.go` (append)
- (Implementation already in Task 3 — these tests lock the behavior in.)

**Interfaces:**
- Consumes: `mutateAndPush`, the `newRepoWithRemote`/`gitT` helpers from Task 3.

- [ ] **Step 1: Write the failing test** — append to `internal/deploy/mutate_test.go`:

```go
func TestMutateAndPush_RollbackOnPushReject(t *testing.T) {
	d := New(nil, ExecRunner{}, t.TempDir())
	work, remote := newRepoWithRemote(t)
	preSHA := strings.TrimSpace(gitT(t, work, "rev-parse", "HEAD"))

	// Advance origin from a second clone so our push is a non-fast-forward.
	other := filepath.Join(t.TempDir(), "other")
	gitT(t, filepath.Dir(other), "clone", remote, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, other, "add", "README.md")
	gitT(t, other, "commit", "-m", "theirs")
	gitT(t, other, "push", "origin", "main")

	if _, err := d.mutateAndPush(work, config.GitSource{}, Credential{}, pb.CommitKind_COMMIT_EDIT, "README.md", "", []byte("mine\n"), "Update README.md"); err == nil {
		t.Fatalf("expected push rejection")
	}
	// Local tree rolled back to preSHA, working file unchanged, no dangling commit.
	if got := strings.TrimSpace(gitT(t, work, "rev-parse", "HEAD")); got != preSHA {
		t.Fatalf("HEAD = %q, want rolled back to %q", got, preSHA)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "README.md")); string(b) != "seed\n" {
		t.Fatalf("working file not rolled back: %q", b)
	}
}

func TestMutateAndPush_RollbackRemovesCreatedLeftover(t *testing.T) {
	d := New(nil, ExecRunner{}, t.TempDir())
	work, remote := newRepoWithRemote(t)
	// Force a push reject as above.
	other := filepath.Join(t.TempDir(), "other2")
	gitT(t, filepath.Dir(other), "clone", remote, other)
	if err := os.WriteFile(filepath.Join(other, "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, other, "add", "x")
	gitT(t, other, "commit", "-m", "x")
	gitT(t, other, "push", "origin", "main")

	if _, err := d.mutateAndPush(work, config.GitSource{}, Credential{}, pb.CommitKind_COMMIT_CREATE, "newfile.txt", "", []byte("n"), "Create newfile.txt"); err == nil {
		t.Fatalf("expected push rejection")
	}
	if _, err := os.Stat(filepath.Join(work, "newfile.txt")); !os.IsNotExist(err) {
		t.Fatalf("created leftover not removed on rollback")
	}
}

func TestMutateAndPush_DetachedHeadRejected(t *testing.T) {
	d := New(nil, ExecRunner{}, t.TempDir())
	work, _ := newRepoWithRemote(t)
	gitT(t, work, "checkout", "--detach", "HEAD")
	if _, err := d.mutateAndPush(work, config.GitSource{}, Credential{}, pb.CommitKind_COMMIT_EDIT, "README.md", "", []byte("x\n"), "m"); err == nil {
		t.Fatalf("detached HEAD must be rejected")
	}
}
```

- [ ] **Step 2: Run to verify it passes** (Task 3's implementation already satisfies these)

Run: `go test ./internal/deploy/ -run TestMutateAndPush -v`
Expected: PASS for all `TestMutateAndPush*`. If `RollbackOnPushReject` fails, verify the `rollback()` runs `reset --hard preSHA` before returning the push error.

- [ ] **Step 3: Commit**

```bash
git add internal/deploy/mutate_test.go
git commit -m "test(m24): rollback-on-push-failure and detached-HEAD gate

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Deployer.Commit (resolve + concurrency guard) and Snapshots filter

**Files:**
- Modify: `internal/deploy/deployer.go` (add `Commit`; filter `committing` from `Snapshots`)
- Test: `internal/deploy/deployer_test.go` (append)

**Interfaces:**
- Consumes: `mutateAndPush`; `d.host.Source`; `d.Root`; `d.dir`; `d.mu`/`d.states`/`clearState`; `newFakeHost`/`fakeHost` (test harness, `deployer_test.go`).
- Produces: `(*Deployer).Commit(name string, kind pb.CommitKind, rel, newRel string, content []byte, message string, cred Credential) (*pb.CommitResult, error)`. Phase constant `phaseCommitting = "committing"`, excluded from `Snapshots`.

- [ ] **Step 1: Write the failing test** — append to `internal/deploy/deployer_test.go`:

```go
func TestDeployerCommit(t *testing.T) {
	work, remote := newRepoWithRemote(t)
	// deployRoot/app1 IS the work clone.
	deployRoot := filepath.Dir(work)
	app1 := filepath.Base(work)

	h := newFakeHost()
	h.sources[app1] = config.GitSource{Repo: "origin-unused"}
	d := New(h, ExecRunner{}, deployRoot)

	res, err := d.Commit(app1, pb.CommitKind_COMMIT_EDIT, "README.md", "", []byte("via deployer\n"), "Update README.md", Credential{})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.GetBranch() != "main" {
		t.Fatalf("branch = %q", res.GetBranch())
	}
	if got, _ := remoteHead(t, remote, "README.md"); got != "via deployer\n" {
		t.Fatalf("not pushed: %q", got)
	}

	// Unknown app rejected.
	if _, err := d.Commit("ghost", pb.CommitKind_COMMIT_EDIT, "x", "", []byte("y"), "m", Credential{}); err == nil {
		t.Fatalf("unknown app must error")
	}
}

func TestDeployerCommit_RejectsWhileDeploying(t *testing.T) {
	work, _ := newRepoWithRemote(t)
	deployRoot := filepath.Dir(work)
	app1 := filepath.Base(work)
	h := newFakeHost()
	h.sources[app1] = config.GitSource{Repo: "r"}
	d := New(h, ExecRunner{}, deployRoot)

	d.mu.Lock()
	d.states[app1] = state{phase: phaseBuilding}
	d.mu.Unlock()

	if _, err := d.Commit(app1, pb.CommitKind_COMMIT_EDIT, "README.md", "", []byte("x"), "m", Credential{}); err == nil {
		t.Fatalf("Commit during deploy must be rejected")
	}
}
```

(`newRepoWithRemote`, `remoteHead`, `gitT` live in `mutate_test.go`, same package — reuse directly.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/deploy/ -run TestDeployerCommit -v`
Expected: FAIL — `Commit` undefined, `phaseCommitting` undefined.

- [ ] **Step 3: Implement.** In `internal/deploy/deployer.go` add the phase constant alongside the others:

```go
const (
	phaseCloning    = "cloning"
	phaseBuilding   = "building"
	phaseFailed     = "failed"
	phaseCommitting = "committing"
)
```

In `Snapshots`, skip the transient committing state (so a brief write never shows a phantom proc):

```go
	for name, st := range d.states {
		if st.phase == phaseCommitting {
			continue
		}
		out = append(out, pb.ProcInfo{
			Name:   name,
			State:  st.phase,
			Source: "git",
			Detail: st.detail,
		})
	}
```

Add the public method (resolve src + dir, concurrency guard, delegate):

```go
// Commit applies one file mutation in the app's clone and pushes it to origin.
// It refuses to run while a deploy/redeploy is in flight (and marks a transient
// committing state so a concurrent deploy is refused too).
func (d *Deployer) Commit(name string, kind pb.CommitKind, rel, newRel string, content []byte, message string, cred Credential) (*pb.CommitResult, error) {
	src, ok := d.host.Source(name)
	if !ok || src.Repo == "" {
		return nil, fmt.Errorf("app %q is not git-sourced", name)
	}
	dir, ok := d.Root(name)
	if !ok {
		return nil, fmt.Errorf("not a git deployment")
	}
	d.mu.Lock()
	if _, busy := d.states[name]; busy {
		d.mu.Unlock()
		return nil, fmt.Errorf("app %q is deploying", name)
	}
	d.states[name] = state{phase: phaseCommitting}
	d.mu.Unlock()
	defer d.clearState(name)

	return d.mutateAndPush(dir, src, cred, kind, rel, newRel, content, message)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/deploy/ -run 'TestDeployerCommit|TestSnapshots' -v && go test ./internal/deploy/ -count=1`
Expected: PASS (all deploy tests).

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/deployer.go internal/deploy/deployer_test.go
git commit -m "feat(m24): Deployer.Commit with deploy/commit mutual exclusion

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Daemon command case

**Files:**
- Modify: `internal/daemon/command.go` (add `case *pb.ControlOp_Commit:` before `default:`)
- Test: `internal/daemon/command_test.go` (append)

**Interfaces:**
- Consumes: `pb.ControlOp_Commit`, `pb.CommitRequest`, `deploy.Credential`, `s.deployer.Commit`.
- Produces: a `ControlResult{Ok, Commit}` / `{Ok:false, Error}` response for the commit op.

- [ ] **Step 1: Write the failing test** — append to `internal/daemon/command_test.go`:

```go
func TestHandleFleetCommand_Commit(t *testing.T) {
	// Reuse the deploy package's real-git test repo by shelling out here.
	deployRoot := t.TempDir()
	app := "app1"
	work := filepath.Join(deployRoot, app)

	run := func(dir string, args ...string) {
		c := exec.Command("git", append([]string{"-c", "user.email=t@e", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(deployRoot, "remote.git")
	run(deployRoot, "init", "--bare", "--initial-branch=main", remote)
	run(deployRoot, "clone", remote, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", "README.md")
	run(work, "commit", "-m", "seed")
	run(work, "push", "origin", "main")

	h := &fakeDeployHost{sources: map[string]config.GitSource{app: {Repo: "r"}}}
	s := &Server{mgr: manager.New(context.Background()), deployer: deploy.New(h, deploy.ExecRunner{}, deployRoot)}
	defer s.mgr.StopAll()

	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_EDIT, Path: "README.md",
		Content: []byte("edited\n"), Message: "Update README.md",
	}}}
	res := s.handleFleetCommand(&pb.Command{Op: op})
	if !res.GetOk() || res.GetCommit().GetBranch() != "main" {
		t.Fatalf("commit: ok=%v branch=%q err=%q", res.GetOk(), res.GetCommit().GetBranch(), res.GetError())
	}

	// nil deployer → not supported
	s2 := &Server{mgr: manager.New(context.Background())}
	if res := s2.handleFleetCommand(&pb.Command{Op: op}); res.GetOk() {
		t.Fatalf("nil deployer commit must fail")
	}
}
```

Check whether `command_test.go` already defines a host type usable here (e.g. `deployHost`). If a suitable fake with a settable `sources` map exists, use it instead of `fakeDeployHost` and delete the literal above. Otherwise add this minimal host near the top of the test file:

```go
type fakeDeployHost struct{ sources map[string]config.GitSource }

func (h *fakeDeployHost) Exists(string) bool                       { return false }
func (h *fakeDeployHost) Source(n string) (config.GitSource, bool) { s, ok := h.sources[n]; return s, ok }
func (h *fakeDeployHost) Launch(config.App) error                  { return nil }
func (h *fakeDeployHost) Restart(string) error                     { return nil }
func (h *fakeDeployHost) Writers(string) (io.Writer, io.Writer)    { return io.Discard, io.Discard }
```

Ensure imports include `os`, `os/exec`, `path/filepath`, `io`, `marshal/internal/config`, `marshal/internal/deploy`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommand_Commit -v`
Expected: FAIL — no `ControlOp_Commit` case (`unknown op type`).

- [ ] **Step 3: Implement.** In `internal/daemon/command.go`, add before `default:`:

```go
	case *pb.ControlOp_Commit:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		c := v.Commit
		cc := c.GetCredential()
		cred := deploy.Credential{Username: cc.GetUsername(), Token: cc.GetToken()}
		res, cerr := s.deployer.Commit(c.GetApp(), c.GetKind(), c.GetPath(), c.GetNewPath(), c.GetContent(), c.GetMessage(), cred)
		if cerr != nil {
			return &pb.ControlResult{Ok: false, Error: cerr.Error()}
		}
		return &pb.ControlResult{Ok: true, Commit: res}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommand -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/command.go internal/daemon/command_test.go
git commit -m "feat(m24): daemon Commit op case

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Dashboard endpoints (PUT/DELETE file, POST rename)

**Files:**
- Modify: `internal/dashboard/files.go` (add 3 handlers + a `commitControl` helper)
- Modify: `internal/dashboard/handlers.go` (register 3 routes, after line 81)
- Test: `internal/dashboard/files_test.go` (append)

**Interfaces:**
- Consumes: `h.resolveCredential` (`apps.go`), `h.fileControl` (`files.go`), `pb.CommitRequest`/`pb.CommitKind`, `writeJSON`, `requireSession`, `userKey`.
- Produces: `PUT/DELETE /api/fleet/{agent}/apps/{app}/file`, `POST /api/fleet/{agent}/apps/{app}/rename`; JSON response `{"sha":...,"branch":...}` on success.

- [ ] **Step 1: Write the failing test** — append to `internal/dashboard/files_test.go`:

```go
func TestWriteFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "abc1234", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	body := strings.NewReader(`{"content":"hello\n","message":"Update README.md"}`)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=README.md", body)
	req.AddCookie(cookie)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]string
	json.NewDecoder(resp.Body).Decode(&got)
	if got["sha"] != "abc1234" || got["branch"] != "main" {
		t.Fatalf("body = %+v", got)
	}
	cr := c.gotOp.GetCommit()
	if cr.GetApp() != "app1" || cr.GetKind() != pb.CommitKind_COMMIT_EDIT ||
		cr.GetPath() != "README.md" || string(cr.GetContent()) != "hello\n" {
		t.Fatalf("op = %+v", cr)
	}
}

func TestDeleteFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "d1", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=old.txt", strings.NewReader(`{"message":"Delete old.txt"}`))
	req.AddCookie(cookie)
	resp, _ := srv.Client().Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if c.gotOp.GetCommit().GetKind() != pb.CommitKind_COMMIT_DELETE {
		t.Fatalf("kind = %v", c.gotOp.GetCommit().GetKind())
	}
}

func TestRenameFileEndpoint(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true, Commit: &pb.CommitResult{Sha: "r1", Branch: "main"}}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/fleet/dev-1/apps/app1/rename", strings.NewReader(`{"from":"a.txt","to":"b.txt","message":"Rename a.txt → b.txt"}`))
	req.AddCookie(cookie)
	resp, _ := srv.Client().Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	cr := c.gotOp.GetCommit()
	if cr.GetKind() != pb.CommitKind_COMMIT_RENAME || cr.GetPath() != "a.txt" || cr.GetNewPath() != "b.txt" {
		t.Fatalf("op = %+v", cr)
	}
}

func TestWriteFileEndpoint_TooLarge(t *testing.T) {
	c := &fakeFilesController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, c, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	cookie := loginCookie(t, srv.Client(), srv.URL)

	big := strings.Repeat("a", (1<<20)+1)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/api/fleet/dev-1/apps/app1/file?path=big.txt", strings.NewReader(`{"content":"`+big+`"}`))
	req.AddCookie(cookie)
	resp, _ := srv.Client().Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for oversize", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dashboard/ -run 'TestWriteFileEndpoint|TestDeleteFileEndpoint|TestRenameFileEndpoint' -v`
Expected: FAIL — 404/405 (routes unregistered).

- [ ] **Step 3: Implement.** In `internal/dashboard/files.go`, add (top-level) the request bodies, the max constant, and the handlers:

```go
const maxCommitBytes = 1 << 20 // 1 MiB content cap, matches the read cap

type writeBody struct {
	Content    string `json:"content"`
	Message    string `json:"message"`
	Credential string `json:"credential"`
}
type deleteBody struct {
	Message    string `json:"message"`
	Credential string `json:"credential"`
}
type renameBody struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Message    string `json:"message"`
	Credential string `json:"credential"`
}

// writeFileFiles serves PUT /api/fleet/{agent}/apps/{app}/file?path=<rel>.
// Edits an existing file or creates it; commits and pushes via the agent.
func (h *handler) writeFileFiles(w http.ResponseWriter, r *http.Request) {
	agent, app := r.PathValue("agent"), r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	var body writeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if len(body.Content) > maxCommitBytes {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "file too large (max 1 MiB)"})
		return
	}
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": cerr.Error()})
		return
	}
	msg := body.Message
	if msg == "" {
		msg = "Update " + r.URL.Query().Get("path")
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_EDIT, Path: r.URL.Query().Get("path"),
		Content: []byte(body.Content), Message: msg, Credential: cred,
	}}}
	h.commitControl(w, r, agent, app, "edit", op)
}

// deleteFileFiles serves DELETE /api/fleet/{agent}/apps/{app}/file?path=<rel>.
func (h *handler) deleteFileFiles(w http.ResponseWriter, r *http.Request) {
	agent, app := r.PathValue("agent"), r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	var body deleteBody
	_ = json.NewDecoder(r.Body).Decode(&body) // empty body is fine
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": cerr.Error()})
		return
	}
	msg := body.Message
	if msg == "" {
		msg = "Delete " + r.URL.Query().Get("path")
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_DELETE, Path: r.URL.Query().Get("path"),
		Message: msg, Credential: cred,
	}}}
	h.commitControl(w, r, agent, app, "delete", op)
}

// renameFiles serves POST /api/fleet/{agent}/apps/{app}/rename.
func (h *handler) renameFiles(w http.ResponseWriter, r *http.Request) {
	agent, app := r.PathValue("agent"), r.PathValue("app")
	if agent == "" || app == "" {
		http.Error(w, "agent and app required", http.StatusBadRequest)
		return
	}
	var body renameBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.From == "" || body.To == "" {
		http.Error(w, "from and to required", http.StatusBadRequest)
		return
	}
	cred, cerr := h.resolveCredential(body.Credential)
	if cerr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": cerr.Error()})
		return
	}
	msg := body.Message
	if msg == "" {
		msg = "Rename " + body.From + " → " + body.To
	}
	op := &pb.ControlOp{Op: &pb.ControlOp_Commit{Commit: &pb.CommitRequest{
		App: app, Kind: pb.CommitKind_COMMIT_RENAME, Path: body.From, NewPath: body.To,
		Message: msg, Credential: cred,
	}}}
	h.commitControl(w, r, agent, app, "rename", op)
}

// commitControl dispatches a commit op, maps errors (503/400), audit-logs the
// outcome (never the token), and returns {sha,branch} on success.
func (h *handler) commitControl(w http.ResponseWriter, r *http.Request, agent, app, kind string, op *pb.ControlOp) {
	res, ok := h.fileControl(w, r, agent, op)
	if !ok {
		return
	}
	cr := res.GetCommit()
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: commit %s %s/%s -> %s by %s: sha=%s branch=%s",
		kind, agent, app, op.GetCommit().GetPath(), user, cr.GetSha(), cr.GetBranch())
	writeJSON(w, http.StatusOK, map[string]string{"sha": cr.GetSha(), "branch": cr.GetBranch()})
}
```

Add `"encoding/json"` and `"log"` to the `files.go` import block.

In `internal/dashboard/handlers.go`, after line 81 (`GET .../file`) register:

```go
	mux.HandleFunc("PUT /api/fleet/{agent}/apps/{app}/file", h.requireSession(h.writeFileFiles))
	mux.HandleFunc("DELETE /api/fleet/{agent}/apps/{app}/file", h.requireSession(h.deleteFileFiles))
	mux.HandleFunc("POST /api/fleet/{agent}/apps/{app}/rename", h.requireSession(h.renameFiles))
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/dashboard/ -run 'File|Rename' -v && go test ./internal/dashboard/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dashboard/files.go internal/dashboard/handlers.go internal/dashboard/files_test.go
git commit -m "feat(m24): dashboard write/delete/rename file endpoints

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Web — api client functions + editable FileBrowser

**Files:**
- Modify: `web/src/api.ts` (add `CommitResult` type + `writeFile`/`deleteFile`/`renameFile`)
- Modify: `web/src/FileBrowser.tsx` (editing UI, takes a `credential` prop)
- Modify: the FileBrowser call site (find with `grep -rn "FileBrowser" web/src`, likely `web/src/ProcessDetail.tsx`) to pass `credential={p?.credential}`
- Build: `make ui` (regenerates `internal/dashboard/dist`, tracked)

**Interfaces:**
- Consumes: `writeFile`/`deleteFile`/`renameFile`; the existing `Proc.credential` field (M22).
- Produces: an editable Files card. No further Go consumers.

- [ ] **Step 1: Add the api client functions.** Append to `web/src/api.ts`:

```ts
export type CommitResult = { sha: string; branch: string };

export async function writeFile(
  agent: string, app: string, path: string, content: string, message: string, credential?: string,
): Promise<CommitResult> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content, message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `save failed (${r.status})`);
  return r.json();
}

export async function deleteFile(
  agent: string, app: string, path: string, message: string, credential?: string,
): Promise<CommitResult> {
  const q = new URLSearchParams({ path });
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/file?${q}`, {
    method: "DELETE",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `delete failed (${r.status})`);
  return r.json();
}

export async function renameFile(
  agent: string, app: string, from: string, to: string, message: string, credential?: string,
): Promise<CommitResult> {
  const r = await fetch(`/api/fleet/${encodeURIComponent(agent)}/apps/${encodeURIComponent(app)}/rename`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ from, to, message, credential: credential || "" }),
  });
  if (r.status === 401) throw new Error("unauthorized");
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || `rename failed (${r.status})`);
  return r.json();
}
```

- [ ] **Step 2: Replace `web/src/FileBrowser.tsx`** with the editable version:

```tsx
import { useEffect, useState } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { javascript } from "@codemirror/lang-javascript";
import { json } from "@codemirror/lang-json";
import { python } from "@codemirror/lang-python";
import { go } from "@codemirror/lang-go";
import {
  listDir, readFile, fileDownloadURL, writeFile, deleteFile, renameFile,
  type DirEntry, type FileContent,
} from "./api";

function langFor(name: string) {
  const ext = name.split(".").pop()?.toLowerCase();
  switch (ext) {
    case "ts": case "tsx": case "js": case "jsx": return [javascript({ jsx: true, typescript: true })];
    case "json": return [json()];
    case "py": return [python()];
    case "go": return [go()];
    default: return [];
  }
}

function joinPath(dir: string, name: string) { return dir ? `${dir}/${name}` : name; }
function parentPath(p: string) { const i = p.lastIndexOf("/"); return i < 0 ? "" : p.slice(0, i); }

export function FileBrowser({ agent, app, credential }: { agent: string; app: string; credential?: string }) {
  const [path, setPath] = useState("");
  const [entries, setEntries] = useState<DirEntry[]>([]);
  const [open, setOpen] = useState<FileContent | null>(null);
  const [draft, setDraft] = useState("");          // editor buffer
  const [msg, setMsg] = useState("");              // commit message
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [reload, setReload] = useState(0);

  useEffect(() => {
    let stop = false;
    setErr(null);
    listDir(agent, app, path)
      .then((l) => { if (!stop) setEntries(l.entries); })
      .catch((e) => { if (!stop) setErr(String(e.message || e)); });
    return () => { stop = true; };
  }, [agent, app, path, reload]);

  async function onEntry(e: DirEntry) {
    if (e.is_dir) { setOpen(null); setPath(joinPath(path, e.name)); return; }
    setErr(null); setNote(null);
    try {
      const f = await readFile(agent, app, joinPath(path, e.name));
      setOpen(f); setDraft(f.content); setMsg(`Update ${f.path}`);
    } catch (e2: any) { setErr(String(e2.message || e2)); }
  }

  const editable = !!open && !open.binary && !open.truncated;

  async function onSave() {
    if (!open) return;
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await writeFile(agent, app, open.path, draft, msg || `Update ${open.path}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      setOpen({ ...open, content: draft });
      setReload((n) => n + 1);
    } catch (e: any) { setErr(String(e.message || e)); }
    finally { setBusy(false); }
  }

  async function onNewFile() {
    const name = window.prompt("New file path (relative to current folder):");
    if (!name) return;
    const rel = joinPath(path, name);
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await writeFile(agent, app, rel, "", `Create ${rel}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      setReload((n) => n + 1);
    } catch (e: any) { setErr(String(e.message || e)); }
    finally { setBusy(false); }
  }

  async function onDelete(e: DirEntry) {
    const rel = joinPath(path, e.name);
    if (!window.confirm(`Delete ${rel}? This commits and pushes the deletion.`)) return;
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await deleteFile(agent, app, rel, `Delete ${rel}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      if (open?.path === rel) setOpen(null);
      setReload((n) => n + 1);
    } catch (e2: any) { setErr(String(e2.message || e2)); }
    finally { setBusy(false); }
  }

  async function onRename(e: DirEntry) {
    const to = window.prompt(`Rename ${e.name} to:`, e.name);
    if (!to || to === e.name) return;
    const from = joinPath(path, e.name);
    const dest = joinPath(path, to);
    setBusy(true); setErr(null); setNote(null);
    try {
      const res = await renameFile(agent, app, from, dest, `Rename ${from} → ${dest}`, credential);
      setNote(`Pushed ${res.sha.slice(0, 7)} to ${res.branch}`);
      if (open?.path === from) setOpen(null);
      setReload((n) => n + 1);
    } catch (e2: any) { setErr(String(e2.message || e2)); }
    finally { setBusy(false); }
  }

  const crumbs = path ? path.split("/") : [];
  return (
    <div className="filebrowser">
      <div className="fb-note">
        Editing commits &amp; pushes to origin per change. Redeploy to apply changes to the running app.
      </div>
      <div className="crumb fb-crumb">
        <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(""); }}>{app}</a>
        {crumbs.map((c, i) => {
          const sub = crumbs.slice(0, i + 1).join("/");
          return <span key={sub}><span className="sep">/</span>
            <a href="#" onClick={(ev) => { ev.preventDefault(); setOpen(null); setPath(sub); }}>{c}</a></span>;
        })}
        <button className="fb-action" disabled={busy} onClick={onNewFile} style={{ marginLeft: "auto" }}>+ New file</button>
      </div>
      {err && <div className="fb-err">{err}</div>}
      {note && <div className="fb-note">{note}</div>}
      <div className="fb-body">
        <ul className="fb-list">
          {path !== "" && (
            <li className="fb-row" onClick={() => { setOpen(null); setPath(parentPath(path)); }}>
              <span className="fb-name">../</span></li>
          )}
          {entries.map((e) => (
            <li key={e.name} className="fb-row">
              <span className="fb-name" onClick={() => onEntry(e)}>{e.is_dir ? "📁 " : "📄 "}{e.name}</span>
              <span className="fb-size">{e.is_dir ? "" : `${e.size} B`}</span>
              <span className="fb-rowactions">
                <button className="fb-action" disabled={busy} onClick={() => onRename(e)}>Rename</button>
                {!e.is_dir && <button className="fb-action" disabled={busy} onClick={() => onDelete(e)}>Delete</button>}
              </span>
            </li>
          ))}
        </ul>
        <div className="fb-view">
          {!open && <div className="fb-empty">Select a file to view or edit.</div>}
          {open && open.binary && (
            <div className="fb-empty">
              Binary file ({open.size} B). <a href={fileDownloadURL(agent, app, open.path)} download>Download</a>
            </div>
          )}
          {open && !open.binary && (
            <>
              {open.truncated && <div className="fb-note">Showing first 1 MiB of {open.size} B — too large to edit. <a href={fileDownloadURL(agent, app, open.path)} download>Download first 1 MiB</a></div>}
              <CodeMirror value={editable ? draft : open.content} editable={editable} readOnly={!editable}
                onChange={editable ? setDraft : undefined} extensions={langFor(open.path)} theme="dark" />
              {editable && (
                <div className="fb-saverow">
                  <input className="fb-msg" value={msg} onChange={(e) => setMsg(e.target.value)} placeholder="Commit message" />
                  <button className="fb-action" disabled={busy || draft === open.content} onClick={onSave}>Save &amp; push</button>
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Pass the credential prop.** Find the call site:

Run: `grep -rn "<FileBrowser" web/src`

Update it to forward the proc's credential, e.g. `<FileBrowser agent={agent} app={p.name} credential={p?.credential} />`. (`Proc.credential` already exists from M22 — confirm with `grep -n "credential" web/src/api.ts`.)

- [ ] **Step 4: Build the UI**

Run: `make ui`
Expected: Vite build succeeds; `internal/dashboard/dist` updates. Then `go build ./... ` to confirm the embed still compiles.

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/FileBrowser.tsx web/src/ProcessDetail.tsx internal/dashboard/dist
git commit -m "feat(m24): editable file browser (save/new/rename/delete) in dashboard

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

(Adjust the `git add` list to the actual call-site file if it is not `ProcessDetail.tsx`.)

---

### Task 9: Full verification, live demo, handoff

**Files:**
- Create: `docs/handoffs/2026-06-20-m24-commit-push.md`

- [ ] **Step 1: Full test + lint sweep**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all packages green; `gofmt -l .` prints nothing. Fix anything that surfaces.

- [ ] **Step 2: Live demo** (per CLAUDE.md convention). Use a scratch data dir under `/tmp/marshal-m24-demo` and standard ports `:9000`/`:9001`. Set up auth (password + enroll token + fingerprint) while the server is **down**, then start it, enroll an agent `dev-1`. Create an **auth-required `file://` git remote** with a write-capable token credential `gh-demo` (mirror the M22 demo setup). Deploy an app from it on a branch. Then via the dashboard Files card:
  - Edit a tracked file → Save & push → confirm the toast shows `Pushed <sha> to <branch>` and the commit lands on the remote (`git --git-dir=<remote> show <branch>:<path>`).
  - Create a new file in a subfolder; delete a file; rename a file — confirm each lands on origin.
  - Click **Redeploy** and confirm the running app now reflects a pushed edit (the milestone's "redeploy preserves the edit").
  - Attempt a write with a read-only/absent credential → confirm a clean 400 and that the clone rolled back (working tree unchanged, nothing dangling: `git -C <clone> status` clean, HEAD unmoved).
  - Confirm the **token never appears** in the per-app log, `dump.json`, or the agent data dir (`grep -r <token> <dataDir>` → no hits).
  - Confirm `.git/config` cannot be edited (attempt → 400).
  Tear down by data dir only (preserve the user's standing launchd daemon); verify no orphan processes (`pgrep -fl marshal`).

- [ ] **Step 3: Write the handoff** `docs/handoffs/2026-06-20-m24-commit-push.md` covering: current state + branch, what changed this session (per the spec/this plan), key decisions, build/run/test, live-demo results, known issues/deferred (multi-file commits, auto-redeploy, branch creation, SSH keys, M23 carry-overs), and the concrete next step (merge `--no-ff` to `main` via `finishing-a-development-branch`; then SSH deploy keys or another direction).

- [ ] **Step 4: Commit**

```bash
git add docs/handoffs/2026-06-20-m24-commit-push.md
git commit -m "docs: M24 commit & push handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review (completed by plan author)

**Spec coverage:** §4.1 confineNew → Task 2; §4.2 .git guard → Task 2; §4.3 branch gate → Tasks 3/4; §4.4 Commit flow (all kinds, identity, targeted staging, rollback) → Tasks 3/4; §4.5 concurrency guard → Task 5; §5 proto → Task 1; §6 daemon → Task 6; §7 dashboard endpoints + cap + audit + error mapping → Task 7; §8 web UI → Task 8; §10 testing + live demo → spread across tasks + Task 9. No gaps.

**Placeholder scan:** no TBD/TODO; every code step has full code. The one conditional ("if a suitable fake host exists, reuse it") gives a concrete fallback literal, so it is not a placeholder.

**Type consistency:** `mutateAndPush`/`Commit` signatures match between Tasks 3, 5, 6. `pb.CommitKind_COMMIT_*`, `pb.CommitRequest`, `pb.CommitResult`, `GetCommit()/GetSha()/GetBranch()` consistent from Task 1 onward. `gitArgs(credActive bool, args ...string)` usage in Task 3 matches the existing signature in `deployer.go`. Dashboard `commitControl` reuses the real `fileControl` signature from `files.go`.

**Note for implementer (faithful-to-intent divergence):** the spec's UI gating ("editor stays read-only when not on a branch") is implemented **reactively** — the editor is editable for text/non-oversize files, and a not-a-branch / missing-credential rejection surfaces as the honest 400 banner on save, with the working tree rolled back. This keeps the M23 read path (`browse.go`) pure (no git calls in listing) and still satisfies the spec's "honest banner explaining why." Flag this in the handoff.
