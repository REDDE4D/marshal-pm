# M-G · Control additions — handoff

**Date:** 2026-06-24
**Branch:** `mG-control-additions` (off `dev`) — **ready to merge `--no-ff` into `dev`**.
**Spec:** `docs/superpowers/specs/2026-06-24-mG-control-additions-design.md`
**Plan:** `docs/superpowers/plans/2026-06-24-mG-control-additions.md`
**Program status:** `docs/handoffs/2026-06-24-dashboard-program-status.md` (the data-program resume point).

## TL;DR

M-G adds three control-plane capabilities, data-first (backend + minimal transitional UI; the
polished buttons land with M-A):

1. **Graceful reload** — a new `reload` control op = **rolling per-instance restart**: per app,
   restart instances one at a time (stop → wait exit → start → wait online) so a multi-instance app
   never has more than one instance down at once. `reload all` rolls across every app.
2. **Restart-all** — UI-only affordance over the existing `restart` op with selector `"all"` (no
   backend change). A per-agent "restart all" button (confirm) in the Overview agent header.
3. **Log download** — `GET /api/logs/download`: the full retained log history as a `text/plain`
   attachment, honoring the `stream`/`q` filters; a "download" link in the process log controls.

Built spec → plan → 5 subagent-driven TDD tasks (each reviewed clean) → opus whole-branch review
(**2 Important, 0 Critical**) → fix + re-review clean → live fleet demo. Full suite green.

## What changed (by file)

- **`proto/marshal/v1/fleet.proto`** — `ControlOp` oneof gains `Selector reload = 10;` (additive;
  fields 1–9 untouched). `internal/pb/fleet.pb.go` regenerated via `make proto`.
- **`internal/manager/manager.go`** — new `Reload(sel string) ([]InstanceSnapshot, error)`: rolling
  restart reusing `resolve`/`startInstance`. New `waitInstanceOnline(ctx, in, timeout)` (ticker +
  `select` on `ctx.Done()`/ticker/timeout; `reloadOnlineTimeout = 10s`). Unexported `onReloadStep
  func()` **test seam** on `Manager` (nil in prod; fires after stop, before the replacement starts).
  Reload **adds no restart-event recording** (matches manual `Restart`; M-E consistency). On
  manager-context cancellation mid-reload it returns `context.Canceled` instead of spinning up
  doomed instances.
- **`internal/daemon/command.go`** — `case *pb.ControlOp_Reload:` → `s.mgr.Reload(target)`, after
  the restart case; returns affected procs like restart.
- **`internal/dashboard/control.go`** — `controlOp` accepts `action:"reload"` → `ControlOp_Reload`.
- **`internal/dashboard/logs.go`** — `logsDownload` handler (`GET /api/logs/download`): validates
  params (400 on missing agent/selector), calls `logsHist.Since(agent, selector, 0, 0, filter, q)`
  (**limit 0 = no limit** = full history), sets `Content-Type: text/plain; charset=utf-8` +
  `Content-Disposition: attachment; filename="<agent>-<selector>.log"` (via `downloadName`, which
  sanitizes `/ \ "`), writes `ts <out|err> <label> | text` per line. Error-before-headers ordering.
- **`internal/dashboard/handlers.go`** — registers `GET /api/logs/download` behind `requireSession`.
- **`web/src/api.ts`** — `control` action union widened to include `"reload"`; new
  `logsDownloadURL(agent, selector, {stream, q})`.
- **`web/src/ControlButtons.tsx`** — reload button (between restart and stop; enabled when
  connected && running; uses the existing confirm flow).
- **`web/src/RestartAllButton.tsx`** (new) — per-agent confirm button → `control(agent, "all",
  "restart")`.
- **`web/src/Overview.tsx`** — renders `<RestartAllButton>` in the agent header when procs > 0.
- **`web/src/ProcessDetail.tsx`** — "download" link in the log controls wired to the current
  `stream` and debounced search.
- **`internal/dashboard/dist/**`** — embedded SPA bundle rebuilt (`make ui`).
- **`CHANGELOG.md`** — `[Unreleased] → Added` entry covering reload + restart-all + log download.

## Key decisions / non-obvious points

- **Reload = rolling, not zero-downtime.** Marshal has no cluster/socket-sharing, so a true
  zero-downtime reload isn't possible; "rolling per-instance restart" is the honest analog. The
  difference from `restart` is purely sequencing (one instance down at a time vs. all at once).
