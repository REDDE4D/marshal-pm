# Marshal — M28 Notification Hardening — Handoff

**Date:** 2026-06-22
**Branch:** `m28-notification-hardening` (off `dev`), all tasks implemented + reviewed +
live-demoed; **ready to merge `--no-ff` into `dev`** via finishing-a-development-branch.

Read this with the M26 (notification service) and M27 (recovery notices) handoffs — M28
hardens that same notification subsystem.

---

## TL;DR

Closes the two notification carryovers from the M27 handoff:

1. **Per-event-type cooldown overrides** — `notify.Settings` gains
   `CooldownOverrides map[EventType]int` (json `cooldown_overrides,omitempty`). Map-key
   **presence** is the signal: absent = inherit the global `CooldownSeconds`; present
   (including an explicit `0`, which disables cooldown for that type) = use that value. The
   dispatcher's `allow()` now looks up the rate via a new pure helper
   `Settings.cooldownFor(EventType) time.Duration` instead of reading `CooldownSeconds`
   directly. So `recovered` (and every type) can have its own rate; the cooldown key is
   unchanged (`agent\x00process\x00type`), so each type already had its own bucket — now it
   has its own number too.
2. **Prune the cooldown map** — the dispatcher's `last` map (previously
   `map[string]time.Time`, unbounded since M26) is now `map[string]cooldownEntry{at, typ}` and
   is pruned **inside `allow()`'s existing locked section** (no goroutine, no new mutex):
   entries past their own cooldown are deleted, bounding the map to "distinct keys seen within
   their cooldown window."

Plus the settings UI gained a six-row per-event override block (function-first; Signal/M19 UI
is M30).

Built spec → plan → 5 subagent-driven TDD tasks (each task-reviewed clean) → opus
whole-branch review (**READY TO MERGE**) → 1 doc-only fix → live fleet demo.
Spec: `docs/superpowers/specs/2026-06-22-m28-notification-hardening-design.md`.
Plan: `docs/superpowers/plans/2026-06-22-m28-notification-hardening.md`.

## What changed (by file)

- **`internal/notify/model.go`** — `Settings.CooldownOverrides map[EventType]int` +
  `func (s Settings) cooldownFor(t EventType) time.Duration` (override-if-present-via-comma-ok,
  else global). `model_test.go`: `TestCooldownForPrecedence` (global / override / explicit-0 /
  nil-map).
- **`internal/notify/dispatcher.go`** — `last` becomes `map[string]cooldownEntry` (`{at
  time.Time; typ EventType}`); `NewDispatcher` updated. `allow()` takes one `Settings()`
  snapshot, gates via `cooldownFor(e.Type)`, records `{now, e.Type}`, then calls
  `pruneLocked(s, now)` (still under `d.mu`). `pruneLocked` deletes entries where
  `now.Sub(e.at) >= s.cooldownFor(e.typ)`. `dispatcher_test.go`:
  per-type-override-gates-at-own-rate, zero-override-disables, prune-shrinks-map +
  retains-in-window.
- **`internal/notify/store.go`** — **no code change**; the map round-trips through existing
  JSON. `store_test.go`: nil round-trip, populated round-trip, legacy-file (no field → nil
  map + global intact).
- **`internal/dashboard/notifications.go`** — **no handler change**; `putSettings` already
  decodes the whole `Settings`. `notifications_test.go`:
  `TestPutSettingsRoundTripsCooldownOverrides`.
- **`web/src/api.ts`** — `NotifSettings` gains `cooldown_overrides?: Record<string, number>`.
- **`web/src/Notifications.tsx`** — `SettingsSection` renders six fixed override rows (one per
  `EVENT_TYPES`); empty input = key omitted (inherit), a number (incl. 0) = override;
  placeholder shows `<global> (global)`. `make ui` regenerated the embedded
  `internal/dashboard/dist` bundle (committed).
- **`CHANGELOG.md`** — `[Unreleased]` Added (per-event-type overrides) + Fixed (cooldown map
  pruned/bounded).

## Key decisions / non-obvious

