package notify

import (
	"context"
	"testing"
	"time"
)

// events returns a copy of what the capture emitter recorded. recEmitter (with
// its mu/evs fields and Emit/count methods) is defined in detector_test.go,
// same package; this just adds the accessor the coalescer tests need.
func (r *recEmitter) events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.evs))
	copy(out, r.evs)
	return out
}

func intPtr(n int) *int { return &n }

// newTestCoalescer wires a Coalescer to a recEmitter with a fixed-window
// fakeStore (both helpers are defined elsewhere in the package's tests).
func newTestCoalescer(window int, now func() time.Time, opts ...CoalesceOption) (*Coalescer, *fakeStore, *recEmitter) {
	st := &fakeStore{settings: Settings{CooldownSeconds: 300, CoalesceWindowSeconds: intPtr(window)}}
	cap := &recEmitter{}
	opts = append([]CoalesceOption{WithCoalesceClock(now)}, opts...)
	return NewCoalescer(cap, st, opts...), st, cap
}

func TestCoalesceMergesCrashThenRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	c, _, cap := newTestCoalescer(10, func() time.Time { return now })
	c.Emit(Event{Type: EventCrash, Agent: "a", Process: "p", Detail: "crashed"})
	if n := len(cap.events()); n != 0 {
		t.Fatalf("alert should be buffered, got %d forwarded", n)
	}
	now = now.Add(4 * time.Second)
	c.Emit(Event{Type: EventRecovered, Agent: "a", Process: "p", Detail: "recovered after crash"})
	ev := cap.events()
	if len(ev) != 1 {
		t.Fatalf("want 1 merged event, got %d", len(ev))
	}
	if ev[0].Type != EventCrash {
		t.Fatalf("merged event should keep the crash type, got %s", ev[0].Type)
	}
	if ev[0].ResolvedIn != 4*time.Second {
		t.Fatalf("want ResolvedIn 4s, got %s", ev[0].ResolvedIn)
	}
}

func TestCoalesceFlushesUnrecoveredAlert(t *testing.T) {
	now := time.Unix(1000, 0)
	c, _, cap := newTestCoalescer(10, func() time.Time { return now })
	c.Emit(Event{Type: EventCrash, Agent: "a", Process: "p"})
	c.flush(now.Add(5 * time.Second))
	if n := len(cap.events()); n != 0 {
		t.Fatalf("should not flush before window, got %d", n)
	}
	c.flush(now.Add(10 * time.Second))
	ev := cap.events()
	if len(ev) != 1 || ev[0].Type != EventCrash || ev[0].ResolvedIn != 0 {
		t.Fatalf("want 1 plain crash after window, got %+v", ev)
	}
}

func TestCoalesceForwardsLoneRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	c, _, cap := newTestCoalescer(10, func() time.Time { return now })
	c.Emit(Event{Type: EventRecovered, Agent: "a", Process: "p"})
	ev := cap.events()
	if len(ev) != 1 || ev[0].Type != EventRecovered {
		t.Fatalf("lone recovery should pass through, got %+v", ev)
	}
}

func TestCoalesceRecrashResetsWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	c, _, cap := newTestCoalescer(10, func() time.Time { return now })
	c.Emit(Event{Type: EventCrash, Agent: "a", Process: "p"})
	reset := now.Add(8 * time.Second)
	now = reset
	c.Emit(Event{Type: EventCrash, Agent: "a", Process: "p"}) // re-crash refreshes the buffer
	c.flush(reset.Add(5 * time.Second))                       // only +5s since reset: not due
	if n := len(cap.events()); n != 0 {
		t.Fatalf("re-crash should reset the window, got %d", n)
	}
	c.flush(reset.Add(10 * time.Second)) // +10s since reset: due
	if n := len(cap.events()); n != 1 {
		t.Fatalf("want 1 flushed after reset window, got %d", n)
	}
}

func TestCoalesceDisabledPassesThrough(t *testing.T) {
	now := time.Unix(1000, 0)
	c, _, cap := newTestCoalescer(0, func() time.Time { return now }) // window 0 = disabled
	c.Emit(Event{Type: EventCrash, Agent: "a", Process: "p"})
	c.Emit(Event{Type: EventRecovered, Agent: "a", Process: "p"})
	ev := cap.events()
	if len(ev) != 2 || ev[0].Type != EventCrash || ev[1].Type != EventRecovered {
		t.Fatalf("disabled coalescing should forward both events as-is, got %+v", ev)
	}
}

func TestCoalesceMergesAgentDownThenUp(t *testing.T) {
	now := time.Unix(1000, 0)
	c, _, cap := newTestCoalescer(10, func() time.Time { return now })
	c.Emit(Event{Type: EventAgentDown, Agent: "a"})
	now = now.Add(3 * time.Second)
	c.Emit(Event{Type: EventAgentUp, Agent: "a"})
	ev := cap.events()
	if len(ev) != 1 || ev[0].Type != EventAgentDown || ev[0].ResolvedIn != 3*time.Second {
		t.Fatalf("want 1 merged agent_down, got %+v", ev)
	}
}

func TestCoalesceDrainsPendingOnShutdown(t *testing.T) {
	now := time.Unix(1000, 0)
	// Long sweep so only the shutdown drain can flush.
	c, _, cap := newTestCoalescer(10, func() time.Time { return now }, WithSweepInterval(time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	c.Emit(Event{Type: EventCrash, Agent: "a", Process: "p"})
	cancel()
	<-done
	if n := len(cap.events()); n != 1 {
		t.Fatalf("shutdown should drain pending alert, got %d", n)
	}
}
