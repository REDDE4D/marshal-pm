package notify

import (
	"context"
	"sync"
	"time"
)

// settingsReader is the minimal store surface the Coalescer needs (the *Store
// and the test fakeStore both satisfy it).
type settingsReader interface{ Settings() Settings }

// recoverableAlert reports whether an event is an alert that has a recovery
// counterpart and so should be buffered for coalescing.
func recoverableAlert(t EventType) bool {
	switch t {
	case EventCrash, EventRestartLoop, EventDeployFail, EventAgentDown:
		return true
	}
	return false
}

// recoveryEvent reports whether an event is a recovery that can close a
// buffered alert for the same (agent, process) key.
func recoveryEvent(t EventType) bool {
	return t == EventRecovered || t == EventAgentUp
}

// pending is a buffered alert awaiting either its recovery or window expiry.
type pending struct {
	ev Event
	at time.Time
}

// Coalescer buffers recoverable alerts and merges each with its recovery if it
// arrives within the window, forwarding the result to the wrapped Emitter
// (the Dispatcher). It implements Emitter, so it slots in front of the
// Dispatcher in the Detector → Dispatcher path.
type Coalescer struct {
	out     Emitter
	store   settingsReader
	now     func() time.Time
	sweep   time.Duration
	mu      sync.Mutex
	pending map[string]pending
}

// CoalesceOption configures a Coalescer.
type CoalesceOption func(*Coalescer)

// WithCoalesceClock overrides the clock (tests).
func WithCoalesceClock(fn func() time.Time) CoalesceOption {
	return func(c *Coalescer) { c.now = fn }
}

// WithSweepInterval overrides the flush-sweep interval (tests).
func WithSweepInterval(d time.Duration) CoalesceOption {
	return func(c *Coalescer) { c.sweep = d }
}

// NewCoalescer builds a Coalescer forwarding to out and reading its window from store.
func NewCoalescer(out Emitter, store settingsReader, opts ...CoalesceOption) *Coalescer {
	c := &Coalescer{
		out:     out,
		store:   store,
		now:     time.Now,
		sweep:   time.Second,
		pending: map[string]pending{},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func coalesceKey(e Event) string { return e.Agent + "\x00" + e.Process }

// Emit buffers recoverable alerts, merges recoveries with a buffered alert, and
// forwards everything else straight through. With the window disabled (0) it is
// a pass-through.
func (c *Coalescer) Emit(e Event) {
	if c.store.Settings().coalesceWindow() <= 0 {
		c.out.Emit(e)
		return
	}
	now := c.now()
	switch {
	case recoverableAlert(e.Type):
		c.mu.Lock()
		c.pending[coalesceKey(e)] = pending{ev: e, at: now}
		c.mu.Unlock()
	case recoveryEvent(e.Type):
		c.mu.Lock()
		p, ok := c.pending[coalesceKey(e)]
		if ok {
			delete(c.pending, coalesceKey(e))
		}
		c.mu.Unlock()
		if !ok {
			c.out.Emit(e) // no buffered alert: forward the recovery as-is
			return
		}
		merged := p.ev
		merged.ResolvedIn = now.Sub(p.at)
		if merged.ResolvedIn <= 0 {
			merged.ResolvedIn = time.Nanosecond // keep the >0 merged marker
		}
		c.out.Emit(merged)
	default:
		c.out.Emit(e)
	}
}

// flush forwards and removes every pending alert whose age has reached the
// window — alerts that did not recover in time. Pure (clock injected by caller)
// so tests can drive it without real time.
func (c *Coalescer) flush(now time.Time) {
	window := c.store.Settings().coalesceWindow()
	c.mu.Lock()
	var due []Event
	for k, p := range c.pending {
		if window <= 0 || now.Sub(p.at) >= window {
			due = append(due, p.ev)
			delete(c.pending, k)
		}
	}
	c.mu.Unlock()
	for _, e := range due {
		c.out.Emit(e)
	}
}

// drain forwards all pending alerts regardless of age (used on shutdown so a
// real crash buffered mid-window is not lost).
func (c *Coalescer) drain() {
	c.mu.Lock()
	var all []Event
	for k, p := range c.pending {
		all = append(all, p.ev)
		delete(c.pending, k)
	}
	c.mu.Unlock()
	for _, e := range all {
		c.out.Emit(e)
	}
}

// Run sweeps pending alerts on a ticker until ctx is cancelled, then drains.
func (c *Coalescer) Run(ctx context.Context) {
	t := time.NewTicker(c.sweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.drain()
			return
		case <-t.C:
			c.flush(c.now())
		}
	}
}
