# Marshal Dashboard M19 — Redesign + Drill-Down ("Signal" identity) — Design

**Date:** 2026-06-18
**Status:** approved (pending implementation)
**Scope:** `web/src` (most of the work), `internal/dashboard` (small: a recent-error-count
endpoint), `internal/server` + `internal/logstore` (a count query). No
proto, agent, or manager changes.

## Problem

The dashboard was built function-first across M11–M18: a single `Fleet.tsx` view with a plain
`styles.css` (system font, one blue accent), an inline expand-row for per-process charts/logs,
and no visual identity. It works but was never *designed*, and the information architecture is
flat — everything lives on one screen. This milestone gives Marshal a deliberate brand identity
and an overview → detail drill-down.

## Goals

1. A distinctive visual identity ("Signal") applied across every screen.
2. An **overview** that surfaces fleet health at a glance — summary metrics + rich per-process
   cards with inline controls — and drills into a **process detail page** for charts and logs.
3. No behavior regressions: polling, controls, metric charts, and server-side log search keep
   working exactly as today.

## Non-goals (deferred)

- **One-click port open** — its own later milestone (needs a `port` field in app config, proto
  threading, and host-resolution for the open link).
- Light mode (Signal is a dark identity; a light variant is a clean later add).
- Any new backend features beyond the two small additions below.

## Identity — "Signal"

A telemetry/dev-console aesthetic: near-black, monospace-forward, electric accents.

**Tokens** (CSS custom properties on `:root`):

```
--bg            #0A0A0C   page
--panel         #121216   cards / surfaces
--panel-2       #16161B   row hover / nested
--border        #26262C   hairline
--border-soft   #1C1C22
--text          #C7CAD2   body
--text-dim      #7A7E8C   muted
--text-faint    #4D5160   timestamps / hints
--cyan          #2DD4BF   primary accent (links, cpu, focus)
--lime          #A3E635   online / highlight / cursor
--danger        #F87171   errors / stop
--mem           #5B6BD8   memory series
--radius        10px / 12px (cards) / 7px (controls)
font: "JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, monospace
```

- **Type:** JetBrains Mono, vendored as a subset **woff2 (OFL-licensed) bundled into `web/`**
  and `@font-face`-loaded — no runtime CDN, so the self-hosted dashboard stays offline. System
  monospace fallback. Lowercase terminal labels; **never ALL-CAPS**. Two weights (400/500/700
  available; use 400 + 500, 700 only for the wordmark).
- **Numbers are prominent:** summary-card values render at ~30px/500; per-card cpu/mem values at
  ~15px; everything else small and quiet so the data pops.
- **Logo/wordmark:** `marshal_` (the trailing `_` is a lime terminal cursor) beside a bracket-
  `>`-cursor mark (inline SVG: a rounded square outline in cyan, a `>` chevron + caret in lime).
  Reused in the overview header, the login card, and as the favicon.
- **Flat:** no gradients/shadows; hairline borders; status encoded by a colored dot + text.

## Information architecture

A tiny **hash router** (`#/` overview, `#/a/<agent>/p/<proc>` detail) — hand-rolled, no
dependency. Back-button and deep-linking work; unknown hashes fall back to the overview. The
existing SPA static-fallback already serves `index.html` for any path, so this is purely
client-side.

### Overview page (`#/`)

- **Header:** wordmark + mark, `sign out`.
- **Summary cards** (five, computed client-side from `/api/fleet`): agents online (`n / m`),
  processes (`up / total`), total cpu (`%`), total memory, and **errors** = count of processes
  in a failed state (see "Error counter"). Big prominent numbers, muted labels. The errors card
  turns its number `--danger` when non-zero.
- **Per agent:** a small header (status dot + agent name + last-seen) followed by a stack of
  **full-width process cards**, one per process:
  - **Top row:** status dot + process name + state text (`online`/`stopped`/`errored`) on the
    left; **start · restart · stop** buttons on the right, **state-aware** (online ⇒ start
    disabled; stopped/errored ⇒ restart/stop disabled). Buttons `stopPropagation` so they don't
    trigger navigation. Stop is danger-tinted; a confirm step is preserved from today's control
    flow.
  - **Meta line:** `agent · pid · uptime · N restarts` (or `agent · exited code C · N restarts ·
    <when>` when not online).
  - **Metrics line:** `cpu` sparkline + value, `mem` sparkline + value, and a **recent-error
    badge** (`<i ti-alert-triangle> 12` in `--danger`) shown only when the process has stderr
    output in the last 5 min; a `view details →` affordance on the right.
  - The whole card is a link to the detail page; an errored process gets a `--danger` left
    accent and red status dot.

### Process detail page (`#/a/<agent>/p/<proc>`)

- **Back link** (`← fleet`) + breadcrumb (`agent / process`).
- **Header card:**
  - Top row: status dot + process name (large) + state pill on the left; the start/restart/stop
    controls (state-aware, with labels) on the right.
  - **Stat-tile row** (four small tiles): `cpu` (current %), `memory` (current), `uptime`, and
    `errors · 5m` (the recent-stderr count, prominent here in `--danger` when non-zero). This is
    the recent-error count's primary home; the overview card badge mirrors it.
  - **Meta line:** `pid · N restarts · started <time>`. (`started` is derived client-side from
    `now − uptime_ms` — no backend field needed. The launch **command** is intentionally *not*
    shown: `ProcInfo` does not carry it, and adding it would require a proto + agent change,
    which is out of scope for M19.)
