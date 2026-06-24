# M-G · Control additions — design

**Date:** 2026-06-24
**Milestone:** M-G (dashboard data program; see `2026-06-23-dashboard-program-roadmap.md`).
**Branch:** create `mG-control-additions` off `dev`.
**Predecessors:** M-B/M-C/M-D/M-E merged to `dev` (see `2026-06-24-dashboard-program-status.md`).

## Goal

Add three control-plane capabilities, **data-first** (backend capability + minimal
transitional UI now; the polished buttons land with the M-A redesign):

1. **Graceful reload** — a new control op, distinct from restart.
2. **Restart-all** — restart every app on an agent (UI affordance over the existing op).
3. **Log download** — a new read endpoint returning a process's full log history as a file.

## Context (current control path)

- `proto/marshal/v1/fleet.proto` — `ControlOp` oneof (lines ~192–204): `stop=1`, `restart=2`,
  `delete=3`, `start=4`, `deploy=5`, `redeploy=6`, `list_dir=7`, `read_file=8`, `commit=9`.
  `Selector{ string target = 1; }` (`daemon.proto`); `target` resolves to an app name, a numeric
  instance id, or the literal `"all"` via `manager.resolve()`.
- `internal/server/server.go` `FleetControl` → broker `session.dispatch()` → agent command stream.
- `internal/daemon/command.go` `handleFleetCommand` — the op switch on the agent side; routes each
  op to a `manager` method.
- `internal/manager/manager.go` — `Stop(sel)`, `Restart(sel)`, `Delete(sel)`; helpers
  `resolve`, `collectInstances`, `stopInstances`, `startInstance`. `Restart` stops **all**
  instances of an app at once, then starts them all fresh.
- `internal/dashboard/control.go` `POST /api/control` (`{agent, selector, action}`) → `controlOp()`
  switch (`restart`/`stop`/`delete`) → `h.controller.Control()`.
- `internal/dashboard/logs.go` `GET /api/logs` — `logsHist.Since(agent, selector, after, limit,
  streamFilter, q)` → JSON `{cursor, lines}`. Query params: `agent`, `selector`, `stream`
  (all/stdout/stderr), `limit`, `after`, `q`.
- `web/src` SPA: `ControlButtons.tsx` (start/restart/stop), `api.ts` `control()` (action union),
  the log view component. **No reload / restart-all / download controls exist in the live UI** —
  those appear only in the M-A redesign prototype (`demo3.html`), so M-G builds the backend plus
  minimal transitional controls, exactly as M-B/C/D/E did.
- Proto regen: `make proto` → `scripts/gen-proto.sh` → `internal/pb/*.pb.go`.

## Design

### 1. Graceful reload (`reload`) — net-new op

**Semantics: rolling graceful restart.** For each app matched by the selector, restart its
instances **one at a time**: stop instance _i_ (existing graceful path — SIGTERM, wait up to
`kill_timeout`, then SIGKILL), wait for its goroutine to finish, start a fresh instance _i_, then
proceed to _i+1_. A multi-instance app keeps its remaining instances running throughout; a
single-instance app degrades to an ordinary graceful restart. Because `reload` carries a
`Selector`, `reload all` performs a rolling reload across every app and all their instances.

**Difference from `restart`:** `restart` stops every instance of an app at once and then starts
them all; `reload` uses the same primitives but sequences them per instance (never more than one
instance of a given app down at a time).

**Layers:**
- **Proto:** add `Selector reload = 10;` to the `ControlOp` oneof. Run `make proto`; commit the
  regenerated `internal/pb`.
- **Manager:** add `Reload(sel string) ([]InstanceSnapshot, error)`. Resolve apps under the same
  locking discipline `Restart` uses. For each app, iterate its instances by index: stop that one
  instance, wait on its `done`, then `startInstance(spec, idx)` and replace it in the app's slice,
  before moving to the next index. Reuses `resolve`, the per-instance stop, and `startInstance`.
