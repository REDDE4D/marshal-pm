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
	b.unregister("web-1", s1)                                               // stale; must be ignored
	if got, ok := b.get("web-1"); !ok || got != s2 {
		t.Fatal("stale unregister dropped the current session")
	}
	b.unregister("web-1", s2)
	if _, ok := b.get("web-1"); ok {
		t.Fatal("current session not removed")
	}
}
