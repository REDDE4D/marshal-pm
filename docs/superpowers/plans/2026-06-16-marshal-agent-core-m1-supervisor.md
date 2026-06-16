# Marshal Agent Core — Milestone 1: Foreground Supervisor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `marshal run <marshal.yaml>` — a foreground supervisor that starts one or more apps (with N fork-mode instances each), keeps them alive per their restart policy with exponential backoff, and gracefully stops them on Ctrl-C.

**Architecture:** Pure-Go, no external services. A `config` package parses `marshal.yaml`. A `proc` package wraps `os/exec` to spawn one OS process with injected env. A `supervisor.Instance` owns the restart state machine for a single process. A `manager.Manager` fans an app definition out into N instances and runs many apps concurrently. `cmd/marshal` wires them behind a Cobra `run` command with SIGINT/SIGTERM handling. Later milestones (daemon, gRPC, logs-to-file, metrics, boot startup) build on these packages without rewriting them.

**Tech Stack:** Go 1.22+, `gopkg.in/yaml.v3` (config), `github.com/spf13/cobra` (CLI), `context` + `os/exec` + `os/signal` (supervision). Tests use Go's standard `testing` plus real child processes via `sh -c`.

**Scope note:** This milestone deliberately has **no daemon, no socket, no gRPC, no log files, no metrics history, no boot integration** — those are milestones M2–M4. Logs in M1 inherit the terminal's stdout/stderr. Platform: Linux + macOS.

**Module path:** Use `module marshal` (a local module path). Internal imports are therefore `marshal/internal/...`. When the project is published, rename the module path once in `go.mod` and update imports.

---

## File Structure

| Path | Responsibility |
|------|----------------|
| `go.mod`, `go.sum` | Module definition and deps. |
| `internal/config/config.go` | `marshal.yaml` types, custom `Duration`, `Load`, `ApplyDefaults`, `Validate`. |
| `internal/config/config_test.go` | Config parsing/defaults/validation tests. |
| `internal/proc/proc.go` | `Spec` + `Process`: spawn one OS process, inject env + `MARSHAL_INSTANCE_ID`, signal/wait/kill. |
| `internal/proc/proc_test.go` | Process spawn/exit/signal tests using `sh -c`. |
| `internal/supervisor/state.go` | `State` enum. |
| `internal/supervisor/backoff.go` | Pure `Backoff(attempt, base, max)` function. |
| `internal/supervisor/backoff_test.go` | Backoff table tests. |
| `internal/supervisor/instance.go` | `Instance`: single-process restart state machine + `Snapshot`. |
| `internal/supervisor/instance_test.go` | Restart-policy / backoff / graceful-stop / errored tests. |
| `internal/manager/manager.go` | `Manager`: fan app into N instances, run many apps, aggregate snapshots. |
| `internal/manager/manager_test.go` | Multi-instance / multi-app tests. |
| `cmd/marshal/main.go` | Cobra root + `run` command, signal handling, status print. |
| `cmd/marshal/run_test.go` | End-to-end test of `run` against a temp `marshal.yaml`. |
| `README.md` | Quick start for M1. |

---

## Task 1: Scaffold the Go module

**Files:**
- Create: `go.mod`
- Create: `internal/version/version.go`
- Test: `internal/version/version_test.go`

- [ ] **Step 1: Initialize the module and the first package**

Run:
```bash
cd "/Users/sebastiankuprat/process manager"
go mod init marshal
mkdir -p internal/version cmd/marshal internal/config internal/proc internal/supervisor internal/manager
```

- [ ] **Step 2: Write a failing test**

Create `internal/version/version_test.go`:
```go
package version

import "testing"

func TestString(t *testing.T) {
	if String() == "" {
		t.Fatal("version string must not be empty")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/version/`
Expected: FAIL — `undefined: String`.

- [ ] **Step 4: Implement minimally**

Create `internal/version/version.go`:
```go
// Package version exposes the Marshal build version.
package version

// Version is overridden at build time via -ldflags.
var Version = "0.0.0-dev"

// String returns the current version string.
func String() string { return Version }
```

- [ ] **Step 5: Run it to verify it passes**

Run: `go test ./internal/version/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod internal/version/
git commit -m "chore: scaffold marshal Go module"
```

---

