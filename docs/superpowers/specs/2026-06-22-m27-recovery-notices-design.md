# M27 — Recovery / "resolved" notices — Design

**Date:** 2026-06-22
**Status:** approved, ready for implementation plan
**Milestone:** M27 (targets release **v0.2.0**)

## Problem

The notification detector (`internal/notify/detector.go`) emits alerts when a process
enters a bad state — `crash` (→ restarting), `restart_loop` (→ errored), `deploy_fail`
(→ failed) — and `agent_down` / `agent_up` for agent connectivity. There is no
process-level "all clear": once a process recovers, operators keep staring at the last
alarm with no signal that it resolved. M27 adds a `recovered` event so a process returning
to `online` after an alerting condition produces a single "Process recovered" notice.

Agent-level recovery already exists (`agent_up`); this is the process-level analogue.

## Goals

- Emit exactly one `recovered` event when a process that was in an alerting condition
  returns to `online`, including the deploy path that recovers *through* an intermediate
  `building` state (`failed → building → online`).
- Default the feature ON, controllable by a single global setting, for both fresh installs
  and existing on-disk `notifications.json` files.
- No new noise sources: clean stops and brand-new processes never produce a recovery notice.

## Non-goals

- No change to agent-level up/down detection.
- No per-rule recovery configuration beyond the existing rule `Events` matching (a rule
  with empty `Events` matches any event, including `recovered`).
- No new transport/channel work; recovery flows through the existing dispatch path.

## Decisions (locked during brainstorming)

1. **Detection model — stateful alerting tracking.** The detector remembers which
   `(agent, process)` pairs are currently in an alerting condition and emits recovery when
   such a pair reaches `online`. This correctly handles deploy recovery
   (`failed → building → online`), where the state immediately before `online` is not the
   alerting one. (Rejected: stateless `prev→next` only, which misses that path.)
2. **Gating — global Settings toggle, default ON.** A single switch can silence all
   recovery notices; otherwise recovery routes through existing rules like any event.
3. **Naming — `recovered` / "Process recovered".**

## Design

### 1. Model (`internal/notify/model.go`)

- Add event type:
  ```go
  EventRecovered EventType = "recovered"
  ```
- Extend `Settings` with an **inverted** flag:
  ```go
  type Settings struct {
      CooldownSeconds  int  `json:"cooldown_seconds"`
      SuppressRecovery bool `json:"suppress_recovery"`
  }
  ```
  The flag is inverted (suppress, not enable) so the Go/JSON zero value `false` means
  "recovery enabled." This makes the feature default-ON without any special-casing in the
  store loader, and existing `notifications.json` files that predate the field
  automatically get recovery ON. The UI presents it as a normal "Send recovery notices"
  checkbox, checked when `!suppress_recovery`.

### 2. Detector (`internal/notify/detector.go`)

The detector is the only component that gains cross-tick state.

- Add a field to `Detector`:
  ```go
  alerting map[string]EventType // key: agent\x00process -> last alert type
  ```
  Initialised in `NewDetector`.

- The existing pure `diff(prev, next, now) []Event` stays **unchanged** (still computes
  alert transitions; all current tests keep passing).

- Add a `Detector` method that runs each tick after `diff`, e.g.
  `recoveries(alerts []Event, next []*pb.AgentState, now time.Time) []Event`:
  1. For each `alert` with a non-empty `Process`, set `alerting[key] = alert.Type`.
  2. Sweep every process in `next`:
     - state `online` and `key` present in `alerting` → append an `EventRecovered`
       (Detail via `recoveryDetail(prevType)`), then `delete(alerting, key)`.
     - state `stopped` and `key` present → `delete(alerting, key)` **silently** (clean
       stop; the alarm is moot, no notice).
  3. Prune `alerting` keys whose `(agent, process)` no longer appears in `next` so the map
     stays bounded across fleet churn.

- `Run` calls `diff` then `recoveries`, emits both sets (alerts first, then recoveries),
  and updates `d.prev = next` as today.

