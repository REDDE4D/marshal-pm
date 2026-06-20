package deploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"marshal/internal/config"
	"marshal/internal/pb"
)

// fakeCall records one invocation of fakeRunner.Run.
type fakeCall struct {
	cmd []string
	env []string
}

// fakeRunner records the commands it is asked to run and returns a scripted
// error for the Nth call.
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	errAt map[int]error // call index (0-based) -> error to return
}

func (f *fakeRunner) Run(_ context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := len(f.calls)
	f.calls = append(f.calls, fakeCall{cmd: append([]string{name}, args...), env: env})
	if f.errAt != nil {
		if err, ok := f.errAt[idx]; ok {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) cmds() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.cmd
	}
	return out
}

// find returns the first recorded call whose cmd slice contains arg.
func (f *fakeRunner) find(arg string) *fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.calls {
		for _, a := range f.calls[i].cmd {
			if a == arg {
				return &f.calls[i]
			}
		}
	}
	return nil
}

// fakeHost records launches/restarts and answers existence/source queries.
type fakeHost struct {
	mu        sync.Mutex
	launched  []config.App
	restarted []string
	existing  map[string]bool
	sources   map[string]config.GitSource
}

func newFakeHost() *fakeHost {
	return &fakeHost{existing: map[string]bool{}, sources: map[string]config.GitSource{}}
}
func (h *fakeHost) Exists(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.existing[name]
}
func (h *fakeHost) Source(name string) (config.GitSource, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sources[name]
	return s, ok
}
func (h *fakeHost) Launch(app config.App) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.launched = append(h.launched, app)
	h.existing[app.Name] = true
	return nil
}
func (h *fakeHost) Restart(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.restarted = append(h.restarted, name)
	return nil
}
func (h *fakeHost) Writers(string) (io.Writer, io.Writer) { return io.Discard, io.Discard }

func gitApp(name string) config.App {
	return config.App{
		Name: name, Cmd: "./server", Instances: 1,
		Source: &config.GitSource{Repo: "https://example/r.git", Ref: "main", Build: "go build -o server ."},
	}
}

