# Git Deploy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deploy an app from a git repo (clone → optional/auto build → run) via the dashboard, asynchronously with live status, plus redeploy of an existing git app.

**Architecture:** A new agent `deploy` package runs clone/build out of band in a goroutine, tracks per-app deploy phase in memory, and on success hands the resolved `config.App` to the existing start chain. The fleet heartbeat merges synthetic `ProcInfo` entries for in-flight/failed deploys so the dashboard shows progress. Git source rides on `AppSpec`; new `ControlOp_Deploy`/`Redeploy` ops keep the synchronous `Start` path untouched.

**Tech Stack:** Go 1.26 (stdlib `os/exec`, `os`, `context`); protobuf via `go generate ./internal/pb` (protoc); React/TypeScript web client (Vite); `git` CLI on the agent host.

## Global Constraints

- TDD: failing test first, then implementation. `go test ./... -race -count=1` green before finishing; `gofmt -l .` silent; `go vet ./...` clean.
- Module path is `marshal`; imports are `marshal/internal/...`.
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Feature work on a branch, not `main`.
- Auth for git is the host's own git credentials — **no secret storage in Marshal** this milestone.
- Build commands run through `sh -c`. Build auto-detect table is Go + Node only.
- Web client has no test framework: verify with `make ui` (`tsc -b`) + live demo.
- Spec: `docs/superpowers/specs/2026-06-19-git-deploy-design.md`.

---

### Task 0: Create the feature branch

