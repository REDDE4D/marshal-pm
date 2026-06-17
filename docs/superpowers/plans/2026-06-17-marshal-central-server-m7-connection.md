# M7 — Central Server Connection + Live Fleet State — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One Marshal binary can run as a central server (`marshal server`); a daemon configured with a `server:` block dials it over an agent-initiated gRPC stream, pushes process-state snapshots up, and `marshal fleet ps` reads the aggregated live state.

**Architecture:** A new `Fleet` gRPC service (`proto/marshal/v1/fleet.proto`) reuses the existing `ProcInfo` message. The server (`internal/server`) keeps an in-memory registry keyed by agent name. The agent-side client (`internal/fleet`) runs inside the daemon, pushing full snapshots every 2s (which doubles as a liveness heartbeat) with exponential-backoff reconnect. Config reaches the auto-spawned daemon through a persisted `fleet.json` written by `marshal start`, not over the socket — so the `Daemon` proto service is unchanged.

**Tech Stack:** Go 1.26.4, gRPC (`google.golang.org/grpc` v1.81.1), protobuf (protoc 35.0 on PATH), cobra CLI, `text/tabwriter` for table output.

## Global Constraints

- Module path is `marshal`; internal imports are `marshal/internal/...`.
- Go 1.26.4 (Homebrew `/opt/homebrew/bin/go`). No new third-party dependencies — gRPC and protobuf are already in `go.mod`.
- TDD: failing test first, then minimal implementation. Keep packages small and single-responsibility.
- Proto is regenerated with `go generate ./internal/pb` (protoc + the `tool` plugins already in `go.mod`). Never hand-edit `*.pb.go`.
- M7 transport is **plaintext / unauthenticated** (insecure gRPC credentials). TLS + tokens are M10.
- Before finishing: `gofmt -l .` lists nothing, `go vet ./...` clean, `go build ./...` ok, `go test ./... -race -count=1` green.
- Commit per task; imperative subject; trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Do this work on a branch (e.g. `m7-fleet-connection`) cut from `main`, not on `main`.
- Spec: `docs/superpowers/specs/2026-06-17-marshal-central-server-m7-connection-design.md`.

## File Structure

- `proto/marshal/v1/fleet.proto` (new) — `Fleet` service + `AgentMessage`/`ServerMessage`/`Hello`/`HelloAck`/`StateSnapshot`/`ListFleet*`/`AgentState`. Reuses `ProcInfo` from `daemon.proto`.
- `internal/pb/doc.go` (modify) — extend the `//go:generate` directive to include `fleet.proto`.
- `internal/pb/fleet.pb.go`, `internal/pb/fleet_grpc.pb.go` (generated).
- `internal/config/config.go` (modify) — `ServerConfig` struct + `Config.Server *ServerConfig` + validation.
- `internal/store/store.go` (modify) — `SaveServer`/`LoadServer` persisting `fleet.json`.
- `internal/server/registry.go` (new) — in-memory `Registry` (mutex, clock-injectable connected/offline logic).
- `internal/server/server.go` (new) — `Fleet` gRPC server (`Connect` stream + `ListFleet`) + `Serve` helper.
- `internal/fleet/client.go` (new) — agent-side client: dial, `Hello`, periodic snapshot push, reconnect/backoff.
- `internal/daemon/fleet.go` (new) — `procInfos`/`fleetSnapshot` adapter + start the client in `Run`.
- `cmd/marshal/server.go` (new) — `marshal server` subcommand.
- `cmd/marshal/fleet.go` (new) — `marshal fleet ps` subcommand + `printFleet`.
- `cmd/marshal/control.go` (modify) — `marshal start` persists the `server:` block via `persistServer`.
- `cmd/marshal/main.go` (modify) — register `serverCmd()` and `fleetCmd()`.

---

### Task 1: Proto — `Fleet` service + regenerate

**Files:**
- Create: `proto/marshal/v1/fleet.proto`
- Modify: `internal/pb/doc.go` (the `//go:generate` line)
- Generated: `internal/pb/fleet.pb.go`, `internal/pb/fleet_grpc.pb.go`

**Interfaces:**
- Consumes: `ProcInfo` from `proto/marshal/v1/daemon.proto` (same proto package `marshal.v1`, same Go package `pb`).
- Produces: `pb.FleetClient`, `pb.FleetServer`, `pb.RegisterFleetServer`, `pb.NewFleetClient`, `pb.Fleet_ConnectServer`, `pb.Fleet_ConnectClient`, and messages `pb.AgentMessage` (oneof `AgentMessage_Hello`, `AgentMessage_Snapshot`), `pb.ServerMessage` (oneof `ServerMessage_HelloAck`), `pb.Hello`, `pb.HelloAck`, `pb.StateSnapshot`, `pb.ListFleetRequest`, `pb.ListFleetResponse`, `pb.AgentState`.

- [ ] **Step 1: Write `proto/marshal/v1/fleet.proto`**

