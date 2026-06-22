# Marshal — M27 Recovery / "resolved" Notices — Handoff

**Date:** 2026-06-22
**Branch:** `m27-recovery-notices` (off `dev`), all tasks implemented + reviewed; **about to be
merged `--no-ff` into `dev`** via finishing-a-development-branch.

Read this with the M26 handoff (`2026-06-22-m26-notification-service.md`) — M27 extends that
notification service.

---

## TL;DR

Marshal now emits a **`recovered`** event ("Process recovered") when a process that was
alerting (crash → restarting, restart-loop → errored, deploy-fail → failed) returns to
**`online`** — the process-level "all clear" to complement the existing `agent_up`. It also
catches deploy recovery that passes through an intermediate `building` state. A global
**"Send recovery notices"** setting (on by default) can silence recovery; alerts are never
affected. Recovery routes through the existing rules + cooldown + channels.

Built spec → plan → 6 subagent-driven TDD tasks (each task-reviewed clean) → opus
whole-branch review ("READY TO MERGE — clean") → 2 tiny test-strengthening fixes → live
fleet demo. Spec: `docs/superpowers/specs/2026-06-22-m27-recovery-notices-design.md`. Plan:
`docs/superpowers/plans/2026-06-22-m27-recovery-notices.md`.

## What changed (by file)

- **`internal/notify/model.go`** — new `EventRecovered EventType = "recovered"`; `Settings`
  gains `SuppressRecovery bool` (`json:"suppress_recovery"`). The flag is **inverted**
  (suppress, not enable) so the zero value keeps recovery ON by default — including for
  `notifications.json` files written before the field existed (no loader special-casing).
- **`internal/notify/detector.go`** — `Detector` gains a cross-tick
  `alerting map[string]EventType` keyed by `agent\x00process` (last alert type). The pure
  `diff` is **unchanged**. New `recoveries(alerts, next, now)` runs each tick: marks
  processes that alerted, then sweeps `next` — a flagged process reaching `online` emits one
  `recovered` (Detail via `recoveryDetail`) and clears the flag; a clean `stopped` clears it
  **silently**; processes absent from the snapshot are pruned. `Run` emits alerts then
  recoveries. A process cannot both alert and recover in one tick (an alert tick's next-state
  is never `online`), so mark-then-sweep is safe.
- **`internal/notify/render.go`** — title `EventRecovered: "Process recovered"`.
- **`internal/notify/dispatcher.go`** — `Emit` drops `recovered` events at the top when
  `SuppressRecovery` is set (before the cooldown check, so a suppressed recovery consumes no
  cooldown slot). Cooldown already keys on `Type`, so `recovered` has its own bucket.
- **`web/src/api.ts`** — `NotifSettings` gains optional `suppress_recovery?: boolean`.
- **`web/src/Notifications.tsx`** — `"recovered"` added to `EVENT_TYPES` (rule event
  checkbox); settings section gains a "Send recovery notices" checkbox (checked when
  `!suppress_recovery`; Save writes `suppress_recovery: !checked`). `make ui` regenerated the
  embedded `internal/dashboard/dist` bundle (committed) so the running binary serves it.
- **`CHANGELOG.md`** — `[Unreleased] → Added` entry.
- Tests: `detector_test.go` (recovery lifecycle: crash/loop/deploy-path/clean-stop/prune +
  `recoveryDetail`), `dispatcher_test.go` (suppress both directions), `store_test.go`
  (default-on + persistence + legacy file), `render_test.go` (title), `notifications_test.go`
  (HTTP `putSettings` round-trips `suppress_recovery`).

## Key decisions / non-obvious

- **Stateful tracking, not stateless prev→next** — recovery needs memory across ticks
  because deploy recovery (`failed → building → online`) has a non-alerting state right
  before `online`. The `alerting` map carries the flag across the `building` tick.
- **Inverted setting solves default-ON cleanly** — a plain `bool` can't distinguish "unset"
  from "explicitly false" (the store treats `CooldownSeconds==0` as unset). `SuppressRecovery`
  defaulting to false = recovery-on needs zero special-casing and is back-compatible.