func TestStartClonesBuildsAndLaunches(t *testing.T) {
	root := t.TempDir()
	host := newFakeHost()
	runner := &fakeRunner{}
	d := New(host, runner, root)

	app := gitApp("web")
	if err := d.Start(app, Credential{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()

	cmds := runner.cmds()
	if len(cmds) != 3 {
		t.Fatalf("want clone+checkout+build (3 cmds), got %d: %v", len(cmds), cmds)
	}
	if cmds[0][0] != "git" || cmds[0][1] != "clone" {
		t.Fatalf("first cmd not git clone: %v", cmds[0])
	}
	if cmds[1][0] != "git" || cmds[1][1] != "checkout" || cmds[1][2] != "main" {
		t.Fatalf("second cmd not git checkout main: %v", cmds[1])
	}
	if cmds[2][0] != "sh" || cmds[2][1] != "-c" || cmds[2][2] != "go build -o server ." {
		t.Fatalf("third cmd not the build: %v", cmds[2])
	}
	if len(host.launched) != 1 {
		t.Fatalf("expected one launch, got %d", len(host.launched))
	}
	got := host.launched[0]
	if got.Cwd != filepath.Join(root, "web") {
		t.Fatalf("cwd not set to checkout: %q", got.Cwd)
	}
	if got.Source == nil {
		t.Fatal("launched app lost its Source")
	}
	if len(d.Snapshots()) != 0 {
		t.Fatal("successful deploy should clear its state")
	}
}

func TestStartBuildFailureLeavesFailedState(t *testing.T) {
	root := t.TempDir()
	host := newFakeHost()
	runner := &fakeRunner{errAt: map[int]error{2: errBuild()}} // build (3rd call) fails
	d := New(host, runner, root)

	if err := d.Start(gitApp("web"), Credential{}); err != nil {
		t.Fatalf("Start should accept: %v", err)
	}
	d.wait()

	if len(host.launched) != 0 {
		t.Fatal("failed build must not launch the app")
	}
	snaps := d.Snapshots()
	if len(snaps) != 1 || snaps[0].GetState() != phaseFailed {
		t.Fatalf("expected one failed snapshot, got %+v", snaps)
	}
}

func TestStartRejectsEmptyRepoAndDuplicate(t *testing.T) {
	root := t.TempDir()
	host := newFakeHost()
	d := New(host, &fakeRunner{}, root)

	bad := gitApp("web")
	bad.Source.Repo = ""
	if err := d.Start(bad, Credential{}); err == nil {
		t.Fatal("expected error for empty repo")
	}
	host.existing["web"] = true
	if err := d.Start(gitApp("web"), Credential{}); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func errBuild() error { return &buildErr{} }

type buildErr struct{}

func (*buildErr) Error() string { return "exit status 1" }

func TestRedeployFetchesRebuildsRestarts(t *testing.T) {
	root := t.TempDir()
	host := newFakeHost()
	host.existing["web"] = true
	host.sources["web"] = config.GitSource{Repo: "https://example/r.git", Ref: "main", Build: "go build ./..."}
	runner := &fakeRunner{}
	d := New(host, runner, root)

	if err := d.Redeploy("web", Credential{}); err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
	d.wait()

	cmds := runner.cmds()
	if len(cmds) != 3 || cmds[0][1] != "fetch" || cmds[1][1] != "reset" || cmds[2][0] != "sh" {
		t.Fatalf("unexpected redeploy cmds: %v", cmds)
	}
	if len(host.restarted) != 1 || host.restarted[0] != "web" {
		t.Fatalf("expected a restart of web, got %v", host.restarted)
	}
	if len(d.Snapshots()) != 0 {
		t.Fatal("successful redeploy should clear state")
	}
}

func TestRedeployBuildFailureDoesNotRestart(t *testing.T) {
	root := t.TempDir()
	host := newFakeHost()
	host.existing["web"] = true
	host.sources["web"] = config.GitSource{Repo: "https://example/r.git", Build: "go build ./..."}
	// cmds: fetch(0), reset(1), build(2) -> fail build.
	runner := &fakeRunner{errAt: map[int]error{2: errBuild()}}
	d := New(host, runner, root)

	if err := d.Redeploy("web", Credential{}); err != nil {
		t.Fatalf("Redeploy: %v", err)
	}
	d.wait()

	if len(host.restarted) != 0 {
		t.Fatal("failed rebuild must not restart the running app")
	}
	snaps := d.Snapshots()
	if len(snaps) != 1 || snaps[0].GetState() != phaseFailed {
		t.Fatalf("expected failed state, got %+v", snaps)
	}
}

func TestRedeployRejectsNonGitApp(t *testing.T) {
	d := New(newFakeHost(), &fakeRunner{}, t.TempDir())
	if err := d.Redeploy("nope", Credential{}); err == nil {
		t.Fatal("expected error when app has no git source")
	}
}

// envHas reports whether env contains want. "GIT_ASKPASS" matches any
// "GIT_ASKPASS=..." entry; "K=V" matches exactly.
func envHas(env []string, want string) bool {
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

func TestCloneUsesAskpassAndHidesToken(t *testing.T) {
	fr := &fakeRunner{}
	host := newFakeHost()
	d := New(host, fr, t.TempDir())

	app := config.App{Name: "priv", Source: &config.GitSource{Repo: "https://github.com/me/priv.git"}}
	if err := d.Start(app, Credential{Username: "octocat", Token: "ghp_SECRET"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()

	clone := fr.find("clone")
	if clone == nil {
		t.Fatal("no clone call recorded")
	}
	// Token must never appear in argv.
	for _, a := range clone.cmd {
		if strings.Contains(a, "ghp_SECRET") {
			t.Fatalf("token leaked into argv: %v", clone.cmd)
		}
	}
	// URL carries the username only.
	if !argvHas(clone.cmd, "https://octocat@github.com/me/priv.git") {
		t.Fatalf("username not embedded in clone URL: %v", clone.cmd)
	}
	// Credential env is present on the clone, token only in env.
	if !envHas(clone.env, "GIT_ASKPASS") || !envHas(clone.env, "MARSHAL_GIT_TOKEN=ghp_SECRET") ||
		!envHas(clone.env, "MARSHAL_GIT_USER=octocat") || !envHas(clone.env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("credential env missing/incomplete: %v", clone.env)
	}
}

func TestNoCredentialNoAskpass(t *testing.T) {
	fr := &fakeRunner{}
	d := New(newFakeHost(), fr, t.TempDir())
	app := config.App{Name: "pub", Source: &config.GitSource{Repo: "https://github.com/me/pub.git"}}
	if err := d.Start(app, Credential{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()
	clone := fr.find("clone")
	if envHas(clone.env, "GIT_ASKPASS") {
		t.Fatalf("askpass set without a credential")
	}
	if !argvHas(clone.cmd, "https://github.com/me/pub.git") {
		t.Fatalf("URL should be unmodified without a credential: %v", clone.cmd)
	}
}

// argvHasSeq reports whether args contains a immediately followed by b.
func argvHasSeq(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestCloneDisablesCredentialHelperWhenManaged(t *testing.T) {
	fr := &fakeRunner{}
	host := newFakeHost()
	d := New(host, fr, t.TempDir())

	// With a managed credential: clone args must disable credential helpers.
	app := config.App{Name: "priv2", Source: &config.GitSource{Repo: "https://github.com/me/priv2.git"}}
	if err := d.Start(app, Credential{Username: "octocat", Token: "ghp_x"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()

	clone := fr.find("clone")
	if clone == nil {
		t.Fatal("no clone call recorded")
	}
	// Must have -c credential.helper= (consecutive args).
	if !argvHasSeq(clone.cmd, "-c", "credential.helper=") {
		t.Fatalf("managed clone did not disable credential helpers: %v", clone.cmd)
	}
	// Username-only URL still present.
	if !argvHas(clone.cmd, "https://octocat@github.com/me/priv2.git") {
		t.Fatalf("username-in-URL missing from managed clone: %v", clone.cmd)
	}
	// Token must not appear in argv.
	for _, a := range clone.cmd {
		if strings.Contains(a, "ghp_x") {
			t.Fatalf("token leaked into argv: %v", clone.cmd)
		}
	}

	// Without a managed credential: clone args must NOT disable credential helpers.
	fr2 := &fakeRunner{}
	d2 := New(newFakeHost(), fr2, t.TempDir())
	app2 := config.App{Name: "pub2", Source: &config.GitSource{Repo: "https://github.com/me/pub2.git"}}
	if err := d2.Start(app2, Credential{}); err != nil {
		t.Fatalf("Start (no-cred): %v", err)
	}
	d2.wait()

	clone2 := fr2.find("clone")
	if clone2 == nil {
		t.Fatal("no clone call recorded (no-cred)")
	}
	if argvHas(clone2.cmd, "credential.helper=") {
		t.Fatalf("no-cred clone should not disable credential helpers: %v", clone2.cmd)
	}
}

func TestDeployerRoot(t *testing.T) {
	deployRoot := t.TempDir()
	d := New(nil, nil, deployRoot)

	// Unknown app: no dir on disk.
	if _, ok := d.Root("ghost"); ok {
		t.Errorf("Root(ghost) ok=true, want false")
	}
	// Make a deployment dir.
	if err := os.MkdirAll(filepath.Join(deployRoot, "app1"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := d.Root("app1")
	if !ok || got != filepath.Join(deployRoot, "app1") {
		t.Errorf("Root(app1) = (%q,%v), want (%q,true)", got, ok, filepath.Join(deployRoot, "app1"))
	}
	// Name with a separator must be rejected (no traversal via app name).
	if _, ok := d.Root("../etc"); ok {
		t.Errorf("Root(../etc) ok=true, want false")
	}
}

func TestSnapshotsAndForget(t *testing.T) {
	root := t.TempDir()
	d := New(newFakeHost(), &fakeRunner{}, root)

	// Manually seed a failed deploy state + a deploy dir.
	dir := filepath.Join(root, "web")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d.setState("web", phaseFailed, "build exited 1")

	snaps := d.Snapshots()
	if len(snaps) != 1 || snaps[0].GetName() != "web" ||
		snaps[0].GetState() != phaseFailed || snaps[0].GetSource() != "git" ||
		snaps[0].GetDetail() != "build exited 1" {
		t.Fatalf("unexpected snapshots: %+v", snaps)
	}

	if !d.Forget("web") {
		t.Fatal("Forget should report an existing entry")
	}
	if len(d.Snapshots()) != 0 {
		t.Fatal("state not cleared")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("deploy dir not removed")
	}
	if d.Forget("web") {
		t.Fatal("Forget on absent entry should report false")
	}
}

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