```proto
syntax = "proto3";

package marshal.v1;

option go_package = "marshal/internal/pb;pb";

import "marshal/v1/daemon.proto"; // reuse ProcInfo

// Fleet is the agent<->server control surface (M7: connection + live state).
service Fleet {
  // Agent-initiated, long-lived, bidirectional. M7 uses only the upstream direction.
  rpc Connect(stream AgentMessage) returns (stream ServerMessage);
  // Read path for the fleet CLI (and, later, the dashboard).
  rpc ListFleet(ListFleetRequest) returns (ListFleetResponse);
}

message AgentMessage {
  oneof msg {
    Hello hello = 1;            // sent once on connect
    StateSnapshot snapshot = 2; // full process-state snapshot (periodic)
  }
}

message ServerMessage {
  oneof msg {
    HelloAck hello_ack = 1;
    // reserved: command messages land here in M9
  }
}

message Hello {
  string agent_name = 1;
  string marshal_version = 2;
}

message HelloAck {}

message StateSnapshot { repeated ProcInfo procs = 1; } // ProcInfo from daemon.proto

message ListFleetRequest {}

message ListFleetResponse { repeated AgentState agents = 1; }

message AgentState {
  string agent_name = 1;
  bool connected = 2;
  int64 last_seen_unix = 3;
  repeated ProcInfo procs = 4;
}
```

- [ ] **Step 2: Extend the generate directive in `internal/pb/doc.go`**

Replace the existing `//go:generate` line with one that lists both proto files:

```go
//go:generate protoc --go_out=../.. --go_opt=module=marshal --go-grpc_out=../.. --go-grpc_opt=module=marshal -I ../../proto ../../proto/marshal/v1/daemon.proto ../../proto/marshal/v1/fleet.proto
```

- [ ] **Step 3: Regenerate and verify it compiles**

Run: `go generate ./internal/pb && go build ./... && go vet ./internal/pb`
Expected: `fleet.pb.go` and `fleet_grpc.pb.go` created; build + vet succeed with no output.

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/doc.go internal/pb/fleet.pb.go internal/pb/fleet_grpc.pb.go
git commit -m "feat(proto): add Fleet service (Connect stream + ListFleet)"
```

---

### Task 2: Config — `server:` block

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.ServerConfig{ Address string; Name string }` and `config.Config.Server *ServerConfig`. A present block with an empty `Address` is a validation error; absence yields `nil`.

- [ ] **Step 1: Write the failing tests in `internal/config/config_test.go`**

```go
func TestParseServerBlock(t *testing.T) {
	cfg, err := Parse([]byte("server:\n  address: srv:9000\n  name: web-1\napps:\n  - name: api\n    cmd: ./api\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server == nil || cfg.Server.Address != "srv:9000" || cfg.Server.Name != "web-1" {
		t.Fatalf("server = %+v", cfg.Server)
	}
}

func TestParseNoServerBlock(t *testing.T) {
	cfg, err := Parse([]byte("apps:\n  - name: api\n    cmd: ./api\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != nil {
		t.Fatalf("expected nil server, got %+v", cfg.Server)
	}
}

func TestServerBlockRequiresAddress(t *testing.T) {
	if _, err := Parse([]byte("server:\n  name: web-1\napps:\n  - name: api\n    cmd: ./api\n")); err == nil {
		t.Fatal("expected error for missing server.address")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/config/ -run TestParseServerBlock -v`
Expected: FAIL (compile error: `cfg.Server` undefined).

- [ ] **Step 3: Implement in `internal/config/config.go`**

Add the type (place it near `App`):

```go
// ServerConfig points the agent at a central server. Presence enables fleet mode.
type ServerConfig struct {
	Address string `yaml:"address" json:"address"`
	Name    string `yaml:"name" json:"name,omitempty"`
}
```

Add the field to `Config`:

```go
// Config is the top-level marshal.yaml document.
type Config struct {
	Server *ServerConfig `yaml:"server" json:"server,omitempty"`
	Apps   []App         `yaml:"apps"`
}
```

Add validation inside `validate()` (before the final `return nil`):

```go
	if c.Server != nil && c.Server.Address == "" {
		return fmt.Errorf("server.address is required when a server block is present")
	}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all config tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add optional server: block (address + name)"
```

---

### Task 3: Store — persist `fleet.json`