- [ ] **Step 1: Branch from main**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout -b m21-git-deploy
```

- [ ] **Step 2: Confirm clean baseline**

Run: `go test ./... -count=1 >/dev/null && echo OK`
Expected: `OK`

---

### Task 1: Proto — GitSource, AppSpec.source, ProcInfo fields, Deploy/Redeploy ops

**Files:**
- Modify: `proto/marshal/v1/daemon.proto`
- Modify: `proto/marshal/v1/fleet.proto`
- Regenerate: `internal/pb/*.pb.go` (via `go generate`)
- Test: `internal/pb/gitsource_test.go` (create)

**Interfaces:**
- Produces: `pb.GitSource{Repo, Ref, Build, Subdir string}`; `pb.AppSpec.Source *pb.GitSource` (field 11, optional); `pb.ProcInfo.Source string` (field 10), `pb.ProcInfo.Detail string` (field 11); `pb.DeployRequest{App *pb.AppSpec}`; `pb.RedeployRequest{Target string}`; `pb.ControlOp_Deploy{Deploy *pb.DeployRequest}` (oneof field 5), `pb.ControlOp_Redeploy{Redeploy *pb.RedeployRequest}` (oneof field 6). Accessors `GetSource()`, `GetDetail()`, `GetApp()`, `GetTarget()`.

- [ ] **Step 1: Add GitSource + AppSpec.source to daemon.proto**

In `proto/marshal/v1/daemon.proto`, immediately above `message AppSpec`, add:

```proto
// GitSource describes deploying an app from a git repository. It mirrors
// config.GitSource. Only repo is required; ref empty → default branch,
// build empty → auto-detected, subdir empty → repo root.
message GitSource {
  string repo   = 1;
  string ref    = 2;
  string build  = 3;
  string subdir = 4;
}
```

Inside `message AppSpec`, after `optional LogRetention logs = 10;`, add:

```proto
  optional GitSource source = 11; // M21 git deploy; nil for command apps
```

- [ ] **Step 2: Add ProcInfo.source + ProcInfo.detail to daemon.proto**

Inside `message ProcInfo`, after `int64 mem = 9;  // M3`, add:

```proto
  string source = 10; // M21 "command" | "git" — drives the redeploy button
  string detail = 11; // M21 status summary for synthetic deploy entries
```

- [ ] **Step 3: Add Deploy/Redeploy messages + ControlOp variants to fleet.proto**

In `proto/marshal/v1/fleet.proto`, above `message ControlOp`, add:

```proto
message DeployRequest   { AppSpec app    = 1; } // M21 git source rides on the AppSpec
message RedeployRequest { string  target = 1; } // M21 app name
```

Inside the `ControlOp` oneof, after `StartRequest start   = 4;`, add:

```proto
    DeployRequest   deploy   = 5; // M21
    RedeployRequest redeploy = 6; // M21
```

- [ ] **Step 4: Regenerate protobuf code**

Run: `cd "/Users/sebastiankuprat/process manager" && go generate ./internal/pb && echo DONE`
Expected: `DONE`, and `git status` shows modified `internal/pb/daemon.pb.go`, `internal/pb/fleet.pb.go`.

- [ ] **Step 5: Write a generation sanity test**

Create `internal/pb/gitsource_test.go`:

```go
package pb

import "testing"

func TestGitSourceAndDeployOpsGenerated(t *testing.T) {
	spec := &AppSpec{Name: "x", Cmd: "c", Source: &GitSource{Repo: "r", Ref: "main", Build: "go build", Subdir: "sub"}}
	if spec.GetSource().GetRepo() != "r" || spec.GetSource().GetSubdir() != "sub" {
		t.Fatalf("AppSpec.Source round-trip failed: %+v", spec.GetSource())
	}
	pi := &ProcInfo{Source: "git", Detail: "build exited 1"}
	if pi.GetSource() != "git" || pi.GetDetail() != "build exited 1" {
		t.Fatalf("ProcInfo source/detail failed: %+v", pi)
	}
	op := &ControlOp{Op: &ControlOp_Deploy{Deploy: &DeployRequest{App: spec}}}
	if op.GetDeploy().GetApp().GetName() != "x" {
		t.Fatal("ControlOp_Deploy round-trip failed")
	}
	rop := &ControlOp{Op: &ControlOp_Redeploy{Redeploy: &RedeployRequest{Target: "x"}}}
	if rop.GetRedeploy().GetTarget() != "x" {
		t.Fatal("ControlOp_Redeploy round-trip failed")
	}
}
```

- [ ] **Step 6: Run the test**

Run: `go test ./internal/pb/ -run TestGitSourceAndDeployOpsGenerated -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add proto/ internal/pb/
git commit -m "feat(proto): add GitSource, ProcInfo source/detail, Deploy/Redeploy ops

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: config.GitSource + config.App.Source + appSpecToConfig mapping

**Files:**
- Modify: `internal/config/config.go` (App struct + new GitSource type)
- Modify: `internal/daemon/convert.go` (`appSpecToConfig`)
- Test: `internal/daemon/convert_test.go` (add case)

**Interfaces:**
- Produces: `config.GitSource{Repo, Ref, Build, Subdir string}`; `config.App.Source *config.GitSource` (json `source,omitempty`). `appSpecToConfig` now copies the source proto↔config both directions.
- Consumes: `pb.AppSpec.GetSource()` from Task 1.

- [ ] **Step 1: Add the config type + field**

In `internal/config/config.go`, after the `App` struct, add:

```go
// GitSource describes deploying an app from a git repository (M21).
type GitSource struct {
	Repo   string `yaml:"repo" json:"repo"`
	Ref    string `yaml:"ref" json:"ref,omitempty"`
	Build  string `yaml:"build" json:"build,omitempty"`
	Subdir string `yaml:"subdir" json:"subdir,omitempty"`
}
```

In the `App` struct, after the `Logs` field, add:

```go
	Source *GitSource `yaml:"source" json:"source,omitempty"` // M21 git deploy
```

- [ ] **Step 2: Write the failing mapping test**

In `internal/daemon/convert_test.go`, add:

```go
func TestAppSpecToConfigCopiesGitSource(t *testing.T) {
	spec := &pb.AppSpec{
		Name: "web", Cmd: "./server", Instances: 1,
		Source: &pb.GitSource{Repo: "https://example/r.git", Ref: "main", Build: "go build -o server .", Subdir: "cmd"},
	}
	app, err := appSpecToConfig(spec)
	if err != nil {
		t.Fatalf("appSpecToConfig: %v", err)
	}
	if app.Source == nil {
		t.Fatal("Source not copied")
	}
	if app.Source.Repo != "https://example/r.git" || app.Source.Ref != "main" ||
		app.Source.Build != "go build -o server ." || app.Source.Subdir != "cmd" {
		t.Fatalf("Source mismatch: %+v", app.Source)
	}
}
```

- [ ] **Step 3: Run it, verify it fails**

Run: `go test ./internal/daemon/ -run TestAppSpecToConfigCopiesGitSource -v`
Expected: FAIL (`app.Source` is nil).

- [ ] **Step 4: Implement the mapping**

In `internal/daemon/convert.go`, inside `appSpecToConfig`, after the `if lr := s.GetLogs(); lr != nil { ... }` block and **before** `cfg := config.Config{...}`, add:

```go
	if gs := s.GetSource(); gs != nil {
		app.Source = &config.GitSource{
			Repo:   gs.GetRepo(),
			Ref:    gs.GetRef(),
			Build:  gs.GetBuild(),
			Subdir: gs.GetSubdir(),
		}
	}
```

- [ ] **Step 5: Run it, verify it passes**

Run: `go test ./internal/daemon/ -run TestAppSpecToConfigCopiesGitSource -v`
Expected: PASS.

- [ ] **Step 6: Verify config.Prepare tolerates a git app with empty cwd**

Run: `go test ./internal/config/ -count=1`
Expected: PASS (no new failures — confirms adding the optional field didn't break validation).

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/daemon/convert.go internal/daemon/convert_test.go
git commit -m "feat(config): add GitSource to config.App and appSpecToConfig

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Build auto-detect

**Files:**
- Create: `internal/deploy/detect.go`
- Test: `internal/deploy/detect_test.go`

**Interfaces:**
- Produces: `func DetectBuild(dir string) string` — returns the shell build command for a checkout, or `""` when no build is needed. Used by the deployer in Task 5/6 when the user left `build` empty.

- [ ] **Step 1: Write the failing tests**

Create `internal/deploy/detect_test.go`:

```go
package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectBuild(t *testing.T) {
	cases := []struct {
		name  string
		setup func(dir string)
		want  string
	}{
		{"go module", func(d string) { write(t, d, "go.mod", "module x\n") }, "go build ./..."},
		{"node with build script",
			func(d string) { write(t, d, "package.json", `{"scripts":{"build":"vite build"}}`) },
			"npm ci && npm run build"},
		{"node without build script",
			func(d string) { write(t, d, "package.json", `{"scripts":{"start":"node ."}}`) },
			"npm ci"},
		{"node with no scripts key",
			func(d string) { write(t, d, "package.json", `{"name":"x"}`) },
			"npm ci"},
		{"empty repo", func(d string) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(dir)
			if got := DetectBuild(dir); got != tc.want {
				t.Fatalf("DetectBuild=%q want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run it, verify it fails**

Run: `go test ./internal/deploy/ -run TestDetectBuild -v`
Expected: FAIL (`DetectBuild` undefined / package missing).

- [ ] **Step 3: Implement DetectBuild**

Create `internal/deploy/detect.go`:

```go
// Package deploy clones, builds, and launches apps from git sources (M21).
package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DetectBuild infers a shell build command for a checkout when the user did
// not supply one. Returns "" when no build step is needed (run as-is). The
// table is intentionally small (Go + Node); an explicit build always wins
// upstream, so this is only a convenience default.
func DetectBuild(dir string) string {
	if exists(filepath.Join(dir, "go.mod")) {
		return "go build ./..."
	}
	if pkg := filepath.Join(dir, "package.json"); exists(pkg) {
		if hasNpmBuildScript(pkg) {
			return "npm ci && npm run build"
		}
		return "npm ci"
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasNpmBuildScript(pkgPath string) bool {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, ok := pkg.Scripts["build"]
	return ok
}
```

- [ ] **Step 4: Run it, verify it passes**

Run: `go test ./internal/deploy/ -run TestDetectBuild -v`
Expected: PASS (all sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/detect.go internal/deploy/detect_test.go
git commit -m "feat(deploy): build-command auto-detection (Go + Node)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Deployer scaffolding — interfaces, struct, Snapshots, Forget

**Files:**
- Create: `internal/deploy/deployer.go`
- Test: `internal/deploy/deployer_test.go`

**Interfaces:**
- Produces:
  - `type Runner interface { Run(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error }`
  - `type Host interface { Exists(name string) bool; Source(name string) (config.GitSource, bool); Launch(app config.App) error; Restart(name string) error; Writers(label string) (stdout, stderr io.Writer) }`
  - `type Deployer struct { ... }` with `func New(host Host, runner Runner, deployRoot string) *Deployer`
  - `func (d *Deployer) Snapshots() []pb.ProcInfo` — synthetic entries for in-flight/failed deploys (returns values, caller wraps to pointers)
  - `func (d *Deployer) Forget(name string) bool` — clears state + removes the deploy dir; returns whether an entry existed
  - phase constants `phaseCloning="cloning"`, `phaseBuilding="building"`, `phaseFailed="failed"`
- Consumes: `config.App`, `config.GitSource` (Task 2); `pb.ProcInfo` (Task 1); `DetectBuild` (Task 3).

- [ ] **Step 1: Write the failing tests for Snapshots + Forget**

Create `internal/deploy/deployer_test.go`:

```go
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
```

- [ ] **Step 2: Run it, verify it fails**

Run: `go test ./internal/deploy/ -run TestSnapshotsAndForget -v`
Expected: FAIL (`New`, `setState`, etc. undefined).

- [ ] **Step 3: Implement the scaffolding**

Create `internal/deploy/deployer.go`:

```go
package deploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/config"
	"marshal/internal/pb"
)

const (
	phaseCloning  = "cloning"
	phaseBuilding = "building"
	phaseFailed   = "failed"
)

// Runner executes a command in dir, streaming combined output to stdout/stderr.
type Runner interface {
	Run(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error
}

// Host is the agent surface the deployer drives after a successful build.
type Host interface {
	Exists(name string) bool                       // an app of this name is already managed
	Source(name string) (config.GitSource, bool)   // persisted git source for redeploy
	Launch(app config.App) error                   // mgr.Add + persist (the start chain)
	Restart(name string) error                     // restart in place (picks up new binary)
	Writers(label string) (stdout, stderr io.Writer)
}

type state struct {
	phase  string
	detail string
}

// Deployer clones, builds, and launches git-sourced apps out of band, tracking
// per-app phase so the fleet heartbeat can surface progress.
type Deployer struct {
	host       Host
	runner     Runner
	deployRoot string

	mu     sync.Mutex
	states map[string]state
}

// New builds a Deployer. deployRoot is the directory under which each app's
// checkout lives (deployRoot/<name>).
func New(host Host, runner Runner, deployRoot string) *Deployer {
	return &Deployer{host: host, runner: runner, deployRoot: deployRoot, states: map[string]state{}}
}

func (d *Deployer) setState(name, phase, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[name] = state{phase: phase, detail: detail}
}

func (d *Deployer) clearState(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.states, name)
}

func (d *Deployer) dir(name string) string { return filepath.Join(d.deployRoot, name) }

// Snapshots returns one synthetic ProcInfo per tracked (in-flight or failed)
// deploy, so the heartbeat shows it even before any real instance exists.
func (d *Deployer) Snapshots() []pb.ProcInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]pb.ProcInfo, 0, len(d.states))
	for name, st := range d.states {
		out = append(out, pb.ProcInfo{
			Name:   name,
			State:  st.phase,
			Source: "git",
			Detail: st.detail,
		})
	}
	return out
}

// Forget clears any tracked state for name and removes its deploy dir. Returns
// true if a state entry existed.
func (d *Deployer) Forget(name string) bool {
	d.mu.Lock()
	_, had := d.states[name]
	delete(d.states, name)
	d.mu.Unlock()
	_ = os.RemoveAll(d.dir(name))
	return had
}
```

- [ ] **Step 4: Run it, verify it passes**

Run: `go test ./internal/deploy/ -run TestSnapshotsAndForget -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/deploy/deployer.go internal/deploy/deployer_test.go
git commit -m "feat(deploy): deployer scaffolding (Host/Runner ifaces, Snapshots, Forget)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Deployer.Start — clone → build → launch (+ reject + build-fail)

**Files:**
- Modify: `internal/deploy/deployer.go`
- Test: `internal/deploy/deployer_test.go` (add cases)

**Interfaces:**
- Produces: `func (d *Deployer) Start(app config.App) error` — validates synchronously (returns an error to reject: empty repo, or name already running/mid-deploy), then runs clone+build+launch in a goroutine. Exposes `func (d *Deployer) wait()` **for tests only** to block until in-flight goroutines finish (implemented with a `sync.WaitGroup`).
- Consumes: `Host`, `Runner`, `DetectBuild`.

- [ ] **Step 1: Write failing tests for Start (success, build-fail, reject)**

Add to `internal/deploy/deployer_test.go`:

```go
func gitApp(name string) config.App {
	return config.App{
		Name: name, Cmd: "./server", Instances: 1,
		Source: &config.GitSource{Repo: "https://example/r.git", Ref: "main"},
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
```

- [ ] **Step 2: Run them, verify they fail**

Run: `go test ./internal/deploy/ -run TestStart -v`
Expected: FAIL (`Start`, `wait` undefined).

- [ ] **Step 3: Implement Start (+ wait, + run helpers)**

In `internal/deploy/deployer.go`, add `"fmt"` and `"sync"` (already imported) usage, add a `wg sync.WaitGroup` field to `Deployer`:

```go
type Deployer struct {
	host       Host
	runner     Runner
	deployRoot string

	mu     sync.Mutex
	states map[string]state
	wg     sync.WaitGroup // tracks in-flight deploy goroutines (test sync)
}
```

Add these methods:

```go
// Start validates a git deploy and launches it in the background. The returned
// error rejects the request synchronously (duplicate name, empty repo); a nil
// return means "accepted" — progress is reported via Snapshots.
func (d *Deployer) Start(app config.App) error {
	if app.Source == nil || app.Source.Repo == "" {
		return fmt.Errorf("git source requires a repo")
	}
	if d.host.Exists(app.Name) {
		return fmt.Errorf("app %q already exists", app.Name)
	}
	d.mu.Lock()
	if _, busy := d.states[app.Name]; busy {
		d.mu.Unlock()
		return fmt.Errorf("app %q already exists", app.Name)
	}
	d.states[app.Name] = state{phase: phaseCloning}
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runDeploy(app, false)
	}()
	return nil
}

// runDeploy performs clone (or fetch when redeploy)+build, then launches or
// restarts. On any failure it leaves a failed state for the dashboard.
func (d *Deployer) runDeploy(app config.App, redeploy bool) {
	ctx := context.Background()
	dir := d.dir(app.Name)
	stdout, stderr := d.host.Writers(app.Name + "#0")
	src := *app.Source

	d.setState(app.Name, phaseCloning, "")
	if err := d.fetch(ctx, dir, src, redeploy, stdout, stderr); err != nil {
		d.setState(app.Name, phaseFailed, summarize("clone", err))
		return
	}

	d.setState(app.Name, phaseBuilding, "")
	buildDir := dir
	if src.Subdir != "" {
		buildDir = filepath.Join(dir, src.Subdir)
	}
	build := src.Build
	if build == "" {
		build = DetectBuild(buildDir)
	}
	if build != "" {
		if err := d.runner.Run(ctx, buildDir, stdout, stderr, "sh", "-c", build); err != nil {
			d.setState(app.Name, phaseFailed, summarize("build", err))
			return
		}
	}

	if redeploy {
		if err := d.host.Restart(app.Name); err != nil {
			d.setState(app.Name, phaseFailed, summarize("restart", err))
			return
		}
		d.clearState(app.Name)
		return
	}

	launch := app
	launch.Cwd = buildDir
	if err := d.host.Launch(launch); err != nil {
		d.setState(app.Name, phaseFailed, summarize("launch", err))
		return
	}
	d.clearState(app.Name)
}

// fetch clones into dir for a fresh deploy, or fetches+resets for a redeploy.
func (d *Deployer) fetch(ctx context.Context, dir string, src config.GitSource, redeploy bool, stdout, stderr io.Writer) error {
	if !redeploy {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		if err := d.runner.Run(ctx, "", stdout, stderr, "git", "clone", src.Repo, dir); err != nil {
			return err
		}
		if src.Ref != "" {
			return d.runner.Run(ctx, dir, stdout, stderr, "git", "checkout", src.Ref)
		}
		return nil
	}
	if err := d.runner.Run(ctx, dir, stdout, stderr, "git", "fetch", "origin"); err != nil {
		return err
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	return d.runner.Run(ctx, dir, stdout, stderr, "git", "reset", "--hard", "origin/"+ref)
}

func summarize(stage string, err error) string { return stage + " failed: " + err.Error() }

// wait blocks until all in-flight deploy goroutines finish. Tests only.
func (d *Deployer) wait() { d.wg.Wait() }
```

Add `"fmt"` to the import block.

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./internal/deploy/ -run TestStart -v`
Expected: PASS (all three).

- [ ] **Step 5: Run the whole package with race**

Run: `go test ./internal/deploy/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/deploy/deployer.go internal/deploy/deployer_test.go
git commit -m "feat(deploy): Deployer.Start clones, builds, and launches git apps

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Deployer.Redeploy — fetch → rebuild → restart (swap only on success)

**Files:**
- Modify: `internal/deploy/deployer.go`
- Test: `internal/deploy/deployer_test.go` (add cases)

**Interfaces:**
- Produces: `func (d *Deployer) Redeploy(name string) error` — looks up the persisted source via `Host.Source`; errors if the app has no git source; otherwise runs `runDeploy(app, true)` in a goroutine.
- Consumes: `Host.Source`, `Host.Restart`, `runDeploy` (Task 5).

- [ ] **Step 1: Write failing tests (success restarts; build-fail keeps app)**

Add to `internal/deploy/deployer_test.go`:

```go
func TestRedeployFetchesRebuildsRestarts(t *testing.T) {
	root := t.TempDir()
	host := newFakeHost()
	host.existing["web"] = true
	host.sources["web"] = config.GitSource{Repo: "https://example/r.git", Ref: "main", Build: "go build ./..."}
	runner := &fakeRunner{}
	d := New(host, runner, root)

	if err := d.Redeploy("web"); err != nil {
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

	if err := d.Redeploy("web"); err != nil {
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
	if err := d.Redeploy("nope"); err == nil {
		t.Fatal("expected error when app has no git source")
	}
}
```

- [ ] **Step 2: Run them, verify they fail**

Run: `go test ./internal/deploy/ -run TestRedeploy -v`
Expected: FAIL (`Redeploy` undefined).

- [ ] **Step 3: Implement Redeploy**

In `internal/deploy/deployer.go`, add:

```go
// Redeploy fetches the latest commit for an existing git app, rebuilds, and
// restarts it in place. If the rebuild fails the running app is left untouched.
func (d *Deployer) Redeploy(name string) error {
	src, ok := d.host.Source(name)
	if !ok || src.Repo == "" {
		return fmt.Errorf("app %q is not git-sourced", name)
	}
	d.mu.Lock()
	if _, busy := d.states[name]; busy {
		d.mu.Unlock()
		return fmt.Errorf("app %q is already deploying", name)
	}
	d.states[name] = state{phase: phaseBuilding}
	d.mu.Unlock()

	app := config.App{Name: name, Source: &src}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runDeploy(app, true)
	}()
	return nil
}
```

- [ ] **Step 4: Run them, verify they pass**

Run: `go test ./internal/deploy/ -run TestRedeploy -v`
Expected: PASS.

- [ ] **Step 5: Full package + race + vet/fmt**

Run: `go test ./internal/deploy/ -race -count=1 && go vet ./internal/deploy/ && gofmt -l internal/deploy/`
Expected: tests PASS, vet silent, gofmt prints nothing.

- [ ] **Step 6: Commit**

```bash
git add internal/deploy/deployer.go internal/deploy/deployer_test.go
git commit -m "feat(deploy): Deployer.Redeploy (fetch, rebuild, restart-on-success)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Daemon wiring — real Runner, Host impl, command routing, snapshot merge, Source stamping

**Files:**
- Create: `internal/deploy/exec_runner.go` (exec-based Runner)
- Modify: `internal/manager/manager.go` (`InstanceSnapshot.Source` from spec)
- Modify: `internal/daemon/server.go` (construct deployer; Host methods; `Launch` helper; `fleetSnapshot` already in fleet.go)
- Modify: `internal/daemon/convert.go` (`snapshotToProc` stamps `Source`)
- Modify: `internal/daemon/fleet.go` (merge `deployer.Snapshots()`)
- Modify: `internal/daemon/command.go` (Deploy/Redeploy/Delete cases)
- Modify: `internal/store/store.go` (`DeploysDir()` helper)
- Test: `internal/daemon/command_test.go` (Deploy/Redeploy routing), `internal/daemon/fleet_test.go` (merge + stamping) — create the latter if absent.

**Interfaces:**
- Consumes: `deploy.New`, `deploy.Deployer.{Start,Redeploy,Snapshots,Forget}`, `deploy.Runner` (Tasks 4–6).
- Produces: `manager.InstanceSnapshot.Source string`; `(*Server)` implements `deploy.Host`; `store.Store.DeploysDir() string`.

- [ ] **Step 1: ExecRunner test + impl**

Create `internal/deploy/exec_runner_test.go`:

```go
package deploy

import (
	"bytes"
	"context"
	"testing"
)

func TestExecRunnerStreamsOutputAndError(t *testing.T) {
	var out bytes.Buffer
	r := ExecRunner{}
	if err := r.Run(context.Background(), "", &out, &out, "sh", "-c", "echo hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "hello\n" {
		t.Fatalf("output %q", got)
	}
	if err := r.Run(context.Background(), "", &out, &out, "sh", "-c", "exit 3"); err == nil {
		t.Fatal("expected non-zero exit to error")
	}
}
```

Create `internal/deploy/exec_runner.go`:

```go
package deploy

import (
	"context"
	"io"
	"os/exec"
)

// ExecRunner runs commands with os/exec, streaming combined output to the
// provided writers. dir, when non-empty, is the working directory.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
```

Run: `go test ./internal/deploy/ -run TestExecRunner -v`
Expected: PASS.

- [ ] **Step 2: Add InstanceSnapshot.Source (failing manager test)**

In `internal/manager/manager.go`, add to `InstanceSnapshot`:

```go
	Source     string // "command" | "git" (M21), derived from the app spec
```

In `List()` (and wherever snapshots are built — search for `InstanceSnapshot{`), set `Source` based on the managed app's spec: `"git"` if `spec.Source != nil` else `"command"`. Add a test in `internal/manager/manager_test.go`:

```go
func TestListReportsGitSource(t *testing.T) {
	m := newTestManager(t) // use the package's existing helper
	_, err := m.Add(config.App{Name: "g", Cmd: "sleep", Args: []string{"60"}, Instances: 1,
		Source: &config.GitSource{Repo: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range m.List() {
		if s.Name == "g" && s.Source != "git" {
			t.Fatalf("want source=git, got %q", s.Source)
		}
	}
}
```

(If `newTestManager`/`Add` helpers differ, mirror the existing manager tests' setup.) Run the test, see it fail, then wire `Source` in `List()` until it passes.

- [ ] **Step 3: snapshotToProc stamps Source**

In `internal/daemon/convert.go`, in `snapshotToProc`, add `Source: s.Source,` to the returned `&pb.ProcInfo{...}`. (No `Detail` — real instances leave it empty.)

- [ ] **Step 4: store.DeploysDir helper**

In `internal/store/store.go`, after `LogsDir`, add:

```go
// DeploysDir is where git checkouts live (one subdir per app).
func (s *Store) DeploysDir() string { return filepath.Join(s.base, "deploys") }
```

- [ ] **Step 5: Construct the deployer + implement deploy.Host on Server**

In `internal/daemon/server.go`:
- Add field `deployer *deploy.Deployer` to `Server`.
- In `Run` (where the `Server` is built, near the `gs := grpc.NewServer()` wiring), after the `Server` value exists, construct: `srv.deployer = deploy.New(srv, deploy.ExecRunner{}, st.DeploysDir())`.
- Factor the per-app launch out of `doStart` into a reusable method and add the `Host` methods:

```go
// launchApp admits one already-converted app into the manager and sets its log
// policy. Shared by doStart and the deployer's Launch.
func (s *Server) launchApp(app config.App) ([]manager.InstanceSnapshot, error) {
	if s.logs != nil {
		s.logs.SetPolicy(app.Name, logPolicy(app, s.logPolicyDefault))
	}
	return s.mgr.Add(app)
}

// --- deploy.Host ---

func (s *Server) Exists(name string) bool {
	for _, sp := range s.mgr.Specs() {
		if sp.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) Source(name string) (config.GitSource, bool) {
	for _, sp := range s.mgr.Specs() {
		if sp.Name == name && sp.Source != nil {
			return *sp.Source, true
		}
	}
	return config.GitSource{}, false
}

func (s *Server) Launch(app config.App) error {
	if _, err := s.launchApp(app); err != nil {
		return err
	}
	if s.store != nil {
		_ = s.store.Save(s.mgr.Specs())
	}
	return nil
}

func (s *Server) Restart(name string) error {
	_, err := s.mgr.Restart(name)
	return err
}

func (s *Server) Writers(label string) (io.Writer, io.Writer) {
	if s.logs == nil {
		return io.Discard, io.Discard
	}
	return s.logs.WriterPair(label)
}
```

Update `doStart`'s loop body to call `s.launchApp(app)` instead of the inline `SetPolicy`+`mgr.Add`. Add imports `"io"`, `"marshal/internal/config"`, `"marshal/internal/deploy"` as needed.

- [ ] **Step 6: Merge deployer snapshots into the heartbeat**

In `internal/daemon/fleet.go`, in `fleetSnapshot()`, after building `procs` from `mgr.List()`, append the synthetic deploy entries:

```go
	if s.deployer != nil {
		for _, dp := range s.deployer.Snapshots() {
			dp := dp
			procs = append(procs, &dp)
		}
	}
```

(Match the existing variable name for the proc slice in that function.)

- [ ] **Step 7: Route Deploy/Redeploy/Delete in command.go (failing test first)**

Add to `internal/daemon/command_test.go`:

```go
func TestHandleFleetCommandDeployAccepts(t *testing.T) {
	s := newCommandTestServer(t) // mirror the helper the other command tests use
	res := s.handleFleetCommand(&pb.Command{Op: &pb.ControlOp{
		Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: &pb.AppSpec{
			Name: "web", Cmd: "./server", Instances: 1,
			Source: &pb.GitSource{Repo: "https://example/r.git"},
		}}},
	}})
	if !res.GetOk() {
		t.Fatalf("deploy should be accepted, got error %q", res.GetError())
	}
}

func TestHandleFleetCommandDeployRejectsNoRepo(t *testing.T) {
	s := newCommandTestServer(t)
	res := s.handleFleetCommand(&pb.Command{Op: &pb.ControlOp{
		Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: &pb.AppSpec{
			Name: "web", Cmd: "./server", Source: &pb.GitSource{},
		}}},
	}})
	if res.GetOk() {
		t.Fatal("deploy with empty repo should be rejected")
	}
}
```

If the existing command tests construct the `Server` differently, reuse their exact constructor. The deployer must be non-nil in the test server — construct it with a temp `deploy.New(s, deploy.ExecRunner{}, t.TempDir())` (the goroutine will fail to clone a fake URL, but `Deploy` returns *accepted* synchronously, which is what these tests assert).

In `internal/daemon/command.go`, add cases to the `switch`:

```go
	case *pb.ControlOp_Deploy:
		app, cerr := appSpecToConfig(v.Deploy.GetApp())
		if cerr != nil {
			return &pb.ControlResult{Ok: false, Error: cerr.Error()}
		}
		if derr := s.deployer.Start(app); derr != nil {
			return &pb.ControlResult{Ok: false, Error: derr.Error()}
		}
		return &pb.ControlResult{Ok: true}

	case *pb.ControlOp_Redeploy:
		if derr := s.deployer.Redeploy(v.Redeploy.GetTarget()); derr != nil {
			return &pb.ControlResult{Ok: false, Error: derr.Error()}
		}
		return &pb.ControlResult{Ok: true}
```

Update the `ControlOp_Delete` case so it also clears any deployer entry, and treats a clear as success even when the manager had no instance (a failed-deploy card):

```go
	case *pb.ControlOp_Delete:
		snaps, err = s.mgr.Delete(v.Delete.GetTarget())
		forgot := false
		if s.deployer != nil {
			forgot = s.deployer.Forget(v.Delete.GetTarget())
		}
		if err != nil && forgot {
			err = nil // the target was a failed/in-flight deploy, now cleared
		}
		if err == nil && s.store != nil {
			_ = s.store.Save(s.mgr.Specs())
		}
```

- [ ] **Step 8: Build, run daemon + manager + deploy tests**

Run: `go build ./... && go test ./internal/daemon/ ./internal/manager/ ./internal/deploy/ -race -count=1`
Expected: build OK, all PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/deploy/exec_runner.go internal/deploy/exec_runner_test.go internal/manager/ internal/daemon/ internal/store/store.go
git commit -m "feat(daemon): wire deployer — routing, snapshot merge, source stamping

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Dashboard HTTP — git source on POST /api/apps + redeploy endpoint

**Files:**
- Modify: `internal/dashboard/apps.go`
- Modify: `internal/dashboard/handlers.go` (register `POST /api/apps/redeploy`)
- Test: `internal/dashboard/apps_test.go` (add cases; mirror existing table)

**Interfaces:**
- Consumes: `pb.ControlOp_Deploy`, `pb.ControlOp_Redeploy`, `pb.DeployRequest`, `pb.RedeployRequest`, `pb.GitSource` (Task 1); the existing `h.controller.Control(ctx, agent, op)`.
- Produces: `gitSource` request struct; `deployOp(agent, gitSource) *pb.ControlOp`; handler `(*handler).redeploy`.

- [ ] **Step 1: Write failing handler tests**

In `internal/dashboard/apps_test.go`, mirror the existing fake-controller test setup and add:

```go
func TestAppsGitSourceSendsDeployOp(t *testing.T) {
	fc := &fakeController{result: &pb.ControlResult{Ok: true}}
	h := newAppsTestHandler(t, fc) // reuse the helper the command-source tests use
	body := `{"agent":"dev-1","source":{"type":"git","name":"web","cmd":"./server","repo":"https://example/r.git","ref":"main","build":"go build -o server ."}}`
	rec := postJSON(t, h, "/api/apps", body)
	if rec.Code != http.StatusOK { // 200 {ok:true}
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	dep, ok := fc.lastOp.GetOp().(*pb.ControlOp_Deploy)
	if !ok {
		t.Fatalf("expected ControlOp_Deploy, got %T", fc.lastOp.GetOp())
	}
	if dep.Deploy.GetApp().GetSource().GetRepo() != "https://example/r.git" {
		t.Fatalf("repo not forwarded: %+v", dep.Deploy.GetApp().GetSource())
	}
}

func TestAppsGitSourceRequiresRepo(t *testing.T) {
	h := newAppsTestHandler(t, &fakeController{result: &pb.ControlResult{Ok: true}})
	body := `{"agent":"dev-1","source":{"type":"git","name":"web","cmd":"./server"}}`
	rec := postJSON(t, h, "/api/apps", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRedeploySendsRedeployOp(t *testing.T) {
	fc := &fakeController{result: &pb.ControlResult{Ok: true}}
	h := newAppsTestHandler(t, fc)
	rec := postJSON(t, h, "/api/apps/redeploy", `{"agent":"dev-1","name":"web"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	rop, ok := fc.lastOp.GetOp().(*pb.ControlOp_Redeploy)
	if !ok || rop.Redeploy.GetTarget() != "web" {
		t.Fatalf("expected redeploy of web, got %+v", fc.lastOp.GetOp())
	}
}
```

(If the existing test file names the fake controller / helpers differently, reuse those exact names — do not introduce a second fake.)

- [ ] **Step 2: Run them, verify they fail**

Run: `go test ./internal/dashboard/ -run 'TestAppsGitSource|TestRedeploy' -v`
Expected: FAIL (git branch + redeploy route absent).

- [ ] **Step 3: Add the git source struct + branch in apps.go**

In `internal/dashboard/apps.go`, extend the request to carry git fields. Replace the single `commandSource` decode with a two-stage decode keyed on `type`. Add a `gitSource` struct:

```go
type gitSource struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	Cmd    string `json:"cmd"`
	Args   []string `json:"args"`
	Instances int32 `json:"instances"`
	Env    map[string]string `json:"env"`
	Restart string `json:"restart"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Build  string `json:"build"`
	Subdir string `json:"subdir"`
}
```

In `apps`, after decoding `body.Agent` and reading `body.Source.Type`, branch:

```go
	switch body.Source.Type {
	case "command":
		// ... existing command path unchanged ...
	case "git":
		var g gitSource
		// re-decode the source object into gitSource
		raw, _ := json.Marshal(body.SourceRaw) // see note
		_ = json.Unmarshal(raw, &g)
		if g.Name == "" || g.Repo == "" {
			http.Error(w, "name and repo required", http.StatusBadRequest)
			return
		}
		op := deployOp(g)
		// forward via h.controller.Control exactly like the command path,
		// reusing the same error mapping (401/502/400/ok:false), returning
		// 200 {ok:true} on accept.
	default:
		http.Error(w, "unsupported source type", http.StatusBadRequest)
		return
	}
```

Simplest concrete approach to avoid a second decode: change `addAppRequest.Source` to `json.RawMessage` and unmarshal into `commandSource` or `gitSource` after reading a small `{ "type": ... }` probe. Implement it that way:

```go
type addAppRequest struct {
	Agent  string          `json:"agent"`
	Source json.RawMessage `json:"source"`
}

func (h *handler) apps(w http.ResponseWriter, r *http.Request) {
	var body addAppRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Agent == "" {
		http.Error(w, "agent required", http.StatusBadRequest)
		return
	}
	var probe struct{ Type string `json:"type"` }
	_ = json.Unmarshal(body.Source, &probe)
	var op *pb.ControlOp
	var name string
	switch probe.Type {
	case "command":
		var s commandSource
		if err := json.Unmarshal(body.Source, &s); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if s.Name == "" || s.Cmd == "" {
			http.Error(w, "name and cmd required", http.StatusBadRequest)
			return
		}
		op, name = startOp(s), s.Name
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
		op, name = deployOp(g), g.Name
	default:
		http.Error(w, "unsupported source type", http.StatusBadRequest)
		return
	}
	h.dispatchApp(w, r, body.Agent, name, op) // shared Control + error mapping
}
```

Factor the existing Control call + logging + error mapping out of the old `apps` into `dispatchApp(w, r, agent, name string, op *pb.ControlOp)` so both `apps` and `redeploy` reuse it verbatim. Add `deployOp`:

```go
func deployOp(g gitSource) *pb.ControlOp {
	spec := &pb.AppSpec{
		Name: g.Name, Cmd: g.Cmd, Args: g.Args, Instances: g.Instances,
		Env: g.Env, Restart: g.Restart,
		Source: &pb.GitSource{Repo: g.Repo, Ref: g.Ref, Build: g.Build, Subdir: g.Subdir},
	}
	return &pb.ControlOp{Op: &pb.ControlOp_Deploy{Deploy: &pb.DeployRequest{App: spec}}}
}
```

- [ ] **Step 4: Add the redeploy handler + route**

In `internal/dashboard/apps.go`:

```go
type redeployRequest struct {
	Agent string `json:"agent"`
	Name  string `json:"name"`
}

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
	op := &pb.ControlOp{Op: &pb.ControlOp_Redeploy{Redeploy: &pb.RedeployRequest{Target: body.Name}}}
	h.dispatchApp(w, r, body.Agent, body.Name, op)
}
```

In `internal/dashboard/handlers.go`, after the `POST /api/apps` line, add:

```go
	mux.HandleFunc("POST /api/apps/redeploy", h.requireSession(h.redeploy))
```

- [ ] **Step 5: Run the new tests + the full dashboard package**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS (new + existing, including the original command-source tests after the `dispatchApp` refactor).

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/
git commit -m "feat(dashboard): git source on POST /api/apps + redeploy endpoint

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Web UI — git toggle in AddAppModal, card states, redeploy button, api client

**Files:**
- Modify: `web/src/api.ts`
- Modify: `web/src/AddAppModal.tsx`
- Modify: `web/src/Overview.tsx` (or the card component that renders state + actions)
- Modify: `web/src/styles.css`
- Build artifact: `internal/dashboard/dist/*` (via `make ui`)

**Interfaces:**
- Consumes: `POST /api/apps` git body shape `{agent, source:{type:"git", name, cmd, args, instances, env, restart, repo, ref, build, subdir}}`; `POST /api/apps/redeploy {agent, name}`; `ProcInfo.source` + `ProcInfo.detail` + states `cloning|building|failed` on the fleet poll.
- Produces: `addApp` extended to accept a git source; `redeploy(agent, name)` client fn.

- [ ] **Step 1: Extend the API client**

In `web/src/api.ts`:
- Add a `GitSource` type and a discriminated `CommandSource | GitSource` union for `addApp`'s source param (extend the existing `CommandSource`).

```ts
export interface GitSource {
  type: "git";
  name: string;
  cmd: string;
  args?: string[];
  instances?: number;
  env?: Record<string, string>;
  restart?: string;
  repo: string;
  ref?: string;
  build?: string;
  subdir?: string;
}

export async function redeploy(agent: string, name: string): Promise<{ ok: boolean; error?: string }> {
  const res = await fetch("/api/apps/redeploy", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ agent, name }),
  });
  if (res.status === 401) throw new Error("error 401");
  return res.json();
}
```

Widen `addApp`'s `source` parameter type to `CommandSource | GitSource` (the body shape is unchanged — it already sends `{agent, source}`).

- [ ] **Step 2: Add the Command|Git toggle + git fields to AddAppModal**

In `web/src/AddAppModal.tsx`:
- Add `const [sourceType, setSourceType] = useState<"command" | "git">("command")` and a segmented toggle at the top of the form.
- When `git`: render `repo` (required), `ref` (placeholder "default branch"), `build` (placeholder "auto-detect"), and under advanced a `subdir`. Keep the shared `name`, `instances`, `env`, `restart`, and the start `cmd` (label it "start command" in git mode). Hide the command-only `cwd` field in git mode (cwd is the checkout).
- On submit, build the source object with `type: sourceType` and the matching fields, then call the existing `addApp(agent, source)`.
- Disable submit until: command mode → name+cmd; git mode → name+cmd+repo.

- [ ] **Step 3: Card states + redeploy button**

In the card/Overview component that renders a proc's `state`:
- Render `cloning`/`building` as a live "deploying…" / "building…" label (reuse the existing non-online styling).
- Render `failed` in red, showing `detail`, with the existing delete affordance as the dismiss.
- When `source === "git"` and the app is online, show a **Redeploy** button that calls `redeploy(agent, name)` and relies on the 2s poll to reflect progress.

- [ ] **Step 4: Styles**

In `web/src/styles.css`, add the minimal classes referenced above (e.g. `.state-deploying`, `.state-failed`, `.btn-redeploy`) consistent with the existing Signal styles.

- [ ] **Step 5: Type-check + build the embedded UI**

Run: `make ui`
Expected: `tsc -b` passes with no errors; `internal/dashboard/dist` updates.

- [ ] **Step 6: Commit**

```bash
git add web/src/ internal/dashboard/dist
git commit -m "feat(web): git deploy in AddAppModal + redeploy button + card states

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Full verification, handoff, live demo

- [ ] **Step 1: Whole-suite gates**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all PASS, vet silent, gofmt prints nothing.

- [ ] **Step 2: Write the handoff**

Create `docs/handoffs/2026-06-19-m21-git-deploy.md` per the CLAUDE.md handoff convention (current state, what changed + why, build/run/test, deferred — note managed-credentials is the next milestone and the file-manager/editor idea is further out — and the concrete next step).

- [ ] **Step 3: Live demo**

Follow the CLAUDE.md live-demo convention on ports `:9000`/`:9001` (scratch `XDG_DATA_HOME`, auth set while the server is down). Against a connected agent:
deploy a small public repo with an explicit build command; deploy another relying on auto-detect; force a build failure and confirm the output shows in the dashboard log view and the card reads `failed`; redeploy a deployed app and confirm it restarts with a new commit. Report observations. Tear down (stop agent/server/Vite, remove scratch dir) and confirm `pgrep -fl marshal` shows no demo orphans.

- [ ] **Step 4: Commit the handoff**

```bash
git add docs/handoffs/2026-06-19-m21-git-deploy.md
git commit -m "docs: M21 handoff — deploy an app from git

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to merge `m21-git-deploy` into `main` (local `--no-ff`, no git remote).

---

## Self-review notes

- **Spec coverage:** async deploy + status (Tasks 4–7), build (Task 3 + 5), redeploy/swap-on-success (Task 6), host-cred auth (no secret code — satisfied by ExecRunner inheriting env in Task 7), full build output to per-app log (`Writers("<name>#0")` in Task 5 + `s.logs.WriterPair` in Task 7), persistence/restart (config.App.Source in Task 2; never-persist-failed is inherent — only `Launch` saves), delete clears deployer (Task 7 Step 7), proto contract (Task 1), dashboard endpoints + UI (Tasks 8–9), testing + demo (Task 10).
- **Type consistency:** `Host`/`Runner`/`Deployer` signatures defined in Task 4 are used unchanged in Tasks 5–7; `deployOp`/`dispatchApp`/`gitSource` defined and consumed within Task 8; `InstanceSnapshot.Source` (Task 7 Step 2) consumed by `snapshotToProc` (Task 7 Step 3).
- **Note for implementer:** the daemon/manager/dashboard test helpers (`newCommandTestServer`, `newAppsTestHandler`, `fakeController`, `postJSON`, `newTestManager`) are illustrative names — reuse whatever the existing test files in each package already provide rather than creating duplicates.
