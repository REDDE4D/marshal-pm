# Dashboard UX-clarity pass — design spec

**Date:** 2026-06-25
**Status:** Design approved in brainstorming. Ready for the implementation plan (writing-plans).
**Base branch:** `ui-clarity-pass` off `dev` (after `dev` is fast-forwarded to `main`).
**Trigger:** A user filled the Notifications "add channel" form, clicked the unrelated "save settings"
button, and nothing saved — with no feedback. Diagnosis on the live server (`89.163.150.187`)
confirmed the backend and save paths are healthy; the failure was a **class of UI-clarity problem**
(ambiguous commit buttons, silently-disabled buttons, weak/absent feedback, missing empty states)
that recurs across the dashboard.

## Goal & non-goals

**Goal:** Make the dashboard unambiguous and self-explanatory — every action says what it commits,
every disabled control explains itself, every outcome is visibly reported, and empty lists guide the
user — by fixing the problem at the **shared-primitive** level and rolling the primitives across all
pages (approach "A", primitive-first).

**Non-goals (deliberately out of scope):**
- No visual re-theme. This does not start or block the stalled "Marshal Instrument" redesign
  (`docs/superpowers/specs/2026-06-23-dashboard-redesign-design.md`); the clarity primitives are
  semantic and carry into any future theme.
- No new product features, no API/data-model changes beyond what clearer feedback requires.
- No backend changes (the notify/settings/channel persistence is verified correct).

## Audit basis

A full frontend audit catalogued ~28 findings clustering into: ambiguous/duplicate action buttons,
disabled buttons with no explanation (~8 sites), silent or easy-to-miss feedback, missing empty
states, inconsistent confirmation patterns (`window.confirm`/`window.prompt` vs inline "pending" vs
modal), and unclear field/label semantics. Shared components live in `web/src/components/`
(`Controls.tsx`: Button, Input, Field, Toggle, Segment, Chip; `Ledger.tsx`; `Modal.tsx` — already
accessible with focus-trap + Esc).

## Section 1 — Shared primitives

All upgrades are **backward-compatible** (new props optional); call sites migrate incrementally.

1. **`Field` (upgrade, `components/Controls.tsx`)** — add optional `required` (subtle dim marker,
   not a loud asterisk), `hint` (small dim help line under the control), `error` (rose line that
   replaces `hint` when present).
2. **`Button` (upgrade)** — add `disabledReason?: string`. When set, the button renders disabled
   with that text as an accessible hover tooltip; when undefined, enabled. The reason *is* the
   disabled condition, so a greyed button always explains itself.
3. **`EmptyState` (new)** — `message` + optional `action`/hint; muted in-place guidance where an
   empty list would render blank.
4. **`StatusMessage` (new)** — one inline component standardizing the scattered ad-hoc `<span>`
   feedback: consistent placement (next to the triggering action), color semantics (teal = success,
   rose = error, dim = info), and timing — **success auto-clears after ~4s, errors persist** until
   the next action. Inline, **not** a floating toast (toasts read like the rejected generic-app
   look). A small `useStatus()` hook may back it.
5. **`ConfirmDialog` / `PromptDialog` (new, built on `Modal`)** — promise-returning dialogs that
   replace unstyled `window.confirm`/`window.prompt` for **irreversible** actions (delete
   file/credential/channel/rule, kill). Inherit Modal's focus-trap + Esc. **Scope rule:** only
   irreversible actions get a modal; frequent recoverable actions (restart) keep the lightweight
   inline "click again to confirm" — but with the confusing silent 3s auto-dismiss removed and a
   clearer label.
6. **Action-labeling conventions (applied during rollout)** — commit buttons name what they commit
   and sit visually *inside* their form section; toggle-state labels (`ack`/`acked`,
   `start`/`restart`) get clarifying tooltips; no two same-looking save buttons without clear
   section ownership. Recorded as a short conventions note for future pages.

## Section 2 — Rollout per page

**Notifications** — per-section commit button moved inside its own field block with a sub-label;
`+ add channel`/`+ add rule` → "Add channel"/"Add rule" with `disabledReason` ("Enter a name
first"); `name` field gets `required` + `hint` ("A label for this channel, e.g. tgbot — separate
from the bot token"); `EmptyState` for zero channels/rules; `StatusMessage` everywhere; per-channel
test reports which channel + the real error.

**Credentials** — `disabledReason` (type-aware, lists missing field) on submit; clarify the
"generate key"/"add credential" mode via a sub-label; add a `StatusMessage` success confirmation
(today it silently clears the form).

**Add-app modal** — `disabledReason` on "add app"; error surfaced at the **top** of the form;
required markers + hints on the conditional git fields.

**Overview / Fleet** — `EmptyState` for "no agents" pointing at **+ connect agent**; promote
link-styled `+ add app`/`+ connect agent` to real buttons; standardize `RestartAllButton` onto the
same fixed inline-confirm pattern as `ControlButtons`.

**FileBrowser** — replace `window.confirm`/`window.prompt` (delete, rename) with
`ConfirmDialog`/`PromptDialog`; `disabledReason` on Save ("No changes to save"); success via
`StatusMessage` (auto-clear).

**Errors / Logs / ProcessDetail** — Errors: tooltip on `ack`/`acked` ("Acknowledge — stops this
error from nagging"); Logs: `disabledReason` on the process selector ("No processes for this
agent"); ProcessDetail: `disabledReason` on disconnected controls, `EmptyState` for empty
recent-logs, note the "last N lines" with the live link.

**Login** — keep the intentionally-generic auth error; fix submit re-enable/focus after a failed
attempt.

**Shared cleanup** — `ControlButtons`: drop the silent 3s auto-dismiss + clearer "click to confirm".

## Section 3 — Sequencing, testing, logistics

**Phases (each independently shippable):**
- **Phase 1 — Primitives** (`Field`, `Button`, `EmptyState`, `StatusMessage`, `ConfirmDialog`/
  `PromptDialog`); no page behavior changes.
- **Phase 2 — High-confusion pages** (Notifications, Credentials, Add-app).
- **Phase 3 — Remaining pages** (Overview/Fleet, FileBrowser, Errors, Logs, ProcessDetail, Login,
  ControlButtons fix).

**Testing:**
- TDD on primitive logic with vitest + **`@testing-library/react`** + jsdom (new dev-deps):
  `Field` renders hint/error/required; `Button` disables + tooltips on `disabledReason`;
  `StatusMessage` auto-clears success but holds errors; `ConfirmDialog` resolves/rejects.
- Per-phase Playwright smoke against a local demo for key flows (add-channel, add-credential,
  delete-with-confirm).
- `go test ./... -race` stays green (backend untouched).

**Branch & release:** fast-forward `dev` to `main`, branch `ui-clarity-pass` off `dev`. Ship as
**v0.10.0** (minor: noticeable UX changes + new reusable components), `CHANGELOG` under Changed/Added
as work lands. Phase 3 may split to **v0.10.1** to land 1+2 sooner.

## Risks / open items

- `@testing-library/react` + jsdom dev-dependency addition (approved as default; revisit on review).
- `StatusMessage` timing constants (success ~4s) tunable during implementation.
- Converting `RestartAllButton` to the shared inline-confirm pattern is a small interaction change;
  verify it still reads clearly in the agent header.
