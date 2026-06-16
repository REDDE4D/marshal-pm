# Marshal M2 — Daemon + Control CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the M1 foreground supervisor into a long-lived `marshald` daemon that owns all processes, driven by a thin `marshal` gRPC client over a Unix socket — with full process lifecycle, daemon auto-spawn, and save/resurrect.

**Architecture:** A single gRPC service (`Daemon`) over a Unix domain socket under `~/.marshal/` (or `$XDG_DATA_HOME/marshal/`). The daemon wraps a refactored, **dynamic** `manager.Manager` (runtime add/stop/restart/delete). The CLI dials the socket and auto-spawns the daemon when it is not running. Persistence is a `dump.json` of app definitions. Logs and metrics are deliberately deferred to M3 (the proto reserves them).

**Tech Stack:** Go 1.26, `google.golang.org/grpc` + `google.golang.org/protobuf` (protoc codegen committed under `internal/pb`), `spf13/cobra`, `gopkg.in/yaml.v3`. Reuses M1 packages `config`, `proc`, `supervisor`, `manager`.

**Spec:** `docs/superpowers/specs/2026-06-16-marshal-agent-core-m2-daemon-design.md`

---

## File structure

**New:**
- `proto/marshal/v1/daemon.proto` — the gRPC contract (service + messages).
- `internal/pb/daemon.pb.go`, `internal/pb/daemon_grpc.pb.go` — generated (do-not-edit, committed).
- `internal/pb/doc.go` — `go:generate` directive + package doc.
- `internal/store/store.go` — state-dir layout + `dump.json` read/write.
- `internal/store/store_test.go`.
- `internal/daemon/convert.go` — AppSpec↔config.App and snapshot→ProcInfo conversions.
- `internal/daemon/server.go` — `Daemon` service implementation + socket serve loop.
- `internal/daemon/server_test.go`.
- `internal/client/client.go` — dial + daemon auto-spawn.
- `cmd/marshal/daemon.go` — the `marshal daemon` command (foreground server).
- `cmd/marshal/control.go` — the lifecycle commands (start/stop/restart/delete/list/describe/save/resurrect/kill).

**Modified:**
- `internal/config/config.go` — add `Duration` JSON (un)marshaling + exported `Prepare()`.
- `internal/manager/manager.go` — refactor to a dynamic manager (runtime mutation API).
- `internal/manager/manager_test.go` — rewrite for the new API.
- `cmd/marshal/main.go` — register new commands; rework `run` onto the new manager API.
- `go.mod` / `go.sum` — add grpc + protobuf.

---

## Task 1: gRPC toolchain, dependencies, proto, and generated code

**Files:**
- Create: `proto/marshal/v1/daemon.proto`
- Create: `internal/pb/doc.go`
- Generate: `internal/pb/daemon.pb.go`, `internal/pb/daemon_grpc.pb.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Install the protobuf toolchain**

Run (macOS, Homebrew):
```bash
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```
Ensure `$(go env GOPATH)/bin` is on `PATH` (the two plugins install there).
Verify:
```bash
protoc --version            # libprotoc 3.x or newer
protoc-gen-go --version
protoc-gen-go-grpc --version
```
Expected: all three print a version (no "command not found").

- [ ] **Step 2: Add the Go gRPC dependencies**

Run:
```bash
cd "/Users/sebastiankuprat/process manager"
go get google.golang.org/grpc@latest
go get google.golang.org/protobuf@latest
```
Expected: `go.mod` now lists `google.golang.org/grpc` and `google.golang.org/protobuf` as direct requires.

- [ ] **Step 3: Write the proto contract**

Create `proto/marshal/v1/daemon.proto`:
```proto
syntax = "proto3";

package marshal.v1;

option go_package = "marshal/internal/pb;pb";

// Daemon is the control surface marshald exposes over the Unix socket.
// M2 implements every RPC except Logs (reserved for M3).
service Daemon {
  rpc Start(StartRequest) returns (ProcList);
  rpc Stop(Selector) returns (ProcList);
  rpc Restart(Selector) returns (ProcList);
  rpc Delete(Selector) returns (ProcList);
  rpc List(Empty) returns (ProcList);
  rpc Describe(Selector) returns (ProcList);
  rpc Save(Empty) returns (Ack);
  rpc Resurrect(Empty) returns (ProcList);
  rpc Kill(Empty) returns (Ack);
  rpc Logs(LogRequest) returns (stream LogLine); // M3 — defined, not implemented
}

message Empty {}

message Ack {
  bool ok = 1;
  string message = 2;
}

// AppSpec mirrors config.App. kill_timeout is a Go duration string ("5s").
message AppSpec {
  string name = 1;
  string cmd = 2;
  repeated string args = 3;
  string cwd = 4;
  int32 instances = 5;
  map<string, string> env = 6;
  string restart = 7; // always | on-failure | no
  int32 max_restarts = 8;
  string kill_timeout = 9;
}

message StartRequest { repeated AppSpec apps = 1; }

// Selector resolves to a name, a numeric id, or the literal "all".
message Selector { string target = 1; }

// ProcInfo mirrors a supervised instance snapshot. cpu/mem are reserved for M3.
message ProcInfo {
  int32 id = 1;
  string name = 2;
  int32 instance_id = 3;
  string state = 4;
  int32 pid = 5;
  int64 uptime_ms = 6;
  int32 restarts = 7;
  double cpu = 8; // M3
  int64 mem = 9;  // M3
}

message ProcList { repeated ProcInfo procs = 1; }

// M3 — log streaming.
message LogRequest {
  string target = 1;
  int32 lines = 2;
  bool follow = 3;
}

