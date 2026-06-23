# Dashboard program roadmap — redesign + supporting data

**Date:** 2026-06-23
**Branch:** `ui-redesign` (off `dev`) — program branch; each milestone gets its own branch off `dev`.
**Decision:** Build the **full mocked surface** — the redesign **plus** all the data behind it.
Feature freeze (see `feature-freeze-ui-consistency.md` memory) is **lifted** for this program.
**Sequencing chosen by user:** **data-first, redesign last** — implement the backend capabilities
on the current UI, then ship the full redesign with every cell already populated.

## Why a program, not one spec

"Support all the functions we mocked up" requires ~6 net-new backend/agent capabilities on top of
the visual redesign. That's too large for a single spec/plan, so it's decomposed into milestones,
each with its own **spec → plan → implement** cycle. The visual design is already approved and
written as the **M-A** spec (`2026-06-23-dashboard-redesign-design.md`); the prototype
(`.superpowers/brainstorm/46891-1782222731/content/demo3.html`) is the visual source of truth.

## Gap analysis (what exists vs. net-new)

From a backend exploration of `internal/` (metrics, metricstore, logstore, supervisor, server,
dashboard, daemon, pb):

**Exists (restyle only):** per-process cpu/mem/pid/state/uptime/cumulative-restarts/source;
CPU+mem time-series (metricstore, 5s sample, bucketed) → charts + sparklines; logs (logstore,
stream filter + text search); restart/stop/delete control; git file browser; notifications;
credentials. Dashboard API: `/api/fleet`, `/api/metrics`, `/api/logs`, `/api/logstats`,
`/api/control`, plus auth/apps/credentials/notifications/files.

**Net-new (per milestone below).**

## Milestones (in execution order — data first)

### M-B · Agent & host metadata  (small)
Persist + expose hostname, IP/address, OS, arch, **agent/marshald version** (already sent in the
`Hello` proto but dropped at the registry — wire it through), host uptime, richer last-seen.
Touches: `proto/fleet.proto` (AgentState), `internal/server/registry.go`, `daemon/convert.go`,
`/api/fleet` view, SPA agent band meta. **Verdict baseline: PARTIAL today.**

### M-D · Extended per-process metrics  (small)
Add threads, open fds, exit code to the per-process sample/info. gopsutil already gives per-proc
data; extend `internal/metrics/sampler.go` (`Sample`), `ProcInfo` proto, convert, API, UI.

### M-C · Host system metrics  (medium)
Agent collects host CPU%/load average, total/free memory, network I/O (add gopsutil host/cpu/mem/
net). New host sample path + storage/stream + proto fields + `/api/fleet` (or a new host-metrics
endpoint) + cluster cells. **Verdict baseline: MISSING today.**

### M-E · Restart history  (medium)
Record timestamped restart events (new lightweight event store, SQLite like logstore/metricstore)
so "restarts 24h", last-restart, and velocity are real. Touches supervisor restart path
(`internal/supervisor/instance.go`) + a store + API.

### M-G · Control additions  (small)
Graceful **reload** (distinct from restart), per-agent **restart all** (bulk selector), and a log
**download** endpoint. Extend `ControlOp` proto + control handler + supervisor; wire to the
already-mocked buttons.

### M-F · Errors / Exceptions subsystem  (largest)
Group stderr into error **signatures** (dedupe by normalized message), store occurrence history +
last-seen + counts (reuse the event-store pattern from M-E), expose an errors API, and build the
**Errors page** (cluster + signature ledger + occurrences bar-sparkline + time-range filter).
Today only per-process stderr counts for the last 5 min exist (`logstore.ErrorCounts`).

### M-A · Full redesign  (frontend, design already locked)
The "Marshal Instrument" restyle of every page + new shell (icon rail, context bar), shared
components, live-log modal with filtering, quick actions, Notifications rewrite, and the Errors
page UI — now backed by real data from B–F. Full hardening pass. See the M-A spec.

## Conventions for the program

- Each milestone: branch off `dev`, TDD where logic changes, `CHANGELOG.md` `[Unreleased]` entry,
  handoff doc, and a live demo per the project conventions. Merge back to `dev` (`--no-ff`).
- Proto changes regenerate `internal/pb` (check the generate step in the repo).
- "Show only real data": until a metric's milestone lands, the redesign must omit/placeholder that
  cell rather than imply data we don't have. Since redesign is last, every cell should be real by
  the time M-A ships — but keep this rule for any interim UI surfacing.
- Versioning: likely several minor bumps across the program; decide release cadence as milestones
  land (could cut a release after a coherent group, e.g. metrics milestones).

## Next step

Design **M-B** (agent & host metadata): confirm the field set + where each value is sourced
(agent host introspection vs. existing `Hello`), the proto/registry/API changes, then plan + build.
