# Dashboard redesign — "Marshal Instrument" design spec

**Date:** 2026-06-23
**Branch:** `ui-redesign` (off `dev`)
**Status:** Design approved in brainstorming (visual companion). Not yet planned/implemented.
**Supersedes:** the conformance-only "UI consistency pass" sketched in
`docs/handoffs/2026-06-23-ui-consistency-production-readiness.md`. The user chose a **full
redesign** (rethink the look), **full hardening**, and a **from-scratch Notifications rewrite**.

## Goal & rationale

Replace the current all-mono terminal "Signal" theme with a distinctive **instrument / ledger**
design language inspired by the **app.pm2.io** dashboard (reference screenshots in `ref/`), but
with Marshal's own identity. The user explicitly rejected the generic "AI app" look (floating
cards with a colored left edge, gradient logos, bento grids). The agreed direction:

- **One continuous surface divided by hairline rules** — not floating cards.
- **Dense, tabular, numbers-forward** ledgers and metric clusters (PM2.io energy).
- **Semantic, muted multi-hue palette** — each *metric type* owns a colour; colour carries
  meaning, never decoration. Restrained, not playful.
- **PM2.io layout DNA:** a left **icon rail** for nav, big light-weight stat numbers, per-row
  quick actions, a dedicated **Errors/exceptions** view, toggle switches for settings.

The interactive prototype that defines this spec is preserved at
`.superpowers/brainstorm/46891-1782222731/content/demo3.html` (plus the exploration screens
`directions.html`, `monit.html`, `instrument-overview.html`, `pm2io-overview.html`). **When
implementing, open demo3.html as the visual source of truth.**

## Design tokens ("Marshal Instrument")

Replaces the current `web/src/styles.css` `:root`.

```
--bg:#0C0E12  --panel:#0F1217  --row:#101319  --line:#1A1D24  --line2:#14171D
--tx:#C4C8D0  --dim:#727B89    --faint:#4A515E --bright:#EDEFF3
/* semantic hues — muted, one per metric type */
--teal:#34D0BA   (CPU · primary action · active nav · online accent)
--indigo:#8189EC (memory)
--olive:#9DC15A  (uptime · healthy/online status)
--amber:#E0A458  (restarts · warnings · reload)
--rose:#E5707E   (errors · danger · stop)
--sky:#5BA8D4    (network · links · info/secondary)
--r:6px  --r-sm:4px
```

- **Typography:** introduce **Inter** for chrome/labels/prose; keep **JetBrains Mono** for all
  data — numbers, process names, PIDs, logs, code — with `font-variant-numeric: tabular-nums`.
  Both fonts self-hosted/bundled (Inter is new to the project).
- **Labels:** small-caps style — `text-transform:uppercase; letter-spacing:.08–.12em`, ~10.5px.
- **Surfaces:** hairline borders (`--line`/`--line2`), radii 4–7px, **no colored side-borders on
  cards**, no gradients as decoration.
- **Scale note:** type/spacing tuned up from the first draft (stat numbers ~32px, table rows
  ~13px/52px tall, content max-width ~1340px) so it doesn't read "zoomed out."

## Shell & navigation (PM2.io-style)

- **Left icon rail** (~78px, fixed): brand mark (`m$`), then section items each = icon + tiny
  uppercase label: **Fleet · Errors (red count badge) · Logs · Notify · Creds**, with **Settings**
  pinned to the bottom. Active item = teal with a 2px inset bar.
- **Top context bar** (~56px): left = current page + breadcrumb ("Fleet · 3 agents · 7 proc",
  "web-01 / api-gateway"); right = `+ agent`, `+ add app`, `sign out`.
- **Content** centered, max-width ~1340px.

This replaces today's topbar-only nav in `App.tsx`/per-page `.topbar`. Routing gains an
`#/errors` route; the rail is a shared shell component wrapping every authed page.

## Shared components

- **Metric cluster** — horizontal grid of cells divided by vertical hairlines; each cell =
  small-caps label + big light mono value (`32px`, unit in smaller `--faint`) + a sub-detail line.
  Used on Fleet (fleet totals), Process detail (process metrics), Errors (error totals).
- **Ledger table** — numbered rows (`01,02…`), small-caps header, tabular mono figures,
  hairline row dividers, status as a coloured square + word. Hover reveals **labeled quick-action
  pills** (icon + word): **▤ Log · ▸ Restart** (teal) · **⟲ Reload** (amber) · **■ Stop** (rose).
  Used for processes, channels, rules, credentials, exceptions.
- **Section header** — `01 · TITLE` + ruling line + optional right-side control/count.
- **Sparkline** (per-process CPU trend) and **bar-sparkline** (error occurrences), colour-coded.
- **Area+line chart** — semantic-coloured (teal CPU, indigo mem), time-range segment (15m/1h/6h).
  Reuse/restyle the existing `MetricChart`.
- **Controls** — segmented toggles, **toggle switches** (settings booleans), event **chips**,
  instrument inputs/selects, buttons (default teal / `.warn` amber / `.dgr` rose / `.ghost`).
- **Modals** — header + body + footer. Includes the **live-log modal** (below).

## Pages

