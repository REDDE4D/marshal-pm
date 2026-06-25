package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/pb"
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
