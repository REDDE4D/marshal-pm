package deploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"marshal/internal/config"
)

// fakeRunner records the commands it is asked to run and returns a scripted
// error for the Nth call.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	errAt map[int]error // call index (0-based) -> error to return
}

func (f *fakeRunner) Run(_ context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := len(f.calls)
	f.calls = append(f.calls, append([]string{name}, args...))
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
	return f.calls
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
	app.Source.Build = "go build -o server ."
	if err := d.Start(app); err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.wait()

	cmds := runner.cmds()
	if len(cmds) != 2 {
		t.Fatalf("want clone+build (2 cmds), got %d: %v", len(cmds), cmds)
	}
	if cmds[0][0] != "git" || cmds[0][1] != "clone" {
		t.Fatalf("first cmd not git clone: %v", cmds[0])
	}
	if cmds[1][0] != "sh" || cmds[1][1] != "-c" || cmds[1][2] != "go build -o server ." {
		t.Fatalf("second cmd not the build: %v", cmds[1])
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
	runner := &fakeRunner{errAt: map[int]error{1: errBuild()}} // build (2nd call) fails
	d := New(host, runner, root)

	if err := d.Start(gitApp("web")); err != nil {
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
	if err := d.Start(bad); err == nil {
		t.Fatal("expected error for empty repo")
	}
	host.existing["web"] = true
	if err := d.Start(gitApp("web")); err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func errBuild() error { return &buildErr{} }

type buildErr struct{}

func (*buildErr) Error() string { return "exit status 1" }

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