- **Map presence = signal** (parallels M27's inverted `SuppressRecovery`): a `map[EventType]int`
  cleanly distinguishes "unset → inherit global" from "explicit 0 → disable", which a plain
  int can't (the store treats `CooldownSeconds==0` as unset). nil map + `omitempty` =
  back-compatible with pre-M28 files, no loader special-casing.
- **`cooldownEntry` carries its own type** so the prune sweep applies the right per-type
  cooldown without string-splitting the composite key.
- **Lazy prune in the locked section** (not a goroutine): emits are infrequent (gated by the
  2 s detector tick + cooldowns), so an O(n) sweep per passing emit is negligible and needs no
  new concurrency.
- **Per-event-type, NOT per-rule** (rejected in brainstorming): an event can match multiple
  rules, which muddies the cooldown key; per-type covers the stated need.

## Whole-branch review (opus) — verdict: READY TO MERGE

No Critical. One **Important but non-blocking, doc-only** finding (fixed in commit `e32ef4b`):
the prune comment + spec §2 claimed pruning "changes no observable behavior", but a **runtime
cooldown *increase*** can permit one early re-fire for an already-swept key — **benign** (it
can only over-notify, never suppress a real alert; needs the rare combo of a runtime raise + a
sweep landing in the gap). Reviewer + controller agreed: correct the wording, not the logic
(robustness would add real complexity for a benign edge — YAGNI). The four prior Minors were
confirmed correctly classified (zero-override same-sweep delete = observably identical; test
`len(d.last)` no-lock safe under sync delivery; HTTP test `json.Marshal` discard matches house
style; UI empty `{}` is a functional no-op — `cooldownFor` falls through and `omitempty` drops
it on persist).

## Live demo result (2026-06-22, scratch `/tmp/marshal-m28-demo`, server `:9000`/`:9001`, sink `:9099`)

- **Real server + store round-trip**: authenticated `PUT /api/notifications/settings` with
  `cooldown_overrides` → `GET` echoed the map → on-disk `notifications.json` held it.
  Conclusive integration beyond the unit fakes.
- **End-to-end pipeline**: real `crash` events flowed detector → dispatcher → webhook channel
  → sink with `cooldown_overrides` active.
- **Browser UI (Playwright, headless)**: `#/notifications` rendered the new **"Per-event
  cooldown (seconds)"** block with all six rows; empty rows show the `<global> (global)`
  placeholder; setting `recovered=120` via the UI + Save + reload repopulated to `120`
  (persisted), and on-disk settings then held `{crash:5, recovered:120}` — full
  UI→HTTP→store→disk round-trip. Screenshot `/tmp/m28-ui-after-reload.png` (now removed with
  the scratch dir).
- **Cadence RATE A/B not cleanly shown live**: the 2 s detector sampling of a sub-2 s flapper
  caught `crash` transitions ~18 s apart (the sampling floor dwarfs the 5 s cooldown), so
  detection — not cooldown — gates the observed cadence. This is the documented M26/M27
  limitation (M26 skipped live fast-flap; M27 used a durable `errored` state). The rate logic
  itself is deterministically unit-tested with a fake clock
  (`TestDispatcherPerTypeCooldownOverride`, `...ZeroOverrideDisablesCooldown`).
- **Teardown** by data dir + PID only; standing launchd daemon (pid 3119) preserved;
  `pgrep -fl marshal` shows no demo orphans; ports freed; scratch dir removed.

## How to build / run / test

```bash
go test ./... -race -count=1     # all 25 pkgs green (verified)
go vet ./... ; gofmt -l .        # clean / silent (verified)
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
make build                       # version-stamped binary
```

## Known issues / deferred

- **Cooldown rate is still sampling-limited** for sub-2 s flaps (durable states are reliable)
  — inherited M26/M27 characteristic, not specific to M28.
- **Prune vs runtime cooldown increase** (documented above): one benign early re-fire possible
  immediately after raising a cooldown. Accepted by design; wording corrected.
- UI is function-first (Signal/M19 pass still pending — that's M30).

## Concrete next step

1. **Merge `m28-notification-hardening` → `dev` (`--no-ff`)** via finishing-a-development-branch.
2. Then the remaining two milestones from the original "do all four" request:
   - **M29 — Alert/recovery coalescing/digest** (the heavier architectural one: buffering /
     delayed delivery to merge transient alert+recovery pairs).
   - **M30 — Signal/M19 UI styling pass** (the dashboard redesign; the per-event rows added
     here are intentionally function-first and will be restyled then).
3. Cut a release (v0.3.0) when ready: move `CHANGELOG.md` `[Unreleased]` into a dated section,
   merge `dev → main` (`--no-ff`), tag, push.

The full SDD ledger is in `.superpowers/sdd/progress.md` (git-ignored).
