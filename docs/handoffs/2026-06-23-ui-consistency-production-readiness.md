# UI consistency & production-readiness pass — Handoff

**Date:** 2026-06-23
**Branch:** `dev` (clean; M30 merged at `830a1b2`). No code changed in this session beyond
writing this handoff. `main` unchanged.

## Why this handoff

Direction from the user: **make the dashboard visually consistent and production-ready before
adding any more features.** "Some pages look off." This is a focused polish/conformance pass —
**freeze new features** until the existing UI is solid and uniform. It supersedes the previously
sketched "M31 — Signal/M19 UI styling pass" and gives it concrete scope.

This is **not** (yet) an implementation handoff — no plan has been written. It is a grounded
survey of what's inconsistent and a recommended path. The next session should confirm scope with
the user (see *Open scope questions*), then brainstorm → spec → plan → implement per the project
convention.

## Background: the design system already exists

`web/src/styles.css` (~178 declarations) is a coherent dark "Signal" design system:
- **Tokens** in `:root`: `--bg #0A0A0C`, `--panel #121216`, `--panel-2 #16161B`,
  `--border #26262C`, `--border-soft`, `--text #C7CAD2`, `--dim`, `--faint`, accents
  `--cyan #2DD4BF` / `--lime #A3E635` / `--danger #F87171` / `--mem`; radii `--r/--r-lg/--r-sm`;
  `--mono` (JetBrains Mono). `color-scheme: dark`.
- **Shared classes**: `.app` (page shell), `.topbar`/`.topbar-actions`, `.brand`, `.btn`
  (+ `.btn.primary`, `.ctl-btn`, `.seg`), `.card`/`.pcard`/`.stat-card`/`.tile`, `.modal*`,
  `.field` (+ `.hint`), `.crumb`, `.summary`, log/metric/credential/file-browser classes.

**Most pages conform** and look right: Overview, ProcessDetail, Credentials, AddAppModal,
ConnectAgentModal, Login, ControlButtons, ProcessCard, SummaryCards, MetricChart, LogView,
Sparkline. The goal is to bring the stragglers up to this same system — not to redesign it.

## Concrete findings (what's off, with evidence)

### 1. The Notifications page is the main offender — `web/src/Notifications.tsx`
Reached at `#/notifications`. It is almost entirely outside the design system:
- **Missing the app shell entirely.** `App.tsx` renders `<Notifications />` with **no**
  `onLogout` prop, and the component returns `<div className="panel">` — there is **no `.app`
  wrapper, no `.topbar`, no brand/Logo, no sign-out, no "← fleet" back link.** Every other
  page wraps in `.app` + `.topbar` (see `Overview.tsx:65`, `Credentials.tsx:80`,
  `ProcessDetail.tsx:99`). So this page has no chrome and no way back.
- **`.panel` class does not exist** in `styles.css` (`grep -c "\.panel"` → 0). The wrapper is
  unstyled.
- **All bare elements, no classes** (only 3 `className` uses in 191 lines): `<section>`, `<h2>`,
  `<h3>`, `<h4>`, `<ul>/<li>`, `<select>`, `<input>`, `<label>`, and **6 bare `<button>`s**
  (no `.btn`). On the dark mono theme these render as default-browser white inputs / gray
  system-font buttons / serif headings — visually broken.
- This is the function-first UI from M27 (channels/rules/settings), M28 (per-event cooldown
  rows), M29 (settings) and M30 (coalesce-window row) that was always deferred to "the UI pass."
  This page **is** the UI pass.

### 2. No heading styles anywhere — `web/src/styles.css`
`h1`–`h4` have **zero** CSS rules (`grep -cE "h[1-4]\s*\{"` → 0). Conforming pages avoid raw
headings by using explicit classes (`.cred-head`, `.modal-title`, `.dtitle .pname`,
`.stat-label`). Notifications relies on raw `<h2>/<h3>/<h4>`, which therefore render as default
serif/oversized. Need a shared heading treatment (e.g. a `.section-title` class, or scoped
`.card h3` / `.app h2` rules) and apply it.

