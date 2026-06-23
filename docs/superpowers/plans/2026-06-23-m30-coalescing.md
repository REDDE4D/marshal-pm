# M30 — Alert/recovery coalescing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge a transient crash-then-recover blip into a single notification instead of sending a separate alert and recovery.

**Architecture:** A new `Coalescer` unit sits between the `Detector` and the `Dispatcher` (both `Emitter`s). It buffers recoverable alerts keyed by `(agent, process)`; if the matching recovery arrives within a configurable window it forwards one merged event (carrying the original alert type), otherwise it flushes the original alert after the window. The merged event keeps the original `EventType` so the Dispatcher's existing cooldown/routing/`SuppressRecovery` logic is untouched.

**Tech Stack:** Go 1.26 (stdlib only — `context`, `sync`, `time`), React/TypeScript + Vite dashboard (embedded via `go:embed`).

## Global Constraints

- Module path is `marshal`; package imports are `marshal/internal/...`.
- TDD: write the failing test first, run it red, implement minimal code, run green, commit.
- Commit messages: imperative subject + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work stays on branch `m30-coalescing` (already created off `dev`); do not touch `main`.
- Record changelog entries under `## [Unreleased]` in `CHANGELOG.md` as part of the work.
- Verify before finishing: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (lists nothing), `make build`.
- Coalescing window config uses pointer/presence semantics like `Settings.CooldownOverrides`: `nil` = default 10s; explicit `0` = disabled (immediate); `N` = N seconds.
- Coalescing scope: `crash`, `restart_loop`, `deploy_fail`, `agent_down` are "recoverable alerts"; `recovered`, `agent_up` are "recoveries".

---

### Task 1: `Event.ResolvedIn` field + merged-notice rendering

Adds the data field that marks a coalesced event and teaches `render` to produce the combined "…then recovered" line. Nothing reads the field yet; the Coalescer (Task 3) sets it.

**Files:**
- Modify: `internal/notify/model.go` (the `Event` struct, ~lines 22-29)
- Modify: `internal/notify/render.go` (the `render` function)
- Test: `internal/notify/render_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Event.ResolvedIn time.Duration` (>0 marks a coalesced alert); `render(Event) Message` now appends a recovery line when `ResolvedIn > 0`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/notify/render_test.go`. The file currently imports only `strings` and `testing` — change the import block to add `time`:

```go
import (
	"strings"
	"testing"
	"time"
)
```

Then add:

```go
func TestRenderMergedRecovery(t *testing.T) {
	m := render(Event{Type: EventCrash, Agent: "dev-1", Process: "api", Detail: "crashed (restart #2)", ResolvedIn: 4 * time.Second})
	if !strings.Contains(m.Title, "then recovered") {
		t.Fatalf("merged title should note recovery, got %q", m.Title)
	}
	if !strings.Contains(m.Body, "recovered after 4s") {
		t.Fatalf("merged body should note recovery duration, got %q", m.Body)
	}
}