message LogLine {
  string name = 1;
  int32 instance_id = 2;
  bool stderr = 3;
  string line = 4;
}
```

- [ ] **Step 4: Add the codegen directive**

Create `internal/pb/doc.go`:
```go
// Package pb holds the generated gRPC/protobuf code for the marshald Daemon
// service. Do not edit the generated *.pb.go files by hand; regenerate with
// `go generate ./internal/pb`.
package pb

//go:generate protoc --go_out=. --go_opt=module=marshal --go-grpc_out=. --go-grpc_opt=module=marshal -I ../../proto ../../proto/marshal/v1/daemon.proto
```

- [ ] **Step 5: Generate the code**

Run:
```bash
cd "/Users/sebastiankuprat/process manager"
go generate ./internal/pb
```
Expected: creates `internal/pb/daemon.pb.go` and `internal/pb/daemon_grpc.pb.go` (no errors).

- [ ] **Step 6: Verify it compiles and tidy**

Run:
```bash
go build ./internal/pb && go mod tidy
```
Expected: builds clean; `go.mod` keeps grpc + protobuf as direct deps.

- [ ] **Step 7: Commit**

```bash
git add proto internal/pb go.mod go.sum
git commit -m "feat(pb): gRPC Daemon contract + generated code

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: config — Duration JSON + exported Prepare

The daemon must (a) JSON-serialize `config.App` into `dump.json` (today `Duration` only has YAML support) and (b) reuse defaults+validation when admitting an `AppSpec`. We add JSON (un)marshaling to `Duration` and expose the existing defaults+validate as `Prepare()`.

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/config_test.go`:
```go
func TestDurationJSONRoundTrip(t *testing.T) {
	type wrap struct {
		KT Duration `json:"kt"`
	}
	in := wrap{KT: Duration{Duration: 7 * time.Second}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"kt":"7s"}` {
		t.Fatalf("got %s, want {\"kt\":\"7s\"}", b)
	}
	var out wrap
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.KT.Duration != 7*time.Second {
		t.Fatalf("got %v, want 7s", out.KT.Duration)
	}
}

func TestPrepareAppliesDefaultsAndValidates(t *testing.T) {
	cfg := &Config{Apps: []App{{Name: "api", Cmd: "./server"}}}
	if err := cfg.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	a := cfg.Apps[0]
	if a.Instances != 1 || a.Restart != RestartAlways || a.MaxRestarts != 16 ||
		a.KillTimeout.Duration != 5*time.Second {
		t.Fatalf("defaults not applied: %+v", a)
	}

	bad := &Config{Apps: []App{{Name: "x"}}} // no cmd
	if err := bad.Prepare(); err == nil {
		t.Fatal("Prepare: want error for missing cmd")
	}
}
```
Add `"encoding/json"` to the test file's imports (keep `"time"`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestDurationJSON|TestPrepare' -v`
Expected: FAIL — `Prepare` undefined; JSON marshals `Duration` as an object, not `"7s"`.

- [ ] **Step 3: Implement Duration JSON + Prepare**

In `internal/config/config.go`, add `"strconv"` to the import block. Then add the JSON methods right after the existing `UnmarshalYAML` method:
```go
// MarshalJSON renders the duration as a Go duration string (e.g. "5s").
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(d.Duration.String())), nil
}

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}
```
Add the exported `Prepare` and rewire `Parse` to use it. Replace the body of `Parse` so it ends with:
```go
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
```
with:
```go
	if err := cfg.Prepare(); err != nil {
		return nil, err
	}
	return &cfg, nil
```
and add the method:
```go
// Prepare applies per-app defaults and validates the config in place.
func (c *Config) Prepare() error {
	c.applyDefaults()
	return c.validate()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all config tests, old and new).

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat(config): Duration JSON (un)marshal and exported Prepare()

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: internal/store — state dir + dump.json

A small, pure package: resolve the state directory and read/write `dump.json`. Tested entirely against a temp directory via `NewAt`.

**Files:**
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/store/store_test.go`:
```go
package store

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/config"
)

