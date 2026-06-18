# M14 Dashboard Process Controls (Restart + Stop) — Handoff

**Date:** 2026-06-18
**Branch:** `m14-controls` (NOT yet merged to main)
**Gate:** green — `go test ./... -race -count=1` passes (19 packages), `gofmt -l .` silent,
`go vet ./...` clean, `make ui` regenerated the embedded SPA, `go build -o marshal ./cmd/marshal`
succeeds.

---

## Current state

M14 is complete (pending merge). The read-only dashboard now has a **write path**: each
process row in the Fleet table has **Restart** and **Stop** buttons (an "Actions" column) that
drive the agent through the *already-existing* fleet control path. Clicking a button shows an
inline **Confirm <action>? / ✕** step before firing.

No proto, agent, or manager changes — M14 is purely a surfacing job over the control path the
CLI already uses (`marshal fleet stop|restart`). Actions are **app-granular** (they target the
proc's app name; `manager.resolve` matches `"all"`, a numeric id, or an exact app name — never
`name#idx`).

Design spec: `docs/superpowers/specs/2026-06-18-marshal-dashboard-m14-process-controls-design.md`.
Implementation plan: `docs/superpowers/plans/2026-06-18-marshal-dashboard-m14-process-controls.md`.

Branch commits (newest first):

```
e6561b1 feat(dashboard): restart/stop buttons with inline confirm
1cabf9f feat(dashboard): POST /api/control endpoint (restart/stop)
d29aa83 feat(server): Control adapter; Serve takes a shared *Server
```

(Branched from `fcd031e` on `main`, which already carries the M14 spec + plan commits.)

---

## What was built

### Task 1 — `internal/server/server.go` (commit d29aa83)

