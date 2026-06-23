# M30 — Alert/recovery coalescing — Design

**Date:** 2026-06-23
**Milestone:** M30
**Status:** Approved (brainstorm) — pending implementation plan

## Problem

The notification pipeline today is synchronous fire-and-forget. `Detector.Run` polls the
fleet every 2s; each detected transition is handed to `Dispatcher.Emit`, which gates by
cooldown, matches rules, and fans out to channels. A process that crashes and recovers
within a few seconds (`online → restarting → online`) therefore produces **two**
notifications — a crash alert and a recovery — for what is really one transient blip.

M30 coalesces such blips: when a recoverable alert is followed by its recovery within a
short window, subscribers receive **one merged notice** ("crashed then recovered in Ns")
instead of two messages.

## Decisions (from brainstorm)

- **Behavior:** buffer-and-flush. Hold a recoverable alert for a window `W`; if the matching
  recovery arrives within `W`, emit a single merged notice and drop both; otherwise flush the
  original alert (delayed by up to `W`).
- **Window config:** a single global setting, default 10s; explicit 0 disables coalescing
  (immediate delivery = today's behavior).
- **Scope:** all recoverable alerts coalesce with their recovery — process-level `crash`,
  `restart_loop`, `deploy_fail` (recovery = `recovered`) and agent-level `agent_down`
  (recovery = `agent_up`).
- **Placement:** a dedicated `Coalescer` unit between Detector and Dispatcher (approach A).
  The Dispatcher is left untouched.

## Architecture

A new `Coalescer` (`internal/notify/coalesce.go`) implements `Emitter` and wraps the real
`Dispatcher` (also an `Emitter`):

```
Detector ──Emit──▶ Coalescer ──Emit──▶ Dispatcher ──▶ channels
```

Server wiring (`internal/server/server.go`, currently lines ~396–398) changes from:

```go
disp := notify.NewDispatcher(ns, channels.New)
det  := notify.NewDetector(reg, disp, 2*time.Second)
go det.Run(ctx)
```

to:

```go
disp := notify.NewDispatcher(ns, channels.New)
co   := notify.NewCoalescer(disp, ns)        // ns provides Settings()
det  := notify.NewDetector(reg, co, 2*time.Second)
go co.Run(ctx)     // sweep loop: flushes expired buffers
go det.Run(ctx)
```

Because everything the Coalescer forwards still flows through `Dispatcher.Emit`, the
Dispatcher's cooldown gating, rule routing, and `SuppressRecovery` handling continue to work
unchanged. This keeps the M28-hardened dispatcher code untouched.

## Coalescer internals

State: a map keyed by `(agent, process)` of pending buffered alerts, guarded by a mutex.

```go
type pending struct {
    ev Event      // original alert: crash / restart_loop / deploy_fail / agent_down
    at time.Time  // when buffered
}

type Coalescer struct {
    out     Emitter            // the Dispatcher
    store   StoreReader        // for Settings().coalesceWindow()
    now     func() time.Time   // injectable clock (WithClock)
    sweep   time.Duration      // sweep interval (1s)
    mu      sync.Mutex
    pending map[string]pending // key: agent\x00process
}
```

Key is `agent + "\x00" + process` (agent-level events use process "").

### Emit(e Event)

Classifies the event:

- **Recoverable alert** (`crash`, `restart_loop`, `deploy_fail`, `agent_down`):
  if `coalesceWindow() == 0` → forward immediately via `out.Emit(e)`. Otherwise store in
  `pending[key]` (replacing any existing entry — a re-crash refreshes the buffer and resets
  the window). Do **not** forward yet.
- **Recovery** (`recovered`, `agent_up`): if `pending[key]` exists → emit one merged notice
  (see below), delete the pending entry, drop the recovery. If no pending entry → forward the
  recovery as-is (preserves "crashed long ago, recovered now" → normal recovered notice).
- **Anything else** → forward immediately (future-proofing).

### Run(ctx) / flush(now)

`Run` runs a sweep ticker (`sweep`, 1s). Each tick calls the pure method `flush(now)`, which
forwards (via `out.Emit`) and removes every pending entry whose age has reached the window —
these are real, sustained alerts that did not recover in time. On `ctx.Done()`, `Run` flushes
all remaining pending entries before returning, so a real crash buffered at shutdown is not
lost.

The window is read **live** from `store.Settings().coalesceWindow()` on each Emit and each
sweep, so a dashboard change takes effect without a restart (consistent with how the
Dispatcher reads cooldown live).

Keeping `flush(now)` pure with an injectable clock means tests drive coalescing
deterministically with no real sleeps — the same style as Detector and Dispatcher.

## Merged notice

The merged notice keeps the **original alert's `EventType`** (e.g. `crash`) so it routes to
the same rules and respects the same cooldown key. A new event type is explicitly rejected
because it would bypass users' existing rules.

To render the combined line, add one field to `Event` (`internal/notify/model.go`):

```go
ResolvedIn time.Duration // >0 ⇒ alert self-resolved within the coalescing window
```

When merging, the Coalescer forwards the buffered alert event with
`ResolvedIn = recoveryTime - bufferedTime`. `render` (`internal/notify/render.go`) checks it:

- `ResolvedIn > 0` → title becomes e.g. `"Process crashed then recovered"`; body appends
  `"— recovered after 4s"`.
- `ResolvedIn == 0` → rendering is byte-for-byte identical to today.

The merged event still flows through `Dispatcher.Emit`, so it is cooldown-gated by the crash
key and routed to crash rules. `SuppressRecovery` does **not** suppress it — it is a crash
event, not a `recovered` event, which is correct: the user wants to know the blip happened.

## Settings & config

New field on `Settings` (`internal/notify/model.go`), using pointer/presence semantics that
mirror M28's `CooldownOverrides`:

```go
CoalesceWindowSeconds *int `json:"coalesce_window_seconds,omitempty"`
```

- `nil` (absent — configs written before this field) → default **10s**.
- explicit `0` → **disabled** (immediate delivery, today's behavior).
- `N` → N-second window.

A helper encapsulates the semantics:

```go
const defaultCoalesceWindowSeconds = 10

func (s Settings) coalesceWindow() time.Duration {
    if s.CoalesceWindowSeconds == nil {
        return defaultCoalesceWindowSeconds * time.Second
    }
    return time.Duration(*s.CoalesceWindowSeconds) * time.Second
}
```

The pointer cleanly distinguishes "never set → use 10s default" from "deliberately disabled
(0)" — the same int-zero-ambiguity that `CooldownSeconds` has and that `CooldownOverrides`
already solved with presence semantics.

**Settings UI (dashboard):** one new row — a number input for the coalescing window — with
help text noting that enabling it delays alerts by up to the window. (Function-first; visual
styling deferred to M31's Signal pass, consistent with the M28 cooldown rows.)

## Edge cases & lifecycle

- **Re-crash during the window:** the new alert replaces the pending entry and resets its
  timer — the latest crash is treated as current.
- **Clean stop during the window:** the Detector emits nothing on a clean stop (it silently
  clears `alerting`), so the Coalescer receives no recovery; the buffered alert flushes on
  window expiry. Acceptable — the process did crash. Known behavior.
- **Shutdown (ctx cancel):** `Run` flushes all pending alerts before returning.
- **Window changed to 0 at runtime:** already-pending entries flush on the next sweep; new
  alerts pass straight through.

## Testing (TDD)

`internal/notify/coalesce_test.go`, fake clock, `WithSyncDelivery`-style capture into a test
`Emitter` that records forwarded events:

- crash then recovery within window → exactly one forwarded event, `ResolvedIn > 0`, type
  still `crash`.
- crash with no recovery → after the window elapses (driven via clock + `flush`), one plain
  crash event, `ResolvedIn == 0`.
- recovery with no pending alert → forwarded unchanged as `recovered`.
- re-crash within the window resets the timer (no early flush).
- window = 0 → pass-through: alert forwarded immediately, recovery not merged.
- `agent_down` → `agent_up` coalesces the same way.
- shutdown (`ctx` cancel) flushes pending alerts.

`render_test.go`:

- `ResolvedIn > 0` produces the merged title/body.
- `ResolvedIn == 0` is unchanged from current output.

## Out of scope

- Per-event-type windows (global only for M30; could mirror `CooldownOverrides` later).
- Multi-event digests / batching unrelated alerts into one message (this is pairwise
  alert↔recovery coalescing only).
- Any visual styling of the new settings row (M31).

## Changelog

Add under `## [Unreleased]`:
- **Added:** Alert/recovery coalescing — transient crash-then-recover blips within a
  configurable window (default 10s) are delivered as a single merged notice instead of two.