- **Transient restarts produce crash+recovered pairs** — `online→restarting→online` alerts
  then recovers; accepted by design, per-type cooldown limits spam.
- **Sampling caveat (inherited from M26)** — the detector samples every 2s; durable states
  (`errored`, `online`) are caught reliably, sub-2s flaps are not. The live demo used a
  durable `errored` state for the conclusive A/B.

## Whole-branch review (opus) — verdict: READY TO MERGE (clean)

No Critical, no Important. Adversarially confirmed: mark→sweep→prune ordering safe; deploy
path caught; map bounded; `diff` untouched; no new shared-state race (`alerting`/`prev` only
touched in the single `Run` goroutine; dispatcher fan-out touches only mutex-guarded
state); suppress gate placement correct; inverted-setting default holds for fresh + legacy +
round-trip; frontend inversion correct. Two Minor test-strengthening fixes applied
(commit `d35ef1d`): assert no-event on the prune tick; add an HTTP-layer `suppress_recovery`
round-trip test. Minor (no fix, by design): transient crash+recovered pairs;
`recoveryDetail` default branch unreachable in prod (covered by a synthetic test).

## Live demo result (2026-06-22, scratch `/tmp/marshal-m27-demo`, server `:9000`/`:9001`, sink `:9099`)

Real fleet: demo server + agent `dev-1` (scratch data dirs), a local webhook **sink**, and a
marker-gated app. Delivered to the sink (verbatim):
- **`recovered` after crash**: `{"type":"recovered","detail":"recovered after crash",...}` —
  a crashing process brought back online produced a recovery notice.
- **A/B suppress proof on a durable `errored` state**:
  - suppress **ON** → the `errored` cycle delivered `crash` + `restart_loop` (alerts NOT
    suppressed) and the subsequent `online` produced **no** event (recovery suppressed).
  - suppress **OFF** → the same cycle delivered
    `{"type":"recovered","detail":"recovered after restart loop",...}`.
- The `suppress_recovery` field reads/writes through `PUT /api/notifications/settings`.
- The running binary's served bundle (`/assets/index-*.js`) contains "Send recovery
  notices", `suppress_recovery`, and `recovered` — the rebuilt embedded UI is live.
- Login UI rendered (screenshot captured). The logged-in notifications-page screenshot
  couldn't be captured cleanly (a Playwright/SPA hash-nav timing quirk, not a product bug —
  the served-bundle grep confirms the UI strings ship).
- Teardown by data-dir + PID only; the user's standing launchd daemon (pid 3119) preserved;
  `pgrep -fl marshal` shows no demo orphans; ports freed; scratch dir removed.

## How to build / run / test

```bash
go test ./... -race -count=1     # all 25 pkgs green
go vet ./... ; gofmt -l .        # clean / silent
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
make build                       # version-stamped binary
```

## Known issues / deferred

- **Recovery is sampling-limited** like the other events — sub-2s flaps between polls are
  missed (durable states are reliable).
- **Cooldown map (`dispatcher.last`) still unpruned** (M26 carryover) — now also holds
  `recovered` keys; still kilobytes for normal fleets.
- **No dedicated recovery cooldown control** — `recovered` shares the single global cooldown
  setting with all event types (its own per-type bucket, but the same number).
- UI is function-first (M19/Signal styling pass still pending).

## Concrete next step

1. **Merge `m27-recovery-notices` → `dev` (`--no-ff`)** — in progress via
   finishing-a-development-branch.
2. **Cut v0.2.0** when ready: move `CHANGELOG.md` `[Unreleased]` (CI + test-coverage + M27
   recovery) into `## [0.2.0] - <date>`, update compare links, merge `dev → main` (`--no-ff`),
   tag `v0.2.0`, push `main`, `dev`, and the tag.
3. Candidate follow-ups: prune the cooldown map; per-event-type or per-rule cooldown;
   digest/coalescing of alert+recovery pairs; the Signal/M19 UI pass.

The full SDD ledger is in `.superpowers/sdd/progress.md` (git-ignored).
