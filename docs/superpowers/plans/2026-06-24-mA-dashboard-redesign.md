# M-A · "Marshal Instrument" Dashboard Redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restyle and restructure the entire Marshal dashboard SPA into the locked "Marshal Instrument" design language (icon rail + context bar, hairline ledgers, semantic metric clusters, live-log modal, Notifications rewrite, real Errors page), backed end-to-end by the now-real data from M-B…M-G, with a full hardening pass.

**Architecture:** React 18 + Vite SPA under `web/src/`, hash-routed, no external router. The redesign is **presentation-first**: a token rewrite (`styles.css`), a shared **AppShell** (rail + context bar) wrapping every authed page, a small library of shared components (cluster, ledger, controls, sparkline, modal), and a thin layer of **pure helper functions** (`web/src/lib/`) that carry the only genuinely new logic (relative-time, log-level classification, log-filter matching, fleet aggregation, status mapping, route→nav). The pure helpers are TDD'd with **Vitest** (new, pure-function tests only — no DOM). Presentational work is verified by the TypeScript build (`tsc -b`, which `make ui` runs) plus an in-browser Playwright audit per page, matching how M-B…M-G UI was verified.

**Tech Stack:** React 18.3, TypeScript 5.6 (strict), Vite 5.4, `@fontsource` (JetBrains Mono present; **add `@fontsource/inter`**), `@uiw/react-codemirror` (FileBrowser, unchanged), **Vitest (new devDep)**. Backend untouched — every endpoint M-A consumes already exists.

## Global Constraints

- **Visual source of truth:** `.superpowers/brainstorm/46891-1782222731/content/demo3.html`. Its `<style>` block (lines 8–149) is the canonical CSS; its markup (lines 153–349) is the canonical structure for every page/modal. When a task says "markup per demo3 §X", transcribe that section's classes/structure faithfully, then bind real data.
- **Design spec:** `docs/superpowers/specs/2026-06-23-dashboard-redesign-design.md` (tokens, shell, components, pages, hardening checklist). The spec's *Planning decisions* section (resolved 2026-06-24) governs scope.
- **Show only real data (HARD RULE):** render only fields the API actually returns (see *Data-wiring map* below). Never fabricate a metric the mockup shows but the backend lacks. The single intentional `—` placeholder is `open_fds` on macOS (`-1`). The mockup's "▲14% vs prev" error delta and the "node/python :port" runtime/port labels are **not real → omit them** (use `proc.detail`/`source` instead). Document any omission in a code comment.
- **Tokens:** exact palette from the spec — `--bg:#0C0E12 --panel:#0F1217 --row:#101319 --line:#1A1D24 --line2:#14171D --tx:#C4C8D0 --dim:#727B89 --faint:#4A515E --bright:#EDEFF3`; semantic hues `--teal:#34D0BA` (CPU/primary/online-accent), `--indigo:#8189EC` (memory), `--olive:#9DC15A` (uptime/healthy), `--amber:#E0A458` (restarts/warn/reload), `--rose:#E5707E` (errors/danger/stop), `--sky:#5BA8D4` (network/links/info); radii `--r:6px --r-sm:4px`.
- **Fonts:** Inter for chrome/labels/prose; JetBrains Mono (`font-variant-numeric: tabular-nums`) for ALL data (numbers, names, PIDs, logs). Both via `@fontsource`, imported in `main.tsx`.
- **Framework facts:** hash routing (`web/src/router.ts`); build output embeds to `internal/dashboard/dist` via `make ui` (= `cd web && npm install && npm run build`, where `npm run build` = `tsc -b && vite build`). The embedded bundle MUST be committed.
- **Conventions (CLAUDE.md):** TDD where logic changes (the `lib/` helpers); commit messages imperative + trailer `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`; update `CHANGELOG.md` `[Unreleased]` as part of the work; branch is `mA-redesign` off `dev`; one `--no-ff` merge at the end.
- **No new backend, no proto changes.** If a task seems to need one, stop — it's out of scope for M-A.

---

## Data-wiring map (mockup cell → real API field)

Every implementer binds against this. Types are from `web/src/api.ts`.

**Fleet cluster** (`fleetSummary(agents)` helper, Task 9):
- Agents = `agents.length`; sub = `${online} online · ${errored} errored` where online = `connected && no errored proc`, errored = agents with ≥1 errored proc.
- Running = count procs with running state; sub = `of ${totalProcs} processes`.
- Errored = count errored procs; sub = first errored proc name (or "none").
- Avg CPU = mean of `agent.host.cpu_percent` over connected agents that report `host` (guard: if none report host, show `—`). NOT a mean of proc cpu.
- Total Mem = `sum(host.mem_used)` with sub `of ${sum(host.mem_total)}` (guard `—` if no host).
- Restarts 24h = `sum(proc.restarts_24h)`; sub = `${nProcsWith>0} process(es)`.

**Agent section header** (demo3 §fleet `.sec`): glyph from connected/errored; name; meta line = `${ip} · ${os}/${arch} · seen ${relativeTime(last_seen_unix)}` (omit any missing piece); `⟲ restart all` (existing `RestartAllButton`, restyled); proc count.

**Process ledger row** (demo3 §fleet `.lr.pcols`): index; name; sub = `source` badge + (`proc.detail` ? ` · ${detail}` : "") — **no fabricated runtime/port**; status = `statusOf(state)` (helper, Task 9); PID; CPU% (`proc.cpu`); MEM (`proc.mem` MB); Uptime (`relativeTime` of `now-uptime_ms`); ↻ = `proc.restarts_24h`; Trend = CPU sparkline from `getMetrics` 5-min series (as Overview already wires). Errored rows show `—` for pid/cpu/mem/uptime.

**Errors page** (`ErrorsResponse`): cluster cells = `cluster.errors`, `cluster.signatures`, `cluster.affected_procs`, `relativeTime(cluster.last_error_unix)` + sub agent/proc of most-recent signature. **Drop "▲% vs prev"** (no prev-window data). Ledger rows = `signatures[]`: sample (message), `source||"—"`, `${agent} / ${proc}` + `${affected.length} proc`, bar-sparkline from `buckets`, `count`, `relativeTime(last_unix)`. Range tabs all/24h/7d → `getErrors(range)`. Row click → navigate to that signature's `#/a/<agent>/p/<proc>`.