1. **Fleet overview** (`#/`) — fleet metric cluster (Agents, Running, Errored, Avg CPU, Total
   Mem, Restarts 24h) + one **section per agent** (status glyph, host meta, `⟲ restart all`),
   each containing that agent's **process ledger**. Rows are clickable → detail; hover → quick
   actions + live-log.
2. **Process detail** (`#/a/<agent>/p/<proc>`) — breadcrumb, status header with controls
   (`▤ live log · ▸ restart · ⟲ reload · ■ stop`), and **sub-tabs: Overview · Files · Logs**.
   - *Overview:* process metric cluster + CPU/Mem charts (time-range) + recent-logs panel.
   - *Files:* the git **FileBrowser** restyled — note line, breadcrumb + `+ new file`, push
     confirmation, two-pane (file ledger | code editor) + commit-message & `save & push` bar.
     Only for `source === "git"` apps (unchanged behaviour).
3. **Errors** (`#/errors`) — **NEW page.** Exceptions cluster (Errors 24h, Distinct signatures,
   Affected procs, Last error) + a **ledger of error signatures**: message + source location,
   source agent/process, occurrences bar-sparkline, count, last-seen; time-range filter (all/24h/7d).
   *See "New data needs" — this page's backend support must be scoped before building.*
4. **Logs** (`#/logs` or in-detail) — stream (all/stdout/stderr) + limit segments, text/regex
   filter, download, terminal stream with colour-coded levels.
5. **Notifications** (`#/notifications`) — **full rewrite** in the new language: Channels ledger
   + add form; Rules ledger (coloured event tags) + add form (event **chips**); Settings (recovery
   **toggle**, global cooldown, coalesce window, per-event cooldown override grid). Behaviour/API
   identical to today (M27–M30) — restyle + restructure only.
6. **Credentials** (`#/credentials`) — stored ledger + add form (https/ssh segmented type switch).
7. **Login** — minimal centred form on the instrument background, mono wordmark + tagline.
8. **Live-log modal** — opened from a row's `▤ Log` quick action or the detail header. Header
   (process name + live/pause) + **filter bar** (text/regex filter, stream selector, level
   toggles info/warn/error, download) + streaming logbox with a blinking live cursor.

## New data needs / "show only real data" (MUST scope before planning)

The mockups show some metrics Marshal may not collect today. **Only render data the agent
actually provides; omit or clearly defer the rest** — never imply data we don't have.

- **Confirmed available** (from `ProcessDetail`/`api.ts`): per-process `cpu`, `mem`, `uptime_ms`,
  `restarts`, `pid`, `state`; per-process log error counts via `getLogStats`; CPU/mem metric
  buckets via `getMetricsForProc` (powers charts + sparklines). Agent: name, connected, last-seen.
- **Shown in mockups but NOT known to exist — confirm or drop:** host-level CPU/Mem/load, Net
  I/O, threads/fds, process `source`/port/runtime labels beyond what exists, agent OS/arch/version.
- **Errors page** needs error **signature grouping** (group log errors by message, track
  occurrence history + last-seen), which `getLogStats` (per-process counts only) does not provide.
  **Recommended phasing:** ship the Errors UI shell driven by existing per-process error counts
  first; treat full signature grouping + occurrence history as a follow-up backend task. Decide
  during planning whether Errors is in-scope-now or deferred.

## Hardening (full — user chose "full hardening")

Per page, as a checklist during implementation:
- Loading / empty / error states for every data view (cluster, ledgers, charts, logs).
- No console errors; network failures surfaced, not swallowed.
- Sensible narrow-viewport behaviour (rail collapse, ledger horizontal scroll/stacking).
- Keyboard & focus: visible focus rings, modal focus-trap + Esc-to-close, tab order.
- Accessibility: form labels, button `aria-label`s for icon-only controls, status not by colour
  alone (square + word already helps), contrast check on the muted hues.
- React error boundaries around page roots.

## Implementation sequencing (for the plan)

1. Tokens + base CSS (`styles.css` rewrite) and bundle **Inter**.
2. App shell: icon rail + context bar + `#/errors` route (new shared layout).
3. Shared components: metric cluster, ledger row + quick actions, section header, sparkline,
   chart restyle, toggle/segment/chip/input, modal base.
4. Pages, one at a time (TDD where logic changes): Fleet → Detail (+Files) → Logs → Credentials →
   Login → Notifications (rewrite) → live-log modal → Errors (scope-gated).
5. Fold in the four deferred minor items from the prior handoff (connect-modal clipboard
   `.catch()`, empty `address/name` in `connectToken()`, `dashboard.Serve` doc-comment, dead
   `connectTokenReq` fields).
6. Hardening pass against the checklist.
7. `make ui` (rebuild embedded `internal/dashboard/dist` — commit the bundle), `make build`,
   `go test ./... -race -count=1`, then an in-browser audit screenshotting every route/modal.

## Open questions for planning

- **Errors page scope:** UI shell on existing counts now, or full signature-grouping backend
  first, or defer the page entirely to a later milestone?
- **Which mockup metrics are real?** Confirm the agent's actual metric surface and prune the
  clusters accordingly before building.
- **Version/changelog:** this is a feature-scale change — likely a minor bump with a sizable
  `Changed` block; confirm whether it ships as one milestone or several.