- **`reload` is not a health gate.** A replacement that never reaches `online` within
  `reloadOnlineTimeout` (10s) is skipped past **without** failing the reload (matches `Restart`).
  The one exception: manager-context cancellation (daemon shutdown) makes Reload return an error.
  This was the final-review's Important #2 — deliberately documented rather than adding fail-fast
  health-detection (which would false-fail legitimately slow-starting apps). See spec "Known
  limitations".
- **Reload of a stopped app starts it** (it iterates `spec.Instances` regardless of prior state).
- **Restart-all needed no backend work** — `manager.resolve("all")` already existed; M-G is just the
  button. The `reload`-as-rolling path is reachable for "all" too via `reload all`.
- **Test seam `onReloadStep`** lets `TestReloadIsRolling` deterministically assert the rolling
  invariant (`steps>0 && minOnline==instances-1`) and `TestReloadAbortsOnContextCancel` assert the
  prompt ctx-cancel abort — both without timing flakiness.

## Final review outcome (opus, whole branch) — fixed, re-reviewed clean

- **0 Critical.** **Important #1 (fixed):** reload during daemon shutdown could stall up to
  `10s × instances` (busy-poll of the full timeout for instances spawned on a canceled `m.ctx`,
  while holding `opMu` → blocks `StopAll`). Fix: ctx-aware `waitInstanceOnline` + abort-on-cancel in
  `Reload` (commit `fdc0d06`), with a deterministic abort test. **Important #2 (documented):**
  non-online replacement skipped without error — recorded as a known limitation (see above).
- Minors (non-blocking, swept or noted): proto comment whitespace reverted; `downloadName` unit
  test added; `io.ReadAll` err checked + `resp.Body` closed in dashboard tests; busy-poll replaced
  by ticker. Cosmetic leftovers: download body uses the raw `name#idx` label (fine for a text
  export); restart-all button enabled whenever connected (backend handles a no-op).

## Live demo (2026-06-24, scratch `/tmp/marshal-mG-demo`, real server :9000 / dashboard :9001) — PASS

- Auth set while server down; enrolled a real `demo-agent` (`marshal start`, isolated
  `XDG_DATA_HOME`) running a 2-instance `web` app that logs every second.
- **reload** (`POST /api/control action:reload`): `{"ok":true}`; `/api/fleet` PIDs went
  30485/30486 → 30662/30665, both `online`, **restarts=0** (confirms reload records no restart
  event — M-E consistent).
- **restart all** (`action:restart, selector:all`): `{"ok":true}`; PIDs changed again
  (30763/30765).
- **log download** (`GET /api/logs/download`): `200`, `content-type: text/plain; charset=utf-8`,
  `content-disposition: attachment; filename="demo-agent-web.log"`, 82 lines (full history);
  `&stream=stdout&q=line%205` correctly filtered to 4 matching lines.
- **In-browser (Playwright):** detail page renders the **reload** button (restart→reload→stop) and
  the **download** link; Overview renders the per-agent **restart all** button. Screenshots
  `/tmp/mG-overview.png`, `/tmp/mG-detail.png` shown to the user.
- Teardown: demo agent killed by its data dir, demo server stopped, scratch removed; standing
  launchd daemon (PID 899) preserved; no orphans.

## Verification

`go build ./...`, `go test ./... -race -count=1` (27 pkgs), `go vet ./...`, `gofmt -l .` (clean),
`make build` and `make ui` all green at branch tip.

## Deferred / known issues

- **Reload is not a health gate** (see above) — by design; documented in the spec.
- **Daemon startup DB-handle leak** — still open, spun off earlier as a background task
  (`task_1072a088`); unrelated to M-G.
- UI is **minimal transitional** — M-A delivers the real treatment and wires the polished buttons.

## Next step

Merge `mG-control-additions` → `dev` (`--no-ff`), delete the branch, push `dev`. Remaining roadmap:
**M-F** (errors/exceptions subsystem — largest; reuses M-E's event-store pattern), then **M-A**
(the full redesign, backed by all the B–F data). Recommend **M-F** next, then **M-A**. Consider
cutting a release after a coherent group (e.g. the metrics + control milestones) — decide cadence
when M-F lands.

## Build / run / test

```bash
make proto   # regenerate internal/pb from proto/marshal/v1
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```