## Task 2: Config parsing, defaults, and validation

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/config/config_test.go`:
```go
package config

import (
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./server
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	app := cfg.Apps[0]
	if app.Instances != 1 {
		t.Errorf("instances default = %d, want 1", app.Instances)
	}
	if app.Restart != RestartAlways {
		t.Errorf("restart default = %q, want always", app.Restart)
	}
	if app.MaxRestarts != 16 {
		t.Errorf("max_restarts default = %d, want 16", app.MaxRestarts)
	}
	if app.KillTimeout.Duration != 5*time.Second {
		t.Errorf("kill_timeout default = %v, want 5s", app.KillTimeout.Duration)
	}
}

func TestParseDuration(t *testing.T) {
	cfg, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./server
    kill_timeout: 12s
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Apps[0].KillTimeout.Duration; got != 12*time.Second {
		t.Errorf("kill_timeout = %v, want 12s", got)
	}
}

func TestValidateRejectsMissingName(t *testing.T) {
	_, err := Parse([]byte(`
apps:
  - cmd: ./server
`))
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidateRejectsDuplicateName(t *testing.T) {
	_, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./a
  - name: api
    cmd: ./b
`))
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestValidateRejectsBadRestartMode(t *testing.T) {
	_, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./server
    restart: sometimes
`))
	if err == nil {
		t.Fatal("expected error for bad restart mode")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 3: Implement the config package**

Create `internal/config/config.go`:
```go
// Package config loads and validates marshal.yaml app definitions.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// RestartMode controls when an exited process is restarted.
type RestartMode string

const (
	RestartAlways    RestartMode = "always"
	RestartOnFailure RestartMode = "on-failure"
	RestartNo        RestartMode = "no"
)

// Duration is a time.Duration that unmarshals from a string like "5s".
type Duration struct{ time.Duration }

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// App is one supervised application definition.
type App struct {
	Name        string            `yaml:"name"`
	Cmd         string            `yaml:"cmd"`
	Args        []string          `yaml:"args"`
	Cwd         string            `yaml:"cwd"`
	Instances   int               `yaml:"instances"`
	Env         map[string]string `yaml:"env"`
	Restart     RestartMode       `yaml:"restart"`
	MaxRestarts int               `yaml:"max_restarts"`
	KillTimeout Duration          `yaml:"kill_timeout"`
}

// Config is the top-level marshal.yaml document.
type Config struct {
	Apps []App `yaml:"apps"`
}

// Load reads and parses a marshal.yaml file from disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes, applies defaults, and validates.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Apps {
		a := &c.Apps[i]
		if a.Instances == 0 {
			a.Instances = 1
		}
		if a.Restart == "" {
			a.Restart = RestartAlways
		}
		if a.MaxRestarts == 0 {
			a.MaxRestarts = 16
		}
		if a.KillTimeout.Duration == 0 {
			a.KillTimeout.Duration = 5 * time.Second
		}
	}
}

func (c *Config) validate() error {
	if len(c.Apps) == 0 {
		return fmt.Errorf("config has no apps")
	}
	seen := map[string]bool{}
	for _, a := range c.Apps {
		if a.Name == "" {
			return fmt.Errorf("app with cmd %q has no name", a.Cmd)
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate app name %q", a.Name)
		}
		seen[a.Name] = true
		if a.Cmd == "" {
			return fmt.Errorf("app %q has no cmd", a.Name)
		}
		switch a.Restart {
		case RestartAlways, RestartOnFailure, RestartNo:
		default:
			return fmt.Errorf("app %q has invalid restart mode %q", a.Name, a.Restart)
		}
		if a.Instances < 1 {
			return fmt.Errorf("app %q has invalid instances %d", a.Name, a.Instances)
		}
	}
	return nil
}
```

- [ ] **Step 4: Add the dependency and run tests**

Run:
```bash
go get gopkg.in/yaml.v3@v3.0.1
go test ./internal/config/
```
Expected: PASS (all five tests).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/config/
git commit -m "feat: marshal.yaml config parsing with defaults and validation"
```

---

## Task 3: Process spawning (`proc`)

**Files:**
- Create: `internal/proc/proc.go`
- Test: `internal/proc/proc_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/proc/proc_test.go`:
```go
package proc

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestStartAndWaitSuccess(t *testing.T) {
	p, err := Start(Spec{Cmd: "sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if p.Pid() <= 0 {
		t.Fatalf("pid = %d, want > 0", p.Pid())
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait returned error for exit 0: %v", err)
	}
}

func TestWaitReportsFailure(t *testing.T) {
	p, err := Start(Spec{Cmd: "sh", Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Wait(); err == nil {
		t.Fatal("wait returned nil for exit 3, want error")
	}
}

func TestInstanceIDInjected(t *testing.T) {
	// Child writes its instance id to a temp file we then read.
	f, err := os.CreateTemp(t.TempDir(), "iid")
	if err != nil {
		t.Fatal(err)
	}
	p, err := Start(Spec{
		Cmd:        "sh",
		Args:       []string{"-c", "printf %s \"$MARSHAL_INSTANCE_ID\" > " + f.Name()},
		InstanceID: 2,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	data, _ := os.ReadFile(f.Name())
	if string(data) != "2" {
		t.Fatalf("MARSHAL_INSTANCE_ID = %q, want 2", string(data))
	}
}

func TestSignalStopsProcess(t *testing.T) {
	p, err := Start(Spec{Cmd: "sh", Args: []string{"-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = p.Wait(); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit after SIGTERM")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/proc/`
Expected: FAIL — `undefined: Start` / `undefined: Spec`.

- [ ] **Step 3: Implement `proc`**

Create `internal/proc/proc.go`:
```go
// Package proc spawns and signals a single supervised OS process.
package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Spec describes one process to launch.
type Spec struct {
	Cmd        string
	Args       []string
	Cwd        string
	Env        map[string]string
	InstanceID int
}

// Process is a running OS process.
type Process struct {
	cmd *exec.Cmd
}

// Start launches the process described by spec. Stdout/stderr inherit the
// parent's (file capture arrives in milestone M3).
func Start(spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Cmd, spec.Args...)
	cmd.Dir = spec.Cwd
	cmd.Env = buildEnv(spec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", spec.Cmd, err)
	}
	return &Process{cmd: cmd}, nil
}

func buildEnv(spec Spec) []string {
	env := os.Environ()
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, "MARSHAL_INSTANCE_ID="+strconv.Itoa(spec.InstanceID))
	return env
}

// Pid returns the OS process id.
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Wait blocks until the process exits, returning a non-nil error on non-zero exit.
func (p *Process) Wait() error { return p.cmd.Wait() }

// Signal sends a signal to the process.
func (p *Process) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }

// Kill force-kills the process.
func (p *Process) Kill() error { return p.cmd.Process.Kill() }
```

- [ ] **Step 4: Run to verify passing**

Run: `go test ./internal/proc/`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/proc/
git commit -m "feat: proc package to spawn and signal one process"
```

---

## Task 4: Backoff function

**Files:**
- Create: `internal/supervisor/backoff.go`
- Test: `internal/supervisor/backoff_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/supervisor/backoff_test.go`:
```go
package supervisor

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	base := 100 * time.Millisecond
	max := 15 * time.Second
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 100 * time.Millisecond},
		{1, 200 * time.Millisecond},
		{2, 400 * time.Millisecond},
		{3, 800 * time.Millisecond},
		{20, 15 * time.Second}, // capped
	}
	for _, c := range cases {
		if got := Backoff(c.attempt, base, max); got != c.want {
			t.Errorf("Backoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/supervisor/ -run TestBackoff`
Expected: FAIL — `undefined: Backoff`.

- [ ] **Step 3: Implement backoff**

Create `internal/supervisor/backoff.go`:
```go
package supervisor

import "time"

// Backoff returns base*2^attempt, capped at max. attempt is zero-based.
func Backoff(attempt int, base, max time.Duration) time.Duration {
	d := base
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}
```

- [ ] **Step 4: Run to verify passing**

Run: `go test ./internal/supervisor/ -run TestBackoff`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/supervisor/backoff.go internal/supervisor/backoff_test.go
git commit -m "feat: exponential backoff helper"
```

---

## Task 5: Supervisor state enum

**Files:**
- Create: `internal/supervisor/state.go`

- [ ] **Step 1: Implement the state enum (no test — pure constants)**

Create `internal/supervisor/state.go`:
```go
package supervisor

// State is the lifecycle state of a supervised instance.
type State string

const (
	StateStarting   State = "starting"
	StateOnline     State = "online"
	StateStopping   State = "stopping"
	StateStopped    State = "stopped"
	StateRestarting State = "restarting"
	StateErrored    State = "errored"
)
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/supervisor/`
Expected: no output (success).

- [ ] **Step 3: Commit**

```bash
git add internal/supervisor/state.go
git commit -m "feat: supervisor state enum"
```

---

## Task 6: Instance supervisor — restart state machine

**Files:**
- Create: `internal/supervisor/instance.go`
- Test: `internal/supervisor/instance_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/supervisor/instance_test.go`:
```go
package supervisor

import (
	"context"
	"sync"
	"testing"
	"time"

	"marshal/internal/config"
	"marshal/internal/proc"
)

func testPolicy(mode config.RestartMode) Policy {
	return Policy{
		Mode:        mode,
		MinUptime:   500 * time.Millisecond,
		MaxRestarts: 3,
		BaseBackoff: 10 * time.Millisecond,
		MaxBackoff:  50 * time.Millisecond,
		KillTimeout: time.Second,
	}
}

// runInstance runs i.Run in a goroutine and returns a cancel + wait closure.
func runInstance(i *Instance) (cancel func(), wait func()) {
	ctx, c := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); i.Run(ctx) }()
	return c, wg.Wait
}

func TestInstanceOnlineThenStop(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "sleep 30"}}, testPolicy(config.RestartAlways))
	cancel, wait := runInstance(i)
	time.Sleep(200 * time.Millisecond)
	if got := i.Snapshot().State; got != StateOnline {
		t.Fatalf("state = %q, want online", got)
	}
	cancel()
	wait()
	if got := i.Snapshot().State; got != StateStopped {
		t.Fatalf("state after cancel = %q, want stopped", got)
	}
}

func TestInstanceOnFailureDoesNotRestartOnCleanExit(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 0"}}, testPolicy(config.RestartOnFailure))
	_, wait := runInstance(i)
	wait() // returns once the instance stops itself
	if got := i.Snapshot().Restarts; got != 0 {
		t.Fatalf("restarts = %d, want 0 (clean exit, on-failure)", got)
	}
	if got := i.Snapshot().State; got != StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
}

func TestInstanceErroredAfterMaxRestarts(t *testing.T) {
	// Fast-crashing process: exits immediately, always restarts, hits the cap.
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 1"}}, testPolicy(config.RestartAlways))
	_, wait := runInstance(i)
	wait()
	if got := i.Snapshot().State; got != StateErrored {
		t.Fatalf("state = %q, want errored", got)
	}
}

func TestInstanceGracefulStopFallsBackToKill(t *testing.T) {
	// Ignores SIGTERM, so the KillTimeout path must SIGKILL it.
	i := NewInstance(
		proc.Spec{Cmd: "sh", Args: []string{"-c", "trap '' TERM; while true; do sleep 0.1; done"}},
		testPolicy(config.RestartAlways),
	)
	cancel, wait := runInstance(i)
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	cancel()
	wait()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("graceful stop took %v, expected kill near KillTimeout (1s)", elapsed)
	}
	if got := i.Snapshot().State; got != StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/supervisor/ -run TestInstance`
Expected: FAIL — `undefined: NewInstance` / `undefined: Policy` / `undefined: Instance`.

- [ ] **Step 3: Implement the instance supervisor**

Create `internal/supervisor/instance.go`:
```go
package supervisor

import (
	"context"
	"sync"
	"syscall"
	"time"

	"marshal/internal/config"
	"marshal/internal/proc"
)

// Policy is the resolved restart policy for one instance.
type Policy struct {
	Mode        config.RestartMode
	MinUptime   time.Duration
	MaxRestarts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	KillTimeout time.Duration
}

// Snapshot is a point-in-time view of an instance.
type Snapshot struct {
	State     State
	Pid       int
	Restarts  int
	StartedAt time.Time
}

// Instance supervises a single OS process, restarting it per Policy.
type Instance struct {
	spec   proc.Spec
	policy Policy

	mu        sync.Mutex
	state     State
	pid       int
	restarts  int   // total restarts
	unstable  int   // consecutive sub-MinUptime restarts
	startedAt time.Time
}

// NewInstance builds an instance supervisor. Call Run to start it.
func NewInstance(spec proc.Spec, policy Policy) *Instance {
	return &Instance{spec: spec, policy: policy, state: StateStarting}
}

// Snapshot returns the current observable state.
func (i *Instance) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	return Snapshot{State: i.state, Pid: i.pid, Restarts: i.restarts, StartedAt: i.startedAt}
}