func TestRenderPlainAlertUnchanged(t *testing.T) {
	m := render(Event{Type: EventCrash, Agent: "dev-1", Process: "api", Detail: "crashed (restart #2)"})
	if strings.Contains(m.Title, "then recovered") {
		t.Fatalf("plain alert must not be marked merged, got %q", m.Title)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run 'TestRenderMergedRecovery|TestRenderPlainAlertUnchanged' -v`
Expected: compile error — `Event` has no field `ResolvedIn`.

- [ ] **Step 3: Add the field and rendering**

In `internal/notify/model.go`, add to the `Event` struct (after `Time time.Time`):

```go
	// ResolvedIn, when >0, marks a coalesced alert: the condition resolved
	// within this duration, so it renders as a single "…then recovered" notice
	// instead of a separate alert and recovery. Zero means a normal alert.
	ResolvedIn time.Duration
```

Replace the body of `render` in `internal/notify/render.go`:

```go
// render builds a human-facing Message for an event.
func render(e Event) Message {
	title := eventTitles[e.Type]
	if title == "" {
		title = string(e.Type)
	}
	who := e.Agent
	if e.Process != "" {
		who = fmt.Sprintf("%s / %s", e.Agent, e.Process)
	}
	if e.ResolvedIn > 0 {
		title += " then recovered"
		body := fmt.Sprintf("[%s] %s: %s — recovered after %s", who, title, e.Detail, e.ResolvedIn.Round(time.Second))
		return Message{Title: fmt.Sprintf("Marshal: %s (%s)", title, who), Body: body, Event: e}
	}
	body := fmt.Sprintf("[%s] %s: %s", who, title, e.Detail)
	return Message{Title: fmt.Sprintf("Marshal: %s (%s)", title, who), Body: body, Event: e}
}
```

Add `"time"` to the imports in `render.go` (it currently imports only `"fmt"`):

```go
import (
	"fmt"
	"time"
)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/ -run 'TestRender' -v`
Expected: PASS (all render tests, including the two new ones and the existing ones).

- [ ] **Step 5: Commit**

```bash
git add internal/notify/model.go internal/notify/render.go internal/notify/render_test.go
git commit -m "feat(notify): Event.ResolvedIn + merged recovery rendering

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `CoalesceWindowSeconds` setting + `coalesceWindow()` helper

Adds the config field with pointer/presence semantics and the helper the Coalescer reads. No store-load normalization is needed: `nil` is resolved to the default inside the helper, and `SetSettings` replaces the whole struct so the pointer (including an explicit `0`) is preserved.

**Files:**
- Modify: `internal/notify/model.go` (the `Settings` struct + a new const + helper)
- Test: `internal/notify/model_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Settings.CoalesceWindowSeconds *int` (json `coalesce_window_seconds,omitempty`); `Settings.coalesceWindow() time.Duration`; `const defaultCoalesceWindowSeconds = 10`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/notify/model_test.go` (it is in `package notify` and already imports `testing`; add `"time"` to its import block if absent):

```go
func TestCoalesceWindowDefaultsWhenNil(t *testing.T) {
	if got := (Settings{}).coalesceWindow(); got != 10*time.Second {
		t.Fatalf("nil window should default to 10s, got %s", got)
	}
}

func TestCoalesceWindowExplicitZeroDisables(t *testing.T) {
	z := 0
	if got := (Settings{CoalesceWindowSeconds: &z}).coalesceWindow(); got != 0 {
		t.Fatalf("explicit 0 should be 0s (disabled), got %s", got)
	}
}

func TestCoalesceWindowExplicitValue(t *testing.T) {
	w := 25
	if got := (Settings{CoalesceWindowSeconds: &w}).coalesceWindow(); got != 25*time.Second {
		t.Fatalf("want 25s, got %s", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run 'TestCoalesceWindow' -v`
Expected: compile error — `Settings` has no field `CoalesceWindowSeconds` / no method `coalesceWindow`.

- [ ] **Step 3: Add the field, const, and helper**

In `internal/notify/model.go`, add to the `Settings` struct (after `CooldownOverrides`):

```go
	// CoalesceWindowSeconds sets how long a recoverable alert is buffered to see
	// whether its recovery arrives — in which case the two are merged into one
	// notice. Pointer/presence semantics, like CooldownOverrides:
	//   nil        = use the default (defaultCoalesceWindowSeconds);
	//   explicit 0 = disable coalescing (deliver immediately);
	//   N          = an N-second window.
	CoalesceWindowSeconds *int `json:"coalesce_window_seconds,omitempty"`
```

Add the const near the top of the file (after the `EventType` consts is fine) and the helper next to `cooldownFor`:

```go
const defaultCoalesceWindowSeconds = 10

// coalesceWindow returns the coalescing window: the explicit setting if present
// (including an explicit 0, which disables coalescing), otherwise the default.
func (s Settings) coalesceWindow() time.Duration {
	if s.CoalesceWindowSeconds == nil {
		return defaultCoalesceWindowSeconds * time.Second
	}
	return time.Duration(*s.CoalesceWindowSeconds) * time.Second
}
```

(`model.go` already imports `"time"`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/ -run 'TestCoalesceWindow' -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/notify/model.go internal/notify/model_test.go
git commit -m "feat(notify): CoalesceWindowSeconds setting + coalesceWindow helper

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: The `Coalescer`

The core unit: buffers recoverable alerts, merges them with their recovery inside the window, flushes unrecovered alerts after the window, and drains on shutdown. This is the largest task; the steps build up the test file and the implementation together.

**Files:**
- Create: `internal/notify/coalesce.go`
- Test: `internal/notify/coalesce_test.go`

**Interfaces:**
- Consumes: `Emitter` (the `Dispatcher`); `Settings.coalesceWindow()` (Task 2); `Event.ResolvedIn` (Task 1); `EventType` consts.
- Produces: `type Coalescer struct{...}`; `NewCoalescer(out Emitter, store settingsReader, opts ...CoalesceOption) *Coalescer`; methods `Emit(Event)`, `Run(ctx context.Context)`, `flush(now time.Time)`; options `WithCoalesceClock(func() time.Time)`, `WithSweepInterval(time.Duration)`. `Coalescer` satisfies `Emitter`, so it can be passed to `NewDetector`.

- [ ] **Step 1: Write the failing tests**

Create `internal/notify/coalesce_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/notify/ -run TestCoalesce -v`
Expected: compile error — `NewCoalescer`, `Coalescer`, `WithCoalesceClock`, `WithSweepInterval`, `CoalesceOption` undefined.

- [ ] **Step 3: Write the Coalescer**

Create `internal/notify/coalesce.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/notify/ -run TestCoalesce -race -v`
Expected: PASS (all 7 TestCoalesce* tests).

- [ ] **Step 5: Run the whole notify package**

Run: `go test ./internal/notify/ -race -count=1`
Expected: PASS (existing dispatcher/detector/render/store/model tests still green).

- [ ] **Step 6: Commit**

```bash
git add internal/notify/coalesce.go internal/notify/coalesce_test.go
git commit -m "feat(notify): Coalescer merges transient alert/recovery blips

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Wire the Coalescer into the server + settings round-trip test + changelog

Inserts the Coalescer into the live notification path and proves the new setting survives the dashboard PUT round-trip. The window now defaults to 10s for the running server.

**Files:**
- Modify: `internal/server/server.go` (~lines 396-398, the notify wiring)
- Test: `internal/dashboard/notifications_test.go`
- Modify: `CHANGELOG.md` (`## [Unreleased]`)

**Interfaces:**
- Consumes: `notify.NewCoalescer` (Task 3); `Settings.CoalesceWindowSeconds` (Task 2).
- Produces: nothing new (wiring + docs).

- [ ] **Step 1: Write the failing dashboard round-trip test**

Add to `internal/dashboard/notifications_test.go` (mirrors `TestPutSettingsRoundTripsCooldownOverrides`):

```go
func TestPutSettingsRoundTripsCoalesceWindow(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	w := 30
	body, _ := json.Marshal(notify.Settings{CooldownSeconds: 60, CoalesceWindowSeconds: &w})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/settings", bytes.NewReader(body))
	h.putSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body)
	}
	if n.settings.CoalesceWindowSeconds == nil || *n.settings.CoalesceWindowSeconds != 30 {
		t.Fatalf("coalesce_window_seconds not stored: %+v", n.settings)
	}
}
```

- [ ] **Step 2: Run it to verify it passes already (no Go handler change needed)**

Run: `go test ./internal/dashboard/ -run TestPutSettingsRoundTripsCoalesceWindow -v`
Expected: PASS — `putSettings` decodes into `notify.Settings`, which already carries the new field (Task 2). This test guards against future regressions in the decode path.

(If it FAILS to compile, Task 2 was not completed — `CoalesceWindowSeconds` must exist on `notify.Settings`.)

- [ ] **Step 3: Wire the Coalescer into the server**

In `internal/server/server.go`, replace the three wiring lines (currently `disp := ...`, `det := notify.NewDetector(reg, disp, 2*time.Second)`, `go det.Run(ctx)`) with:

```go
			disp := notify.NewDispatcher(ns, channels.New)
			co := notify.NewCoalescer(disp, ns)
			det := notify.NewDetector(reg, co, 2*time.Second)
			go co.Run(ctx)
			go det.Run(ctx)
```

Also update the comment on the line above (currently `// Notification service: detector polls the registry; dispatcher routes to channels.`) to:

```go
			// Notification service: detector polls the registry; the coalescer
			// merges transient alert/recovery blips; the dispatcher routes to channels.
```

- [ ] **Step 4: Build and test the server**

Run: `go build ./... && go test ./internal/server/ -count=1`
Expected: builds clean; server tests PASS.

- [ ] **Step 5: Add the changelog entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added`, add:

```markdown
- Alert/recovery coalescing: a transient crash-then-recover blip (within a
  configurable window, default 10s; set to 0 to disable) is now delivered as a
  single merged notice instead of a separate alert and recovery.
```

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go internal/dashboard/notifications_test.go CHANGELOG.md
git commit -m "feat(server): wire Coalescer into the notification path

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Dashboard settings UI row + rebuild embedded bundle

Adds the window control to the notifications settings UI (function-first; visual styling is deferred to M31) and regenerates the embedded SPA bundle so the running binary serves it.

**Files:**
- Modify: `web/src/api.ts` (`NotifSettings` type, ~line 373)
- Modify: `web/src/Notifications.tsx` (`SettingsSection`, ~lines 157-189)
- Modify: `internal/dashboard/dist/**` (regenerated by `make ui` — do not hand-edit)

**Interfaces:**
- Consumes: the `coalesce_window_seconds` JSON field (Tasks 2/4).
- Produces: nothing other tasks depend on (UI leaf).

- [ ] **Step 1: Extend the API type**

In `web/src/api.ts`, change the `NotifSettings` type to add the field:

```ts
export type NotifSettings = { cooldown_seconds: number; suppress_recovery?: boolean; cooldown_overrides?: Record<string, number>; coalesce_window_seconds?: number };
```

- [ ] **Step 2: Add the control and include it in the save payload**

In `web/src/Notifications.tsx`, inside `SettingsSection`, add a state hook next to the existing ones (after the `recovery` state, ~line 159):

```tsx
  const [coalesce, setCoalesce] = useState(cfg.settings.coalesce_window_seconds ?? 10);
```

Add the input row after the "Send recovery notices" label (after ~line 172):

```tsx
      <label>Coalesce window (seconds, 0 = off): <input type="number" value={coalesce} onChange={(e) => setCoalesce(Number(e.target.value))} /></label>
```

Update the `putNotifSettings` call in the Save button (~line 184) to include the field:

```tsx
        await putNotifSettings({ cooldown_seconds: cooldown, suppress_recovery: !recovery, cooldown_overrides: co, coalesce_window_seconds: coalesce });
```

- [ ] **Step 3: Type-check / build the SPA into the embedded dist**

Run: `make ui`
Expected: `cd web && npm install && npm run build` completes with no TypeScript errors; `internal/dashboard/dist/assets/` gets a new `index-*.js` bundle.

- [ ] **Step 4: Rebuild the binary with the new bundle embedded**

Run: `make build`
Expected: builds clean; `./marshal --version` runs.

- [ ] **Step 5: Commit**

```bash
git add web/src/api.ts web/src/Notifications.tsx internal/dashboard/dist
git commit -m "feat(dashboard): coalesce-window control in notification settings

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Full verification

Final gate across the whole repo before review/demo.

**Files:** none (verification only).

- [ ] **Step 1: Race + full test suite**

Run: `go test ./... -race -count=1`
Expected: PASS (no failures, no race warnings).

- [ ] **Step 2: Vet and format**

Run: `go vet ./... && gofmt -l .`
Expected: `go vet` silent; `gofmt -l .` lists **no files**.

- [ ] **Step 3: Versioned build**

Run: `make build`
Expected: builds clean; `./marshal --version` prints a git-derived version.

- [ ] **Step 4: Confirm the branch state**

Run: `git log --oneline dev..HEAD`
Expected: the spec commit plus the five task commits, all on `m30-coalescing`.

---

## Post-plan (not tasks — handled by the orchestrating session)

After all tasks pass: request an Opus whole-branch review (`requesting-code-review`), run a live demo per the CLAUDE.md live-demo convention (scratch `XDG_DATA_HOME`, server on `:9000`/`:9001`, exercise a crash-then-recover blip and confirm one merged notice arrives), write the handoff to `docs/handoffs/`, then merge `m30-coalescing` → `dev` with `--no-ff`.