**Process detail cluster** (demo3 §detail `.c6`): CPU (`proc.cpu` + peak from buckets max over window); Memory (`proc.mem` + peak); Uptime (`relativeTime` + `since ${date(now-uptime_ms)}`); Restarts (`proc.restarts` cumulative + sub `restarts_24h` in 24h / "stable"); Errors 5m (`getLogStats(agent)[proc]`, existing 5-min window); Threads (`proc.threads` + sub `fds ${open_fds===-1?"—":open_fds}`).
Detail header meta = `${state} · pid ${pid} · ${source} · ${detail||""}` (omit empties). Charts via `getMetricsForProc` (existing), range 15m/1h/6h.

**Rail Errors badge** = `cluster.signatures` from a lightweight `getErrors("24h")` poll owned by the shell (~15s); hidden when 0.

**Logs / Notifications / Credentials / Login / modals:** bind to the existing functions exactly as the current components do (`getLogs`, `getNotifications`+mutations, `listCredentials`+mutations, `login`, `addApp`, `connectToken`). M-A changes presentation/structure only; behavior identical.

---

## File structure

**New files:**
- `web/src/lib/format.ts` — `relativeTime`, `formatBytes`, `formatDateShort` (pure).
- `web/src/lib/format.test.ts` — Vitest.
- `web/src/lib/status.ts` — `statusOf(state)` → `{kind:"online"|"errored"|"stopped", word, glyph, dotClass}` (pure).
- `web/src/lib/status.test.ts`.
- `web/src/lib/logs.ts` — `classifyLevel(line)`, `matchFilter(text, query)` (pure).
- `web/src/lib/logs.test.ts`.
- `web/src/lib/fleet.ts` — `fleetSummary(agents)` (pure).
- `web/src/lib/fleet.test.ts`.
- `web/src/AppShell.tsx` — rail + context bar + content wrapper + `<ErrorBoundary>`; consumed by every authed page.
- `web/src/ErrorBoundary.tsx` — React error boundary.
- `web/src/components/Cluster.tsx` — `MetricCluster`, `Cell`.
- `web/src/components/Ledger.tsx` — `SectionHeader`, `LedgerHeader`, `LedgerRow`, `QuickActions`.
- `web/src/components/Controls.tsx` — `Segment`, `Toggle`, `Chip`, `Field`, `Button` (variants).
- `web/src/components/BarSparkline.tsx` — error-occurrence bars.
- `web/src/components/Modal.tsx` — `Modal` base (header/body/footer, focus-trap, Esc).
- `web/src/components/StatusGlyph.tsx` — square + word from `statusOf`.
- `web/src/LiveLogModal.tsx` — the new live-log modal.
- `web/src/LogPanel.tsx` — shared log controls + `LogView` (used by Logs page).
- `web/src/Logs.tsx` — the `#/logs` page.
- `web/src/hooks/useLogStream.ts` — shared polling hook (Detail recent-logs, LogPanel, LiveLogModal).
- `web/vitest.config.ts` — Vitest config.

**Modified files:** `web/package.json` (+`@fontsource/inter`, +`vitest`, +`test` script), `web/src/main.tsx` (font imports), `web/src/styles.css` (full rewrite), `web/src/router.ts` (+logs routes, nav helper), `web/src/App.tsx` (shell integration + logs route), every page (`Overview.tsx`, `ProcessDetail.tsx`, `ProcessCard.tsx`, `Errors.tsx`, `Notifications.tsx`, `Credentials.tsx`, `Login.tsx`, `FileBrowser.tsx`, `LogView.tsx`, `MetricChart.tsx`, `Sparkline.tsx`, `ControlButtons.tsx`, `RestartAllButton.tsx`, `AddAppModal.tsx`, `ConnectAgentModal.tsx`, `SummaryCards.tsx`→folded into Cluster), `CHANGELOG.md`.

**Rail note (decision):** demo3 shows a bottom "Settings" icon, but there is no global-settings surface beyond Notifications. Per the hardening rule "no non-functional controls," **omit the Settings rail item** for M-A (rail = Fleet · Errors · Logs · Notify · Creds). Document with a comment; revisit if a settings page is ever specced.

---

## Phase 0 — Foundation

### Task 1: Vitest + Inter + pure-helper scaffold

**Files:**
- Modify: `web/package.json`
- Create: `web/vitest.config.ts`
- Modify: `web/src/main.tsx`
- Create: `web/src/lib/format.ts`, `web/src/lib/format.test.ts`

**Interfaces:**
- Produces: `relativeTime(unixSec: number, nowSec?: number): string`; `formatBytes(n: number): string`; `formatDateShort(unixSec: number): string`.

- [ ] **Step 1: Add deps + test script.** In `web/package.json` add to `devDependencies`: `"vitest": "^2.1.8"`; to `dependencies`: `"@fontsource/inter": "^5.1.0"`; add script `"test": "vitest run"`. Run `cd web && npm install`.

- [ ] **Step 2: Vitest config.** Create `web/vitest.config.ts`:
```ts
import { defineConfig } from "vitest/config";
export default defineConfig({ test: { environment: "node", include: ["src/**/*.test.ts"] } });
```

- [ ] **Step 3: Write the failing test.** Create `web/src/lib/format.test.ts`:
```ts
import { describe, it, expect } from "vitest";
import { relativeTime, formatBytes, formatDateShort } from "./format";

describe("relativeTime", () => {
  const now = 1_700_000_000; // fixed
  it("seconds", () => expect(relativeTime(now - 2, now)).toBe("2s"));
  it("minutes", () => expect(relativeTime(now - 8 * 60, now)).toBe("8m"));
  it("hours", () => expect(relativeTime(now - 2 * 3600, now)).toBe("2h"));
  it("days with hours", () => expect(relativeTime(now - (6 * 86400 + 2 * 3600), now)).toBe("6d 2h"));
  it("exact days drops zero hours", () => expect(relativeTime(now - 21 * 86400, now)).toBe("21d"));
  it("zero/absent → dash", () => expect(relativeTime(0, now)).toBe("—"));
  it("future clamps to 0s", () => expect(relativeTime(now + 5, now)).toBe("0s"));
});
describe("formatBytes", () => {
  it("MB", () => expect(formatBytes(212 * 1024 * 1024)).toBe("212 MB"));
  it("GB", () => expect(formatBytes(1.2 * 1024 ** 3)).toBe("1.2 GB"));
  it("zero", () => expect(formatBytes(0)).toBe("0 B"));
});
describe("formatDateShort", () => {
  it("renders Mon D", () => expect(formatDateShort(1_718_000_000)).toMatch(/[A-Z][a-z]{2} \d{1,2}/));
});
```

- [ ] **Step 4: Run, verify it fails.** Run `cd web && npx vitest run src/lib/format.test.ts`. Expected: FAIL (module not found).

