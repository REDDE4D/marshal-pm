# Handoff — Dashboard UX-clarity pass (v0.10.0)

**Date:** 2026-06-25
**State:** DONE, released. `main` at `v0.10.0` (tagged + pushed); `dev` fast-forwarded/promoted; GitHub
release published: https://github.com/REDDE4D/marshal-pm/releases/tag/v0.10.0. Feature branch
`ui-clarity-pass` merged (`--no-ff`) and deleted (local + remote).

## What & why
Triggered by a real support incident: a user on the live server (`89.163.150.187`, tgbot's
`marshal server`) reported "notification settings won't save." Root cause was **not** a save bug —
the backend persists correctly (verified live). The user had filled the Channels "add channel" form
and clicked the unrelated **"save settings"** button; "+ add channel" was the actual commit button,
and nothing explained the mismatch. That exposed a *class* of clarity problems across the dashboard,
so we did a **primitive-first consistency pass** (spec + plan below).

Prior to this, v0.9.0 had already fixed the *original* masking bug (settings save now surfaces real
errors; added `POST /api/notifications/test` "send test to all channels"). v0.10.0 is the clarity pass
on top.

## Design / plan / execution records
- Spec: `docs/superpowers/specs/2026-06-25-dashboard-ux-clarity-pass-design.md`
- Plan: `docs/superpowers/plans/2026-06-25-dashboard-ux-clarity-pass.md` (16 tasks, 3 phases)
- SDD ledger: `.superpowers/sdd/progress.md` (git-ignored scratch — per-task commits + reviews)

## What shipped
**Phase 1 — shared primitives** (`web/src/components/`):
- `Button.disabledReason?: string` — disabled buttons explain themselves via tooltip (Controls.tsx).
- `Field` `required` / `hint` / `error` affordances (Controls.tsx).
- `EmptyState.tsx` — guidance for empty lists.
- `StatusMessage.tsx` + `useStatus()` — inline feedback; **success auto-clears after 4000ms, errors persist**; teal/rose/dim semantics. (Not a toast — deliberate.)
- `ConfirmDialog.tsx` (`ConfirmDialog` + `PromptDialog`) on the existing accessible `Modal`.
- Test harness: jsdom + `@testing-library/react` + `jest-dom` (vitest now `environment:"jsdom"`,
  includes `*.test.{ts,tsx}`, `web/src/test/setup.ts`).
- Conventions doc: `web/src/components/README.md`.

**Phases 2–3 — rollout** (behavior/API preserved everywhere; no backend change):
Notifications (Add channel/Add rule labels, name hint, empty states, disabledReason, StatusMessage),
Credentials (type-aware disabledReason, type hint, success feedback), Add-app modal (top error,
disabledReason, git-field hints), Overview (EmptyState, real Buttons), FileBrowser (`window.confirm`/
`window.prompt` → ConfirmDialog/PromptDialog, save disabledReason), Errors/Logs/ProcessDetail
(ack/select tooltips, control buttons → Button+disabledReason "Agent offline", recent-logs EmptyState),
ControlButtons (removed silent 3s pending auto-dismiss), Login (re-enable + refocus password on failure).

## Build / run / test
```bash
cd web && npx vitest run          # 56/56 (11 files)
cd web && npx tsc -b              # clean
cd web && npm run build           # rebuilds embedded dist → ../internal/dashboard/dist
go test ./... -race -count=1      # green (backend untouched)
make build && ./marshal --version # v0.10.0
```

## Verification done
- Per-task: implementer + reviewer subagents (spec + quality) on every task; one Important fixed
  (Credentials double-error display) + one Important fixed (Task 1 commit trailer).
- Final whole-branch review (opus): **READY TO MERGE** — data-wiring preserved across all 8 pages,
  v0.9.0 error-surfacing intact, no new Critical/Important.
- Live smokes (Playwright, scratch server :9001): Phase 2 (add-channel labels/empty state/tooltip/
  StatusMessage/Credentials) and release-binary Phase 3 (Login refocus-on-failure, all empty states,
  zero page errors). Demos torn down; no orphans.

## Deferred / known minor items (OK-to-defer, recorded in ledger)
- Notifications "send test" button keeps native `disabled`+`title` (intentional — tooltip shows while enabled).
- FileBrowser `commitRename` no-op guard runs after `setDialog(null)`; `onEntry` no longer clears status (auto-clears in 4s). No data risk.
- Login submit uses bare `disabled={busy}` vs the README's `disabledReason` convention (cosmetic).
- `web/src/components/README.md` lacks a trailing newline.
- ProcessDetail **reload** still uses `window.confirm` (recoverable action, out of scope; future cleanup).
- `CLAUDE.md` still says "Current baseline: v0.1.0" — stale (now v0.10.0); update when convenient.

## Next step
Nothing required. Optional future cleanup: the deferred minors above, especially migrating
ProcessDetail's reload `window.confirm` to the inline-confirm convention for full consistency.