func (i *Instance) set(state State, pid int, startedAt time.Time) {
	i.mu.Lock()
	i.state = state
	if pid >= 0 {
		i.pid = pid
	}
	if !startedAt.IsZero() {
		i.startedAt = startedAt
	}
	i.mu.Unlock()
}

// Run supervises the process until ctx is canceled, then gracefully stops it.
func (i *Instance) Run(ctx context.Context) {
	for {
		p, err := proc.Start(i.spec)
		if err != nil {
			// Treat spawn failure like a crash for restart accounting.
			if !i.handleExit(ctx, time.Now(), true) {
				return
			}
			continue
		}
		started := time.Now()
		i.set(StateOnline, p.Pid(), started)

		exited := make(chan error, 1)
		go func() { exited <- p.Wait() }()

		select {
		case <-ctx.Done():
			i.stop(p, exited)
			i.set(StateStopped, -1, time.Time{})
			return
		case waitErr := <-exited:
			failed := waitErr != nil
			if !i.handleExit(ctx, started, failed) {
				return
			}
		}
	}
}

// handleExit decides whether to restart. Returns false to terminate Run.
func (i *Instance) handleExit(ctx context.Context, started time.Time, failed bool) bool {
	if ctx.Err() != nil {
		i.set(StateStopped, -1, time.Time{})
		return false
	}

	// Should we restart at all?
	restart := false
	switch i.policy.Mode {
	case config.RestartAlways:
		restart = true
	case config.RestartOnFailure:
		restart = failed
	case config.RestartNo:
		restart = false
	}
	if !restart {
		if failed {
			i.set(StateErrored, -1, time.Time{})
		} else {
			i.set(StateStopped, -1, time.Time{})
		}
		return false
	}

	// Stability accounting.
	uptime := time.Since(started)
	i.mu.Lock()
	i.restarts++
	if uptime < i.policy.MinUptime {
		i.unstable++
	} else {
		i.unstable = 0
	}
	unstable := i.unstable
	i.mu.Unlock()

	if unstable > i.policy.MaxRestarts {
		i.set(StateErrored, -1, time.Time{})
		return false
	}

	i.set(StateRestarting, -1, time.Time{})
	delay := Backoff(unstable, i.policy.BaseBackoff, i.policy.MaxBackoff)
	select {
	case <-ctx.Done():
		i.set(StateStopped, -1, time.Time{})
		return false
	case <-time.After(delay):
		return true
	}
}