- [ ] **Step 5: Implement.** Create `web/src/lib/format.ts`:
```ts
// relativeTime renders a compact "ago"/duration string from a unix-seconds
// timestamp. 0/absent → em dash. nowSec defaults to current time (inject for tests).
export function relativeTime(unixSec: number, nowSec: number = Date.now() / 1000): string {
  if (!unixSec) return "—";
  let s = Math.max(0, Math.floor(nowSec - unixSec));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m`;
  if (s < 86400) return `${Math.floor(s / 3600)}h`;
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  return h ? `${d}d ${h}h` : `${d}d`;
}

export function formatBytes(n: number): string {
  if (n <= 0) return "0 B";
  const gb = n / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = n / 1024 ** 2;
  if (mb >= 1) return `${Math.round(mb)} MB`;
  const kb = n / 1024;
  if (kb >= 1) return `${Math.round(kb)} KB`;
  return `${Math.round(n)} B`;
}

export function formatDateShort(unixSec: number): string {
  return new Date(unixSec * 1000).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}
```

- [ ] **Step 6: Run, verify pass.** Run `cd web && npx vitest run src/lib/format.test.ts`. Expected: PASS.

- [ ] **Step 7: Add Inter fonts.** In `web/src/main.tsx`, add above the JetBrains imports:
```ts
import "@fontsource/inter/400.css";
import "@fontsource/inter/500.css";
import "@fontsource/inter/600.css";
import "@fontsource/inter/700.css";
```

- [ ] **Step 8: Commit.**
```bash
git add web/package.json web/package-lock.json web/vitest.config.ts web/src/lib/format.ts web/src/lib/format.test.ts web/src/main.tsx
git commit -m "build(mA): add Vitest + Inter, format helpers (TDD)"
```

---

### Task 2: Design-token CSS rewrite

**Files:**
- Modify: `web/src/styles.css` (full rewrite)

**Interfaces:**
- Produces: the complete Instrument CSS vocabulary used by every later task. Class names exactly as in demo3 `<style>` (lines 8–149): `.shell .rail .ri .mk .main .top .ctx .lnk .content .crumb .sec .glyph .cluster .c6 .c4 .cell .lh .lr .pcols .qa .qbtn .btn (.warn/.dgr/.ghost/.sm) .actions .subtabs .subtab .field .inp .seg .tg .tgrow .chip .charts .chart .logbar .logbox .cur .fb* .backdrop .modal .mhead .mtitle .mbody .mfoot .loginwrap .loginbox` plus color utilities `.teal .indigo .olive .amber .rose .sky .un .mono`.

- [ ] **Step 1: Replace `:root` + base.** Overwrite `web/src/styles.css`. Transcribe demo3 `<style>` lines 8–149 **verbatim** as the base, with these adaptations:
  - Keep the `--i`/`--m` font-family vars but point `--i` at `'Inter'` and `--m` at `'JetBrains Mono'` (the `@fontsource` imports supply them).
  - Replace the demo's `display:none` toggle scaffolding (`.loginwrap{display:none}`, `.backdrop{display:none}`, `.view{display:none}`) with **no** default-hidden rules — React controls mounting; modals/login render conditionally. Keep `.backdrop{ position:fixed; inset:0; ... }` but default it to `display:flex` (it's only mounted when open).
  - Add a `*:focus-visible{ outline:2px solid var(--teal); outline-offset:1px; }` rule (hardening; Task 19 relies on it).
  - Add `.sr-only` (visually-hidden) utility for icon-button labels.
- [ ] **Step 2: Keep CodeMirror dark theme happy.** Append the existing FileBrowser-related rules that demo3 lacks but the app needs: `.fb-saverow`, `.fb-msg`, `.fb-action`, `.fb-empty`, `.fb-err` — restyle them to the new tokens (hairline borders, mono, `--line`). (FileBrowser markup is reworked in Task 11; these classes back it.)
- [ ] **Step 3: Verify build.** Run `cd web && npx tsc -b && npx vite build`. Expected: build succeeds, bundle written to `internal/dashboard/dist`. (Pages still reference old classes and will look broken until restyled — that's expected mid-phase; the build must still pass.)
- [ ] **Step 4: Commit.**
```bash
git add web/src/styles.css
git commit -m "style(mA): Instrument design tokens + base CSS"
```

---

## Phase 1 — Shell & routing

### Task 3: Router + nav helpers

**Files:**
- Modify: `web/src/router.ts`
- Create: `web/src/lib/nav.ts`, `web/src/lib/nav.test.ts`

**Interfaces:**
- Consumes: existing `Route` union, `parseHash`, `navigate`, `procHref` from `router.ts`.
- Produces: extended `Route` with `{ name: "logs"; agent?: string; proc?: string }`; `logsHref(agent?, proc?)`; `navItemFor(route): "fleet"|"errors"|"logs"|"notif"|"creds"|null` (in `nav.ts`).

- [ ] **Step 1: Failing test.** Create `web/src/lib/nav.test.ts`:
```ts
import { describe, it, expect } from "vitest";
import { navItemFor } from "./nav";
describe("navItemFor", () => {
  it("overview → fleet", () => expect(navItemFor({ name: "overview" })).toBe("fleet"));
  it("detail → fleet", () => expect(navItemFor({ name: "detail", agent: "a", proc: "p" })).toBe("fleet"));
  it("errors → errors", () => expect(navItemFor({ name: "errors" })).toBe("errors"));
  it("logs → logs", () => expect(navItemFor({ name: "logs" })).toBe("logs"));
  it("notifications → notif", () => expect(navItemFor({ name: "notifications" })).toBe("notif"));
  it("credentials → creds", () => expect(navItemFor({ name: "credentials" })).toBe("creds"));
});
```
- [ ] **Step 2: Run, verify fail.** `cd web && npx vitest run src/lib/nav.test.ts` → FAIL.
- [ ] **Step 3: Extend router.** In `web/src/router.ts`: add `| { name: "logs"; agent?: string; proc?: string }` to `Route`; in `parseHash`, parse `#/logs`, `#/logs/<agent>/<proc>` (URL-decoded). Add `export function logsHref(agent?: string, proc?: string): string` returning `#/logs` or `#/logs/${encodeURIComponent(agent)}/${encodeURIComponent(proc)}`.
- [ ] **Step 4: Implement nav.** Create `web/src/lib/nav.ts`:
```ts
import type { Route } from "../router";
export function navItemFor(r: Route): "fleet" | "errors" | "logs" | "notif" | "creds" | null {
  switch (r.name) {
    case "overview": case "detail": return "fleet";
    case "errors": return "errors";
    case "logs": return "logs";
    case "notifications": return "notif";
    case "credentials": return "creds";
    default: return null;
  }
}
```
- [ ] **Step 5: Run, verify pass.** `cd web && npx vitest run src/lib/nav.test.ts` → PASS.
- [ ] **Step 6: Commit.** `git add web/src/router.ts web/src/lib/nav.ts web/src/lib/nav.test.ts && git commit -m "feat(mA): logs route + nav-item helper (TDD)"`

