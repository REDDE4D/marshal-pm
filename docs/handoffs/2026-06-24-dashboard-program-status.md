# Dashboard data program — session status & resume point

**Date:** 2026-06-24
**Branch:** `dev` (integration). Working tree clean except an unrelated untracked file
`ref/plz-karte(2).png` (not part of this work — leave it). `main` unchanged at **v0.2.0**
(`4c09024`); `dev` is ahead by the milestones below, all merged `--no-ff` and pushed to `origin/dev`.

This is the single resume point for the data-first dashboard program. Per-milestone handoffs
hold the deep detail; the program roadmap is
`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`.

## Where the program stands

The program (see roadmap) is **data-first: build the backend capabilities on the current UI,
then ship the full redesign (M-A) last with every cell already real.**

**Done & merged to `dev`** (each: spec → plan → subagent-driven TDD → opus whole-branch review →
live demo → handoff → `--no-ff` merge):

| Milestone | What it added | Handoff |
|-----------|---------------|---------|
| **M-B** | Agent & host metadata (hostname, IP, OS/arch, version, uptime) on the fleet view | `2026-06-23-mB-agent-metadata.md` |
| **M-D** | Extended per-process metrics: threads, open FDs (`—` on macOS), last exit code + reason | `2026-06-23-mD-extended-process-metrics.md` |
| **M-C** | Host system metrics: CPU%, load avg, memory, network I/O **rate** | `2026-06-24-mC-host-system-metrics.md` |
| **M-E** | Restart history rollups: `restarts_24h` + `last_restart` from a SQLite event store | `2026-06-24-mE-restart-history.md` |
| **M-G** | Control additions: graceful rolling **reload** op, per-agent **restart-all** (UI), and a **log download** endpoint (`GET /api/logs/download`) | `2026-06-24-mG-control-additions.md` |
| **M-F** | Errors/exceptions subsystem: server-side **error-signature ledger** (compute-on-read from mirrored stderr), `GET /api/errors`, transitional **Errors page** (`#/errors`) | `2026-06-24-mF-errors-subsystem.md` |

M-B/M-C/M-D/M-E surface as **point-in-time fields** that ride the existing periodic fleet push
(`StateSnapshot` / `AgentState`) → `/api/fleet` → the SPA, with **minimal transitional UI** on the
current dashboard. The live demo for M-E showed all three of M-C/M-D/M-E on one card at once.
M-G adds control-plane ops (new `ControlOp.reload` = rolling per-instance restart) + a read
endpoint; live-demo verified end-to-end on a real fleet (reload/restart-all/download).

**Remaining** (roadmap order; M-A ships last):
- **M-A · Full redesign** (frontend; design already locked) — the "Marshal Instrument" restyle of
  every page, new shell, shared components, live-log modal, Notifications rewrite, Errors page —
  backed by the real data from B–F. Spec: `2026-06-23-dashboard-redesign-design.md`; prototype
  `.superpowers/brainstorm/46891-1782222731/content/demo3.html`.

## State of `dev`

- `CHANGELOG.md` `[Unreleased]` lists M-B/M-C/M-D/M-E/M-G (plus earlier notification/connect work).
  **No release cut yet** — these will likely be one or more minor bumps; decide cadence when a
  coherent group is ready (e.g. cut after the metrics + control milestones, or after M-A).
- New packages this program: `internal/hostmetrics` (M-C), `internal/eventstore` (M-E).
- `ProcInfo` now carries fields 1–18 (M-D added 13–16; M-E added 17–18). `AgentState` carries
  host metadata (M-B) + `HostMetrics` (M-C). All additive. `ControlOp` oneof now has field 10
  `reload` (M-G); new endpoint `GET /api/logs/download`.
- Full suite green at `dev` tip: `go test ./... -race -count=1`, `go vet`, `gofmt -l .` all clean;
  `make build` ok.

## Open follow-up (spun off, not blocking)

- **Daemon DB-handle leak on startup error paths** — a background task was spun off (chip
  `task_1072a088`): if `metricstore.Open`/`net.Listen`/`os.Chmod` fail mid-`Run`, the already-open
  `mdb`/`estore` SQLite handles aren't closed before the error return. Pre-existing `mdb` pattern;
  M-E added `estore` to it. Fix = `defer` the closes after each `Open`. Touches shutdown ordering —
  read the full `Run` defers first. Low impact (rare startup failures, process exiting).

## To resume

1. Read this file, then the roadmap and the chosen milestone's predecessors as needed.
2. Start the next milestone (recommended **M-F**, the errors/exceptions subsystem — largest;
   reuses M-E's event-store pattern) with the same flow used all session:
   **brainstorming → writing-plans → subagent-driven-development** (fresh implementer + spec/quality
   reviewer per task) → opus whole-branch review → live demo (CLAUDE.md convention) → handoff →
   merge `--no-ff` to `dev`. Create the milestone branch off `dev`. After M-F, only **M-A** (the
   full redesign) remains.
3. The SDD ledger (`.superpowers/sdd/progress.md`, git-ignored) currently holds M-G; it gets
   overwritten per milestone (prior ledgers are archived inside each milestone's handoff).

## Build / run / test

```bash
make proto   # regenerate internal/pb from proto/marshal/v1
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```