- New adapter method, the dashboard's write-side dependency:
  `func (s *Server) Control(ctx, agent string, op *pb.ControlOp) (*pb.ControlResult, error)`
  — wraps the existing `FleetControl` RPC; nil error means the op reached the agent (the
  `*ControlResult` carries the agent's `Ok`/`Error`).
- Refactored `Serve` to take a pre-built `*Server`:
  `Serve(ctx, lis, srv *Server, cert)`. `ServeDir` now builds `srv := NewServer(reg, ss, ls, auth)`
  **once** and shares it with both the gRPC service and the dashboard. (The old `Serve`
  constructed its own `*Server` internally — which would have made a *second* broker the
  dashboard couldn't see; that bug class is now eliminated.) Shutdown still closes
  `srv.stores`/`srv.logs`; the nil-auth guard is intact. Five `Serve` test call sites updated.

### Task 2 — `internal/dashboard/control.go` (new) + wiring (commit 1cabf9f)

- `FleetController` interface (one method `Control(ctx, agent, op)`), satisfied **structurally**
  by `*server.Server` — no dashboard→server import (same no-cycle pattern as `LogsHistory`).
- New session-guarded route `POST /api/control`. Body `{"agent","selector","action"}` with
  `action` ∈ {`restart`,`stop`}. Status mapping:
  - missing/empty agent or selector → **400**; unknown action → **400**;
  - transport error (agent not connected / disconnected / timeout) → **502** `{"error":...}`;
  - agent executed, `Ok=false` → **200** `{"ok":false,"error":...}`;
  - agent executed, `Ok=true` → **200** `{"ok":true}`.
  - 10s bounded context derived from the request; one audit `log.Printf` per action
    (user + action + agent + selector + ok/err).
- `controller FleetController` threaded through `newHandler`/`NewHandler`/`dashboard.Serve`
  (inserted **after** the `logs` argument); `ServeDir` passes the shared `srv`. Existing
  `NewHandler` test sites pass `nil` in the new position.
- Also fixed a pre-existing compile error in `internal/fleet/client_test.go` (two old-signature
  `server.Serve` calls the Task 1 plan didn't list — `go build ./...` doesn't compile test files,
  so it slipped Task 1's checks; caught by `go test ./...`).

### Task 3 — `web/src/` (commit e6561b1)

- `api.ts`: `control(agent, selector, action)` + `ControlResult` type. It **never throws** —
  surfaces server errors as values so a failed control call cannot trigger a logout (only the
  fleet poll owns auth/401).
- `Fleet.tsx`: a `ProcActions` component (Restart/Stop with the inline-confirm state machine:
  first click → red "Confirm <action>?" + ✕, ~3s auto-revert; in-flight → "…"; brief ✓/error
  status after). Buttons disabled when the agent is disconnected. The Actions column is the
  8th column (both `colSpan` rows updated 7→8). All button clicks `stopPropagation()` so they
  don't toggle the row's detail panel.
- `styles.css`: action button / confirm / status styling.

The built SPA lives in committed `internal/dashboard/dist/`; `go build` embeds it without a
Node toolchain. Rebuild with `make ui` after any `web/src/` change.

---

## Build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .          # must print nothing
go vet ./...
make ui             # only when web/src/ changes; regenerates internal/dashboard/dist/
```

Run the dashboard as in M11–M13 (password set while the server is **down**, then start with
`--http-listen`, then enroll an agent). Open `https://localhost:<http-port>/`, log in, and use
the per-row Restart/Stop buttons.

---

## Live-demo result (2026-06-18, scratch `/tmp/marshal-m14-demo`, server `:9300`/`:9301`)

End-to-end verified against a real server with one enrolled agent (`demo-1`) running two demo
apps (`ticker`, `beeper`):

1. `POST /api/control` without a cookie → **401**.
2. Restart `ticker` → **200 `{"ok":true}`**; `/api/fleet` showed its **PID change** (16656→16902),
   still `online` — confirms the agent actually restarted it.
3. Stop `beeper` → **200 `{"ok":true}`**; next poll showed `state=stopped`, `pid=0`.
4. Unknown action (`delete`) → **400**; missing selector → **400**.
5. Unknown agent (`ghost`) → **502** `{"error":"...agent \"ghost\" not connected"}`.
6. Bogus selector on the real agent → **200 `{"ok":false,"error":"no app matching \"nope\""}`**.
7. **Rendered UI** (Playwright, real browser): the Fleet table shows the Actions column with
   Restart/Stop per row; clicking Restart swaps that row's button to a red **"Confirm restart?"**
   + ✕ pair while other rows stay normal; confirming fires the action.
8. Teardown: scratch agent daemon + server stopped, scratch dir removed. **No demo orphans** —
   `pgrep` showed only the user's own pre-existing daemon (pid 84457, started 11:30:06, managing
   a `clock` app in the **default** state dir, noted in the M12/M13 handoffs), left untouched.

---

## Review outcome

Per-task reviews (fresh reviewer each) + a final whole-branch review (opus):
**READY TO MERGE — YES, no Critical/Important findings.** The final review validated the
end-to-end control path (complete 400/502/200 mapping, no silent drop/misroute), the shared
`*Server` single-broker wiring, preserved shutdown/store-close + nil-auth guard, no new data
race (the broker was already accessed concurrently), the session guard, and that forwarding an
arbitrary selector to the agent is safe (`manager.resolve` = app/id/`all` only — no shell/glob).

### Deferred / known issues (Minor — carried for a follow-up polish)

1. **Per-row confirm state** — the spec noted "only one button across the table in confirm state
   at a time"; the implementation tracks confirm state per-row (`ProcActions` local state), so two
   different rows *could* be in confirm simultaneously. Simpler than spec, cosmetic.
2. **`control()` try/catch wraps only `r.json()`, not the `fetch`** — a network-level fetch
   rejection escapes as an unhandled async promise rejection (browser console warning). It still
   **cannot** trigger logout, so the "never throws → no logout" invariant holds; tightening it
   (wrap the whole call) is a clean follow-up.
3. **Test-coverage gaps** — no dedicated missing-*agent* 400 test (it shares the same branch as
   the covered missing-*selector* case); `control_test.go` passes nil auth without a clarifying
   comment; audit `user` is read after the `Control` call (harmless).

### Out of scope for M14 (per the spec)

Per-instance targeting (the manager is app-granular); `Start`-new (needs a spec-entry form /
larger trust surface) and `Delete` (destructive); a persisted audit store (M14 logs a line only);
per-action authorization / multi-user roles; optimistic UI state (state convergence relies on the
existing ~2s fleet poll).

Carried over from M11–M13 and still open: in-memory sessions lost on restart; `server passwd` /
`token --rotate` not hot-reloaded; no login rate-limiting; self-signed cert warning; single-user
accounts.

---

## Concrete next step

1. **Merge `m14-controls` to `main`** via the `finishing-a-development-branch` skill (final
   whole-branch review already passed: "ready to merge", no Critical/Important findings).
2. Optionally land the three Minor polish items above as a small follow-up commit.
3. **M15** — the next dashboard milestone (candidates from the carried-over deferred lists: auth
   hardening — session persistence + rate-limiting + hot-reload; server-side log search; or
   extending the control surface with Start/Delete). Pick one and brainstorm it.