**Files:**
- Modify: `internal/store/store.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `(*Store).SaveServer(*config.ServerConfig) error` (atomic write to `fleet.json`) and `(*Store).LoadServer() (*config.ServerConfig, error)` (missing file → `nil, nil`).

- [ ] **Step 1: Write the failing tests in `internal/store/store_test.go`**

```go
func TestSaveLoadServer(t *testing.T) {
	st := NewAt(t.TempDir())
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Name: "web-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Address != "srv:9000" || got.Name != "web-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadServerMissing(t *testing.T) {
	got, err := NewAt(t.TempDir()).LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}
```

(If `store_test.go` does not already import `marshal/internal/config`, add it.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/store/ -run TestSaveLoadServer -v`
Expected: FAIL (`SaveServer` undefined).

- [ ] **Step 3: Implement in `internal/store/store.go`**

Add (mirroring the existing `dumpPath`/`Save`/`Load` style):

```go
func (s *Store) serverPath() string { return filepath.Join(s.base, "fleet.json") }

// SaveServer writes the central-server config to fleet.json atomically.
func (s *Store) SaveServer(sc *config.ServerConfig) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fleet config: %w", err)
	}
	tmp := s.serverPath() + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write fleet config: %w", err)
	}
	if err := os.Rename(tmp, s.serverPath()); err != nil {
		return fmt.Errorf("rename fleet config: %w", err)
	}
	return nil
}

// LoadServer reads fleet.json. A missing file yields (nil, nil).
func (s *Store) LoadServer() (*config.ServerConfig, error) {
	data, err := os.ReadFile(s.serverPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read fleet config: %w", err)
	}
	var sc config.ServerConfig
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("decode fleet config: %w", err)
	}
	return &sc, nil
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/store.go internal/store/store_test.go
git commit -m "feat(store): persist central-server config to fleet.json"
```

---

### Task 4: Server registry (in-memory, clock-injectable)

**Files:**
- Create: `internal/server/registry.go`
- Test: `internal/server/registry_test.go`

**Interfaces:**
- Produces:
  - `server.NewRegistry(opts ...RegOption) *Registry`
  - `RegOption`s: `server.WithOfflineAfter(time.Duration)`, `server.WithClock(func() time.Time)`
  - Methods: `(*Registry).Open(name string)`, `(*Registry).Update(name string, procs []*pb.ProcInfo)`, `(*Registry).Close(name string)`, `(*Registry).List() []*pb.AgentState`
  - `List()` reports `connected = streamOpen && (now − lastSeen ≤ offlineAfter)`; the last snapshot is retained for offline agents. Default `offlineAfter` is 10s, default clock is `time.Now`.

- [ ] **Step 1: Write the failing test in `internal/server/registry_test.go`**

```go
package server

import (
	"testing"
	"time"

	"marshal/internal/pb"
)

func TestRegistryConnectedFreshAndOffline(t *testing.T) {
	now := time.Unix(1000, 0)
	reg := NewRegistry(WithOfflineAfter(10*time.Second), WithClock(func() time.Time { return now }))

	reg.Open("web-1")
	reg.Update("web-1", []*pb.ProcInfo{{Name: "api", State: "online"}})

	got := reg.List()
	if len(got) != 1 || got[0].GetAgentName() != "web-1" || !got[0].GetConnected() {
		t.Fatalf("got %+v", got)
	}
	if len(got[0].GetProcs()) != 1 {
		t.Fatalf("procs = %+v", got[0].GetProcs())
	}

	// No fresh snapshot past the offline window -> offline, snapshot retained.
	now = now.Add(11 * time.Second)
	if reg.List()[0].GetConnected() {
		t.Fatal("expected offline after lapse")
	}
	if len(reg.List()[0].GetProcs()) != 1 {
		t.Fatal("expected last snapshot retained while offline")
	}
}

func TestRegistryCloseMarksOfflineImmediately(t *testing.T) {
	now := time.Unix(2000, 0)
	reg := NewRegistry(WithOfflineAfter(time.Hour), WithClock(func() time.Time { return now }))
	reg.Open("web-1")
	reg.Update("web-1", []*pb.ProcInfo{{Name: "api"}})
	reg.Close("web-1")
	if reg.List()[0].GetConnected() {
		t.Fatal("expected offline immediately after Close")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/server/ -run TestRegistry -v`
Expected: FAIL (`NewRegistry` undefined / no such package).

- [ ] **Step 3: Implement `internal/server/registry.go`**

```go
// Package server implements the Marshal central server: the Fleet gRPC service
// and an in-memory registry of connected agents and their last-known state.
package server

import (
	"sync"
	"time"

	"marshal/internal/pb"
)

type agentEntry struct {
	procs      []*pb.ProcInfo
	streamOpen bool
	lastSeen   time.Time
}

// Registry holds the live fleet state, keyed by agent name.
type Registry struct {
	mu           sync.Mutex
	agents       map[string]*agentEntry
	offlineAfter time.Duration
	now          func() time.Time
}

// RegOption configures a Registry.
type RegOption func(*Registry)

// WithOfflineAfter sets how long after the last snapshot an agent with an open
// stream is still considered connected.
func WithOfflineAfter(d time.Duration) RegOption { return func(r *Registry) { r.offlineAfter = d } }

// WithClock overrides time.Now (used by tests).
func WithClock(fn func() time.Time) RegOption { return func(r *Registry) { r.now = fn } }

// NewRegistry builds an empty registry (default offlineAfter 10s, clock time.Now).
func NewRegistry(opts ...RegOption) *Registry {
	r := &Registry{agents: map[string]*agentEntry{}, offlineAfter: 10 * time.Second, now: time.Now}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *Registry) entry(name string) *agentEntry {
	e := r.agents[name]
	if e == nil {
		e = &agentEntry{}
		r.agents[name] = e
	}
	return e
}

// Open marks an agent's stream as open (called on Hello).
func (r *Registry) Open(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(name)
	e.streamOpen = true
	e.lastSeen = r.now()
}

// Update records a fresh snapshot and bumps last-seen.
func (r *Registry) Update(name string, procs []*pb.ProcInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(name)
	e.procs = procs
	e.streamOpen = true
	e.lastSeen = r.now()
}

// Close marks an agent's stream as closed; its last snapshot is retained.
func (r *Registry) Close(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.agents[name]; e != nil {
		e.streamOpen = false
	}
}

// List snapshots every known agent and computes its connected flag.
func (r *Registry) List() []*pb.AgentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	out := make([]*pb.AgentState, 0, len(r.agents))
	for name, e := range r.agents {
		connected := e.streamOpen && now.Sub(e.lastSeen) <= r.offlineAfter
		out = append(out, &pb.AgentState{
			AgentName:    name,
			Connected:    connected,
			LastSeenUnix: e.lastSeen.Unix(),
			Procs:        e.procs,
		})
	}
	return out
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/server/ -run TestRegistry -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/registry.go internal/server/registry_test.go
git commit -m "feat(server): in-memory fleet registry with offline detection"
```

---

### Task 5: Server gRPC — `Connect` stream + `ListFleet`

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `*Registry` (Task 4); `pb.FleetServer`, `pb.RegisterFleetServer`, `pb.Fleet_ConnectServer` (Task 1).
- Produces:
  - `server.NewServer(reg *Registry) *Server` implementing `pb.FleetServer`.
  - `server.Serve(ctx context.Context, lis net.Listener, reg *Registry) error` — registers the service, serves until `ctx` is canceled (then `GracefulStop`).

- [ ] **Step 1: Write the failing test in `internal/server/server_test.go`**

```go
package server

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func startServer(t *testing.T, reg *Registry) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Serve(ctx, lis, reg) }()
	return lis.Addr().String()
}

func dialFleet(t *testing.T, addr string) pb.FleetClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewFleetClient(conn)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func TestServerConnectListAndOffline(t *testing.T) {
	reg := NewRegistry(WithOfflineAfter(time.Hour))
	addr := startServer(t, reg)
	cl := dialFleet(t, addr)

	stream, err := cl.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: "web-1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{Snapshot: &pb.StateSnapshot{Procs: []*pb.ProcInfo{{Name: "api", State: "online"}}}}}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetConnected() && len(ag[0].GetProcs()) == 1
	})

	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && !ag[0].GetConnected()
	})

	// ListFleet over the wire reflects the same offline state.
	resp, err := cl.ListFleet(context.Background(), &pb.ListFleetRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetAgents()) != 1 || resp.GetAgents()[0].GetConnected() {
		t.Fatalf("agents = %+v", resp.GetAgents())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/server/ -run TestServerConnect -v`
Expected: FAIL (`Serve`/`NewServer` undefined).

- [ ] **Step 3: Implement `internal/server/server.go`**

```go
package server

import (
	"context"
	"io"
	"net"

	"marshal/internal/pb"

	"google.golang.org/grpc"
)

// Server implements pb.FleetServer backed by an in-memory Registry.
type Server struct {
	pb.UnimplementedFleetServer
	reg *Registry
}

// NewServer wires a Fleet server to a registry.
func NewServer(reg *Registry) *Server { return &Server{reg: reg} }

// Connect terminates one agent's upstream. M7 reads Hello + StateSnapshot and
// acks Hello; the downstream direction is reserved for M9.
func (s *Server) Connect(stream pb.Fleet_ConnectServer) error {
	var name string
	for {
		msg, err := stream.Recv()
		if err != nil {
			if name != "" {
				s.reg.Close(name)
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch m := msg.GetMsg().(type) {
		case *pb.AgentMessage_Hello:
			name = m.Hello.GetAgentName()
			s.reg.Open(name)
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{HelloAck: &pb.HelloAck{}}})
		case *pb.AgentMessage_Snapshot:
			if name != "" {
				s.reg.Update(name, m.Snapshot.GetProcs())
			}
		}
	}
}

// ListFleet returns the current aggregated fleet state.
func (s *Server) ListFleet(_ context.Context, _ *pb.ListFleetRequest) (*pb.ListFleetResponse, error) {
	return &pb.ListFleetResponse{Agents: s.reg.List()}, nil
}

// Serve registers the Fleet service on lis and serves until ctx is canceled.
func Serve(ctx context.Context, lis net.Listener, reg *Registry) error {
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, NewServer(reg))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	return gs.Serve(lis)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/server/ -race -v`
Expected: PASS (registry + server tests, race-clean).

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): Fleet gRPC service (Connect stream + ListFleet)"
```

---

### Task 6: Agent fleet client — dial, Hello, periodic push, reconnect

**Files:**
- Create: `internal/fleet/client.go`
- Test: `internal/fleet/client_test.go`

**Interfaces:**
- Consumes: `pb.NewFleetClient`, `pb.Fleet_ConnectClient` (Task 1); `internal/server` (test only, as the peer).
- Produces:
  - `fleet.SnapshotFunc` = `func() []*pb.ProcInfo`
  - `fleet.New(addr, name, version string, snap SnapshotFunc, opts ...Option) *Client`
  - `Option`s: `fleet.WithInterval(time.Duration)` (push cadence, default 2s), `fleet.WithBackoff(min, max time.Duration)` (default 1s/30s)
  - `(*Client).Run(ctx context.Context)` — blocks until `ctx` is canceled, maintaining the connection with exponential-backoff reconnect. Never panics; all errors drive reconnect.

- [ ] **Step 1: Write the failing test in `internal/fleet/client_test.go`**

```go
package fleet_test

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/pb"
	"marshal/internal/server"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func snap() []*pb.ProcInfo { return []*pb.ProcInfo{{Name: "api", State: "online"}} }