### 3. FileBrowser references undefined tokens — `web/src/styles.css:173,175`
`.fb-list` uses `var(--line, #333)` and `.fb-row:hover` uses `var(--hover, #222)`. Neither
`--line` nor `--hover` is defined in `:root`, so they fall back to `#333`/`#222` — close to but
not the Signal palette (`--border #26262C`, `--panel-2 #16161B`). Either define the tokens or
repoint these to the existing ones. (`var(--danger, #e66)` on line 170 is fine — `--danger`
exists.)

### 4. Deferred minor items to fold into this pass (from the M29 / dev-status handoffs)
- `web/src/ConnectAgentModal.tsx` / `api.ts`: `connectToken()` sends `address:""`/`name:""`
  rather than omitting them (backend treats empty == absent, so harmless but sloppy).
- Connect modal copy button: `navigator.clipboard.writeText(...)` has no `.catch()` — a rejected
  clipboard write is unhandled. Add a fallback/toast.
- Backend: `dashboard.Serve`'s doc-comment doesn't mention the `enroll` param.
- Backend: connect handler's `connectTokenReq{Address,Name}` body fields are decoded but unused.

## Recommended path (for the next session)

**Step 0 — In-browser audit first (per the "viewable demo each session" convention).** Stand up
the dashboard (see the M30 handoff or M29 handoff for the scratch-server recipe: auth while
server down, server on `:9000`/`:9001`, log in) and screenshot **every** route and modal:
`#/` (Overview), `#/a/<agent>/p/<proc>` (ProcessDetail), `#/credentials`, `#/notifications`,
the Add-App modal, the Connect-Agent modal, and the Login screen. Confirm the static findings
above and catch anything they miss (loading/empty/error states, overflow, responsiveness).

**Step 1 — Notifications page (the bulk).** Give it the `.app`/`.topbar` shell (Logo +
sign-out + "← fleet" back link; thread `onLogout` from `App.tsx`), convert sections to `.card`,
inputs/selects to `.field`, buttons to `.btn`/`.btn.primary`, and the channel/rule lists to
styled rows (reuse a `.cred-row`-style pattern or add a shared list class). Keep behavior
identical — this is restyling, not a logic change.

**Step 2 — Shared heading treatment.** Add the heading class/rules and apply across pages that
need it.

**Step 3 — FileBrowser tokens.** Define `--line`/`--hover` or repoint to `--border`/`--panel-2`.

**Step 4 — Fold in the four minor items** from finding #4.

**Step 5 — Rebuild + verify.** `make ui` (regenerates the embedded `internal/dashboard/dist`
bundle — commit it), `make build`, then re-run the in-browser audit and screenshot every page
again to confirm consistency. `go test ./... -race -count=1` (the bundle is embedded, so keep
the suite green).

## Open scope questions (confirm with the user before planning)

1. **Conformance only, or a Signal refresh too?** Recommended: pure conformance — apply the
   existing system to the off pages; do not redesign tokens/layout. ("Consistent styling," not
   "new look.")
2. **"Production-ready" = visual consistency only, or also UX states?** The explicit ask is
   styling consistency. But production-readiness usually also means: every page has proper
   loading / empty / error states, no console errors, sane behavior on narrow viewports, and
   keyboard/focus basics. Recommend a short checklist pass on these during the audit; confirm how
   far to take it.
3. **Notifications: incremental restyle vs. rebuild?** The page is small (191 lines) and
   logic-correct; an in-place restyle (swap elements for classed equivalents) is lower-risk than
   a rewrite.

## Build / run / test

```bash
make ui      # rebuild the SPA into internal/dashboard/dist (embedded by Go) — commit the bundle
make build   # build the binary with the embedded bundle
go test ./... -race -count=1
go vet ./... && gofmt -l .
```
Dashboard demo recipe (auth while server down, server on `:9000`/`:9001`): see
`docs/handoffs/2026-06-23-m30-coalescing.md` and `2026-06-22-m29-agent-connect-command.md`.

## Concrete next step

Confirm the *Open scope questions* with the user, then **brainstorm** the scope (creative/visual
work → use the brainstorming skill), write a spec + plan, and execute on a branch off `dev`
(`ui-consistency` or similar). Do **not** start new features until this lands. Start the
implementation session with the Step 0 in-browser audit.