func TestPathsUnderBase(t *testing.T) {
	s := NewAt("/tmp/marshal-test")
	if s.SocketPath() != "/tmp/marshal-test/marshald.sock" {
		t.Fatalf("socket = %s", s.SocketPath())
	}
	if s.LogPath() != "/tmp/marshal-test/marshald.log" {
		t.Fatalf("log = %s", s.LogPath())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewAt(filepath.Join(dir, "state"))

	apps := []config.App{{
		Name: "api", Cmd: "./server", Args: []string{"-p", "8080"},
		Instances: 2, Restart: config.RestartOnFailure, MaxRestarts: 16,
		KillTimeout: config.Duration{Duration: 5 * time.Second},
	}}
	if err := s.Save(apps); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" || got[0].Instances != 2 ||
		got[0].KillTimeout.Duration != 5*time.Second {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s := NewAt(t.TempDir())
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d apps, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -v`
Expected: FAIL — package `store` has no buildable Go source.

- [ ] **Step 3: Implement the store**

Create `internal/store/store.go`:
```go
// Package store owns the marshal state directory (~/.marshal or
// $XDG_DATA_HOME/marshal) and the dump.json app snapshot used by save/resurrect.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/config"
)

// Store resolves paths within the state directory.
type Store struct{ base string }

// New resolves the state directory from $XDG_DATA_HOME (preferred) or $HOME.
func New() (*Store, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return &Store{base: filepath.Join(xdg, "marshal")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return &Store{base: filepath.Join(home, ".marshal")}, nil
}

// NewAt builds a store rooted at an explicit base (used in tests).
func NewAt(base string) *Store { return &Store{base: base} }

// Dir is the state directory.
func (s *Store) Dir() string { return s.base }

// SocketPath is the gRPC Unix socket path.
func (s *Store) SocketPath() string { return filepath.Join(s.base, "marshald.sock") }

// LogPath is where an auto-spawned daemon writes stdout/stderr.
func (s *Store) LogPath() string { return filepath.Join(s.base, "marshald.log") }

func (s *Store) dumpPath() string { return filepath.Join(s.base, "dump.json") }

// EnsureDir creates the state directory if it does not exist.
func (s *Store) EnsureDir() error { return os.MkdirAll(s.base, 0o755) }

// Save writes app definitions to dump.json atomically.
func (s *Store) Save(apps []config.App) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(apps, "", "  ")
	if err != nil {
		return fmt.Errorf("encode dump: %w", err)
	}
	tmp := s.dumpPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write dump: %w", err)
	}
	return os.Rename(tmp, s.dumpPath())
}

// Load reads dump.json. A missing file yields an empty slice and no error.
func (s *Store) Load() ([]config.App, error) {
	data, err := os.ReadFile(s.dumpPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dump: %w", err)
	}
	var apps []config.App
	if err := json.Unmarshal(data, &apps); err != nil {
		return nil, fmt.Errorf("decode dump: %w", err)
	}
	return apps, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/
git commit -m "feat(store): state dir layout and dump.json save/load

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: internal/manager — dynamic mutation API

Refactor the manager from "build all from config, Run blocks" to a dynamic model: an empty manager rooted at a context, with `Add/Stop/Restart/Delete/List/Describe/Specs/StopAll`. Each instance gets its own cancel func + done channel. `supervisor.Instance` is one-shot (its `Run` returns after ctx cancel and cannot be re-run), so `Restart` recreates instances from the stored spec.

This replaces `manager.go` and `manager_test.go` wholesale.

**Files:**
- Modify (replace): `internal/manager/manager.go`
- Modify (replace): `internal/manager/manager_test.go`

- [ ] **Step 1: Write the failing tests**

Replace the entire contents of `internal/manager/manager_test.go` with:
```go
package manager

import (
	"context"
	"testing"
	"time"

	"marshal/internal/config"
	"marshal/internal/supervisor"
)

func sleepApp(name string, instances int) config.App {
	return config.App{
		Name: name, Cmd: "sh", Args: []string{"-c", "sleep 30"},
		Instances: instances, Restart: config.RestartAlways, MaxRestarts: 3,
		KillTimeout: config.Duration{Duration: time.Second},
	}
}

// waitOnline polls until want instances report Online or the deadline passes.
func waitOnline(m *Manager, want int) int {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		online := 0
		for _, s := range m.List() {
			if s.State == supervisor.StateOnline {
				online++
			}
		}
		if online >= want {
			return online
		}
		time.Sleep(20 * time.Millisecond)
	}
	online := 0
	for _, s := range m.List() {
		if s.State == supervisor.StateOnline {
			online++
		}
	}
	return online
}

func TestAddFansIntoInstancesAndAssignsID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)

	snaps, err := m.Add(sleepApp("a", 2))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d instances, want 2", len(snaps))
	}
	if snaps[0].ID != 1 || snaps[0].Name != "a" {
		t.Fatalf("unexpected id/name: %+v", snaps[0])
	}
	if got := waitOnline(m, 2); got != 2 {
		t.Fatalf("online = %d, want 2", got)
	}
	m.StopAll()
}

func TestAddDuplicateNameRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := m.Add(sleepApp("a", 1)); err == nil {
		t.Fatal("second Add: want duplicate-name error")
	}
	m.StopAll()
}

func TestStopThenRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 1)

	if _, err := m.Stop("a"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, s := range m.List() {
		if s.State != supervisor.StateStopped {
			t.Fatalf("after Stop state = %s, want stopped", s.State)
		}
	}

	if _, err := m.Restart("a"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := waitOnline(m, 1); got != 1 {
		t.Fatalf("after Restart online = %d, want 1", got)
	}
	m.StopAll()
}

func TestDeleteRemovesApp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 1)
	if _, err := m.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("after Delete List has %d, want 0", len(m.List()))
	}
}

func TestSelectorByIDAndAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := m.Add(sleepApp("b", 1)); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	waitOnline(m, 2)

	byID, err := m.Describe("2")
	if err != nil {
		t.Fatalf("Describe by id: %v", err)
	}
	if len(byID) != 1 || byID[0].Name != "b" {
		t.Fatalf("id=2 resolved to %+v", byID)
	}

	all, err := m.Describe("all")
	if err != nil {
		t.Fatalf("Describe all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all resolved %d, want 2", len(all))
	}

	if _, err := m.Describe("nope"); err == nil {
		t.Fatal("Describe unknown: want error")
	}
	m.StopAll()
}

func TestSpecsReflectsAddedApps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 2)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	specs := m.Specs()
	if len(specs) != 1 || specs[0].Name != "a" || specs[0].Instances != 2 {
		t.Fatalf("Specs = %+v", specs)
	}
	m.StopAll()
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/manager/ -v`
Expected: FAIL to compile — `New` now takes a context; `Add`, `List`, `StopAll`, etc. don't exist.

- [ ] **Step 3: Implement the dynamic manager**

Replace the entire contents of `internal/manager/manager.go` with:
```go
// Package manager owns a set of supervised apps, each fanned into N instances,
// and supports runtime mutation (add/stop/restart/delete) for the daemon.
package manager

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"marshal/internal/config"
	"marshal/internal/proc"
	"marshal/internal/supervisor"
)

// InstanceSnapshot is a labeled view of one supervised instance.
type InstanceSnapshot struct {
	ID         int    // app id (stable, monotonic)
	Name       string // app name
	InstanceID int    // 0..instances-1
	Label      string // "name#idx"
	supervisor.Snapshot
}

// managedInstance is one running (or stopped) instance and its lifecycle handles.
type managedInstance struct {
	instanceID int
	label      string
	inst       *supervisor.Instance
	cancel     context.CancelFunc
	done       chan struct{}
}

// managedApp groups an app's instances with its definition.
type managedApp struct {
	id    int
	name  string
	spec  config.App
	insts []*managedInstance
}

// Manager supervises apps and their instances under a base context.
type Manager struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	apps   []*managedApp
	nextID int
}

// New builds an empty manager rooted at ctx. Instances spawned by Add run until
// ctx is canceled, the manager is StopAll'd, or they are individually stopped.
func New(ctx context.Context) *Manager {
	mctx, cancel := context.WithCancel(ctx)
	return &Manager{ctx: mctx, cancel: cancel}
}

func policyFor(app config.App) supervisor.Policy {
	return supervisor.Policy{
		Mode:        app.Restart,
		MinUptime:   time.Second,
		MaxRestarts: app.MaxRestarts,
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  15 * time.Second,
		KillTimeout: app.KillTimeout.Duration,
	}
}

// startInstance launches one instance goroutine. Caller holds m.mu.
func (m *Manager) startInstance(app config.App, idx int) *managedInstance {
	spec := proc.Spec{Cmd: app.Cmd, Args: app.Args, Cwd: app.Cwd, Env: app.Env, InstanceID: idx}
	inst := supervisor.NewInstance(spec, policyFor(app))
	ictx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.Run(ictx)
	}()
	return &managedInstance{
		instanceID: idx,
		label:      fmt.Sprintf("%s#%d", app.Name, idx),
		inst:       inst,
		cancel:     cancel,
		done:       done,
	}
}

// Add registers a new app (already defaulted/validated) and starts its instances.
func (m *Manager) Add(app config.App) ([]InstanceSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.apps {
		if a.name == app.Name {
			return nil, fmt.Errorf("app %q already exists", app.Name)
		}
	}
	m.nextID++
	ma := &managedApp{id: m.nextID, name: app.Name, spec: app}
	for idx := 0; idx < app.Instances; idx++ {
		ma.insts = append(ma.insts, m.startInstance(app, idx))
	}
	m.apps = append(m.apps, ma)
	return snapshotApp(ma), nil
}

// Stop gracefully stops the selected apps' instances; the apps remain listed.
func (m *Manager) Stop(sel string) ([]InstanceSnapshot, error) {
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	m.mu.Unlock()

	stopInstances(insts)
	return m.Describe(sel)
}

// Restart stops then recreates the selected apps' instances.
func (m *Manager) Restart(sel string) ([]InstanceSnapshot, error) {
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	m.mu.Unlock()

	stopInstances(insts)

	m.mu.Lock()
	for _, a := range apps {
		fresh := make([]*managedInstance, 0, a.spec.Instances)
		for idx := 0; idx < a.spec.Instances; idx++ {
			fresh = append(fresh, m.startInstance(a.spec, idx))
		}
		a.insts = fresh
	}
	m.mu.Unlock()
	return m.Describe(sel)
}

// Delete stops the selected apps and removes them from management.
func (m *Manager) Delete(sel string) ([]InstanceSnapshot, error) {
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	del := make(map[int]bool, len(apps))
	var removed []InstanceSnapshot
	for _, a := range apps {
		del[a.id] = true
		removed = append(removed, snapshotApp(a)...)
	}
	remaining := m.apps[:0:0]
	for _, a := range m.apps {
		if !del[a.id] {
			remaining = append(remaining, a)
		}
	}
	m.apps = remaining
	m.mu.Unlock()

	stopInstances(insts)
	return removed, nil
}

// List snapshots every instance of every app.
func (m *Manager) List() []InstanceSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []InstanceSnapshot
	for _, a := range m.apps {
		out = append(out, snapshotApp(a)...)
	}
	return out
}

// Describe snapshots the instances of the selected apps.
func (m *Manager) Describe(sel string) ([]InstanceSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	apps, err := m.resolve(sel)
	if err != nil {
		return nil, err
	}
	var out []InstanceSnapshot
	for _, a := range apps {
		out = append(out, snapshotApp(a)...)
	}
	return out, nil
}

// Specs returns the current app definitions (for save).
func (m *Manager) Specs() []config.App {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]config.App, 0, len(m.apps))
	for _, a := range m.apps {
		out = append(out, a.spec)
	}
	return out
}

// StopAll gracefully stops every instance (used on daemon shutdown).
func (m *Manager) StopAll() {
	m.mu.Lock()
	insts := collectInstances(m.apps)
	m.mu.Unlock()
	stopInstances(insts)
}

// resolve maps a selector to apps. Caller holds m.mu. "all" -> every app;
// an integer -> id match; otherwise -> name match.
func (m *Manager) resolve(sel string) ([]*managedApp, error) {
	if sel == "all" {
		return append([]*managedApp(nil), m.apps...), nil
	}
	if id, err := strconv.Atoi(sel); err == nil {
		for _, a := range m.apps {
			if a.id == id {
				return []*managedApp{a}, nil
			}
		}
	}
	for _, a := range m.apps {
		if a.name == sel {
			return []*managedApp{a}, nil
		}
	}
	return nil, fmt.Errorf("no app matching %q", sel)
}

func collectInstances(apps []*managedApp) []*managedInstance {
	var insts []*managedInstance
	for _, a := range apps {
		insts = append(insts, a.insts...)
	}
	return insts
}

// stopInstances cancels each instance's context, then waits for all to exit.
func stopInstances(insts []*managedInstance) {
	for _, in := range insts {
		in.cancel()
	}
	for _, in := range insts {
		<-in.done
	}
}

func snapshotApp(a *managedApp) []InstanceSnapshot {
	out := make([]InstanceSnapshot, 0, len(a.insts))
	for _, in := range a.insts {
		out = append(out, InstanceSnapshot{
			ID:         a.id,
			Name:       a.name,
			InstanceID: in.instanceID,
			Label:      in.label,
			Snapshot:   in.inst.Snapshot(),
		})
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/manager/ -race -v`
Expected: PASS (all manager tests, race-clean).

- [ ] **Step 5: Commit**

```bash
git add internal/manager/
git commit -m "refactor(manager): dynamic add/stop/restart/delete API

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

> Note: `cmd/marshal/main.go` still calls the old `manager.New(cfg)` / `m.Run(ctx)` and will not compile until Task 7. That is expected — the build is green per-package; the full `go build ./...` is fixed in Task 7.

---

## Task 5: internal/daemon — conversions + gRPC service + serve loop

**Files:**
- Create: `internal/daemon/convert.go`
- Create: `internal/daemon/server.go`
- Test: `internal/daemon/server_test.go`

- [ ] **Step 1: Write the conversions**

Create `internal/daemon/convert.go`:
```go
package daemon

import (
	"fmt"
	"time"

	"marshal/internal/config"
	"marshal/internal/manager"
	"marshal/internal/pb"
	"marshal/internal/supervisor"
)

// appSpecToConfig converts a wire AppSpec into a defaulted, validated config.App.
func appSpecToConfig(s *pb.AppSpec) (config.App, error) {
	app := config.App{
		Name:        s.GetName(),
		Cmd:         s.GetCmd(),
		Args:        s.GetArgs(),
		Cwd:         s.GetCwd(),
		Instances:   int(s.GetInstances()),
		Env:         s.GetEnv(),
		Restart:     config.RestartMode(s.GetRestart()),
		MaxRestarts: int(s.GetMaxRestarts()),
	}
	if kt := s.GetKillTimeout(); kt != "" {
		d, err := time.ParseDuration(kt)
		if err != nil {
			return config.App{}, fmt.Errorf("invalid kill_timeout %q: %w", kt, err)
		}
		app.KillTimeout = config.Duration{Duration: d}
	}
	cfg := config.Config{Apps: []config.App{app}}
	if err := cfg.Prepare(); err != nil {
		return config.App{}, err
	}
	return cfg.Apps[0], nil
}

// snapshotToProc converts a manager snapshot into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot) *pb.ProcInfo {
	var uptimeMs int64
	if s.State == supervisor.StateOnline && !s.StartedAt.IsZero() {
		uptimeMs = time.Since(s.StartedAt).Milliseconds()
	}
	return &pb.ProcInfo{
		Id:         int32(s.ID),
		Name:       s.Name,
		InstanceId: int32(s.InstanceID),
		State:      string(s.State),
		Pid:        int32(s.Pid),
		UptimeMs:   uptimeMs,
		Restarts:   int32(s.Restarts),
	}
}

func toProcList(snaps []manager.InstanceSnapshot) *pb.ProcList {
	procs := make([]*pb.ProcInfo, 0, len(snaps))
	for _, s := range snaps {
		procs = append(procs, snapshotToProc(s))
	}
	return &pb.ProcList{Procs: procs}
}
```

- [ ] **Step 2: Write the failing service tests**

Create `internal/daemon/server_test.go`:
```go
package daemon

import (
	"context"
	"testing"

	"marshal/internal/manager"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{mgr: manager.New(ctx)}
	return srv, func() { srv.mgr.StopAll(); cancel() }
}

func sleepSpec(name string, n int32) *pb.AppSpec {
	return &pb.AppSpec{Name: name, Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: n}
}

func TestStartThenList(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	ctx := context.Background()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 2)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	list, err := srv.List(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Procs) != 2 {
		t.Fatalf("got %d procs, want 2", len(list.Procs))
	}
}

