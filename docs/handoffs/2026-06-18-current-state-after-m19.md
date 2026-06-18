# Marshal — Current State (after M19) — Resume Handoff

**Date:** 2026-06-18
**Branch:** `main` (everything below is merged; working tree clean)
**This is the latest resume point** — read this first, then the per-milestone handoffs it links.

---

## TL;DR

Five pieces of work shipped to `main` this session, in order: M15 follow-up cleanups → M16
session invalidation → M17 login-attempt audit log → M18 server-side log search → M19 dashboard
redesign. The dashboard is now a designed product (the "Signal" identity) with an overview →
process-detail drill-down. Gate is green on `main`.

**Verify `main` is healthy:**
```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1     # 20 packages green
gofmt -l . ; go vet ./...        # silent / clean
make ui                          # web/ builds into internal/dashboard/dist (embedded)
```

Recent merges (newest first):
```
6b5695e Merge M19: dashboard redesign + drill-down (Signal identity)
292d954 Merge M18: server-side log search (dashboard + CLI)
6fae905 Merge M17: dashboard login-attempt audit log
0c54d24 Merge M16: dashboard session invalidation on credential change
32e648e Merge M15 follow-up cleanups
2cc756c Merge M15: dashboard auth hardening   (pre-session)
```

---

## What's on `main` now (per milestone)

Each has a spec in `docs/superpowers/specs/`, a plan in `docs/superpowers/plans/`, and a handoff
in `docs/handoffs/` (all dated 2026-06-18). All were built via spec → plan → subagent-driven TDD
→ whole-branch review (opus, all READY TO MERGE) → live demo.

- **M15 cleanups** — `Reload()` reads `auth.json` off the lock (re-checks mtime before swap);
  `TestReloadCorruptKeepsOldData` hardened with `os.Chtimes`.
- **M16 — session invalidation on credential change** — each dashboard session is stamped with a
  credential fingerprint (`SHA-256(PBKDF2.Salt.Iter)`); `requireSession` 401s + drops a session
  whose stamp no longer matches (password changed) or whose user is gone.
- **M17 — login-attempt audit log** — new leaf package `internal/audit` (append-only,
  size-rotating JSONL, 0600); the dashboard records `success`/`invalid_credentials`/`rate_limited`
  per login; `marshal server audit` CLI (`--limit`/`--failures`). Passwords never stored.
- **M18 — server-side log search** — case-insensitive substring filter pushed into SQLite
  (`Tail`/`Since` gained a `text` arg → `AND text LIKE ? ESCAPE`). Surfaced on the dashboard
  (`/api/logs?q=`, search box lifted server-side) and the CLI (`fleet logs --grep`, via a new
  `grep` field on `FleetLogsHistory`).
- **M19 — dashboard redesign + drill-down ("Signal" identity)** — see
  `docs/handoffs/2026-06-18-m19-redesign.md` for full detail. Overview (summary cards +
  full-width process cards with state-aware start/restart/stop, sparklines, recent-error badges)
  → process detail page (stat tiles incl `errors·5m`, side-by-side cpu/mem charts, log panel +
  search) via a hash router. Backend added `logstore.ErrorCounts` + `/api/logstats`. The "start"
  button issues a `Restart` op (revives a stopped/errored proc) — there is **no** backend start
  op (the proto's `ControlOp_Start` launches *new* apps from a spec, which is the next milestone).
  JetBrains Mono is bundled offline via `@fontsource/jetbrains-mono`.

---

## How to run / demo the dashboard in-browser

The dashboard serves HTTPS-only with a self-signed cert, which a managed browser can't load
directly. Use the Vite dev proxy (`web/vite.config.ts` proxies `/api` → `https://localhost:9001`
with `secure:false`):

1. Start a scratch server on **:9001** with a connected agent shipping logs (set the dashboard
   password while the server is **down**, rotate an enroll token + capture the fingerprint, then
   `marshal start <yaml>` with a `server:` block + apps — `marshal start` persists the server
   config and spawns the agent daemon, which connects and ships logs). Use a scratch
   `XDG_DATA_HOME=/tmp/...` so real state is untouched.
2. `npm --prefix web run dev` (Vite on :5173), open `http://localhost:5173`, log in.
3. Tear down (stop agent + server + Vite, remove scratch dir + any `.claude/launch.json`);
   confirm `pgrep -fl marshal` shows only the user's own daemon (currently pid 84457).

(The M18 and M19 handoffs document this dance in full; it worked cleanly via the Preview tool.)

---

## Concrete next step

The user explicitly flagged the next milestone: **add an app via the dashboard** — a "true start"
flow that builds a `StartRequest` (app-spec: name/cmd/args/instances/…) and routes it via the
existing `ControlOp_Start` (`manager.Add`). This is the real meaning of the proto's `Start` op
(distinct from the M19 start-button-as-restart). Needs its own brainstorm → spec → plan: an
"add app" form/modal in the redesigned UI, an `/api/control` mapping for `start` carrying the
spec, and validation. No proto change needed (the op already exists).

## Other deferred items (recorded across the milestone handoffs)

- **One-click port open** — needs an optional `port` in the app config, threaded through
  proto/proc-state, plus host-resolution for the open link.
- **Light-mode variant** of the Signal identity (it's dark-only today).
- **Process command on the detail page** — `ProcInfo` doesn't ship `cmd`; would need a
  proto/agent change.
- Dashboard `/api/audit` view (M17 is CLI-only); FTS5 for log search at scale; per-instance (vs
  per-app) error attribution; rate-limiter persistence; session "log out everywhere" UI.

## Project conventions (unchanged)

- Spec → plan → TDD on a branch; whole-branch review before merge; **handoff after every
  milestone**; **live demo after the handoff** (and per user preference, show the rendered UI
  in-browser, not just payload checks). Design is now in scope — match the Signal tokens; don't
  revert to the old plain `styles.css`. (See project memory: `function-over-design-for-now.md`,
  now recording the Signal direction.)
- Local-only repo (no git remote) — merges are local `--no-ff` to `main`; PRs aren't available.
