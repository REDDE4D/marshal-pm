# M-A · "Marshal Instrument" Dashboard Redesign — Handoff

**Date:** 2026-06-24
**Branch:** `mA-redesign` (off `dev` @ `f70a333`). All 20 tasks complete, whole-branch-reviewed
(**READY TO MERGE**), and **live-demo-verified**. **Ready to merge → `dev` (`--no-ff`), then cut
v0.3.0.** `main` unchanged at v0.2.0.

To resume: read this file. Plan: `docs/superpowers/plans/2026-06-24-mA-dashboard-redesign.md`;
spec: `docs/superpowers/specs/2026-06-23-dashboard-redesign-design.md` (see its *Planning
decisions* section); SDD ledger (git-ignored): `.superpowers/sdd/progress.md` (full per-task
record + Minor-findings triage); program roadmap: `docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`.

## What M-A does

The **final** milestone of the dashboard data program: a complete visual + structural redesign of
the web dashboard (`web/src/`, React 18 + Vite SPA) into the locked **"Marshal Instrument"**
design language — backed end-to-end by the real data shipped in M-B…M-G/M-F. **M-A is frontend-
only**: no proto, no backend behavior changes (one tiny Go cleanup aside — see Task 18). The
prototype `.superpowers/brainstorm/46891-1782222731/content/demo3.html` was the visual source of
truth.

**Shipped:**
- **App shell** (`AppShell.tsx`): left **icon rail** (Fleet · Errors[red count badge] · Logs ·
  Notify · Creds; Settings omitted — no global-settings page, per hardening) + top context bar +
  a single `.content` container + a React `ErrorBoundary`. Wraps every authed page.
- **Shared components** (`web/src/components/`): `Cluster` (MetricCluster/Cell), `Ledger`
  (SectionHeader/LedgerHeader/LedgerRow/QuickActions + StatusGlyph), `Controls`
  (Segment/Toggle/Chip/Field/Input/Button), `Sparkline`/`BarSparkline`, `Modal` (focus-trap/Esc/
  backdrop). Plus pure helpers in `web/src/lib/` (`format`,`status`,`fleet`,`logs`,`nav`) — TDD'd
  with **Vitest** (new to the project; 39 tests).
- **Pages** (all real data, per the plan's Data-wiring map): Fleet overview (cluster +
  per-agent process ledgers + hover quick-actions), Process detail (Overview/Files sub-tabs +
  restyled FileBrowser, cluster, area+line charts, recent-logs), **Logs page** (`#/logs`, new),
  Errors (real `/api/errors` signature ledger + bar-sparklines), **Notifications rewrite**
  (toggles + event chips; identical API/payloads), Credentials, Login, + the Add-app/Connect-agent
  modals on the shared `Modal`.
- **Live-log modal** (`LiveLogModal.tsx`): stream/level/text-regex filtering + pause, opened from a
  fleet row's ▤ Log quick-action and the detail header's ▤ live-log button.
- **Hardening pass**: loading/empty/error states, focus rings, keyboard-accessible controls,
  icon-button aria-labels, error boundary, `@media (max-width:720px)` responsiveness.
- **"Show only real data" honored**: every mocked cell binds to a real backend field; the
  non-real mockup bits were dropped (Errors "▲% vs prev"; fabricated runtime/port labels). The only
  intentional `—` placeholders are macOS `open_fds` (-1) and errored-proc rows.

## Method (how it was built)

Brainstorming (scope only — design was pre-locked) → writing-plans (20-task phased plan) →
**subagent-driven-development**: fresh implementer + spec/quality reviewer per task, fix loops for
every Critical/Important, then an **opus whole-branch review** → live demo → this handoff.
Notable in-loop fixes: AppShell was missing the `.content` container (Task 11 — fixed all pages);
BarSparkline clipped at the real 24-bucket error input (Task 8 — now scales to point count);
several keyboard-a11y span→button conversions (Tasks 4/9); `matchFilter` reconciled to the spec;
and the final-review Minor (restored `control().catch()` to avoid unhandled-rejection noise).

## Build / run / test

```bash
cd web && npx vitest run            # 39 frontend unit tests (pure helpers)
cd web && npm run build             # tsc -b && vite build → rebuilds internal/dashboard/dist (commit it)
make build                          # stamps version from git
go test ./... -race -count=1 && go vet ./... && gofmt -l .
make ui                             # = cd web && npm install && npm run build (rebuild embedded bundle)
```
All green at branch tip (`v0.1.0-158-gdb75047`). The embedded `dist` bundle is committed and
reflects HEAD. **Note for future UI work:** intermediate tasks verified with `npx tsc -b` only
(does not write dist); the bundle is rebuilt+committed once at the end to avoid per-commit churn.

## Live demo (2026-06-24) — PASS

Scratch `/tmp/marshal-mA-demo`, real server `:9000` / dashboard `:9001`. Auth set while server down
(`server passwd`, `token --rotate enroll`, `fingerprint`), then started with `--http-listen :9001`,
then a real `demo-agent` (`marshal start`, isolated `XDG_DATA_HOME`) ran `web`×2 (healthy, logging)
+ `worker` (errors to stderr then exits → restarts). `/api/fleet` showed real host CPU/mem,
`restarts_24h` climbing, threads; `/api/errors` showed 3 signatures (ECONNREFUSED + a Go panic with
**source `main.go:42`** extracted, 24 occurrence buckets). Playwright audited every route
(login/fleet/errors/detail/logs/notifications/credentials) + the live-log modal — all rendered
faithfully with real data; `fds —` on macOS as designed; only console message a benign pre-auth
401. Screenshots shown to user. Teardown by data dir; standing launchd daemon (PID 899) preserved;
no orphans.

## Deferred / known (not blocking — all triaged OK-to-defer in the ledger)

- `ProcessDetail` recent-logs duplicates the `useLogStream` hook's polling logic and still uses an
  index-based React key on its 8-line tail — could adopt `useLogStream` later.
- The Connect/Add-app modals' name/address inputs and the breadcrumb agent link have no dedicated
  backend route/effect (pre-existing; acceptable fallbacks).
- `format.test.ts`'s `formatDateShort` assertion is locale-dependent (could flake in non-English
  CI). Task 1's foundation commit (`dad8bac`) is missing the co-author trailer (history nit).
- Cosmetic: live/pause segment renders in the modal body (Modal has no header-right slot); the
  narrow-viewport `.content{overflow-x}` / `.top .rt` rules are broad but scoped to the media query.

## Next step

1. **Merge** `mA-redesign` → `dev` (`--no-ff`), delete the branch.
2. **Cut v0.3.0** (the program's accumulated M-B…M-G + M-F + this redesign — release decided
   "after M-A" in the spec): move `CHANGELOG.md` `[Unreleased]` → `## [0.3.0] - <date>`, update the
   compare links, merge `dev` → `main` (`--no-ff`), `git tag v0.3.0`, push `main`, `dev`, and the
   tag. **This is the first outward-facing publish of the whole data program — confirm before
   pushing.** After this, the dashboard data program is complete.