func TestClientHelloAndPeriodicPush(t *testing.T) {
	reg := server.NewRegistry(server.WithOfflineAfter(time.Hour))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() { _ = server.Serve(sctx, lis, reg) }()

	c := fleet.New(lis.Addr().String(), "web-1", "test", snap,
		fleet.WithInterval(20*time.Millisecond), fleet.WithBackoff(10*time.Millisecond, 40*time.Millisecond))
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go c.Run(cctx)

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetAgentName() == "web-1" && ag[0].GetConnected() && len(ag[0].GetProcs()) == 1
	})
}

func TestClientReconnectsWhenServerStartsLate(t *testing.T) {
	// Reserve an address, then free it so the server is initially down.
	lis0, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis0.Addr().String()
	_ = lis0.Close()

	c := fleet.New(addr, "web-1", "test", snap,
		fleet.WithInterval(20*time.Millisecond), fleet.WithBackoff(10*time.Millisecond, 40*time.Millisecond))
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go c.Run(cctx) // retries against a dead address

	time.Sleep(60 * time.Millisecond)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("could not rebind %s: %v", addr, err)
	}
	reg := server.NewRegistry(server.WithOfflineAfter(time.Hour))
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() { _ = server.Serve(sctx, lis, reg) }()

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetConnected()
	})
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/fleet/ -run TestClient -v`
Expected: FAIL (`fleet.New` undefined).

- [ ] **Step 3: Implement `internal/fleet/client.go`**

```go
// Package fleet holds the agent-side client that streams local process state to
// the Marshal central server. It is additive: with no server configured it never
// runs, and a server outage never affects local supervision.
package fleet

