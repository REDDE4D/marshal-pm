package server

import (
	"context"
	"errors"
	"sync"

	"github.com/REDDE4D/marshal-pm/internal/pb"
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