func TestStartDuplicateIsAlreadyExists(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	ctx := context.Background()
	req := &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}
	if _, err := srv.Start(ctx, req); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err := srv.Start(ctx, req)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("got %v, want AlreadyExists", err)
	}
}

func TestStopUnknownIsNotFound(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	_, err := srv.Stop(context.Background(), &pb.Selector{Target: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestStartInvalidSpecIsInvalidArgument(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	// Missing cmd fails config validation.
	_, err := srv.Start(context.Background(),
		&pb.StartRequest{Apps: []*pb.AppSpec{{Name: "x"}}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/daemon/ -v`
Expected: FAIL — `Server` undefined.

- [ ] **Step 4: Implement the server + serve loop**

Create `internal/daemon/server.go`:
```go
// Package daemon implements the marshald gRPC Daemon service over a Unix socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"marshal/internal/manager"
	"marshal/internal/pb"
	"marshal/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.DaemonServer backed by a dynamic manager.
type Server struct {
	pb.UnimplementedDaemonServer
	mgr   *manager.Manager
	store *store.Store
	kill  func() // triggers daemon shutdown (set by Run)
}

// Start admits and launches one or more apps.
func (s *Server) Start(_ context.Context, req *pb.StartRequest) (*pb.ProcList, error) {
	var out []manager.InstanceSnapshot
	for _, spec := range req.GetApps() {
		app, err := appSpecToConfig(spec)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		snaps, err := s.mgr.Add(app)
		if err != nil {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		out = append(out, snaps...)
	}
	return toProcList(out), nil
}

func (s *Server) Stop(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Stop, sel)
}

func (s *Server) Restart(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Restart, sel)
}

func (s *Server) Delete(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Delete, sel)
}

func (s *Server) Describe(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Describe, sel)
}

// mutate runs a selector-based manager op, mapping not-found to NotFound.
func (s *Server) mutate(op func(string) ([]manager.InstanceSnapshot, error), sel *pb.Selector) (*pb.ProcList, error) {
	snaps, err := op(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return toProcList(snaps), nil
}

func (s *Server) List(_ context.Context, _ *pb.Empty) (*pb.ProcList, error) {
	return toProcList(s.mgr.List()), nil
}

func (s *Server) Save(_ context.Context, _ *pb.Empty) (*pb.Ack, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "no store configured")
	}
	if err := s.store.Save(s.mgr.Specs()); err != nil {
		return nil, status.Errorf(codes.Internal, "save: %v", err)
	}
	return &pb.Ack{Ok: true, Message: "saved"}, nil
}

func (s *Server) Resurrect(_ context.Context, _ *pb.Empty) (*pb.ProcList, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "no store configured")
	}
	apps, err := s.store.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load dump: %v", err)
	}
	var out []manager.InstanceSnapshot
	for _, app := range apps {
		snaps, err := s.mgr.Add(app) // skip already-running apps
		if err != nil {
			continue
		}
		out = append(out, snaps...)
	}
	return toProcList(out), nil
}