import (
	"context"
	"log"
	"time"

	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SnapshotFunc returns the agent's current process state.
type SnapshotFunc func() []*pb.ProcInfo

// Client maintains one outbound stream to the central server.
type Client struct {
	addr     string
	name     string
	version  string
	snapshot SnapshotFunc
	interval time.Duration
	minBO    time.Duration
	maxBO    time.Duration
}

// Option configures a Client.
type Option func(*Client)

// WithInterval sets the snapshot push cadence (also the liveness heartbeat).
func WithInterval(d time.Duration) Option { return func(c *Client) { c.interval = d } }

// WithBackoff sets the reconnect backoff bounds.
func WithBackoff(min, max time.Duration) Option {
	return func(c *Client) { c.minBO, c.maxBO = min, max }
}

// New builds a fleet client. snap must be non-nil.
func New(addr, name, version string, snap SnapshotFunc, opts ...Option) *Client {
	c := &Client{
		addr: addr, name: name, version: version, snapshot: snap,
		interval: 2 * time.Second, minBO: time.Second, maxBO: 30 * time.Second,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Run maintains the connection until ctx is canceled, reconnecting with
// exponential backoff. Backoff resets after a stream is successfully established.
func (c *Client) Run(ctx context.Context) {
	backoff := c.minBO
	for {
		if ctx.Err() != nil {
			return
		}
		established, err := c.connectOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("fleet: connection to %s ended: %v", c.addr, err)
		}
		if established {
			backoff = c.minBO
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > c.maxBO {
			backoff = c.maxBO
		}
	}
}

// connectOnce dials, sends Hello, then pushes snapshots until the stream errors
// or ctx is canceled. The bool reports whether the stream was established.
func (c *Client) connectOnce(ctx context.Context) (bool, error) {
	conn, err := grpc.NewClient(c.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, err
	}
	defer conn.Close()

	stream, err := pb.NewFleetClient(conn).Connect(ctx)
	if err != nil {
		return false, err
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{
		Hello: &pb.Hello{AgentName: c.name, MarshalVersion: c.version},
	}}); err != nil {
		return false, err
	}
	if err := c.push(stream); err != nil { // immediate first snapshot
		return true, err
	}

	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case <-t.C:
			if err := c.push(stream); err != nil {
				return true, err
			}
		}
	}
}

func (c *Client) push(stream pb.Fleet_ConnectClient) error {
	return stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{
		Snapshot: &pb.StateSnapshot{Procs: c.snapshot()},
	}})
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/fleet/ -race -v`
Expected: PASS (both client tests; this exercises the real end-to-end wire path against `internal/server`).

- [ ] **Step 5: Commit**

```bash
git add internal/fleet/client.go internal/fleet/client_test.go
git commit -m "feat(fleet): agent client with periodic snapshot push and reconnect"
```

---

### Task 7: Daemon wiring — start the fleet client from `Run`

**Files:**
- Create: `internal/daemon/fleet.go`
- Test: `internal/daemon/fleet_test.go`
- Modify: `internal/daemon/server.go` (start the client inside `Run`)

**Interfaces:**
- Consumes: `(*store.Store).LoadServer()` (Task 3); `fleet.New`/`fleet.Client.Run` + `fleet.SnapshotFunc` (Task 6); existing `snapshotToProc` (in `internal/daemon/convert.go`), `manager.Manager.List()`, `manager.InstanceSnapshot`, `version.String()`.
- Produces: `procInfos([]manager.InstanceSnapshot) []*pb.ProcInfo` and `fleetSnapshot(*manager.Manager) fleet.SnapshotFunc`. Wiring: when `st.LoadServer()` is non-nil, `Run` launches `fleet.New(addr, name, version, fleetSnapshot(mgr)).Run(serveCtx)` in a goroutine; `name` defaults to the OS hostname when the config omits it.

- [ ] **Step 1: Write the failing test in `internal/daemon/fleet_test.go`**

```go
package daemon