- **Daemon:** add `case *pb.ControlOp_Reload:` in `handleFleetCommand` → `s.mgr.Reload(target)`,
  returning `procList(snaps)` on success, mirroring the `restart` case.
- **Dashboard:** add `"reload"` to the `controlOp()` switch in `control.go` → `ControlOp_Reload`.
- **Restart-history (M-E) consistency:** a reload restarts real instances, so it must record
  restart events **exactly as a manual `restart` does today** — the implementation will match the
  existing `manager.Restart` event-recording behavior (whatever it is), not introduce new
  semantics. This is verified against the current code during planning.

### 2. Restart-all — UI affordance only (no backend change)

`restart` with selector `"all"` already restarts every app on an agent (`manager.resolve("all")`).
M-G adds a minimal transitional **"restart all"** action at the agent level in the SPA, with a
confirm dialog, calling the existing `/api/control` path with `action:"restart", selector:"all"`.
No proto/manager/daemon change.

### 3. Log download — net-new read endpoint

- **Endpoint:** `GET /api/logs/download`, registered in `handlers.go` behind `requireSession`
  (cookie-based auth, so a plain browser link/navigation works).
- **Params:** `agent`, `selector`, and the same `stream` (all/stdout/stderr) and `q` (grep) filters
  as `/api/logs`. **No `limit`/`after`** — it returns the **full retained history**.
- **Response:** `Content-Type: text/plain; charset=utf-8` and
  `Content-Disposition: attachment; filename="<agent>-<selector>.log"`. One log line per output
  line (human-readable text, matching what the live log view shows).
- **Streaming:** write lines to the response as they are read rather than buffering the whole
  history in memory. If `logsHist`'s current `Since` API forces a bounded `limit`, the plan adds a
  dedicated export/iterator method on the log store (e.g. stream all rows for an agent/selector
  with the stream/q filter applied) rather than passing an arbitrarily large limit.
- **UI:** a minimal transitional **"download"** link in the log view that points at the endpoint
  with the current `agent`/`selector`/`stream`/`q`.

## Testing (TDD — write failing tests first)

- **manager:** `Reload` restarts instances and does so **sequentially** — for a 2-instance app,
  the two instances are never both down at the same moment (assert via the instance lifecycle
  ordering / existing test hooks). Selector resolution (`all`, app name, single instance) behaves
  like `Restart`.
- **daemon/command:** a `ControlOp_Reload` dispatches to `Reload` and returns the affected procs;
  errors surface as `{ok:false,error}`.
- **dashboard:** `/api/control` accepts `action:"reload"` and rejects unknown actions; restart-all
  is just `action:"restart", selector:"all"` (already covered, add a guard test if cheap).
  `/api/logs/download` returns `text/plain`, the attachment header with the right filename, the
  full history (more than the `/api/logs` default limit when applicable), and respects `stream`
  and `q` filters.
- **frontend:** extend the `action` union; the three controls render and call the right
  endpoints. Keep this minimal (transitional UI).
- **whole suite:** `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (clean),
  `make build`.

## Out of scope (deferred to M-A)

- Polished button styling/placement and final UX (M-A redesign owns this).
- Multi-select bulk selector (pick N specific apps) — not needed; `restart all` / `reload all`
  cover bulk.
- A dedicated "rolling restart-all" button — the restart-all **button** uses the existing hard
  `restart all`; rolling-everything is still reachable via `reload all`.

## Deliverables

- Proto + regenerated `internal/pb`; `manager.Reload`; daemon dispatch case; dashboard
  `reload` action + `/api/logs/download`; minimal transitional UI controls; rebuilt embedded SPA
  bundle (`make ui`, committed).
- `CHANGELOG.md` `[Unreleased]` entry (Added).
- Handoff `docs/handoffs/2026-06-24-mG-control-additions.md`; live demo per CLAUDE.md conventions.
- Merge `--no-ff` into `dev`.