- **Metrics card:** a window selector (the existing window options, e.g. 15m / 1h / 6h) above
  **side-by-side** cpu and memory time-series charts (`MetricChart`), recolored to the Signal
  palette (cpu `--cyan`, mem `--mem`), each with its current-value label.
- **Logs card:** the existing log panel — stream segmented control (all/stdout/stderr), limit
  control, and the M18 server-side search box — restyled and given more height. This is the
  current inline-expand content, moved to the page.

Both pages poll on the existing intervals; navigating away cancels their timers (the current
effect-cleanup pattern). The detail page fetches `/api/metrics` and `/api/logs` for its one
process (as the inline view does today) plus `/api/logstats` for the `errors · 5m` tile.

## Error counter

- **Summary "errors" card** — count of processes whose state is failed/errored (non-online with
  a non-zero exit or crash). Derived entirely client-side from the `/api/fleet` proc states; no
  backend.
- **Per-card recent-error badge** — count of **stderr** log lines in the last 5 minutes for that
  process. Requires a lightweight count:
  - `logstore.Store.ErrorCounts(labels []string, sinceMs int64) (map[string]int64, error)` —
    `SELECT label, count(*) FROM log_line WHERE label IN (…) AND stderr = 1 AND ts >= ? GROUP BY
    label`. (Indexed by `(label, ts)`.)
  - `server.logStores.ErrorCounts(agent string, sinceMs int64) (map[string]int64, error)` —
    resolves the agent's labels and delegates.
  - dashboard `LogStats` interface (`ErrorCounts(agent, sinceMs)`), satisfied by
    `*server.logStores`, exposed as `GET /api/logstats?agent=<a>` → `{"counts":{"web#0":3,…}}`.
  - The overview polls `/api/logstats` per visible agent (alongside `/api/fleet`) and sums
    counts across a process's instance labels for its badge.

(Definition note: "recent errors" = stderr lines, matching the red-line convention already used
in the log view. A text-match variant is a future refinement, not in M19.)

## Backend additions (small)

1. **Recent-error count** — the `ErrorCounts` query + `LogStats` interface + `/api/logstats`
   endpoint described above.

That is the only backend change. Everything else is frontend.

> **Revised during implementation:** an earlier draft of this spec added a `start` control op.
> That was wrong — the proto's `ControlOp_Start` carries a `StartRequest` (an app-spec list to
> launch *new* apps), not a by-name selector, so it cannot "start a stopped process by name".
> `manager.Restart` already revives a stopped/errored managed process (stop-then-recreate), so the
> **start button issues a `Restart` op** (handled in the frontend `ControlButtons`) — no backend
> control change. A true "add app via dashboard" (app-spec → `StartRequest`) is a planned future
> milestone.

## Frontend structure

`Fleet.tsx` (380 lines, doing everything) splits into focused components:

- `router.ts` — `useRoute()` hook over `window.location.hash` (parse `#/a/<agent>/p/<proc>`),
  `navigate(hash)` helper.
- `App.tsx` — auth gate, then route → `Overview` or `ProcessDetail`.
- `Overview.tsx` — fetches `/api/fleet` + `/api/logstats`; renders `SummaryCards` + per-agent
  `ProcessCard` list.
- `SummaryCards.tsx` — the metric-card row (pure presentational).
- `ProcessCard.tsx` — one full-width card (status, meta, sparklines, error badge, controls,
  link).
- `ProcessDetail.tsx` — header + controls + `MetricChart`s + the log panel (the moved
  charts/logs logic, including `getLogs` polling + search from M18).
- `ControlButtons.tsx` — the start/restart/stop cluster with the confirm step (shared by card
  and detail).
- `api.ts` — add `getLogStats(agent)` (the start button reuses the existing `restart` control action).
- `styles.css` — replaced by the tokenized Signal system.
- `assets/` — vendored JetBrains Mono woff2 + `@font-face`.
- `Logo.tsx` — the mark + wordmark, reused.

Existing `MetricChart.tsx` / `Sparkline.tsx` keep their shape, recolored via tokens.

## Error handling

- A failed `/api/logstats` poll is best-effort (badges just don't show) — it never logs the user
  out or breaks the overview, mirroring the existing logs-poll behavior.
- A control action surfaces its server error inline on the card/detail (existing pattern).
- Unknown route hash ⇒ overview.

## Testing

Backend (Go, TDD):
1. `logstore.ErrorCounts` — returns per-label stderr counts at/after `sinceMs`; excludes stdout
   and older lines; unknown label ⇒ absent/zero.
2. `dashboard` `controlOp("start", …)` maps to `ControlOp_Start`; `/api/control` with
   `action:"start"` reaches the controller (extend the existing control test + fake).
3. `/api/logstats?agent=` returns the counts map (handler test with a fake `LogStats`).

Frontend: no unit harness — verified by `make ui` building clean and a live in-browser pass
(see below).

Gate: `go test ./... -race -count=1`, `gofmt -l .` silent, `go vet ./...` clean, `go build`,
`make ui`. Then a live in-browser demo via the Vite-proxy + Preview approach: overview (summary
cards + process cards, an errored process showing the red accent + error badge) → start/stop a
process from a card → click into the detail page → charts + logs + search → back. Then a
handoff.

## Out of scope / deferred

- One-click port open (own milestone); light mode; text-match error counts; alerting; any new
  process-management actions beyond start/stop/restart already supported.