import (
	"testing"
	"time"

	"marshal/internal/manager"
	"marshal/internal/supervisor"
)

func TestProcInfosMapsSnapshot(t *testing.T) {
	snaps := []manager.InstanceSnapshot{{
		ID: 1, Name: "api", InstanceID: 0, Label: "api#0",
		Snapshot: supervisor.Snapshot{
			State: supervisor.StateOnline, Pid: 4242, Restarts: 2,
			StartedAt: time.Now().Add(-3 * time.Second),
		},
	}}
	out := procInfos(snaps)
	if len(out) != 1 {
		t.Fatalf("len = %d", len(out))
	}
	p := out[0]
	if p.GetName() != "api" || p.GetPid() != 4242 || p.GetState() != "online" || p.GetRestarts() != 2 {
		t.Fatalf("proc = %+v", p)
	}
	if p.GetUptimeMs() <= 0 {
		t.Fatal("expected positive uptime for an online proc")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/daemon/ -run TestProcInfosMapsSnapshot -v`
Expected: FAIL (`procInfos` undefined).

- [ ] **Step 3: Implement `internal/daemon/fleet.go`**

```go
package daemon

import (
	"marshal/internal/fleet"
	"marshal/internal/manager"
	"marshal/internal/pb"
)

// procInfos adapts manager snapshots to wire ProcInfo. cpu/mem are zero in M7
// (metric streaming is M8); it reuses snapshotToProc for the field mapping.
func procInfos(snaps []manager.InstanceSnapshot) []*pb.ProcInfo {
	out := make([]*pb.ProcInfo, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, snapshotToProc(s, 0, 0))
	}
	return out
}

// fleetSnapshot returns a SnapshotFunc over the manager's current instances.
func fleetSnapshot(m *manager.Manager) fleet.SnapshotFunc {
	return func() []*pb.ProcInfo { return procInfos(m.List()) }
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/daemon/ -run TestProcInfosMapsSnapshot -v`
Expected: PASS.

- [ ] **Step 5: Wire the client into `Run` (in `internal/daemon/server.go`)**

Add imports `"os"` (already present), `"marshal/internal/fleet"`, and `"marshal/internal/version"` to `internal/daemon/server.go`. Immediately after `serveCtx, cancel := context.WithCancel(ctx)` and its `defer cancel()` (around line 223), insert:

```go
	if sc, err := st.LoadServer(); err == nil && sc != nil {
		name := sc.Name
		if name == "" {
			if h, hErr := os.Hostname(); hErr == nil {
				name = h
			}
		}
		fc := fleet.New(sc.Address, name, version.String(), fleetSnapshot(mgr))
		go fc.Run(serveCtx)
	}
```

- [ ] **Step 6: Verify the package builds and all daemon tests pass**

Run: `go build ./... && go test ./internal/daemon/ -race -count=1`
Expected: build clean; PASS. (Existing daemon tests construct `&Server{...}` directly and never call `LoadServer`, so they are unaffected; an auto-spawned daemon with no `fleet.json` starts no client.)

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/fleet.go internal/daemon/fleet_test.go internal/daemon/server.go
git commit -m "feat(daemon): start fleet client when a server config is persisted"
```

---

### Task 8: CLI — `marshal server`

**Files:**
- Create: `cmd/marshal/server.go`
- Test: `cmd/marshal/server_test.go`
- Modify: `cmd/marshal/main.go` (register the command)

**Interfaces:**
- Consumes: `server.Serve`, `server.NewRegistry` (Tasks 4–5).
- Produces: `serverCmd() *cobra.Command` — `marshal server [--listen ADDR]`, default `--listen :9000`.

- [ ] **Step 1: Write the failing test in `cmd/marshal/server_test.go`**

```go
package main

import "testing"

func TestServerCmdInvalidListen(t *testing.T) {
	cmd := serverCmd()
	cmd.SetArgs([]string{"--listen", "127.0.0.1:99999"}) // port out of range -> Listen errors fast
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a listen error for an invalid port")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/marshal/ -run TestServerCmdInvalidListen -v`
Expected: FAIL (`serverCmd` undefined).

- [ ] **Step 3: Implement `cmd/marshal/server.go`**

```go
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"marshal/internal/server"
)

func serverCmd() *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Marshal central server (fleet aggregation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lis, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen %s: %w", listen, err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(cmd.OutOrStdout(), "marshal server: listening on %s\n", lis.Addr())
			return server.Serve(ctx, lis, server.NewRegistry())
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "address to listen on")
	return cmd
}
```

- [ ] **Step 4: Register in `cmd/marshal/main.go`**

Add `serverCmd(),` to the `root.AddCommand(...)` list (e.g. after `killCmd(),`).

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./cmd/marshal/ -run TestServerCmdInvalidListen -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/server.go cmd/marshal/server_test.go cmd/marshal/main.go
git commit -m "feat(cli): add marshal server subcommand"
```

---

### Task 9: CLI — `marshal fleet ps`

**Files:**
- Create: `cmd/marshal/fleet.go`
- Test: `cmd/marshal/fleet_test.go`
- Modify: `cmd/marshal/main.go` (register the command)

**Interfaces:**
- Consumes: `pb.NewFleetClient`, `pb.ListFleetResponse`, `pb.AgentState`, `pb.ProcInfo` (Task 1).
- Produces: `fleetCmd() *cobra.Command` (parent) with child `marshal fleet ps [--server ADDR]`; `printFleet(cmd *cobra.Command, resp *pb.ListFleetResponse)`; `resolveServer(flag string) string` (flag → `MARSHAL_SERVER` → `localhost:9000`).

- [ ] **Step 1: Write the failing tests in `cmd/marshal/fleet_test.go`**

```go
package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/pb"
)

func TestResolveServer(t *testing.T) {
	if got := resolveServer("explicit:1"); got != "explicit:1" {
		t.Fatalf("flag should win, got %q", got)
	}
	t.Setenv("MARSHAL_SERVER", "fromenv:2")
	if got := resolveServer(""); got != "fromenv:2" {
		t.Fatalf("env should win when no flag, got %q", got)
	}
	t.Setenv("MARSHAL_SERVER", "")
	if got := resolveServer(""); got != "localhost:9000" {
		t.Fatalf("default should be localhost:9000, got %q", got)
	}
}

func TestPrintFleet(t *testing.T) {
	resp := &pb.ListFleetResponse{Agents: []*pb.AgentState{
		{AgentName: "web-1", Connected: true, Procs: []*pb.ProcInfo{
			{Id: 1, Name: "api", InstanceId: 0, State: "online", Pid: 10, UptimeMs: 5000},
		}},
		{AgentName: "web-2", Connected: false, LastSeenUnix: time.Now().Add(-30 * time.Second).Unix()},
	}}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printFleet(cmd, resp)
	out := buf.String()
	for _, want := range []string{"web-1", "online", "api", "web-2", "offline"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/marshal/ -run 'TestResolveServer|TestPrintFleet' -v`
Expected: FAIL (`resolveServer`/`printFleet` undefined).

- [ ] **Step 3: Implement `cmd/marshal/fleet.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"marshal/internal/pb"
)

func fleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Operate on the central server / fleet",
	}
	cmd.AddCommand(fleetPsCmd())
	return cmd
}

