# M9 — Fleet Command Channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let an operator drive `marshal fleet start|stop|restart|delete <agent> ...` against the central server, which routes the command down the agent's existing stream, the agent executes it and reports the result back up, and the CLI prints the outcome.

**Architecture:** Commands multiplex on the existing `Fleet.Connect` bidirectional stream. A per-agent **command broker** on the server holds each connected agent's serialized send path plus a pending-request map keyed by a monotonic request id. A unary `FleetControl` RPC allocates an id, sends a `Command` down, and blocks until the matching `CommandResult` returns up the stream (or the RPC context expires). The agent runs a receiver goroutine that executes commands via the daemon's existing manager logic and auto-saves after start/delete.

**Tech Stack:** Go 1.26, gRPC / protobuf (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`, regenerated via `go generate ./internal/pb`), cobra CLI.

## Global Constraints

- TDD: write the failing test first, then the implementation.
- Never hand-edit `*.pb.go`. Regenerate with `go generate ./internal/pb`. If that can't find `protoc`, run from `internal/pb`: `protoc --go_out=../.. --go_opt=module=marshal --go-grpc_out=../.. --go-grpc_opt=module=marshal -I ../../proto ../../proto/marshal/v1/daemon.proto ../../proto/marshal/v1/fleet.proto`.
- Module path is `marshal`; imports are `marshal/internal/...`.
- Gate before finishing: `gofmt -l .` (silent), `go vet ./...` (clean), `go build ./...`, `go test ./... -race -count=1` (all pass).
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Branch: `m9-command-channel` (already created).
- Auth is out of scope (M10). Server stays unauthenticated.

## File Structure

- `proto/marshal/v1/fleet.proto` — MODIFY: add `ControlOp`, `ControlResult`, `Command`, `CommandResult`, `FleetControlRequest`, `FleetControlResponse`; add oneof fields to `ServerMessage`/`AgentMessage`; add `FleetControl` RPC.
- `internal/pb/*.pb.go` — REGENERATED (do not hand-edit).
- `internal/server/broker.go` — CREATE: `broker` + `session` (serialized send, pending map, dispatch/deliver/failAll).
- `internal/server/broker_test.go` — CREATE: broker/session unit tests.
- `internal/server/server.go` — MODIFY: add `broker` to `Server`; wire register/deliver/teardown into `Connect`; route HelloAck through the session; add `FleetControl`.
- `internal/server/server_test.go` — MODIFY: `FleetControl` not-connected test (fake stream).
- `internal/server/e2e_test.go` — MODIFY: real-server round-trip command e2e (real `fleet.New` client + fake executor).
- `internal/fleet/client.go` — MODIFY: `CommandFunc` type, `WithCommands` option, per-connection serialized send, receiver goroutine.
- `internal/fleet/client_test.go` — MODIFY: receiver executes a command and ships the result back.
- `internal/daemon/command.go` — CREATE: `doStart` extraction + `handleFleetCommand`.
- `internal/daemon/command_test.go` — CREATE: `handleFleetCommand` drives the manager and saves on start/delete.
- `internal/daemon/server.go` — MODIFY: `Start` calls `doStart`; wire `fleet.WithCommands(srv.handleFleetCommand)`.
- `cmd/marshal/fleet.go` — MODIFY: add `fleet start|stop|restart|delete` commands + a `fleetControl` helper.
- `cmd/marshal/fleet_test.go` — MODIFY (or e2e in cmd): control command surface.

---

### Task 1: Proto — command messages, oneof fields, FleetControl RPC

**Files:**
- Modify: `proto/marshal/v1/fleet.proto`
- Regenerate: `internal/pb/fleet.pb.go`, `internal/pb/fleet_grpc.pb.go`
- Test: `internal/pb/m9_types_test.go` (Create)

**Interfaces:**
- Produces (generated Go types other tasks consume):
  - `pb.ControlOp` with `GetOp()` returning one of `*pb.ControlOp_Stop`, `*pb.ControlOp_Restart`, `*pb.ControlOp_Delete` (each wrapping `*pb.Selector`), `*pb.ControlOp_Start` (wrapping `*pb.StartRequest`).
  - `pb.ControlResult{Ok bool, Error string, Procs []*pb.ProcInfo}`.
  - `pb.Command{RequestId uint64, Op *pb.ControlOp}`, `pb.CommandResult{RequestId uint64, Result *pb.ControlResult}`.
  - `pb.ServerMessage_Command{Command *pb.Command}`, `pb.AgentMessage_Result{Result *pb.CommandResult}`.
  - `pb.FleetControlRequest{AgentName string, Op *pb.ControlOp}`, `pb.FleetControlResponse{Result *pb.ControlResult}`.
  - `FleetClient.FleetControl(ctx, *pb.FleetControlRequest) (*pb.FleetControlResponse, error)` and the matching `FleetServer` method.

- [ ] **Step 1: Edit the proto — shared op/result shapes**

In `proto/marshal/v1/fleet.proto`, after the `MetricBatch`/`LogBatch` message blocks (anywhere at top level is fine), add:

```proto
// M9 — control-plane command surface, shared by the down-stream Command and the
// CLI's unary FleetControl so the op/result shapes have one definition.
message ControlOp {
  oneof op {
    Selector     stop    = 1;
    Selector     restart = 2;
    Selector     delete  = 3;
    StartRequest start   = 4; // reuses AppSpec from daemon.proto
  }
}

message ControlResult {
  bool              ok    = 1;
  string            error = 2; // set when ok == false
  repeated ProcInfo procs = 3; // affected instances on success
}

message Command       { uint64 request_id = 1; ControlOp     op     = 2; } // server -> agent
message CommandResult { uint64 request_id = 1; ControlResult result = 2; } // agent -> server

message FleetControlRequest  { string agent_name = 1; ControlOp op = 2; }
message FleetControlResponse { ControlResult result = 1; }
```

(`Selector` and `StartRequest` come from `daemon.proto`, already imported; `ProcInfo` likewise.)

- [ ] **Step 2: Add the oneof fields and the RPC**

In the `ServerMessage` oneof, replace the reserved comment with the command field:

```proto
message ServerMessage {
  oneof msg {
    HelloAck hello_ack = 1;
    Command  command   = 2; // M9: control command routed down to the agent
  }
}
```

In the `AgentMessage` oneof, add the result field:

```proto
message AgentMessage {
  oneof msg {
    Hello hello = 1;
    StateSnapshot snapshot = 2;
    MetricBatch metrics = 3;
    LogBatch logs = 4;
    CommandResult result = 5; // M9: result of a routed command
  }
}
```

In the `Fleet` service block, add:

```proto
  // Route a control command to one agent and return its result (M9).
  rpc FleetControl(FleetControlRequest) returns (FleetControlResponse);
```

- [ ] **Step 3: Regenerate the pb**

Run: `go generate ./internal/pb`
Expected: no output; `git status` shows `internal/pb/fleet.pb.go` and `internal/pb/fleet_grpc.pb.go` modified.
(If `protoc` isn't found, use the direct command from Global Constraints.)

- [ ] **Step 4: Write a type-shape test**

Create `internal/pb/m9_types_test.go`:

```go
package pb

import "testing"

// TestM9Types pins the generated command surface so a bad regeneration is caught.
func TestM9Types(t *testing.T) {
	cmd := &Command{RequestId: 7, Op: &ControlOp{Op: &ControlOp_Restart{Restart: &Selector{Target: "api"}}}}
	if cmd.GetOp().GetRestart().GetTarget() != "api" {
		t.Fatal("restart selector did not round-trip")
	}
	sm := &ServerMessage{Msg: &ServerMessage_Command{Command: cmd}}
	if sm.GetCommand().GetRequestId() != 7 {
		t.Fatal("ServerMessage_Command did not round-trip")
	}
	res := &CommandResult{RequestId: 7, Result: &ControlResult{Ok: true, Procs: []*ProcInfo{{Name: "api"}}}}
	am := &AgentMessage{Msg: &AgentMessage_Result{Result: res}}
	if !am.GetResult().GetResult().GetOk() || am.GetResult().GetResult().GetProcs()[0].GetName() != "api" {
		t.Fatal("AgentMessage_Result did not round-trip")
	}
	start := &ControlOp{Op: &ControlOp_Start{Start: &StartRequest{Apps: []*AppSpec{{Name: "web"}}}}}
	if start.GetStart().GetApps()[0].GetName() != "web" {
		t.Fatal("start op did not round-trip")
	}
}
```

- [ ] **Step 5: Build + run**

Run: `go build ./... && go test ./internal/pb/ -run TestM9Types -v`
Expected: build clean; test PASS.

- [ ] **Step 6: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/fleet.pb.go internal/pb/fleet_grpc.pb.go internal/pb/m9_types_test.go
git commit -m "proto: M9 fleet command surface (Command/ControlResult/FleetControl)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Server command broker

**Files:**
- Create: `internal/server/broker.go`
- Test: `internal/server/broker_test.go`

**Interfaces:**
- Consumes: `pb.ServerMessage`, `pb.Command`, `pb.CommandResult`, `pb.ControlResult`, `pb.ControlOp` (Task 1).
- Produces (used by Task 3):
  - `newBroker() *broker`
  - `(*broker) register(name string, send func(*pb.ServerMessage) error) *session`
  - `(*broker) unregister(name string, s *session)` — removes only if `s` is still the current session for `name`.
  - `(*broker) get(name string) (*session, bool)`
  - `(*session) sendMsg(m *pb.ServerMessage) error` — serialized downstream send (used for HelloAck + commands).
  - `(*session) dispatch(ctx context.Context, op *pb.ControlOp) (*pb.ControlResult, error)` — sends a command, waits for the reply or ctx; returns `errDisconnected` if the session is torn down first.
  - `(*session) deliver(r *pb.CommandResult)` — routes an up-stream result to its waiter (no-op for unknown ids).
  - `(*session) failAll()` — fails every pending request (called on disconnect).
  - `var errDisconnected = errors.New("agent disconnected")`

- [ ] **Step 1: Write the failing tests**

Create `internal/server/broker_test.go`:

```go
package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"marshal/internal/pb"
)

func restartOp(target string) *pb.ControlOp {
	return &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: target}}}
}

// dispatch sends a command down and the matching reply routes back to the caller.
func TestSessionDispatchRoundTrip(t *testing.T) {
	b := newBroker()
	var sent []*pb.ServerMessage
	var mu sync.Mutex
	sess := b.register("web-1", func(m *pb.ServerMessage) error {
		mu.Lock()
		sent = append(sent, m)
		mu.Unlock()
		return nil
	})

	done := make(chan *pb.ControlResult, 1)
	go func() {
		res, err := sess.dispatch(context.Background(), restartOp("api"))
		if err != nil {
			t.Errorf("dispatch: %v", err)
		}
		done <- res
	}()

	// Wait for the command to be sent, then reply with its request id.
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(sent) == 1 })
	id := sent[0].GetCommand().GetRequestId()
	sess.deliver(&pb.CommandResult{RequestId: id, Result: &pb.ControlResult{Ok: true}})

	select {
	case res := <-done:
		if !res.GetOk() {
			t.Fatalf("result ok = false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch never returned")
	}
}

// failAll unblocks every pending dispatch with errDisconnected.
func TestSessionFailAllOnDisconnect(t *testing.T) {
	b := newBroker()
	sess := b.register("web-1", func(*pb.ServerMessage) error { return nil })

	errc := make(chan error, 1)
	go func() {
		_, err := sess.dispatch(context.Background(), restartOp("api"))
		errc <- err
	}()
	waitFor(t, func() bool { return sess.pendingLen() == 1 })
	sess.failAll()

	select {
	case err := <-errc:
		if !errors.Is(err, errDisconnected) {
			t.Fatalf("dispatch err = %v, want errDisconnected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch never returned after failAll")
	}
}

// A ctx deadline removes the pending entry and returns ctx.Err().
func TestSessionDispatchContextCancel(t *testing.T) {
	b := newBroker()
	sess := b.register("web-1", func(*pb.ServerMessage) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := sess.dispatch(ctx, restartOp("api")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dispatch err = %v, want DeadlineExceeded", err)
	}
	if sess.pendingLen() != 0 {
		t.Fatalf("pending leaked after ctx cancel: %d", sess.pendingLen())
	}
}

// unregister only drops the session if it is still the current one.
func TestBrokerRegisterUnregister(t *testing.T) {
	b := newBroker()
	s1 := b.register("web-1", func(*pb.ServerMessage) error { return nil })
	if got, ok := b.get("web-1"); !ok || got != s1 {
		t.Fatal("get did not return the registered session")
	}
	s2 := b.register("web-1", func(*pb.ServerMessage) error { return nil }) // reconnect supersedes
	b.unregister("web-1", s1)                                                // stale; must be ignored
	if got, ok := b.get("web-1"); !ok || got != s2 {
		t.Fatal("stale unregister dropped the current session")
	}
	b.unregister("web-1", s2)
	if _, ok := b.get("web-1"); ok {
		t.Fatal("current session not removed")
	}
}
```

Note: `waitFor` already exists in `server_test.go`. The tests use a `sess.pendingLen()` test-helper; add it in Step 2.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/server/ -run 'TestSession|TestBrokerRegister' -v`
Expected: FAIL — `undefined: newBroker` (and friends).

- [ ] **Step 3: Implement the broker**

Create `internal/server/broker.go`:

```go
package server

import (
	"context"
	"errors"
	"sync"

	"marshal/internal/pb"
)

// errDisconnected is returned by dispatch when the agent's session is torn down
// (stream closed) before its command result arrives.
var errDisconnected = errors.New("agent disconnected")

// session is one connected agent's downstream control channel: a serialized
// send path plus the in-flight request table keyed by request id.
type session struct {
	sendMu sync.Mutex // serializes downstream Send (gRPC forbids concurrent Send)
	send   func(*pb.ServerMessage) error

	mu      sync.Mutex
	nextID  uint64
	pending map[uint64]chan *pb.ControlResult
}

func newSession(send func(*pb.ServerMessage) error) *session {
	return &session{send: send, pending: map[uint64]chan *pb.ControlResult{}}
}

// sendMsg serializes all downstream sends for this agent.
func (s *session) sendMsg(m *pb.ServerMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.send(m)
}

// dispatch registers a pending request, sends the command down, and waits for
// the matching result, ctx expiry, or disconnect.
func (s *session) dispatch(ctx context.Context, op *pb.ControlOp) (*pb.ControlResult, error) {
	ch := make(chan *pb.ControlResult, 1)
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.sendMsg(&pb.ServerMessage{Msg: &pb.ServerMessage_Command{
		Command: &pb.Command{RequestId: id, Op: op},
	}}); err != nil {
		s.remove(id)
		return nil, err
	}

	select {
	case res, ok := <-ch:
		if !ok {
			return nil, errDisconnected
		}
		return res, nil
	case <-ctx.Done():
		s.remove(id)
		return nil, ctx.Err()
	}
}

// deliver routes an up-stream result to its waiter; unknown ids are dropped
// (e.g. a request that already timed out).
func (s *session) deliver(r *pb.CommandResult) {
	s.mu.Lock()
	ch := s.pending[r.GetRequestId()]
	delete(s.pending, r.GetRequestId())
	s.mu.Unlock()
	if ch != nil {
		ch <- r.GetResult() // buffered cap 1: never blocks
	}
}

// failAll closes every pending channel so blocked dispatch calls return
// errDisconnected. Called once when the stream tears down.
func (s *session) failAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
}

func (s *session) remove(id uint64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *session) pendingLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// broker maps connected agent names to their live sessions.
type broker struct {
	mu       sync.Mutex
	sessions map[string]*session
}

func newBroker() *broker { return &broker{sessions: map[string]*session{}} }

// register installs a new session for name (superseding any previous one) and
// returns it.
func (b *broker) register(name string, send func(*pb.ServerMessage) error) *session {
	s := newSession(send)
	b.mu.Lock()
	b.sessions[name] = s
	b.mu.Unlock()
	return s
}

// unregister removes name's session only if it is still s (a later reconnect
// must not be dropped by an earlier connection's teardown).
func (b *broker) unregister(name string, s *session) {
	b.mu.Lock()
	if b.sessions[name] == s {
		delete(b.sessions, name)
	}
	b.mu.Unlock()
}

func (b *broker) get(name string) (*session, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[name]
	return s, ok
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/server/ -run 'TestSession|TestBrokerRegister' -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/broker.go internal/server/broker_test.go
git commit -m "server: per-agent command broker (sessions, dispatch, correlation)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Server — wire broker into Connect + FleetControl RPC

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/server_test.go` (add `TestFleetControlNotConnected`)

**Interfaces:**
- Consumes: `broker`, `session`, `errDisconnected` (Task 2); `pb.FleetControlRequest/Response`, `pb.AgentMessage_Result` (Task 1).
- Produces: `(*Server) FleetControl(ctx, *pb.FleetControlRequest) (*pb.FleetControlResponse, error)`; `Server.broker` field populated by `NewServer`.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/server_test.go`:

```go
// FleetControl against an agent with no live session is Unavailable.
func TestFleetControlNotConnected(t *testing.T) {
	srv := NewServer(NewRegistry(), nil, nil)
	_, err := srv.FleetControl(context.Background(), &pb.FleetControlRequest{
		AgentName: "ghost",
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "api"}}},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("FleetControl on absent agent err = %v, want Unavailable", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/server/ -run TestFleetControlNotConnected -v`
Expected: FAIL — `srv.FleetControl undefined`.

- [ ] **Step 3: Add the broker field and constructor wiring**

In `internal/server/server.go`, extend the struct and `NewServer`:

```go
type Server struct {
	pb.UnimplementedFleetServer
	reg    *Registry
	stores *stores
	logs   *logStores
	broker *broker
}

// NewServer wires a Fleet server to a registry and (optional) metric/log stores.
func NewServer(reg *Registry, ss *stores, ls *logStores) *Server {
	return &Server{reg: reg, stores: ss, logs: ls, broker: newBroker()}
}
```

- [ ] **Step 4: Wire the session into Connect**

In `Connect`, register the session at `Hello`, route the `HelloAck` through it, add a `CommandResult` case, and tear the session down when the stream ends. Replace the existing `Connect` body with:

```go
func (s *Server) Connect(stream pb.Fleet_ConnectServer) error {
	var name string
	var sess *session
	defer func() {
		if sess != nil {
			s.broker.unregister(name, sess)
			sess.failAll()
		}
	}()
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
			if name == "" {
				return status.Error(codes.InvalidArgument, "agent_name must not be empty")
			}
			s.reg.Open(name)
			sess = s.broker.register(name, stream.Send)
			var watermark, logWM int64
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					watermark, _ = st.MaxTs()
				}
			}
			if s.logs != nil {
				if st, err := s.logs.get(name); err == nil {
					logWM, _ = st.MaxTs()
				}
			}
			_ = sess.sendMsg(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: watermark, LastLogTsMs: logWM},
			}})
		case *pb.AgentMessage_Snapshot:
			if name != "" {
				s.reg.Update(name, m.Snapshot.GetProcs())
			}
		case *pb.AgentMessage_Metrics:
			if name != "" && s.stores != nil {
				s.storeBatch(name, m.Metrics.GetSamples())
			}
		case *pb.AgentMessage_Logs:
			if name != "" && s.logs != nil {
				s.storeLogBatch(name, m.Logs.GetLines())
			}
		case *pb.AgentMessage_Result:
			if sess != nil {
				sess.deliver(m.Result)
			}
		}
	}
}
```

(`stream.Send` satisfies `func(*pb.ServerMessage) error`, so it is passed directly to `register`.)

- [ ] **Step 5: Add the FleetControl handler**

Add to `internal/server/server.go` (near `ListFleet`):

```go
// FleetControl routes a control command to one connected agent over its existing
// stream and returns the agent's result. The RPC context bounds the wait.
func (s *Server) FleetControl(ctx context.Context, req *pb.FleetControlRequest) (*pb.FleetControlResponse, error) {
	sess, ok := s.broker.get(req.GetAgentName())
	if !ok {
		return nil, status.Errorf(codes.Unavailable, "agent %q not connected", req.GetAgentName())
	}
	res, err := sess.dispatch(ctx, req.GetOp())
	if err != nil {
		if errors.Is(err, errDisconnected) {
			return nil, status.Errorf(codes.Unavailable, "agent %q disconnected", req.GetAgentName())
		}
		return nil, status.FromContextError(err).Err()
	}
	return &pb.FleetControlResponse{Result: res}, nil
}
```

Add `"errors"` to the import block in `server.go`.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/server/ -race -count=1`
Expected: PASS (existing Connect tests still green — note `TestConnectAcksWatermarkAndStoresBatch` still finds the HelloAck at `st.sent[0]` because it is now sent via the session, which calls the same `stream.Send`).

- [ ] **Step 7: Commit**

```bash
git add internal/server/server.go internal/server/server_test.go
git commit -m "server: route commands via broker in Connect + FleetControl RPC

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Agent client — WithCommands + receiver goroutine

**Files:**
- Modify: `internal/fleet/client.go`
- Test: `internal/fleet/client_test.go`

**Interfaces:**
- Consumes: `pb.Command`, `pb.CommandResult`, `pb.ControlResult`, `pb.AgentMessage_Result`, `pb.ServerMessage_Command` (Task 1).
- Produces:
  - `type CommandFunc func(*pb.Command) *pb.ControlResult`
  - `func WithCommands(fn CommandFunc) Option`
  - Behavior: while connected, received `Command`s are executed by `fn` and a `CommandResult` (same `request_id`) is sent back up; sends are serialized with the snapshot/metric/log pushes.

- [ ] **Step 1: Write the failing test**

Add to `internal/fleet/client_test.go`. This needs a real in-process Fleet server stub that sends a `Command` after `Hello` and records the `CommandResult`. If `client_test.go` already has a stub Fleet server, extend it; otherwise add this self-contained test:

```go
func TestClientExecutesCommandAndRepliesResult(t *testing.T) {
	// Stub Fleet server: ack hello, push one restart command, capture the reply.
	gotReply := make(chan *pb.CommandResult, 1)
	srv := &cmdStubServer{gotReply: gotReply}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	executed := make(chan string, 1)
	c := New(lis.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		WithInterval(20*time.Millisecond),
		WithCommands(func(cmd *pb.Command) *pb.ControlResult {
			executed <- cmd.GetOp().GetRestart().GetTarget()
			return &pb.ControlResult{Ok: true, Procs: []*pb.ProcInfo{{Name: "api"}}}
		}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	select {
	case target := <-executed:
		if target != "api" {
			t.Fatalf("executed target = %q, want api", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command was never executed")
	}
	select {
	case reply := <-gotReply:
		if !reply.GetResult().GetOk() || reply.GetResult().GetProcs()[0].GetName() != "api" {
			t.Fatalf("reply = %v, want ok with api proc", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("result was never received by the server")
	}
}

// cmdStubServer acks Hello, sends one restart Command, and captures the reply.
type cmdStubServer struct {
	pb.UnimplementedFleetServer
	gotReply chan *pb.CommandResult
}

func (s *cmdStubServer) Connect(stream pb.Fleet_ConnectServer) error {
	// First inbound message is Hello.
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{HelloAck: &pb.HelloAck{}}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_Command{Command: &pb.Command{
		RequestId: 1,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "api"}}},
	}}}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if r := msg.GetResult(); r != nil {
			s.gotReply <- r
			return nil
		}
	}
}
```

Ensure the test imports `net`, `grpc` (`google.golang.org/grpc`). Check the existing imports in `client_test.go` and add what's missing.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/fleet/ -run TestClientExecutesCommand -v`
Expected: FAIL — `undefined: WithCommands`.

- [ ] **Step 3: Add the field, option, and serialized send**

In `internal/fleet/client.go`, add the type + option and a field on `Client`:

```go
// CommandFunc executes a control command and returns its result.
type CommandFunc func(*pb.Command) *pb.ControlResult

// WithCommands enables down-stream command handling sourced from fn.
func WithCommands(fn CommandFunc) Option { return func(c *Client) { c.commands = fn } }
```

Add `commands CommandFunc` to the `Client` struct (next to `logs LogsFunc`).

- [ ] **Step 4: Serialize sends and start the receiver in connectOnce**

Rework `connectOnce` to route every `Send` through one mutex and run a receiver goroutine. Replace the body from after the dial/`stream` creation. The key changes: introduce a `send` closure guarded by a local mutex, pass it to the push helpers, and add the receiver. Updated `connectOnce`:

```go
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

	var sendMu sync.Mutex
	send := func(m *pb.AgentMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(m)
	}

	if err := send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{
		Hello: &pb.Hello{AgentName: c.name, MarshalVersion: c.version},
	}}); err != nil {
		return false, err
	}
	// Receive the HelloAck to seed the metric and log watermarks.
	var watermark, logWM int64
	if ack, err := stream.Recv(); err != nil {
		return true, err
	} else if a := ack.GetHelloAck(); a != nil {
		watermark = a.GetLastMetricTsMs()
		logWM = a.GetLastLogTsMs()
	}

	// Receiver goroutine: handle commands until the stream errors.
	recvErr := make(chan error, 1)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			if cmd := msg.GetCommand(); cmd != nil && c.commands != nil {
				res := c.commands(cmd)
				_ = send(&pb.AgentMessage{Msg: &pb.AgentMessage_Result{
					Result: &pb.CommandResult{RequestId: cmd.GetRequestId(), Result: res},
				}})
			}
		}
	}()

	if err := c.pushSnapshot(send); err != nil {
		return true, err
	}
	if err := c.pushMetrics(send, &watermark); err != nil {
		return true, err
	}
	if err := c.pushLogs(send, &logWM); err != nil {
		return true, err
	}

	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-recvErr:
			return true, err
		case <-t.C:
			if err := c.pushSnapshot(send); err != nil {
				return true, err
			}
			if err := c.pushMetrics(send, &watermark); err != nil {
				return true, err
			}
			if err := c.pushLogs(send, &logWM); err != nil {
				return true, err
			}
		}
	}
}
```

- [ ] **Step 5: Update the push helpers to take a send func**

Change the three helpers' signatures from `(stream pb.Fleet_ConnectClient)` to `(send func(*pb.AgentMessage) error)` and replace their `stream.Send(...)` calls with `send(...)`:

```go
func (c *Client) pushSnapshot(send func(*pb.AgentMessage) error) error {
	return send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{
		Snapshot: &pb.StateSnapshot{Procs: c.snapshot()},
	}})
}

func (c *Client) pushMetrics(send func(*pb.AgentMessage) error, watermark *int64) error {
	if c.metrics == nil {
		return nil
	}
	samples := c.metrics(*watermark)
	if len(samples) == 0 {
		return nil
	}
	if err := send(&pb.AgentMessage{Msg: &pb.AgentMessage_Metrics{
		Metrics: &pb.MetricBatch{Samples: samples},
	}}); err != nil {
		return err
	}
	var mx int64
	for _, s := range samples {
		if s.GetTsMs() > mx {
			mx = s.GetTsMs()
		}
	}
	if mx > *watermark {
		*watermark = mx
	}
	return nil
}

func (c *Client) pushLogs(send func(*pb.AgentMessage) error, watermark *int64) error {
	if c.logs == nil {
		return nil
	}
	lines := c.logs(*watermark)
	if len(lines) == 0 {
		return nil
	}
	if err := send(&pb.AgentMessage{Msg: &pb.AgentMessage_Logs{
		Logs: &pb.LogBatch{Lines: lines},
	}}); err != nil {
		return err
	}
	var mx int64
	for _, l := range lines {
		if l.GetTsMs() > mx {
			mx = l.GetTsMs()
		}
	}
	if mx > *watermark {
		*watermark = mx
	}
	return nil
}
```

Add `"sync"` to the imports in `client.go`.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/fleet/ -race -count=1`
Expected: PASS (existing client tests still green; new command test passes).

- [ ] **Step 7: Commit**

```bash
git add internal/fleet/client.go internal/fleet/client_test.go
git commit -m "fleet: agent receives and executes down-stream commands (WithCommands)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Daemon — command executor (doStart extraction + handleFleetCommand) and wiring

**Files:**
- Create: `internal/daemon/command.go`
- Test: `internal/daemon/command_test.go`
- Modify: `internal/daemon/server.go` (use `doStart` in `Start`; wire `WithCommands`)

**Interfaces:**
- Consumes: `pb.Command`, `pb.ControlOp`, `pb.ControlResult` (Task 1); `fleet.WithCommands` (Task 4); existing `manager.Manager` ops `Stop/Restart/Delete(target) ([]manager.InstanceSnapshot, error)`, `Add(config.App)`, `Specs()`; `store.Store.Save([]config.App)`; `Server.procList`, `appSpecToConfig`, `logPolicy`.
- Produces: `(*Server) doStart(specs []*pb.AppSpec) ([]manager.InstanceSnapshot, error)`; `(*Server) handleFleetCommand(cmd *pb.Command) *pb.ControlResult`.

- [ ] **Step 1: Write the failing test**

Create `internal/daemon/command_test.go`. It builds a `Server` with a real manager + store and asserts the command runs and persists. Use the existing helpers/patterns in `internal/daemon` tests (check an existing `*_test.go` there for how a `Server`/manager/store is constructed; mirror it). Reference test:

```go
package daemon

import (
	"context"
	"testing"
	"time"

	"marshal/internal/manager"
	"marshal/internal/pb"
	"marshal/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.NewAt(t.TempDir()) // see note below
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	mgr := manager.New(context.Background())
	return &Server{mgr: mgr, store: st}
}

func sleepSpec(name string) *pb.AppSpec {
	return &pb.AppSpec{Name: name, Cmd: "sleep", Args: []string{"30"}, Instances: 1, Restart: "no"}
}

// start deploys a new app and persists it; delete removes and persists.
func TestHandleFleetCommandStartPersists(t *testing.T) {
	s := newTestServer(t)
	defer s.mgr.StopAll()

	res := s.handleFleetCommand(&pb.Command{RequestId: 1, Op: &pb.ControlOp{
		Op: &pb.ControlOp_Start{Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("web")}}},
	}})
	if !res.GetOk() {
		t.Fatalf("start result not ok: %q", res.GetError())
	}
	if len(res.GetProcs()) != 1 || res.GetProcs()[0].GetName() != "web" {
		t.Fatalf("start procs = %v, want one web proc", res.GetProcs())
	}
	// Persisted to the dump.
	apps, err := s.store.Load()
	if err != nil {
		t.Fatalf("load dump: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "web" {
		t.Fatalf("dump = %v, want one web app (start must auto-save)", apps)
	}

	// Restart returns ok and keeps the app present.
	restart := s.handleFleetCommand(&pb.Command{RequestId: 2, Op: &pb.ControlOp{
		Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "web"}},
	}})
	if !restart.GetOk() {
		t.Fatalf("restart not ok: %q", restart.GetError())
	}

	// Delete removes and persists.
	del := s.handleFleetCommand(&pb.Command{RequestId: 3, Op: &pb.ControlOp{
		Op: &pb.ControlOp_Delete{Delete: &pb.Selector{Target: "web"}},
	}})
	if !del.GetOk() {
		t.Fatalf("delete not ok: %q", del.GetError())
	}
	apps, _ = s.store.Load()
	if len(apps) != 0 {
		t.Fatalf("dump after delete = %v, want empty (delete must auto-save)", apps)
	}
}

// an unknown selector yields ok=false with an error, not a panic.
func TestHandleFleetCommandUnknownSelector(t *testing.T) {
	s := newTestServer(t)
	res := s.handleFleetCommand(&pb.Command{RequestId: 1, Op: &pb.ControlOp{
		Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "ghost"}},
	}})
	if res.GetOk() || res.GetError() == "" {
		t.Fatalf("unknown selector result = %v, want ok=false with error", res)
	}
	_ = time.Now // keep import if unused elsewhere
}
```

> **Note for the implementer:** how `store.Store` is constructed in tests varies. Open `internal/store` and `internal/daemon/*_test.go` to find the right constructor (e.g. a `store.New()` that honors an env/temp dir, or a test helper). Use whatever the existing daemon tests use to get a temp-dir-backed store; replace `store.NewAt(t.TempDir())` accordingly. If a manager needs options (e.g. `manager.WithLogs`), construct it as other daemon tests do. The assertions on `handleFleetCommand` are what matter.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/daemon/ -run TestHandleFleetCommand -v`
Expected: FAIL — `s.handleFleetCommand undefined`.

- [ ] **Step 3: Extract `doStart` and refactor `Start`**

In `internal/daemon/server.go`, extract the start body into a method and have `Start` call it:

```go
func (s *Server) Start(_ context.Context, req *pb.StartRequest) (*pb.ProcList, error) {
	snaps, err := s.doStart(req.GetApps())
	if err != nil {
		return nil, err
	}
	return s.procList(snaps), nil
}

// doStart admits and launches one or more apps, returning the new instances.
// Shared by the Daemon.Start RPC and the fleet command executor.
func (s *Server) doStart(specs []*pb.AppSpec) ([]manager.InstanceSnapshot, error) {
	var out []manager.InstanceSnapshot
	for _, spec := range specs {
		app, err := appSpecToConfig(spec)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		if s.logs != nil {
			s.logs.SetPolicy(app.Name, logPolicy(app, s.logPolicyDefault))
		}
		snaps, err := s.mgr.Add(app)
		if err != nil {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		out = append(out, snaps...)
	}
	return out, nil
}
```

- [ ] **Step 4: Implement `handleFleetCommand`**

Create `internal/daemon/command.go`:

```go
package daemon

import (
	"marshal/internal/manager"
	"marshal/internal/pb"
)

// handleFleetCommand executes one control command from the central server and
// returns its result. Reuses the same manager logic the Daemon RPCs use, and
// auto-saves after start/delete so remote deploys survive a restart.
func (s *Server) handleFleetCommand(cmd *pb.Command) *pb.ControlResult {
	var (
		snaps     []manager.InstanceSnapshot
		err       error
		persisted bool // start/delete change the persisted spec set
	)
	switch op := cmd.GetOp().GetOp().(type) {
	case *pb.ControlOp_Start:
		snaps, err = s.doStart(op.Start.GetApps())
		persisted = true
	case *pb.ControlOp_Stop:
		snaps, err = s.mgr.Stop(op.Stop.GetTarget())
	case *pb.ControlOp_Restart:
		snaps, err = s.mgr.Restart(op.Restart.GetTarget())
	case *pb.ControlOp_Delete:
		snaps, err = s.mgr.Delete(op.Delete.GetTarget())
		persisted = true
	default:
		return &pb.ControlResult{Ok: false, Error: "empty command"}
	}
	if err != nil {
		return &pb.ControlResult{Ok: false, Error: err.Error()}
	}
	if persisted && s.store != nil {
		_ = s.store.Save(s.mgr.Specs())
	}
	return &pb.ControlResult{Ok: true, Procs: s.procList(snaps).GetProcs()}
}
```

- [ ] **Step 5: Run the daemon tests**

Run: `go test ./internal/daemon/ -race -count=1`
Expected: PASS (existing tests still green; new command tests pass).

- [ ] **Step 6: Wire the command handler into the fleet client**

In `internal/daemon/server.go` `Run`, where the fleet client is built, add the option:

```go
		fc := fleet.New(sc.Address, name, version.String(),
			fleetSnapshot(mgr, sampler),
			fleet.WithMetrics(metricsSince(mdb)),
			fleet.WithLogs(logsSince(reg)),
			fleet.WithCommands(srv.handleFleetCommand))
		go fc.Run(serveCtx)
```

- [ ] **Step 7: Build + full package test**

Run: `go build ./... && go test ./internal/daemon/ ./internal/fleet/ -race -count=1`
Expected: build clean; PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/command.go internal/daemon/command_test.go internal/daemon/server.go
git commit -m "daemon: execute fleet commands via manager, auto-save on start/delete

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: CLI — fleet start/stop/restart/delete

**Files:**
- Modify: `cmd/marshal/fleet.go`
- Test: `cmd/marshal/fleet_test.go`

**Interfaces:**
- Consumes: `FleetControl` RPC (Task 3); `appToSpec` and `printProcs` (existing in `cmd/marshal`); `resolveServer` (existing).
- Produces: `fleet start|stop|restart|delete` subcommands.

- [ ] **Step 1: Write the failing test**

Add to `cmd/marshal/fleet_test.go` a test that the command tree wires the four verbs and that selector ops build the right `ControlOp`. The simplest reliable test stands up an in-process Fleet server stub capturing the `FleetControlRequest`. If `fleet_test.go` already has a stub-server harness, reuse it. Reference:

```go
func TestFleetRestartSendsControlOp(t *testing.T) {
	captured := make(chan *pb.FleetControlRequest, 1)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, &controlStub{captured: captured})
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	cmd := fleetCmd()
	cmd.SetArgs([]string{"restart", "web-1", "api", "--server", lis.Addr().String()})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	select {
	case req := <-captured:
		if req.GetAgentName() != "web-1" || req.GetOp().GetRestart().GetTarget() != "api" {
			t.Fatalf("captured = %v, want web-1 restart api", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FleetControl was never called")
	}
}

type controlStub struct {
	pb.UnimplementedFleetServer
	captured chan *pb.FleetControlRequest
}

func (s *controlStub) FleetControl(_ context.Context, req *pb.FleetControlRequest) (*pb.FleetControlResponse, error) {
	s.captured <- req
	return &pb.FleetControlResponse{Result: &pb.ControlResult{Ok: true}}, nil
}
```

Add imports as needed (`io`, `net`, `time`, `context`, `grpc`, `pb`).

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/marshal/ -run TestFleetRestartSendsControlOp -v`
Expected: FAIL — the `restart` subcommand doesn't exist yet (`unknown command "restart"`).

- [ ] **Step 3: Implement the control commands**

In `cmd/marshal/fleet.go`, register the new commands in `fleetCmd()`:

```go
	cmd.AddCommand(fleetStartCmd())
	cmd.AddCommand(fleetSelectorCmd("stop", "Stop an app/instance on one agent",
		func(t string) *pb.ControlOp { return &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: t}}} }))
	cmd.AddCommand(fleetSelectorCmd("restart", "Restart an app/instance on one agent",
		func(t string) *pb.ControlOp { return &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: t}}} }))
	cmd.AddCommand(fleetSelectorCmd("delete", "Delete an app/instance on one agent",
		func(t string) *pb.ControlOp { return &pb.ControlOp{Op: &pb.ControlOp_Delete{Delete: &pb.Selector{Target: t}}} }))
```

Add the helpers and the two command builders to `cmd/marshal/fleet.go`:

```go
// fleetControl dials the server, sends one control op to an agent, and prints
// the resulting process table (or the agent's error).
func fleetControl(cmd *cobra.Command, serverAddr string, timeout time.Duration, agent string, op *pb.ControlOp) error {
	conn, err := grpc.NewClient(resolveServer(serverAddr),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := pb.NewFleetClient(conn).FleetControl(ctx, &pb.FleetControlRequest{
		AgentName: agent, Op: op,
	})
	if err != nil {
		return err
	}
	res := resp.GetResult()
	if !res.GetOk() {
		return fmt.Errorf("%s", res.GetError())
	}
	printProcs(cmd, &pb.ProcList{Procs: res.GetProcs()})
	return nil
}

func fleetSelectorCmd(use, short string, build func(target string) *pb.ControlOp) *cobra.Command {
	var serverAddr string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   use + " <agent> <name|id|all>",
		Short: short,
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fleetControl(cmd, serverAddr, timeout, args[0], build(args[1]))
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "command timeout")
	return cmd
}

func fleetStartCmd() *cobra.Command {
	var serverAddr string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "start <agent> <marshal.yaml>",
		Short: "Deploy and start app(s) from a marshal.yaml on one agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[1])
			if err != nil {
				return err
			}
			specs := make([]*pb.AppSpec, 0, len(cfg.Apps))
			for _, a := range cfg.Apps {
				specs = append(specs, appToSpec(a))
			}
			op := &pb.ControlOp{Op: &pb.ControlOp_Start{Start: &pb.StartRequest{Apps: specs}}}
			return fleetControl(cmd, serverAddr, timeout, args[0], op)
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "command timeout")
	return cmd
}
```

Add imports to `cmd/marshal/fleet.go`: `"marshal/internal/config"` (for `config.Load`). `fmt`, `time`, `context`, `grpc`, `insecure`, `pb` are already imported.

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/marshal/ -run TestFleet -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/marshal/fleet.go cmd/marshal/fleet_test.go
git commit -m "cli: marshal fleet start/stop/restart/delete

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: End-to-end — real server + real fleet client round-trip

**Files:**
- Modify: `internal/server/e2e_test.go`

**Interfaces:**
- Consumes: `ServeDir`/`Serve` (existing), `fleet.New` + `fleet.WithCommands` (Task 4), `FleetControl` (Task 3).

This proves the full down-channel over a real gRPC stream: a real `fleet.New` client connects to a real server; `FleetControl` routes a command down, the client's command handler runs, and the result routes back up to the unary caller. The handler is a fake executor (not the manager) so the test stays in `package server` and is fast; Task 5 already proved the manager path.

- [ ] **Step 1: Write the failing test**

Add to `internal/server/e2e_test.go`:

```go
func TestE2EFleetControlRoundTrip(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	srv := NewServer(reg, nil, nil)
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	// Real agent client whose command handler echoes the selector back.
	c := fleet.New(lis.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond),
		fleet.WithCommands(func(cmd *pb.Command) *pb.ControlResult {
			return &pb.ControlResult{Ok: true, Procs: []*pb.ProcInfo{
				{Name: cmd.GetOp().GetRestart().GetTarget(), State: "online"},
			}}
		}))
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go c.Run(cctx)

	// Wait until the agent is registered (its session exists).
	waitFor(t, func() bool { _, ok := srv.broker.get("web-1"); return ok })

	conn := e2eDialFleet(t, lis.Addr().String())
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := pb.NewFleetClient(conn).FleetControl(ctx, &pb.FleetControlRequest{
		AgentName: "web-1",
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "api"}}},
	})
	if err != nil {
		t.Fatalf("FleetControl: %v", err)
	}
	if !resp.GetResult().GetOk() || resp.GetResult().GetProcs()[0].GetName() != "api" {
		t.Fatalf("result = %v, want ok with api proc", resp.GetResult())
	}
}
```

- [ ] **Step 2: Run it to verify it fails (or passes if all prior tasks are done)**

Run: `go test ./internal/server/ -run TestE2EFleetControlRoundTrip -race -v`
Expected: PASS once Tasks 1–4 are merged (it exercises only server + fleet client). If run before Task 4, FAIL on `fleet.WithCommands`.

- [ ] **Step 3: Full gate**

Run:
```bash
gofmt -l .
go vet ./...
go build ./...
go test ./... -race -count=1
```
Expected: `gofmt -l .` prints nothing; vet clean; build clean; all packages PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/server/e2e_test.go
git commit -m "test(server): e2e fleet command round-trip over a real stream

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Smoke test + handoff

**Files:**
- Create: `docs/handoffs/2026-06-17-m9-command-channel.md`

- [ ] **Step 1: Manual smoke test**

```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m9smoke
./marshal server --listen :9000 &        # central server
# app.yaml: a `ticker` app + server:{address: localhost:9000, name: dev-1}
./marshal start /path/to/app.yaml
sleep 3
./marshal fleet ps --server localhost:9000          # dev-1 online, ticker running
./marshal fleet restart dev-1 ticker --server localhost:9000   # prints new proc table
./marshal fleet start dev-1 /path/to/other.yaml --server localhost:9000  # deploys a new app
./marshal fleet ps --server localhost:9000          # new app present
./marshal fleet delete dev-1 <newapp> --server localhost:9000
```

Confirm: restart bumps the restart count / PID; `fleet start` deploys (and the app survives `./marshal kill` + restart via resurrect, proving auto-save); `fleet delete` removes it. Capture the output for the handoff.

- [ ] **Step 2: Write the handoff**

Create `docs/handoffs/2026-06-17-m9-command-channel.md` per the CLAUDE.md handoff convention: current state, what changed this session and why, build/run/test, the smoke proof, deferred items (auth/M10, live follow, dashboard, audit log, best-effort timeout semantics), and the concrete next step (merge `m9-command-channel`; next milestone candidates M10 auth/TLS or the dashboard).

- [ ] **Step 3: Commit**

```bash
git add docs/handoffs/2026-06-17-m9-command-channel.md
git commit -m "docs: M9 fleet command-channel handoff

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- §4 wire protocol → Task 1 (all messages, oneof fields, RPC). ✓
- §5 server broker (serialized send, pending map, dispatch/deliver/failAll, FleetControl, not-connected/disconnect/timeout) → Tasks 2 & 3. ✓
- §6 agent receiver + WithCommands + reuse doStart + auto-save → Tasks 4 & 5. ✓
- §7 CLI start/stop/restart/delete + --server/--timeout + local yaml load → Task 6. ✓
- §8 testing (broker units, FleetControl not-connected, agent handler + save, e2e round-trip) → Tasks 2,3,5,4,7. ✓
- §9 deferred items → recorded in Task 8 handoff. ✓

**Placeholder scan:** No TBD/TODO; every code step shows full code. The one judgement call (store/manager test constructor in Task 5) is explicitly flagged with how to resolve it, because the exact `store` test constructor must be read from the codebase — not guessable safely.

**Type consistency:** `ControlOp`/`ControlResult`/`Command`/`CommandResult`/`FleetControlRequest`/`FleetControlResponse` used identically across tasks. `dispatch(ctx, *pb.ControlOp) (*pb.ControlResult, error)`, `deliver(*pb.CommandResult)`, `register(name, func(*pb.ServerMessage) error)`, `CommandFunc func(*pb.Command) *pb.ControlResult`, `doStart([]*pb.AppSpec) ([]manager.InstanceSnapshot, error)`, `handleFleetCommand(*pb.Command) *pb.ControlResult` are consistent between definition and use. Push helpers uniformly take `send func(*pb.AgentMessage) error`.