func (s *Server) Kill(_ context.Context, _ *pb.Empty) (*pb.Ack, error) {
	if s.kill != nil {
		go s.kill() // shut down after this RPC returns
	}
	return &pb.Ack{Ok: true, Message: "stopping"}, nil
}

// Run starts the daemon: resolves the socket, auto-resurrects, serves until ctx
// is canceled or Kill is called, then gracefully stops everything.
func Run(ctx context.Context, st *store.Store) error {
	if err := st.EnsureDir(); err != nil {
		return err
	}
	mgr := manager.New(ctx)
	if apps, err := st.Load(); err == nil {
		for _, app := range apps {
			_, _ = mgr.Add(app)
		}
	}

	sock := st.SocketPath()
	removeStaleSocket(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}

	gs := grpc.NewServer()
	srv := &Server{mgr: mgr, store: st}
	var once sync.Once
	stopped := make(chan struct{})
	srv.kill = func() { once.Do(func() { close(stopped) }) }
	pb.RegisterDaemonServer(gs, srv)

	go func() {
		select {
		case <-ctx.Done():
		case <-stopped:
		}
		gs.GracefulStop()
	}()

	serveErr := gs.Serve(lis)
	mgr.StopAll()
	_ = os.Remove(sock)
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		return serveErr
	}
	return nil
}