func fleetPsCmd() *cobra.Command {
	var serverAddr string
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List processes across all connected agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := grpc.NewClient(resolveServer(serverAddr),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).ListFleet(ctx, &pb.ListFleetRequest{})
			if err != nil {
				return err
			}
			printFleet(cmd, resp)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	return cmd
}

// resolveServer picks the server address: explicit flag, then $MARSHAL_SERVER,
// then localhost:9000.
func resolveServer(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("MARSHAL_SERVER"); env != "" {
		return env
	}
	return "localhost:9000"
}

// printFleet renders fleet state grouped by agent.
func printFleet(cmd *cobra.Command, resp *pb.ListFleetResponse) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tSTATUS\tID\tNAME\tINST\tSTATE\tPID\tUPTIME\tRESTARTS")
	for _, a := range resp.GetAgents() {
		status := "offline"
		if a.GetConnected() {
			status = "online"
		} else if a.GetLastSeenUnix() > 0 {
			status = fmt.Sprintf("offline %s", time.Since(time.Unix(a.GetLastSeenUnix(), 0)).Round(time.Second))
		}
		if len(a.GetProcs()) == 0 {
			fmt.Fprintf(w, "%s\t%s\t-\t-\t-\t-\t-\t-\t-\n", a.GetAgentName(), status)
			continue
		}
		for _, p := range a.GetProcs() {
			uptime := "-"
			if p.GetUptimeMs() > 0 {
				uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\t%d\t%s\t%d\n",
				a.GetAgentName(), status, p.GetId(), p.GetName(), p.GetInstanceId(),
				p.GetState(), p.GetPid(), uptime, p.GetRestarts())
		}
	}
	_ = w.Flush()
}
```

- [ ] **Step 4: Register in `cmd/marshal/main.go`**

Add `fleetCmd(),` to the `root.AddCommand(...)` list (e.g. after `serverCmd(),`).

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./cmd/marshal/ -run 'TestResolveServer|TestPrintFleet' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/fleet.go cmd/marshal/fleet_test.go cmd/marshal/main.go
git commit -m "feat(cli): add marshal fleet ps"
```

---

### Task 10: CLI — `marshal start` persists the `server:` block

**Files:**
- Modify: `cmd/marshal/control.go` (add `persistServer`; call it from `startCmd`)
- Test: `cmd/marshal/control_test.go`

**Interfaces:**
- Consumes: `config.Config`, `(*store.Store).SaveServer` (Tasks 2–3).
- Produces: `persistServer(st *store.Store, cfg *config.Config) error` — writes `fleet.json` when `cfg.Server != nil`, no-op otherwise. `startCmd` calls it before connecting to the daemon, so the freshly auto-spawned daemon reads it.

- [ ] **Step 1: Write the failing test in `cmd/marshal/control_test.go`**

```go
func TestPersistServer(t *testing.T) {
	st := store.NewAt(t.TempDir())
	if err := persistServer(st, &config.Config{Server: &config.ServerConfig{Address: "srv:9000"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Address != "srv:9000" {
		t.Fatalf("got %+v", got)
	}

	st2 := store.NewAt(t.TempDir())
	if err := persistServer(st2, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	if g, _ := st2.LoadServer(); g != nil {
		t.Fatalf("expected no fleet.json for a config without a server block, got %+v", g)
	}
}
```

