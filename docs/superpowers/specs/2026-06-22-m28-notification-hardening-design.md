# M28 — Notification Hardening (prune cooldown map + per-event-type cooldown)

**Date:** 2026-06-22
**Branch:** `m28-notification-hardening` (off `dev`)
**Status:** design approved; spec under review

Builds on M26 (notification service) and M27 (recovery notices). Closes two deferred
carryovers from the M27 handoff:

1. **Prune the cooldown map** (`dispatcher.last`) — it has grown unbounded since M26; it now
   also holds `recovered` keys.
2. **Per-event-type cooldown** — `recovered` (and every other type) currently shares the
   single global `Settings.CooldownSeconds`. Give each event type its own optional rate.

These two are combined in one milestone because they touch the same cooldown machinery
(`dispatcher.allow()` and the `d.last` map) and the same `Settings` struct.

Out of scope (their own later milestones): alert/recovery **coalescing/digest** (M29) and the
**Signal/M19 UI** styling pass (M30). The UI added here is function-first.

---

## Goals

- The dispatcher's cooldown map stays bounded regardless of fleet size or uptime.
- An operator can set a different cooldown for individual event types (e.g. a longer cooldown
  for `recovered`, or `0` to disable cooldown for `crash`) while a global default covers the
  rest.
- Fully back-compatible: existing `notifications.json` files (no new field) load unchanged
  with the global cooldown applied to all types.

## Non-goals

- Per-rule cooldown (rejected during brainstorming: an event can match multiple rules, which
  muddies the cooldown key and multi-match semantics; per-event-type is sufficient for the
  stated need).
- Coalescing transient alert+recovery pairs (M29).
- Any visual/Signal redesign of the settings UI (M30).

---

## 1. Data model — `internal/notify/model.go`

`Settings` gains one optional field:

```go
type Settings struct {
    CooldownSeconds  int               `json:"cooldown_seconds"`
    SuppressRecovery bool              `json:"suppress_recovery"`
    // CooldownOverrides maps an event type to a per-type cooldown in seconds,
    // overriding CooldownSeconds for that type. A key's PRESENCE is the signal:
    // absent  = inherit the global CooldownSeconds;
    // present = use this value (including an explicit 0, which disables cooldown
    //           for that type). The map sidesteps the int-zero-means-unset
    //           ambiguity that CooldownSeconds has.
    CooldownOverrides map[EventType]int `json:"cooldown_overrides,omitempty"`
}
```

New helper on `Settings` (pure, no clock):

```go
// cooldownFor returns the cooldown duration for an event type: the per-type
// override if present, otherwise the global CooldownSeconds.
func (s Settings) cooldownFor(t EventType) time.Duration {
    secs := s.CooldownSeconds
    if v, ok := s.CooldownOverrides[t]; ok {
        secs = v
    }
    return time.Duration(secs) * time.Second
}
```

`omitempty` + a nil map means files written before this field existed deserialize with
`CooldownOverrides == nil`; `cooldownFor` then always falls through to `CooldownSeconds`. No
loader special-casing (same approach as `SuppressRecovery` in M27).

## 2. Dispatcher — `internal/notify/dispatcher.go`

Two changes to the existing cooldown machinery; the `Emit` flow and the suppress gate are
unchanged.

**2a. Per-type lookup.** `allow()` computes the cooldown via `cooldownFor(e.Type)` instead of
reading `CooldownSeconds` directly. The cooldown key is unchanged
(`agent\x00process\x00type`), so each type already has its own bucket — now it also gets its
own rate.

**2b. Prune (bounded map).** Change the map's value type so an entry carries its own type,
avoiding string-splitting the key during a sweep:

```go
type cooldownEntry struct {
    at  time.Time
    typ EventType
}
// d.last map[string]cooldownEntry
```

Inside the existing locked section of `allow()`, after recording the new entry, sweep the map
and delete any entry where `now.Sub(e.at) >= s.cooldownFor(e.typ)`. An entry whose age has
reached its own cooldown can never gate a future event again (the next event of that key is
always allowed), so eviction is safe and changes no observable behavior. Result: the map is
bounded to "distinct (agent, process, type) keys seen within their cooldown window."

Cost: the sweep is O(n) in the number of live keys, performed only on an emit that passes the
suppress gate. Emits are infrequent (gated by the 2 s detector tick and the cooldowns
themselves), so this is negligible for real fleets. No background goroutine, no new lock.

Runtime setting changes are handled correctly because the cooldown is recomputed from current
`Settings` on every `allow()` and every sweep — nothing stale is stored (we store the event
time, not a precomputed expiry).

## 3. Persistence — `internal/notify/store.go`

No code change expected: the store already round-trips `Settings` as JSON and the map
serializes natively (nil stays nil, no defaulting needed). Verified by tests:

- a nil `CooldownOverrides` persists and reloads as nil;
- a populated map round-trips intact;
- a legacy file with no `cooldown_overrides` key loads with a nil map and the global cooldown
  intact.

If `store.go` turns out to normalize/whitelist fields on write, adjust it to pass the map
through; otherwise leave it untouched.

## 4. HTTP + UI

**API surface** (`web/src/api.ts`): `NotifSettings` gains
`cooldown_overrides?: Record<string, number>`. The existing `PUT /api/notifications/settings`
handler already (de)serializes the whole `Settings` struct, so the map flows through without a
handler change; a test asserts the round-trip.

**Settings UI** (`web/src/Notifications.tsx`): below the existing global cooldown field, add a
**per-event-type cooldown** block — six fixed rows, one per event type (`crash`,
`restart_loop`, `agent_down`, `agent_up`, `deploy_fail`, `recovered`), each with a numeric
input. The input's placeholder shows the inherited global value. Empty input = no override
(key omitted from `cooldown_overrides`); a number (including `0`) = override. On Save, only
rows with a value are serialized into the map. Function-first styling; the Signal/M19 pass is
M30. `make ui` regenerates the embedded `internal/dashboard/dist` bundle (committed) so the
running binary serves it.

## 5. Testing (TDD — failing test first for each)

- `model_test.go` (new or existing): `cooldownFor` precedence — global when no override,
  override when present, explicit `0` override yields a zero duration.
- `dispatcher_test.go`:
  - a per-type override gates that type at its own rate while the global still applies to
    other types;
  - an override of `0` disables cooldown for that type (consecutive events both allowed);
  - **prune**: after an entry's cooldown elapses, the next `allow()` sweep removes it and the
    map shrinks (the core item-1 assertion); a still-within-cooldown entry is retained.
- `store_test.go`: nil round-trip, populated round-trip, legacy file (§3).
- `notifications_test.go`: HTTP `putSettings` round-trips `cooldown_overrides`.

`CHANGELOG.md` gets an `[Unreleased]` entry (Added: per-event-type cooldown overrides;
Fixed/Changed: cooldown map is now pruned/bounded) as part of the work.

---

## Verification / live demo (after implementation)

Per the project's live-demo convention: build the binary, run a scratch fleet on `:9000`/
`:9001` with a local webhook sink, set a short cooldown override for one event type and a
longer one for another, and confirm via the sink that the two types gate at different rates.
Confirm the settings UI persists overrides across a reload. Tear down by data dir + PID;
preserve the standing launchd daemon; verify no orphans with `pgrep -fl marshal`.

## Concrete next step

Invoke the writing-plans skill to turn this spec into a task-by-task implementation plan, then
execute it via subagent-driven TDD (the M27 pattern).