---

### Task 4: AppShell + ErrorBoundary

**Files:**
- Create: `web/src/AppShell.tsx`, `web/src/ErrorBoundary.tsx`
- Modify: `web/src/App.tsx`

**Interfaces:**
- Consumes: `useRoute`, `navigate`, `logsHref` (router); `navItemFor` (nav); `getErrors` (api, badge); `relativeTime`.
- Produces: `<AppShell ctx={ReactNode} right={ReactNode} onLogout={()=>void}>{children}</AppShell>` rendering rail + context bar + `<ErrorBoundary>`-wrapped content. `<ErrorBoundary>` catches render errors and shows a fallback panel.

- [ ] **Step 1: ErrorBoundary.** Create `web/src/ErrorBoundary.tsx` — a class component with `componentDidCatch` logging to console and `render` showing `<div className="content"><div className="sec">…error…</div><p>Something went wrong rendering this page. Reload.</p></div>` when `state.hasError`.
- [ ] **Step 2: AppShell.** Create `web/src/AppShell.tsx`. Structure per demo3 §shell (lines 153–174): `.shell > nav.rail` + `.main > .top + .content`. Rail items (Fleet/Errors/Logs/Notify/Creds) per demo3 lines 156–160 — each an anchor using `navigate(...)`, active class from `navItemFor(useRoute())`. Errors item shows a `.badge` when the shell's errors-signature count > 0. **Omit the Settings item** (see file-structure note) — leave a `{/* Settings: no global-settings page yet; omitted per hardening */}` comment. Context bar: `.ctx` = `ctx` prop, `.rt` = `right` prop + a `sign out` link calling `onLogout`. Each rail icon button has an `aria-label` and the label span; mark `role="navigation"` on the rail.
  - Shell owns the badge: `useEffect` polling `getErrors("24h")` every 15s → `setBadge(r.cluster.signatures)`, swallow errors.
- [ ] **Step 3: Integrate in App.tsx.** Rewrite `App.tsx` so each authed branch renders its page **inside** `<AppShell ctx={…} right={…} onLogout={logout}>`. Add the `logs` route branch → `<Logs agent={…} proc={…}/>` (page lands in Task 12; until then, import lazily or stub returning `null` — but DO NOT leave a dangling import; add the `logs` branch only when Task 12 lands. For now, in Task 4, route `logs` to the Overview to keep types total, with a `// TODO(Task12)` comment). Pages that currently render their own `.topbar`/`.app` (Overview, ProcessDetail, Credentials, Errors) will have those chrome wrappers **removed** in their own tasks — in Task 4, wrap them as-is; double chrome is acceptable for this single transitional commit and is removed page-by-page next.
- [ ] **Step 4: Build.** `cd web && npx tsc -b && npx vite build` → succeeds.
- [ ] **Step 5: Commit.** `git add web/src/AppShell.tsx web/src/ErrorBoundary.tsx web/src/App.tsx && git commit -m "feat(mA): AppShell (icon rail + context bar) + error boundary"`

---

## Phase 2 — Shared components

> All Phase-2 components are presentational. Verify each with `npx tsc -b` (types) — they're exercised visually when pages adopt them in Phase 3. Keep each file focused.

### Task 5: MetricCluster + Cell

**Files:** Create `web/src/components/Cluster.tsx`. Delete-after-migration: `web/src/SummaryCards.tsx` (folded here; remove its import from Overview in Task 10).

**Interfaces:**
- Produces:
  - `Cell({ label, value, unit?, sub?, color? }: { label: string; value: ReactNode; unit?: string; sub?: ReactNode; color?: "teal"|"indigo"|"olive"|"amber"|"rose"|"sky" })`
  - `MetricCluster({ cols, children }: { cols: 4|6; children: ReactNode })`

- [ ] **Step 1:** Implement per demo3 `.cluster/.c6/.c4/.cell/.l/.v/.un/.d` (lines 52–59, 178–185). `MetricCluster` renders `<div className={"cluster c"+cols}>`. `Cell` renders label (`.l`), value (`.v` + optional color util, with `<small>` for `unit`), and `.d` sub. Value uses mono via `.v` (already mono in CSS).
- [ ] **Step 2: Build** `npx tsc -b` → ok.
- [ ] **Step 3: Commit** `git add web/src/components/Cluster.tsx && git commit -m "feat(mA): MetricCluster + Cell components"`

### Task 6: Ledger primitives

**Files:** Create `web/src/components/Ledger.tsx`, `web/src/components/StatusGlyph.tsx`.