(Ensure `control_test.go` imports `marshal/internal/config` and `marshal/internal/store`.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/marshal/ -run TestPersistServer -v`
Expected: FAIL (`persistServer` undefined).

- [ ] **Step 3: Implement `persistServer` and call it from `startCmd` (in `cmd/marshal/control.go`)**

Add the helper:

```go
// persistServer writes the central-server config to the store so the
// (auto-spawned) daemon picks it up at startup. No-op without a server block.
func persistServer(st *store.Store, cfg *config.Config) error {
	if cfg.Server == nil {
		return nil
	}
	return st.SaveServer(cfg.Server)
}
```

In `startCmd`'s `RunE`, after `cfg, err := config.Load(args[0])` (and its error check), before building `specs`, insert:

```go
				st, err := store.New()
				if err != nil {
					return err
				}
				if err := persistServer(st, cfg); err != nil {
					return err
				}
```

(`store` is already imported in `control.go`.)

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./cmd/marshal/ -run TestPersistServer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/control.go cmd/marshal/control_test.go
git commit -m "feat(cli): persist server: block on start so the daemon connects"
```

---

### Task 11: Full gate + manual smoke + handoff

**Files:**
- Create: `docs/handoffs/2026-06-17-m7-central-server-connection.md`

- [ ] **Step 1: Run the full gate**

Run: `gofmt -l . && go vet ./... && go build ./... && go test ./... -race -count=1`
Expected: `gofmt` prints nothing; vet/build/test all clean.

- [ ] **Step 2: Manual smoke (two terminals)**

```bash
go build -o marshal ./cmd/marshal
# terminal 1: central server
./marshal server --listen :9000
# terminal 2: an agent that connects
export XDG_DATA_HOME=/tmp/m7smoke
cat > /tmp/m7app.yaml <<'EOF'
server:
  address: localhost:9000
  name: dev-1
apps:
  - name: ticker
    cmd: /bin/sh
    args: ["-c", "while true; do date; sleep 1; done"]
EOF
./marshal start /tmp/m7app.yaml      # auto-spawns the daemon, which dials the server
./marshal fleet ps --server localhost:9000   # shows dev-1 / ticker / online
./marshal kill                       # stop the daemon
sleep 12
./marshal fleet ps --server localhost:9000   # dev-1 now shows offline
```
Expected: `fleet ps` lists agent `dev-1` with the `ticker` proc `online`, then `offline` after `kill` + the offline window.

- [ ] **Step 3: Write the handoff**

Write `docs/handoffs/2026-06-17-m7-central-server-connection.md` per the CLAUDE.md handoff convention (current state, branch, what changed and why, build/run/test, deferred items — carry forward the §2 non-goals and the "no hot-reload of an already-running daemon" limitation — and the concrete next step: finish the branch via the finishing-a-development-branch flow, then M8 metric/log streaming + server-side storage).

- [ ] **Step 4: Commit**

```bash
git add docs/handoffs/2026-06-17-m7-central-server-connection.md
git commit -m "docs: M7 central-server connection completion handoff"
```

---

## Self-Review

**Spec coverage:**
- §3 server packaging (`marshal server`, `internal/server`) → Tasks 5, 8. ✓
- §3 agent config via `server:` block → Task 2; delivery via `fleet.json` → Tasks 3, 10, 7. ✓
- §3 agent name defaults to hostname → Task 7 wiring. ✓
- §3 stream bidi, upstream only → Task 1 (proto reserves `ServerMessage`), Tasks 5–6 (only Hello/snapshot up, HelloAck down). ✓
- §3 in-memory server → Task 4. ✓
- §3 `marshal fleet ps`, own `--server` (flag→env→localhost:9000) → Task 9. ✓
- §3 CLI↔server via gRPC `ListFleet` → Tasks 5, 9. ✓
- §4 contract reuses `ProcInfo` → Task 1. ✓
- §5 periodic 2s push doubling as heartbeat, reconnect/backoff, standalone, decoupled `SnapshotFunc`, `run` not wired → Tasks 6, 7. ✓
- §6 registry, Open/Update/Close, connected = streamOpen && fresh, offlineAfter 10s, retain last snapshot → Tasks 4, 5. ✓
- §8 error handling: client errors never fatal (Task 6 `Run` logs + backoff, never panics); server drops bad streams (Task 5 `Connect` returns on Recv error after `Close`); CLI connection error surfaces with non-zero exit (Task 9 returns the gRPC error from `RunE`). ✓
- §9 tests: client cadence + reconnect (Task 6), registry + server incl. offline (Tasks 4–5), config parse (Task 2), e2e wire path (Task 6 against real `internal/server`). ✓

**Placeholder scan:** No TBD/TODO; every code step contains complete code; every test step contains real assertions. ✓

**Type consistency:** `SnapshotFunc = func() []*pb.ProcInfo` is defined in Task 6 and consumed identically in Task 7. `Registry` methods `Open/Update/Close/List` defined in Task 4 are used unchanged in Task 5. `resolveServer`/`printFleet`/`persistServer` signatures match between their definition tasks and tests. `server.Serve(ctx, lis, reg)` signature is identical across Tasks 5, 6, 8. ✓
