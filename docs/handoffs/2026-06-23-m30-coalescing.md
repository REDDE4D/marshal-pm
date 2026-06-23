# M30 — Alert/recovery coalescing — Handoff

**Date:** 2026-06-23
**Branch:** `m30-coalescing` (off `dev`), 7 commits ahead of `dev` (`f525f8e`). Reviewed clean and
live-demo-verified; **ready to merge `m30-coalescing` → `dev` (`--no-ff`)**. `main` unchanged.

To resume: read this file. The spec is
`docs/superpowers/specs/2026-06-23-m30-coalescing-design.md`, the plan is
`docs/superpowers/plans/2026-06-23-m30-coalescing.md`, and the SDD ledger (git-ignored) is
`.superpowers/sdd/progress.md`.

## What M30 does

Transient crash-then-recover blips are now delivered as **one** merged notification instead of
two (a crash alert + a recovery). A new `notify.Coalescer` sits between the `Detector` and the
`Dispatcher`:

```
Detector ──Emit──▶ Coalescer ──Emit──▶ Dispatcher ──▶ channels
```

- It buffers "recoverable" alerts (`crash`, `restart_loop`, `deploy_fail`, `agent_down`) keyed
  by `(agent, process)`. If the matching recovery (`recovered`, `agent_up`) arrives within the
  window, it forwards **one** merged event and drops both; otherwise it flushes the original
  alert after the window; on shutdown it drains pending alerts.
- The merged event keeps the **original** `EventType` (so the Dispatcher's existing cooldown
  keying, rule routing, and `SuppressRecovery` all still apply — `SuppressRecovery` only
  short-circuits `EventRecovered`, so it does *not* suppress a merged crash notice). It sets a
  new `Event.ResolvedIn time.Duration` (>0), which `render` turns into a "…then recovered"
  title + "recovered after Ns" body.
- Window = global setting `Settings.CoalesceWindowSeconds *int` with **pointer/presence
  semantics** (mirroring M28's `CooldownOverrides`): `nil` → default **10s**; explicit `0` →
  **disabled** (immediate, today's behavior); `N` → N-second window. Read live from the store on
  each `Emit`/sweep, so dashboard changes take effect without a restart. A new row in the
  notification settings UI exposes it ("Coalesce window (seconds, 0 = off)").

## Files changed (5 task commits + 2 doc commits)

- `internal/notify/model.go` — `Event.ResolvedIn`; `Settings.CoalesceWindowSeconds *int`;
  `const defaultCoalesceWindowSeconds = 10`; `coalesceWindow()` helper.
- `internal/notify/render.go` — merged-notice branch when `ResolvedIn > 0` (plain path
  byte-identical when 0).
- `internal/notify/coalesce.go` — **new**: the `Coalescer` (`NewCoalescer`, `Emit`, `flush`,
  `drain`, `Run`, `WithCoalesceClock`, `WithSweepInterval`, minimal `settingsReader` interface).
- `internal/notify/coalesce_test.go`, `model_test.go`, `render_test.go` — TDD tests (reuse the
  existing `fakeStore` and `recEmitter` test helpers).
- `internal/server/server.go` — wired `co := NewCoalescer(disp, ns)`,
  `det := NewDetector(reg, co, 2s)`, both `go co.Run(ctx)` and `go det.Run(ctx)`.
- `internal/dashboard/notifications_test.go` — `coalesce_window_seconds` PUT round-trip guard.
- `web/src/api.ts`, `web/src/Notifications.tsx`, `internal/dashboard/dist/**` — settings row +
  regenerated embedded bundle (`make ui`).
- `CHANGELOG.md` — `[Unreleased] → Added` entry.

## How it was built / verified

- **Subagent-driven development**: fresh implementer per task (haiku for transcription tasks,
  sonnet for the `make ui` toolchain task) + a per-task spec+quality reviewer (sonnet), then a
  whole-branch review on opus. All task reviews came back spec ✅ / quality Approved.
- **Final opus review verdict: Ready to merge (Yes).** No Critical/Important. Verified
  concurrency: `out.Emit` is never called while holding `c.mu`; no lock-ordering risk against
  the Dispatcher's mutex; the pending map is always accessed under the lock; `Run` drains and
  exits cleanly on `ctx` cancel.
- **State checks (all green):** `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .`
  (empty), `make build` (`v0.1.0-45-g405c80c`).

Build/run/test: `make build`; `go test ./... -race -count=1`; `make ui` rebuilds the SPA bundle.

## Live demo result (scratch `/tmp/marshal-m30-demo`, server `:9000`/`:9001`, sink `:9099`)

A flapping app (online ~5s, then escalating sub-1s crashes with `max_restarts: 20`, then stable)
produced a real `online → restarting → online` transition that the 2s detector sampled.

- **Coalescing ON (`coalesce_window_seconds: 60`):** exactly **one** webhook — `type:crash`,
  delivered ~12s after the event (i.e. at recovery time → the merged emit, *not* the 60s
  window-flush), and **no** separate `recovered`.
- **Coalescing OFF (`coalesce_window_seconds: 0`):** **two** webhooks — `crash` (immediate) and
  `recovered` (~14s later), separately.

That is the feature: one notice vs two. Note the webhook payload carries only
`type/agent/process/detail/time` (not the rendered title/body or `ResolvedIn`), so the
user-visible "…then recovered" wording is proven by `render_test.go`; the demo proves the
1-vs-2-notice behavior and the merged-emit timing. Torn down by isolated `XDG_DATA_HOME` + PID;
the standing launchd daemon was preserved; scratch removed.

## Known behavior / deferred (not bugs)

- **Default-on adds latency on upgrade (by design, documented).** A config written before M30
  has `CoalesceWindowSeconds == nil` → resolves to 10s, so after upgrade a *genuine sustained*
  crash (one that does not recover) is held up to ~window + sweep (~11s) before alerting. This
  is the default the user chose during brainstorming and is in the CHANGELOG. If undesirable,
  defaulting `nil → 0` (opt-in) is the alternative — a one-line change in `coalesceWindow()`.
- **Webhook payload** does not include `ResolvedIn` or the rendered title/body. If a consumer
  wants to distinguish a merged notice programmatically, adding `resolved_in` to the webhook
  body (and the other channels) is a small follow-up — deferred (YAGNI for M30).
- **M31 (Signal UI pass)** should restyle the new coalesce-window row along with the M28
  per-event cooldown rows and the M29 connect-agent modal (function-first for now).
- Minor review notes (all no-fix, logged in the ledger): a one-line comment documenting
  `flush`'s window+sweep worst-case bound; `render_test` plain-path could assert full body
  byte-identity; UI collapses `nil → explicit 10` on first Save (same as existing cooldown rows).

## Concrete next step

Merge `m30-coalescing` → `dev` (`--no-ff`), update the ledger, and delete the branch. Then the
remaining roadmap items are **M31 (Signal/M19 UI styling pass)** and **cutting v0.3.0** (move
`CHANGELOG.md` `[Unreleased]` into a dated `## [0.3.0]`, update compare links, merge `dev → main`
`--no-ff`, tag `v0.3.0`, push `main`/`dev`/tag).