// stop sends SIGTERM, then SIGKILL after KillTimeout. It waits on the existing
// exited channel (fed by Run's single p.Wait goroutine) rather than calling
// p.Wait again — os/exec.Wait is not safe to call twice.
func (i *Instance) stop(p *proc.Process, exited <-chan error) {
	i.set(StateStopping, -1, time.Time{})
	_ = p.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(i.policy.KillTimeout):
		_ = p.Kill()
		<-exited
	}
}
```

- [ ] **Step 4: Run to verify passing**

Run: `go test ./internal/supervisor/ -run TestInstance -v`
Expected: PASS for all four `TestInstance*` tests. (These take a few seconds due to real sleeps.)

- [ ] **Step 5: Run the whole package**

Run: `go test ./internal/supervisor/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/supervisor/instance.go internal/supervisor/instance_test.go
git commit -m "feat: instance supervisor with restart policy and backoff"
```

---

## Task 7: Manager — fan apps into instances

**Files:**
- Create: `internal/manager/manager.go`
- Test: `internal/manager/manager_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/manager/manager_test.go`:
```go
package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"marshal/internal/config"
	"marshal/internal/supervisor"
)

func TestManagerRunsAllInstances(t *testing.T) {
	cfg := &config.Config{Apps: []config.App{
		{Name: "a", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 2,
			Restart: config.RestartAlways, MaxRestarts: 3,
			KillTimeout: config.Duration{Duration: time.Second}},
		{Name: "b", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 1,
			Restart: config.RestartAlways, MaxRestarts: 3,
			KillTimeout: config.Duration{Duration: time.Second}},
	}}

	m := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	snaps := m.Snapshot()
	if len(snaps) != 3 {
		t.Fatalf("got %d instances, want 3", len(snaps))
	}
	online := 0
	for _, s := range snaps {
		if s.State == supervisor.StateOnline {
			online++
		}
	}
	if online != 3 {
		t.Fatalf("online = %d, want 3", online)
	}
	cancel()
	wg.Wait()
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/manager/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement the manager**

Create `internal/manager/manager.go`:
```go
// Package manager runs a whole marshal.yaml: each app fanned into N instances.
package manager

import (
	"context"
	"fmt"
	"sync"

	"marshal/internal/config"
	"marshal/internal/proc"
	"marshal/internal/supervisor"
)

// entry pairs an instance with its display label.
type entry struct {
	label    string
	instance *supervisor.Instance
}

// InstanceSnapshot is a labeled view of one supervised instance.
type InstanceSnapshot struct {
	Label string
	supervisor.Snapshot
}

// Manager supervises every instance of every app in a config.
type Manager struct {
	entries []entry
}

// New builds a Manager from a validated config.
func New(cfg *config.Config) *Manager {
	var entries []entry
	for _, app := range cfg.Apps {
		policy := supervisor.Policy{
			Mode:        app.Restart,
			MinUptime:   1000_000_000, // 1s in ns; see note below
			MaxRestarts: app.MaxRestarts,
			BaseBackoff: 100_000_000,  // 100ms
			MaxBackoff:  15_000_000_000, // 15s
			KillTimeout: app.KillTimeout.Duration,
		}
		for idx := 0; idx < app.Instances; idx++ {
			spec := proc.Spec{
				Cmd:        app.Cmd,
				Args:       app.Args,
				Cwd:        app.Cwd,
				Env:        app.Env,
				InstanceID: idx,
			}
			entries = append(entries, entry{
				label:    fmt.Sprintf("%s#%d", app.Name, idx),
				instance: supervisor.NewInstance(spec, policy),
			})
		}
	}
	return &Manager{entries: entries}
}

// Run starts all instances and blocks until ctx is canceled and all stop.
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, e := range m.entries {
		wg.Add(1)
		go func(in *supervisor.Instance) {
			defer wg.Done()
			in.Run(ctx)
		}(e.instance)
	}
	wg.Wait()
}

// Snapshot returns a labeled snapshot of every instance.
func (m *Manager) Snapshot() []InstanceSnapshot {
	out := make([]InstanceSnapshot, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, InstanceSnapshot{Label: e.label, Snapshot: e.instance.Snapshot()})
	}
	return out
}
```

> **Implementer note on the magic numbers:** the `MinUptime`/`BaseBackoff`/`MaxBackoff` literals are the spec defaults (1s / 100ms / 15s) written in nanoseconds to avoid importing `time` here. Prefer readability: add `import "time"` and use `time.Second`, `100*time.Millisecond`, `15*time.Second` instead. Replace the three literals during implementation.

- [ ] **Step 4: Run to verify passing**

Run: `go test ./internal/manager/`
Expected: PASS.

- [ ] **Step 5: Refactor magic numbers to `time` constants, re-run**

Edit `New` to `import "time"` and use `time.Second`, `100 * time.Millisecond`, `15 * time.Second`.
Run: `go test ./internal/manager/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/manager/
git commit -m "feat: manager fans apps into N supervised instances"
```

---

## Task 8: CLI `marshal run`

**Files:**
- Create: `cmd/marshal/main.go`
- Test: `cmd/marshal/run_test.go`

- [ ] **Step 1: Write a failing end-to-end test**

Create `cmd/marshal/run_test.go`:
```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunSupervisesAndStops builds the marshal binary, runs a config with a
// short-lived app, sends SIGTERM, and asserts a clean shutdown.
func TestRunSupervisesAndStops(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "marshal")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(dir, "marshal.yaml")
	cfg := `
apps:
  - name: hello
    cmd: sh
    args: ["-c", "echo started; sleep 30"]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "run", cfgPath)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	time.Sleep(1 * time.Second)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("marshal run did not exit after SIGINT")
	}

	if !strings.Contains(out.String(), "started") {
		t.Fatalf("expected child output 'started', got:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/marshal/`
Expected: FAIL — build error (no `main`) or missing `run` command.

- [ ] **Step 3: Implement the CLI**

Create `cmd/marshal/main.go`:
```go
// Command marshal is the foreground supervisor CLI (milestone M1).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/config"
	"marshal/internal/manager"
	"marshal/internal/version"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "marshal",
		Short:   "Marshal — a free process supervisor",
		Version: version.String(),
	}
	root.AddCommand(runCmd())
	return root
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <marshal.yaml>",
		Short: "Run and supervise apps in the foreground until interrupted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			m := manager.New(cfg)

			// Periodic status line until shutdown.
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						printStatus(cmd, m)
					}
				}
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "marshal: supervising %d app(s); press Ctrl-C to stop\n", len(cfg.Apps))
			m.Run(ctx) // blocks until ctx canceled and all instances stop
			fmt.Fprintln(cmd.OutOrStdout(), "marshal: all processes stopped")
			return nil
		},
	}
}

func printStatus(cmd *cobra.Command, m *manager.Manager) {
	for _, s := range m.Snapshot() {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %-10s pid=%d restarts=%d\n",
			s.Label, s.State, s.Pid, s.Restarts)
	}
}
```

- [ ] **Step 4: Add cobra and run the test**

Run:
```bash
go get github.com/spf13/cobra@latest
go test ./cmd/marshal/
```
Expected: PASS.

- [ ] **Step 5: Manual smoke test**

Run:
```bash
go build -o /tmp/marshal ./cmd/marshal
printf 'apps:\n  - name: clock\n    cmd: sh\n    args: ["-c", "while true; do date; sleep 1; done"]\n' > /tmp/marshal.yaml
/tmp/marshal run /tmp/marshal.yaml
```
Expected: prints the date every second plus a status line every 2s; Ctrl-C stops cleanly with "all processes stopped".

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/ go.mod go.sum
git commit -m "feat: marshal run command — foreground supervisor CLI"
```

---

## Task 9: Full build, vet, and README

**Files:**
- Create: `README.md`

- [ ] **Step 1: Run the whole test suite and vet**

Run:
```bash
go build ./...
go vet ./...
go test ./...
```
Expected: all PASS, no vet warnings.

- [ ] **Step 2: Write the README**

Create `README.md`:
```markdown
# Marshal

A free, self-hosted process manager — an alternative to PM2 (and the paywalled
PM2 Plus insights) that supervises any kind of OS process.

## Status

Milestone M1: foreground supervisor. Run apps defined in a `marshal.yaml`, with
restart policies, exponential backoff, and N fork-mode instances per app.

Daemon mode, the `marshal` CLI control surface, a central fleet server, log
files, metrics history, and a web dashboard are planned (see
`docs/superpowers/specs/` and `docs/superpowers/plans/`).

## Build

```bash
go build -o marshal ./cmd/marshal
```

## Usage

```yaml
# marshal.yaml
apps:
  - name: api
    cmd: ./server
    args: ["--port", "8080"]
    instances: 2          # fork mode; each gets MARSHAL_INSTANCE_ID
    restart: on-failure   # always | on-failure | no
    max_restarts: 16
    kill_timeout: 5s
```

```bash
marshal run marshal.yaml   # supervise in the foreground; Ctrl-C to stop
```

## License

MIT (or Apache-2.0) — to be finalized.
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: M1 README"
```

---

## Self-Review Notes (for the implementer)

- **Spec coverage (M1 slice):** process model & restart policy (Tasks 4–6), fork-mode instances + `MARSHAL_INSTANCE_ID` (Tasks 3, 7), `marshal.yaml` + defaults + validation (Task 2), graceful stop → kill (Task 6), foreground run command (Task 8). Daemon/socket/gRPC, dump/resurrect, logs-to-file, metrics history, and boot startup are **intentionally deferred** to M2–M4.
- **Graceful-stop correctness:** `stop()` waits on the single `exited` channel rather than calling `p.Wait()` twice (os/exec forbids that). `TestInstanceGracefulStopFallsBackToKill` is the guard for the SIGTERM→SIGKILL fallback path.
- **Timing tests:** several tests use real sleeps and are inherently a few seconds each. If running under heavy CI load they may need their sleeps widened; keep the ratios (MinUptime < observation window) intact.
```