- A pure helper keeps the message text unit-testable:
  ```go
  func recoveryDetail(from EventType) string
  // EventCrash       -> "recovered after crash"
  // EventRestartLoop -> "recovered after restart loop"
  // EventDeployFail  -> "deploy recovered"
  // default          -> "recovered"
  ```

Ordering note: a process that produces an alert this tick has a next state of
`restarting`/`errored`/`failed` (never `online`), so it cannot both alert and recover in
the same tick — mark-then-sweep is safe.

### 3. Dispatcher (`internal/notify/dispatcher.go`)

Gate at the top of `Emit`:
```go
if e.Type == EventRecovered && d.store.Settings().SuppressRecovery {
    return
}
```
The cooldown key already includes `e.Type`, so `recovered` has its own cooldown bucket
independent of the originating `crash`/`deploy_fail`. No other dispatcher change.

### 4. Render (`internal/notify/render.go`)

Add to `eventTitles`:
```go
EventRecovered: "Process recovered",
```

### 5. Frontend (`web/src/Notifications.tsx`)

- Append `"recovered"` to the `EVENT_TYPES` array (line 8) so it appears as a rule
  event checkbox.
- Add a "Send recovery notices" checkbox in the settings section, checked when
  `!settings.suppress_recovery`, writing `suppress_recovery: !checked` back through the
  existing settings PUT.

## Data flow

```
snapshot poll
  → diff(prev, next)              (alert transitions, unchanged)
  → recoveries(alerts, next)      (NEW: mark/sweep/prune over alerting map)
  → Emit(event)
      → suppress gate (recovered + SuppressRecovery → drop)   (NEW)
      → cooldown gate (per agent/process/type)
      → rule match → render → fan out to channels
```

## Edge cases

| Scenario | Behaviour |
|---|---|
| Transient restart `online→restarting→online` | `crash` then `recovered`; cooldown limits spam. Intended. |
| Deploy recovery `failed→building→online` | `recovered` emitted (flag survives the `building` tick). |
| Clean stop while alerting `errored→stopped` | flag cleared silently, no `recovered`. |
| New process appears `(absent)→online` | seeded silently, never alerting → no `recovered`. |
| Process removed while alerting | key pruned; no `recovered`. |
| Steady `online` | not in `alerting` → nothing. |
| Recovery while `SuppressRecovery` set | event computed but dropped at dispatcher. |

## Testing (TDD)

- **detector_test.go**: `restarting→online` ⇒ recovered; `errored→online` ⇒ recovered;
  `failed→building→online` ⇒ recovered (deploy path); full `online→restarting→online`
  sequence ⇒ `[crash, recovered]`; `errored→stopped` ⇒ no event and flag cleared;
  `recoveryDetail` per source type; pruning of a vanished alerting process.
- **dispatcher_test.go**: `SuppressRecovery=true` drops a `recovered` event but still
  delivers `crash`; `SuppressRecovery=false` delivers `recovered`.
- **store_test.go**: fresh store and a legacy `notifications.json` without the field both
  report `SuppressRecovery=false` (recovery ON); `SetSettings` round-trips the flag.
- **render**: `recovered` renders the "Process recovered" title.
- **dashboard** (`notifications_test.go`): `putSettings` round-trips `suppress_recovery`.

## Release

Feature → minor bump → **v0.2.0**. Add a CHANGELOG `[Unreleased] → Added` entry as the
work lands; cut the release (move to `## [0.2.0] - <date>`, merge `dev → main --no-ff`, tag
`v0.2.0`, push) when M27 is verified.

## Implementation order (for the plan)

1. `model.go` — event type + `Settings.SuppressRecovery`.
2. `render.go` — title.
3. `detector.go` — `alerting` map, `recoveries`, `recoveryDetail`, `Run` wiring (TDD).
4. `dispatcher.go` — suppress gate (TDD).
5. `store_test.go` — default + round-trip coverage (logic already works via inverted flag).
6. `web/src/Notifications.tsx` — event checkbox + settings toggle.
7. CHANGELOG entry; build + live demo; handoff.