// removeStaleSocket deletes a leftover socket file if nothing is listening.
func removeStaleSocket(sock string) {
	if _, err := os.Stat(sock); err != nil {
		return
	}
	if c, err := net.Dial("unix", sock); err == nil {
		_ = c.Close()
		return // a live daemon already owns it
	}
	_ = os.Remove(sock)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/daemon/ -race -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/
git commit -m "feat(daemon): gRPC Daemon service + socket serve loop

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: internal/client — dial + daemon auto-spawn

The CLI client checks socket liveness, auto-spawns `marshal daemon` (detached) when needed, waits for readiness, and returns a connected `pb.DaemonClient`. Behavior is exercised end-to-end in Task 8.

**Files:**
- Create: `internal/client/client.go`

- [ ] **Step 1: Implement the client**

Create `internal/client/client.go`:
```go
// Package client dials marshald over its Unix socket, auto-spawning the daemon
// when it is not already running.
package client

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"marshal/internal/pb"
	"marshal/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const spawnTimeout = 3 * time.Second

// Connect returns a connected Daemon client, spawning the daemon if needed.
// The caller must Close the returned conn.
func Connect(st *store.Store) (pb.DaemonClient, *grpc.ClientConn, error) {
	if !alive(st.SocketPath()) {
		if err := spawn(st); err != nil {
			return nil, nil, err
		}
		if err := waitReady(st.SocketPath()); err != nil {
			return nil, nil, err
		}
	}
	conn, err := grpc.NewClient("unix:"+st.SocketPath(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial daemon: %w", err)
	}
	return pb.NewDaemonClient(conn), conn, nil
}

// alive reports whether something is accepting connections on the socket.
func alive(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// spawn launches `marshal daemon` detached, with output to the daemon log.
func spawn(st *store.Store) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate marshal binary: %w", err)
	}
	if err := st.EnsureDir(); err != nil {
		return err
	}
	logf, err := os.OpenFile(st.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logf.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}
	return nil
}

// waitReady polls the socket until the daemon answers or the timeout elapses.
func waitReady(sock string) error {
	deadline := time.Now().Add(spawnTimeout)
	for time.Now().Before(deadline) {
		if alive(sock) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready within %s", spawnTimeout)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./internal/client/`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add internal/client/
git commit -m "feat(client): dial marshald with daemon auto-spawn

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: cmd/marshal — daemon command, control commands, rework run

**Files:**
- Create: `cmd/marshal/daemon.go`
- Create: `cmd/marshal/control.go`
- Modify: `cmd/marshal/main.go`

- [ ] **Step 1: Implement the daemon command**

Create `cmd/marshal/daemon.go`:
```go
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"marshal/internal/daemon"
	"marshal/internal/store"
)

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Run marshald in the foreground (used internally and by boot services)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := store.New()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, st)
		},
	}
}
```

- [ ] **Step 2: Implement the control commands**

Create `cmd/marshal/control.go`:
```go
package main

import (
	"context"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/client"
	"marshal/internal/config"
	"marshal/internal/pb"
	"marshal/internal/store"
)

// withClient connects to (or spawns) the daemon and runs fn.
func withClient(fn func(context.Context, pb.DaemonClient) error) error {
	st, err := store.New()
	if err != nil {
		return err
	}
	c, conn, err := client.Connect(st)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx, c)
}

func appToSpec(a config.App) *pb.AppSpec {
	return &pb.AppSpec{
		Name:        a.Name,
		Cmd:         a.Cmd,
		Args:        a.Args,
		Cwd:         a.Cwd,
		Instances:   int32(a.Instances),
		Env:         a.Env,
		Restart:     string(a.Restart),
		MaxRestarts: int32(a.MaxRestarts),
		KillTimeout: a.KillTimeout.Duration.String(),
	}
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <marshal.yaml>",
		Short: "Start app(s) defined in a marshal.yaml file under the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}
			specs := make([]*pb.AppSpec, 0, len(cfg.Apps))
			for _, a := range cfg.Apps {
				specs = append(specs, appToSpec(a))
			}
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.Start(ctx, &pb.StartRequest{Apps: specs})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
}

// selectorCmd builds stop/restart/delete, which share the same shape.
func selectorCmd(use, short string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := call(ctx, c, &pb.Selector{Target: args[0]})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
}

func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show all managed processes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.List(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
	return cmd
}

func describeCmd() *cobra.Command {
	return selectorCmd("describe <name|id>", "Show detail for an app/instance",
		func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
			return c.Describe(ctx, sel)
		})
}

func saveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save",
		Short: "Persist the current app list to dump.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				ack, err := c.Save(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "marshal:", ack.GetMessage())
				return nil
			})
		},
	}
}

func resurrectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resurrect",
		Short: "Restore apps from dump.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.Resurrect(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
}

func killCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill",
		Short: "Stop the daemon and all managed processes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				ack, err := c.Kill(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "marshal:", ack.GetMessage())
				return nil
			})
		},
	}
}

// printProcs renders a ProcList as an aligned table.
func printProcs(cmd *cobra.Command, list *pb.ProcList) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tINST\tSTATE\tPID\tUPTIME\tRESTARTS")
	for _, p := range list.GetProcs() {
		uptime := "-"
		if p.GetUptimeMs() > 0 {
			uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%d\t%s\t%d\n",
			p.GetId(), p.GetName(), p.GetInstanceId(), p.GetState(), p.GetPid(), uptime, p.GetRestarts())
	}
	_ = w.Flush()
}
```

- [ ] **Step 3: Rework main.go (register commands + rebuild run on the new manager)**

Replace the entire contents of `cmd/marshal/main.go` with:
```go
// Command marshal is the control CLI and daemon entry point for Marshal.
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
	"marshal/internal/pb"
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
	root.AddCommand(
		runCmd(),
		daemonCmd(),
		startCmd(),
		selectorCmd("stop <name|id|all>", "Gracefully stop app(s)",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Stop(ctx, sel)
			}),
		selectorCmd("restart <name|id|all>", "Restart app(s)",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Restart(ctx, sel)
			}),
		selectorCmd("delete <name|id|all>", "Stop and remove app(s) from management",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Delete(ctx, sel)
			}),
		listCmd(),
		describeCmd(),
		saveCmd(),
		resurrectCmd(),
		killCmd(),
	)
	return root
}

// runCmd keeps the M1 foreground supervisor (no daemon), now on the dynamic manager.
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

			m := manager.New(ctx)
			for _, app := range cfg.Apps {
				if _, err := m.Add(app); err != nil {
					return err
				}
			}

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
			<-ctx.Done()
			m.StopAll()
			fmt.Fprintln(cmd.OutOrStdout(), "marshal: all processes stopped")
			return nil
		},
	}
}

func printStatus(cmd *cobra.Command, m *manager.Manager) {
	for _, s := range m.List() {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %-10s pid=%d restarts=%d\n",
			s.Label, s.State, s.Pid, s.Restarts)
	}
}
```

- [ ] **Step 4: Build everything and run the existing run e2e test**

Run:
```bash
go build ./... && go test ./cmd/marshal/ -v
```
Expected: full build is clean; the existing `run` e2e test still passes (the `run` command's external behavior is unchanged).

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/
git commit -m "feat(cli): daemon + control commands; run on dynamic manager

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: End-to-end integration test over the real socket

Drive a real daemon through the real gRPC client, in an isolated `$XDG_DATA_HOME`, with short-lived test processes. Follows the M1 e2e discipline: poll for readiness, never a fixed sleep.

**Files:**
- Create: `cmd/marshal/daemon_e2e_test.go`

- [ ] **Step 1: Write the failing e2e test**

Create `cmd/marshal/daemon_e2e_test.go`:
```go
package main

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/client"
	"marshal/internal/pb"
	"marshal/internal/store"

	"google.golang.org/grpc"
)

// dialReady waits until the in-process daemon is listening, then connects.
// Waiting first guarantees client.Connect sees a live socket and dials rather
// than trying to auto-spawn a subprocess (which, under `go test`, would be the
// test binary, not the marshal CLI).
func dialReady(t *testing.T, st *store.Store) (pb.DaemonClient, *grpc.ClientConn) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", st.SocketPath(), 100*time.Millisecond); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, conn, err := client.Connect(st)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c, conn
}

func waitState(t *testing.T, c pb.DaemonClient, target, state string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		list, err := c.Describe(ctx, &pb.Selector{Target: target})
		cancel()
		if err == nil {
			got := 0
			for _, p := range list.GetProcs() {
				if p.GetState() == state {
					got++
				}
			}
			if got >= n {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s x%d of %q", state, n, target)
}
```

This test file relies on running the daemon. Because the test binary is not the `marshal` binary, replace the auto-spawn path by running the daemon in-process. Add this driver test to the same file:
```go
func TestDaemonLifecycleE2E(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	st, err := store.New()
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// Run the daemon in-process so teardown is hermetic.
	ctx, cancelDaemon := context.WithCancel(context.Background())
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		_ = runDaemonForTest(ctx, st)
	}()
	t.Cleanup(func() { cancelDaemon(); <-daemonDone })

	c, conn := dialReady(t, st)
	defer conn.Close()

	// start
	rpcCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Start(rpcCtx, &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "svc", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 2},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, c, "svc", "online", 2)

	// stop -> stopped
	if _, err := c.Stop(rpcCtx, &pb.Selector{Target: "svc"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitState(t, c, "svc", "stopped", 2)

	// restart -> online again
	if _, err := c.Restart(rpcCtx, &pb.Selector{Target: "svc"}); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitState(t, c, "svc", "online", 2)

	// save + delete + resurrect restores the app
	if _, err := c.Save(rpcCtx, &pb.Empty{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := c.Delete(rpcCtx, &pb.Selector{Target: "svc"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ := c.List(rpcCtx, &pb.Empty{})
	if len(list.GetProcs()) != 0 {
		t.Fatalf("after delete: %d procs, want 0", len(list.GetProcs()))
	}
	if _, err := c.Resurrect(rpcCtx, &pb.Empty{}); err != nil {
		t.Fatalf("Resurrect: %v", err)
	}
	waitState(t, c, "svc", "online", 2)
}
```

- [ ] **Step 2: Add the in-process daemon test helper**

Create a tiny exported-for-test seam. Add to `cmd/marshal/daemon.go`:
```go
// runDaemonForTest runs the daemon serve loop with an explicit context and store.
// It exists so e2e tests can run the daemon in-process with hermetic teardown.
func runDaemonForTest(ctx context.Context, st *store.Store) error {
	return daemon.Run(ctx, st)
}
```

- [ ] **Step 3: Run the e2e test to verify it fails, then passes**

Run: `go test ./cmd/marshal/ -run TestDaemonLifecycleE2E -v`
Expected first run before Step 2's helper exists: FAIL (compile). After Step 2: PASS — start→online, stop→stopped, restart→online, delete empties the list, resurrect restores.

- [ ] **Step 4: Commit**

```bash
git add cmd/marshal/
git commit -m "test(cli): end-to-end daemon lifecycle over the socket

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 9: Full verification, manual smoke, and handoff

**Files:**
- Create: `docs/handoffs/2026-06-16-m2-complete.md` (date the worker's actual completion day if different)

- [ ] **Step 1: Full race + lint pass**

Run:
```bash
cd "/Users/sebastiankuprat/process manager"
go test ./... -race -count=1
go vet ./...
gofmt -l .
```
Expected: all tests pass race-clean; `go vet` silent; `gofmt -l .` lists **nothing** (note: `internal/pb/*.pb.go` is generated and already gofmt-conformant).

- [ ] **Step 2: Manual smoke test**

Run:
```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/marshal-smoke && rm -rf "$XDG_DATA_HOME"
printf 'apps:\n  - name: clock\n    cmd: sh\n    args: ["-c","while true; do date; sleep 1; done"]\n    instances: 2\n' > /tmp/m.yaml
./marshal start /tmp/m.yaml     # auto-spawns the daemon, prints 2 online procs
./marshal list                  # shows clock#0, clock#1 online
./marshal restart clock
./marshal save
./marshal delete all
./marshal resurrect             # clock comes back from dump.json
./marshal kill                  # daemon stops; socket removed
```
Expected: each command behaves as described; `cat $XDG_DATA_HOME/marshald.log` shows daemon output; after `kill`, `$XDG_DATA_HOME/marshald.sock` is gone.

- [ ] **Step 3: Write the handoff**

Create `docs/handoffs/2026-06-16-m2-complete.md` covering: current state (M2 merged, branch), what changed (daemon + gRPC + dynamic manager + auto-spawn + save/resurrect), how to build/run/test, deferred items (logs/metrics → M3, startup → M4, Describe returns ProcList not a single ProcInfo, resurrect skips already-running apps, Windows still deferred), and the next step (M3: log capture + metrics, implementing the reserved `Logs` RPC and `cpu`/`mem` fields). Follow the format of `docs/handoffs/2026-06-16-m1-complete.md`.

- [ ] **Step 4: Finish the branch**

Use the superpowers:finishing-a-development-branch skill to merge to `main` (no remote configured, so PR isn't available) and commit the handoff.

---

## Self-review notes (for the implementer)

- **Spec coverage:** §2 architecture → Tasks 5–7; §3 contract → Task 1 (`Logs`/`cpu`/`mem` defined-but-dormant); §4 packages → Tasks 3–7; §5 auto-spawn/kill → Tasks 5–6; §6 save/resurrect/auto-resurrect → Tasks 3, 5; §7 error mapping → Task 5 (`InvalidArgument`/`AlreadyExists`/`NotFound`); §8 testing → Tasks 2–8.
- **Refinement vs spec:** `Describe` returns `ProcList` (an app has N instances) rather than a single `ProcInfo`. Note this in the handoff.
- **Known sequencing:** after Task 4, `go build ./...` is intentionally red until Task 7 rewires `cmd/marshal`; per-package tests stay green throughout.