**Interfaces:**
- Consumes: `statusOf` (Task 9 — **declare the import now; Task 9 provides it**. Order note: if executing strictly in order, do Task 9 before Task 6, OR inline a local `statusOf` and replace in Task 9. Recommended: pull Task 9's `status.ts` forward as a dependency of this task.)
- Produces:
  - `SectionHeader({ index, title, right?, count? })` — demo3 `.sec` (lines 43–47).
  - `LedgerHeader({ cols, children })` and `LedgerRow({ cols, onClick?, children })` — demo3 `.lh`/`.lr` (lines 61–64); `cols` is a CSS `grid-template-columns` string (e.g. the `.pcols` value line 74).
  - `QuickActions({ actions })` where `actions: { icon: string; label: string; variant?: "warn"|"dgr"; onClick: (e)=>void; title?: string }[]` — demo3 `.qa/.qbtn` (lines 76–83). Each button stops propagation.
  - `StatusGlyph({ state })` (in StatusGlyph.tsx) — square `.sq` + word, colors from `statusOf(state)` (demo3 lines 69–72).

- [ ] **Step 1:** Implement the four ledger pieces + StatusGlyph per the referenced demo3 lines. `LedgerRow` applies `clk` class when `onClick` present and renders children + an absolutely-positioned `QuickActions` only if provided.
- [ ] **Step 2: Build** `npx tsc -b` → ok.
- [ ] **Step 3: Commit** `git add web/src/components/Ledger.tsx web/src/components/StatusGlyph.tsx && git commit -m "feat(mA): ledger primitives + status glyph"`

### Task 7: Controls (Segment, Toggle, Chip, Field, Button)

**Files:** Create `web/src/components/Controls.tsx`.

**Interfaces:**
- Produces:
  - `Segment<T>({ options, value, onChange }: { options: {value:T; label:string}[]; value:T; onChange:(v:T)=>void })` — demo3 `.seg` (lines 102–104).
  - `Toggle({ on, onChange, label?, desc? })` — demo3 `.tg/.tgrow` (lines 106–110).
  - `Chip({ label, on, onClick })` — demo3 `.chip` (lines 111–112).
  - `Field({ label, children })` and `Input` (a styled `<input className="inp">` passthrough) — demo3 `.field/.inp` (lines 97–101).
  - `Button({ variant?, size?, ...rest })` where `variant?: "warn"|"dgr"|"ghost"`, `size?: "sm"` — demo3 `.btn*` (lines 85–90).

- [ ] **Step 1:** Implement. All interactive controls get `aria` (Toggle = `role="switch" aria-checked`; Segment options = `aria-pressed`). Keyboard: Toggle/Chip respond to Enter/Space.
- [ ] **Step 2: Build** `npx tsc -b` → ok.
- [ ] **Step 3: Commit** `git add web/src/components/Controls.tsx && git commit -m "feat(mA): control components (segment, toggle, chip, field, button)"`

### Task 8: Sparkline + BarSparkline + MetricChart restyle

**Files:** Modify `web/src/Sparkline.tsx`, `web/src/MetricChart.tsx`; Create `web/src/components/BarSparkline.tsx`.

**Interfaces:**
- `Sparkline` keeps signature `{ points, width?, height?, color? }`; default color → `var(--teal)`; restyle stroke-width 1.4, `preserveAspectRatio="none"`, viewBox per demo3 row svg (line 189).
- `BarSparkline({ points, color? })` — bars per demo3 errors svg (line 220): scale `points` to 22px height, 6px-wide rects.
- `MetricChart` — restyle to demo3 `.chart` area+line (lines 251–252): gradient fill under the avg line, semantic color by `metric` (teal cpu / indigo mem), drop the dual avg/max into area(avg)+line(avg) with faint max line retained at low opacity. Keep its `{ buckets, metric }` signature.

- [ ] **Step 1:** Update the three files. Keep empty-state guards (`No history yet.` / aria-label "no data").
- [ ] **Step 2: Build** `npx tsc -b` → ok.
- [ ] **Step 3: Commit** `git add web/src/Sparkline.tsx web/src/MetricChart.tsx web/src/components/BarSparkline.tsx && git commit -m "feat(mA): restyle sparkline/chart + bar-sparkline"`

### Task 9: status + fleet helpers + Modal base

**Files:** Create `web/src/lib/status.ts` (+`.test.ts`), `web/src/lib/fleet.ts` (+`.test.ts`), `web/src/components/Modal.tsx`.

> **Ordering:** Task 6 imports `statusOf`. Execute Task 9's `status.ts` before/with Task 6 (or accept the inline-then-replace note in Task 6). Listed here to keep helper TDD together.

**Interfaces:**
- `statusOf(state: string): { kind: "online"|"errored"|"stopped"; word: string; dotClass: "on"|"er"|"st" }`.
- `fleetSummary(agents: Agent[]): { agents:number; online:number; errored:number; running:number; totalProcs:number; avgCpu:number|null; memUsed:number|null; memTotal:number|null; restarts24h:number; erroredName:string|null }`.
- `Modal({ title, onClose, children, footer? })` — demo3 `.backdrop/.modal/.mhead/.mtitle/.mclose/.mbody/.mfoot` (lines 138–143). Focus-trap, Esc-to-close, backdrop-click-to-close, restores focus on unmount.

- [ ] **Step 1: status test.** `web/src/lib/status.test.ts`:
```ts
import { describe, it, expect } from "vitest";
import { statusOf } from "./status";
describe("statusOf", () => {
  it("online/running → online", () => { for (const s of ["online","running"]) { const r = statusOf(s); expect(r.kind).toBe("online"); expect(r.dotClass).toBe("on"); } });
  it("errored/failed → errored", () => { for (const s of ["errored","failed"]) expect(statusOf(s).kind).toBe("errored"); });
  it("stopped/unknown → stopped", () => { expect(statusOf("stopped").kind).toBe("stopped"); expect(statusOf("whatever").kind).toBe("stopped"); });
  it("word echoes the state", () => expect(statusOf("online").word).toBe("online"));
});
```
- [ ] **Step 2: fleet test.** `web/src/lib/fleet.test.ts` — build two fake agents (one online with 2 running procs reporting `host`, one with an errored proc, second agent no `host`) and assert: `agents=2`, `online`/`errored` counts, `running`, `totalProcs`, `restarts24h` = sum, `avgCpu` = mean of the agents that report host, `memUsed/memTotal` = sums over host-reporting agents, `erroredName` = the errored proc's name. Include a case where **no** agent reports host → `avgCpu/memUsed/memTotal === null`. (Use the `Agent`/`Proc`/`AgentHost` types from `../api`.)
- [ ] **Step 3: Run, verify both fail.** `cd web && npx vitest run src/lib/status.test.ts src/lib/fleet.test.ts` → FAIL.
- [ ] **Step 4: Implement `status.ts` and `fleet.ts`** to pass. `statusOf` maps the known states; default → stopped. `fleetSummary` folds the agent list per the Data-wiring map (guard host-less fleets to `null`).
- [ ] **Step 5: Run, verify pass.** → PASS.
- [ ] **Step 6: Modal.** Implement `Modal.tsx` with focus-trap (focus first focusable on mount, trap Tab, Esc + backdrop-click call `onClose`, restore previously-focused element on unmount). `role="dialog" aria-modal="true" aria-label={title}`.
- [ ] **Step 7: Build** `npx tsc -b` → ok.
- [ ] **Step 8: Commit** `git add web/src/lib/status.ts web/src/lib/status.test.ts web/src/lib/fleet.ts web/src/lib/fleet.test.ts web/src/components/Modal.tsx && git commit -m "feat(mA): status + fleetSummary helpers (TDD) + Modal base"`

---

## Phase 3 — Pages

> Each page task ends by replacing that page's old chrome (`.app/.topbar`) with shared components inside `AppShell`, then verifying in-browser via Playwright (see Task 20 for the audit harness; per-page you screenshot just that route). Keep loading/empty/error states (hardening is folded in per page, not deferred entirely to Task 19).

### Task 10: Fleet overview page

**Files:** Modify `web/src/Overview.tsx`, `web/src/ProcessCard.tsx`, `web/src/App.tsx` (ctx for fleet). Remove `SummaryCards` usage.

**Interfaces:** Consumes `MetricCluster/Cell`, `SectionHeader/LedgerHeader/LedgerRow/QuickActions`, `StatusGlyph`, `Sparkline`, `fleetSummary`, `statusOf`, `relativeTime`, `formatBytes`, `RestartAllButton`, `procHref`, `control`.

- [ ] **Step 1:** Rewrite `Overview` to render (inside AppShell, so drop its own `.app/.topbar`): a fleet `MetricCluster cols={6}` from `fleetSummary(agents)` (per Data-wiring map), then **one `SectionHeader` per agent** (index, glyph, name, meta line, `RestartAllButton`, proc count) followed by that agent's process ledger (`LedgerHeader` with `.pcols` columns + a `LedgerRow` per proc). Each row: index, name+sub, `StatusGlyph`, pid, cpu, mem, uptime, restarts_24h, CPU `Sparkline`; `onClick` → `navigate(procHref(agent,proc))`; `QuickActions` = Log (opens LiveLogModal — wire in Task 13; until then, omit the Log action or route to `#/logs/...`), Restart (`control restart`), Reload (`control reload`, warn), Stop (`control stop`, dgr). Errored rows show `—` per map.
- [ ] **Step 2:** Move the "+ add app / + connect agent" actions into the AppShell context bar `right` slot (passed from `App.tsx`), opening the existing modals (restyled in Task 17). Keep the 2s fleet poll / 10s metrics poll / per-agent logstats exactly as today.
- [ ] **Step 3:** `ProcessCard.tsx` is superseded by the ledger row — either delete it or reduce it to the row renderer used by Overview. Remove `SummaryCards.tsx` import; delete the file.
- [ ] **Step 4: Build + audit.** `cd web && npm run build`; then per Task 20 harness, load `#/`, screenshot, confirm: cluster numbers match a live fleet, agent sections render, rows hover reveals quick actions, click navigates. No console errors.
- [ ] **Step 5: Commit** `git add -A && git commit -m "feat(mA): Fleet overview in Instrument language"`

### Task 11: Process detail + Files tab

**Files:** Modify `web/src/ProcessDetail.tsx`, `web/src/FileBrowser.tsx`.

**Interfaces:** Consumes `MetricCluster/Cell`, `SectionHeader`, `Segment`, `MetricChart`, `StatusGlyph`, `useLogStream` (Task 12 — for the recent-logs panel; if executing before Task 12, keep the existing inline log fetch and swap later), `getMetricsForProc`, `getLogStats`, `control`, `relativeTime`, `formatDateShort`.

- [ ] **Step 1:** Rewrite `ProcessDetail` (inside AppShell): breadcrumb (`.crumb`), status header with name + meta + control buttons (`▤ live log` opens LiveLogModal [Task 13; stub to `#/logs/...` until then] · `▸ restart` · `⟲ reload` warn · `■ stop` dgr via `control`), and **subtabs Overview · Files · Logs** (`.subtabs/.subtab`; "Logs" links to `logsHref(agent,proc)`). Overview subtab = detail `MetricCluster cols={6}` (per Data-wiring map) + `Segment` 15m/1h/6h + `.charts` two `MetricChart`s + a recent-logs `.logbox` (tail of `getLogs`). Files subtab renders `<FileBrowser>` only when `source==="git"`.
- [ ] **Step 2:** Restyle `FileBrowser` to demo3 §detail Files (lines 257–273): `.fbnote/.fbcrumb/.fbbody/.fblist/.fbrow/.fbview/.code/.saverow/.pushnote`. Keep ALL existing behavior (listDir/readFile/writeFile/createFile/deleteFile/renameFile, CodeMirror, commit-message + save&push). Only markup/classes change.
- [ ] **Step 3: Build + audit.** Load a git-app detail route, screenshot Overview + Files subtabs; confirm charts render real series, cluster cells real, Files lists/opens/edits. Confirm a command (non-git) app hides the Files tab.
- [ ] **Step 4: Commit** `git add -A && git commit -m "feat(mA): process detail + files tab restyle"`

### Task 12: Logs page + LogPanel + useLogStream + log helpers

**Files:** Create `web/src/Logs.tsx`, `web/src/LogPanel.tsx`, `web/src/hooks/useLogStream.ts`, `web/src/lib/logs.ts` (+`.test.ts`); Modify `web/src/LogView.tsx`, `web/src/App.tsx` (logs branch).

**Interfaces:**
- `classifyLevel(line: LogLine): "error"|"warn"|"info"` — `stderr` → error; else `/\b(warn|warning)\b/i.test(text)` → warn; else info.
- `matchFilter(text: string, query: string): boolean` — empty query → true; `/.../` form → regex (invalid regex → literal-substring fallback); else case-insensitive substring.
- `useLogStream(agent, proc, opts)` → `{ lines: LogLine[]; clear(): void }`, polling `getLogs` every 1.5s with cursor, capped at 5000 lines (extracted from current `ProcessDetail`).
- `LogPanel({ agent, proc })` — controls (`Segment` stream all/stdout/stderr, limit 500/1000, filter input, download `<a href={logsDownloadURL}>`) + `LogView`.
- `Logs({ agent, proc })` — the `#/logs` page: agent+proc selectors (default first connected agent's first proc; deep-link preselects) feeding `LogPanel`.

- [ ] **Step 1: logs test.** `web/src/lib/logs.test.ts`:
```ts
import { describe, it, expect } from "vitest";
import { classifyLevel, matchFilter } from "./logs";
const ln = (text: string, stderr = false) => ({ ts: 0, name: "p", instance: 0, stderr, text });
describe("classifyLevel", () => {
  it("stderr → error", () => expect(classifyLevel(ln("boom", true))).toBe("error"));
  it("warn word → warn", () => expect(classifyLevel(ln("WARNING: disk low"))).toBe("warn"));
  it("plain → info", () => expect(classifyLevel(ln("listening on :8080"))).toBe("info"));
});
describe("matchFilter", () => {
  it("empty → all", () => expect(matchFilter("anything", "")).toBe(true));
  it("substring ci", () => expect(matchFilter("GET /Jobs", "jobs")).toBe(true));
  it("regex", () => expect(matchFilter("GET /v1/jobs 200", "/\\d{3}/")).toBe(true));
  it("bad regex falls back to literal", () => expect(matchFilter("a(b", "/a(b/")).toBe(true));
});
```
- [ ] **Step 2: Run, verify fail.** → FAIL.
- [ ] **Step 3: Implement `logs.ts`** to pass.
- [ ] **Step 4: Run, verify pass.** → PASS.
- [ ] **Step 5:** Extract `useLogStream` from `ProcessDetail`'s current polling logic. Restyle `LogView` to demo3 `.logbox` lines (ts/er/ok/tx spans, color by `classifyLevel`). Build `LogPanel` and `Logs` (demo3 §logs lines 277–282 for layout; add the agent/proc selectors). Wire the `logs` route in `App.tsx` to `<Logs>`.
- [ ] **Step 6: Build + audit.** Load `#/logs`, switch agent/proc, filter (text + `/regex/`), download; confirm stream toggles and colors. No console errors.
- [ ] **Step 7: Commit** `git add -A && git commit -m "feat(mA): Logs page + LogPanel + log helpers (TDD)"`

### Task 13: Live-log modal

**Files:** Create `web/src/LiveLogModal.tsx`; wire openers in `Overview.tsx` (row Log action) and `ProcessDetail.tsx` (header `▤ live log`).

**Interfaces:** Consumes `Modal`, `useLogStream`, `classifyLevel`, `matchFilter`, `Segment`, `logsDownloadURL`. Props `{ agent, proc, onClose }`.

- [ ] **Step 1:** Implement per demo3 §live-log modal (lines 325–345): header (proc name + live/pause `Segment`), filter bar (text/regex input, stream `Segment`, **level toggles info/warn/error** as toggleable `Chip`/`Segment` filtering via `classifyLevel`, download), streaming `.logbox` with blinking `.cur`. Pause stops the poll (gate `useLogStream` with an `enabled` flag). Filter applies `matchFilter` + level set client-side.
- [ ] **Step 2:** Add open state to Overview (per-row Log quick action) and ProcessDetail (header button) → render `<LiveLogModal agent proc onClose>`.
- [ ] **Step 3: Build + audit.** Open from a fleet row and from detail; confirm live tail, pause, text/regex filter, level toggles, Esc closes, focus trapped. No console errors.
- [ ] **Step 4: Commit** `git add -A && git commit -m "feat(mA): live-log modal with filtering"`

### Task 14: Errors page restyle

**Files:** Modify `web/src/Errors.tsx`.

**Interfaces:** Consumes `MetricCluster/Cell`, `SectionHeader`, `Segment`, `LedgerHeader/LedgerRow`, `BarSparkline`, `getErrors`, `relativeTime`, `navigate`/`procHref`.

- [ ] **Step 1:** Rewrite `Errors` (inside AppShell) per demo3 §errors (lines 210–224): `MetricCluster cols={4}` from `cluster` (drop the "▲% vs prev" sub — use range label/last-error proc instead, per Data-wiring map), `SectionHeader` with a range `Segment` all/24h/7d, ledger with the exact `grid-template-columns` from demo3 line 219, rows = signatures with `BarSparkline` of `buckets`; row click → `navigate(procHref(agent,proc))`. Keep loading/empty/error/`truncated` states.
- [ ] **Step 2: Build + audit.** Load `#/errors` against a fleet with real stderr; confirm cluster + ledger + bar-sparkline render and range tabs refetch. No console errors.
- [ ] **Step 3: Commit** `git add -A && git commit -m "feat(mA): Errors page in Instrument language"`

### Task 15: Credentials restyle

**Files:** Modify `web/src/Credentials.tsx`.

**Interfaces:** Consumes `SectionHeader`, `LedgerHeader/LedgerRow`, `Segment`, `Field`, `Button`, existing `listCredentials/createCredential/createSSHCredential/deleteCredential`.

- [ ] **Step 1:** Rewrite per demo3 §creds (lines 307–316): a "Stored" `SectionHeader` + credentials ledger (name, type, added date via `formatDateShort`, delete with confirm), then an "Add credential" section with a `Segment` https/ssh type switch + `Field`s. Keep all behavior incl. SSH public-key display + copy, delete-confirm. Inside AppShell (drop old `.app/.topbar`).
- [ ] **Step 2: Build + audit.** Load `#/credentials`; add+delete an https token and generate an ssh key; confirm public-key display/copy. No console errors.
- [ ] **Step 3: Commit** `git add -A && git commit -m "feat(mA): Credentials in Instrument language"`

### Task 16: Notifications full rewrite

**Files:** Rewrite `web/src/Notifications.tsx`.

**Interfaces:** Consumes `SectionHeader`, `LedgerHeader/LedgerRow`, `Toggle`, `Chip`, `Field`, `Segment`, `Button`, existing `getNotifications/putChannel/deleteChannel/testChannel/putRule/deleteRule/putNotifSettings`. Behavior/API identical to today.

- [ ] **Step 1:** Rewrite per demo3 §notif (lines 284–305): **Channels** ledger (name, type colored, state glyph, secret indicator, test/delete) + add form (type select, name, config inputs per type); **Rules** ledger (name, event tags colored, target, channels, delete) + add form with event **`Chip`s** + name/target fields + channel multiselect; **Settings** = recovery `Toggle`, global cooldown + coalesce-window `Field`s, per-event cooldown override grid. Keep the exact config/secret field sets per channel type that the current component handles (webhook/telegram/slack/email) — read the current `Notifications.tsx` for the field maps and preserve them.
- [ ] **Step 2: Build + audit.** Load `#/notifications`; create a channel, a rule (toggle event chips), flip the recovery toggle + save settings; confirm persistence via reload. No console errors.
- [ ] **Step 3: Commit** `git add -A && git commit -m "feat(mA): Notifications rewrite in Instrument language"`

### Task 17: Login + modals restyle

**Files:** Modify `web/src/Login.tsx`, `web/src/AddAppModal.tsx`, `web/src/ConnectAgentModal.tsx`, `web/src/Logo.tsx`.

**Interfaces:** `AddAppModal`/`ConnectAgentModal` adopt the shared `Modal`. Behavior unchanged.

- [ ] **Step 1:** Restyle `Login` per demo3 §login (lines 322–323): centered `.loginwrap/.loginbox`, mono `mar$hal` wordmark + tagline + password `Field` + full-width sign-in `Button`. Keep `onLogin` + error handling.
- [ ] **Step 2:** Reparent `AddAppModal` and `ConnectAgentModal` onto the shared `Modal` (header/body/footer, focus-trap, Esc) per demo3 §modals (lines 347–349). Preserve ALL form fields/validation/behavior (read current files). Update `Logo` if its colors clash with new tokens (use `--teal`/`--olive`).
- [ ] **Step 3: Build + audit.** Sign out → login screen renders; sign in; open both modals (Esc + backdrop close, focus trapped); add a command app end-to-end. No console errors.
- [ ] **Step 4: Commit** `git add -A && git commit -m "feat(mA): Login + Add-App/Connect-Agent modals restyle"`

---

## Phase 4 — Cleanup & hardening

### Task 18: Fold in deferred minor items

(From the prior UI-consistency handoff, per spec sequencing item 5.)

**Files:** `web/src/ConnectAgentModal.tsx` (clipboard `.catch()` on copy), `web/src/api.ts` (`connectToken` — drop empty `address/name` from body if that's the documented cleanup; verify against the spec's referenced handoff), `internal/dashboard/*.go` (`dashboard.Serve` doc-comment), and any dead `connectTokenReq` fields.

- [ ] **Step 1:** Locate each item: read `docs/handoffs/2026-06-23-ui-consistency-production-readiness.md` for the exact four items. Apply each minimal fix.
- [ ] **Step 2:** `cd web && npm run build`; `go build ./... && go vet ./...`.
- [ ] **Step 3: Commit** `git add -A && git commit -m "chore(mA): fold in deferred minor cleanups"`

### Task 19: Hardening pass

**Files:** any page lacking a state; `web/src/styles.css` (focus rings, narrow-viewport rules).

- [ ] **Step 1: Loading/empty/error states.** Audit every data view (each page's cluster/ledger/charts/logs) for the three states; add any missing (most were added per-page in Phase 3 — this is the sweep).
- [ ] **Step 2: Keyboard/focus/aria.** Confirm focus-visible rings (Task 2 rule), modal focus-trap+Esc (Modal), icon-only buttons have `aria-label`, status conveyed by word+square (not color alone), rail is keyboard-navigable.
- [ ] **Step 3: Narrow viewport.** Add CSS for rail behavior and ledger horizontal-scroll/stacking under ~720px (the clusters and `.pcols` grids must not overflow). Verify with a 390px Playwright viewport screenshot.
- [ ] **Step 4: Error boundaries.** Confirm `AppShell` wraps content in `ErrorBoundary`; spot-check by temporarily throwing in a page (revert after).
- [ ] **Step 5: No console errors.** Re-audit every route with the console panel open.
- [ ] **Step 6: Build + tests.** `cd web && npm run build && npx vitest run`.
- [ ] **Step 7: Commit** `git add -A && git commit -m "harden(mA): states, a11y, focus, narrow-viewport pass"`

---

## Phase 5 — Build, changelog, audit

### Task 20: Final build + CHANGELOG + full in-browser audit

**Playwright audit harness:** use the `playwright-skill` (or the project's live-demo convention) against a scratch demo. Per CLAUDE.md live-demo convention: scratch `XDG_DATA_HOME=/tmp/marshal-mA-demo/...`, server on **:9000 / dashboard :9001** (per the demo-port memory), set password + rotate enroll token **while the server is down**, then start with `--http-listen`, enroll a demo agent, start a few representative demo processes (mix of online + one that crashes/loops to populate errors + restarts, and a git app for the Files tab), then drive the dashboard. **Teardown by data dir** (do NOT broad-pkill — a standing launchd marshal daemon exists per memory). Confirm `pgrep -fl marshal` shows only the standing daemon afterward.

- [ ] **Step 1: Full suite.** `cd web && npx vitest run && npm run build` (0 TS errors); back at repo root `go test ./... -race -count=1 && go vet ./... && gofmt -l .` (gofmt lists nothing — only Task 18 touched Go); `make build`.
- [ ] **Step 2: Confirm embedded bundle committed.** `git status` shows `internal/dashboard/dist` changes staged; commit them with the UI.
- [ ] **Step 3: CHANGELOG.** Add a sizable `### Changed` entry under `[Unreleased]` describing the full "Marshal Instrument" redesign (new shell, ledgers, clusters, live-log modal, Notifications rewrite, real Errors page, hardening) + an `### Added` line for the Logs page if counted as new. Do NOT cut the version yet (release is after M-A, per the spec's planning decision).
- [ ] **Step 4: Live audit.** Stand up the scratch demo; screenshot every route (`#/`, a detail Overview + Files, `#/logs`, `#/errors`, `#/notifications`, `#/credentials`, login) and the live-log + add-app + connect-agent modals; verify each cell is real per the Data-wiring map and there are no console errors. Show the user representative screenshots (per the "viewable demo each session" memory). Tear down; confirm no orphans.
- [ ] **Step 5: Commit** `git add -A && git commit -m "feat(mA): rebuild embedded bundle + CHANGELOG for Instrument redesign"`

---

## Post-plan: handoff & merge

After Task 20: write `docs/handoffs/2026-06-24-mA-dashboard-redesign.md` (per CLAUDE.md handoff convention), then merge `mA-redesign` → `dev` `--no-ff`. **Decide release**: per the planning decision, cut **v0.3.0** on `dev` (move `[Unreleased]` → `## [0.3.0] - <date>`, update compare links), merge `dev` → `main` `--no-ff`, tag `v0.3.0`, push `main`/`dev`/tag.

---

## Self-review (against the spec)

- **Tokens/fonts** → Task 1 (Inter) + Task 2 (full token CSS). ✓
- **Shell (rail + context bar) + #/errors route** → Task 3 (routes) + Task 4 (AppShell). `#/errors` already exists from M-F. ✓
- **Shared components** (cluster, ledger+quick actions, section header, sparkline, chart, toggle/segment/chip/input, modal) → Tasks 5–9. ✓
- **Pages** Fleet→Detail(+Files)→Logs→Credentials→Login→Notifications→live-log→Errors → Tasks 10–17 (order interleaves Logs before the modal since the modal reuses `useLogStream`; Login folded with modals in 17). ✓
- **New data needs** — all resolved real (M-B…M-G); Data-wiring map fixes each cell; non-real cells (prev-delta, runtime/port) explicitly dropped. ✓
- **Hardening (full)** — folded per-page in Phase 3 + swept in Task 19 (states, a11y, focus, narrow-viewport, error boundaries). ✓
- **Deferred 4 minor items** → Task 18. ✓
- **make ui / build / test / audit** → Task 20. ✓
- **Errors-page scope** — restyle real `/api/errors` (resolved). ✓
- **Settings rail item** — explicitly omitted (no page; hardening: no dead controls), documented. ✓
- **Type consistency** — helper signatures (`relativeTime`, `statusOf`, `classifyLevel`, `matchFilter`, `fleetSummary`, `navItemFor`, `logsHref`) are referenced identically across tasks; component prop shapes declared once in their creating task's Interfaces block. ✓

**Known ordering caveat:** Task 6 (Ledger/StatusGlyph) imports `statusOf` from Task 9. Execute `status.ts` (Task 9 Step 1/4) before Task 6, or inline-then-replace as noted. Flagged in both tasks.
