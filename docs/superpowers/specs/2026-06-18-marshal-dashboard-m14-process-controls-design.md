# M14 — Dashboard Process Controls (Restart + Stop) — Design

**Date:** 2026-06-18
**Status:** approved (brainstorm) — ready for implementation plan
**Milestone:** M14 (dashboard write path)

## Summary

Surface the **already-existing** fleet control path (`FleetControl` → broker → agent
stream → `manager`) in the web dashboard, exposing two operations: **Restart** and
**Stop**. This turns the dashboard from read-only into a (narrow) control plane.

The entire backend control mechanism already works end-to-end and is exercised today by the
CLI (`marshal fleet stop|restart <agent> <selector>`). M14 adds **no** proto, agent, or
manager code. It is three thin layers:

1. Share the server's `broker` with the dashboard (small wiring refactor).
2. One new session-guarded HTTP endpoint: `POST /api/control`.
3. Restart/Stop buttons with inline confirmation in the React UI.

## Background (verified in code)

- `cmd/marshal/fleet.go` already builds `ControlOp_Stop` / `ControlOp_Restart` (and
  `Delete`, `Start`) and calls the `FleetControl` RPC.
- `internal/server/server.go: FleetControl` resolves the agent's live session via the
  `broker` (`internal/server/broker.go`) and `dispatch`es the op down the agent's existing
  upstream, returning the agent's `ControlResult`. Disconnected/timeout map to gRPC
  `Unavailable`/context errors.
- `internal/daemon/command.go: handleFleetCommand` executes the op against the manager and
  returns `ControlResult{Ok, Error, Procs}`. Wired via `fleet.WithCommands` in
  `internal/daemon/server.go`.
- `internal/manager/manager.go: resolve(sel)` matches `"all"`, a numeric app id, or an
  **exact app name** — it does **not** parse `name#idx`. Therefore Stop/Restart act at
  **app granularity**: all instances of the named app on that agent. The dashboard selector
  is the proc row's `Name` (the app name), identical to what the CLI accepts.
- `pb.ProcInfo.Name` is the app name; `InstanceId` is a separate field. The dashboard's
  current `procView` drops `InstanceId`, so multiple instances of one app render as
  identical rows — all of which a control action affects together (honest with the manager).

## Architecture — wiring (Approach A)

The dashboard package imports only leaf packages and talks to the server through small
interfaces satisfied structurally by `*server` types (`FleetLister`, `MetricsHistory`,
`LogsHistory`). Control needs the `broker`, which lives on `*server.Server`. Today
`ServeDir` launches the dashboard goroutine **before** `Serve` constructs the `Server`/broker,
so the broker does not exist yet when the dashboard starts.

**Fix:** construct the `Server` once, up front, and share it.

- In `ServeDir`, build `srv := NewServer(reg, ss, ls, auth)` before launching the dashboard
  goroutine. Pass `srv` to both `dashboard.Serve(...)` (as a new `FleetController`) and to a
  refactored `Serve`.
- Refactor `server.Serve` to accept the pre-built `*Server` instead of re-constructing it:
  - New signature: `Serve(ctx context.Context, lis net.Listener, srv *Server, cert tls.Certificate) error`.
  - It still validates `srv.auth != nil`, registers the gRPC service with `srv`, and runs the
    same shutdown/store-close goroutine (the stores are reachable from `srv`, or passed
    alongside — implementation detail for the plan).
  - The five existing test call sites
    (`server_test.go`, `e2e_test.go` ×2, `tls_serve_test.go` ×2) build a `srv` via
    `NewServer(...)` first, then call the new `Serve`. (A thin backward-compat wrapper that
    keeps the old arg list is acceptable if it keeps test churn smaller — implementer's
    choice; either way the production path uses the shared `srv`.)

- New method on `*server.Server`, the control adapter the dashboard depends on:

  ```go
  // Control routes one control op to a connected agent and returns its result.
  func (s *Server) Control(ctx context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error) {
      resp, err := s.FleetControl(ctx, &pb.FleetControlRequest{AgentName: agent, Op: op})
      if err != nil {
          return nil, err
      }
      return resp.GetResult(), nil
  }
  ```

- Dashboard defines the interface (no import cycle — it already imports `marshal/internal/pb`):

  ```go
  // FleetController is the write side of the fleet. *server.Server satisfies it.
  type FleetController interface {
      Control(ctx context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error)
  }
  ```

  `newHandler` / `NewHandler` / `dashboard.Serve` gain a `FleetController` parameter (placed
  after `logs LogsHistory`, matching the existing ordering convention). `ServeDir` passes the
  in-scope `srv`.

## HTTP contract — `POST /api/control`

Session-guarded (same `requireSession` wrapper as `/api/fleet`, `/api/logs`).

**Request body (JSON):**

```json
{ "agent": "demo-1", "selector": "chatty", "action": "restart" }
```

- `action` ∈ {`"restart"`, `"stop"`}.

**Validation / mapping:**

- Missing/empty `agent` or `selector` → **400** `{"error":"..."}`.
- `action` not in the allowed set → **400** `{"error":"unknown action"}`.
- Build the op:
  - `restart` → `&pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: selector}}}`
  - `stop` → `&pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: selector}}}`
- Call `ctrl.Control(ctx, agent, op)` with a bounded context (**10s** timeout) derived from
  the request context.

**Responses:**

| Condition | Status | Body |
|---|---|---|
| transport error (agent not connected / disconnected / timeout) | **502** | `{"error":"<msg>"}` |
| executed, `ControlResult.Ok == false` | **200** | `{"ok":false,"error":"<msg>"}` |
| executed, `ControlResult.Ok == true` | **200** | `{"ok":true}` |

The handler does not need to import gRPC `status`: any non-nil error from `Control` → 502 with
the error string; a nil error with a `ControlResult` → 200 with `ok`/`error` from the result.

**Audit:** one server-side `log.Printf` per action: dashboard user (from `userKey` in
context) + action + agent + selector + ok/err. No persisted audit store in M14.

## Frontend UX (`web/src/`)

- `api.ts`: a `control(agent, selector, action)` helper that POSTs the body and returns the
  parsed `{ ok?, error? }`. It throws on 401 only if we choose to — but control is
  **best-effort** (see below), so it surfaces errors as values, never triggering logout.
- `Fleet.tsx`: each proc row header gains **Restart** and **Stop** buttons. Each button's
  `onClick` calls `e.stopPropagation()` so it does not toggle the row's expand panel.
  - **Inline confirm:** first click swaps the clicked button into a `Confirm? / ✕` pair; the
    confirm click fires the request. A short timeout (≈3s) reverts to the idle button. Only
    one button across the table may be in the confirm state at a time (track
    `{rowKey, action}` in component state).
  - **In-flight:** the row's buttons disable and show `…`; on completion show a brief inline
    status (✓ on success, the error text on failure) that naturally clears on the next fleet
    poll.
  - **Disconnected agent:** buttons are disabled when the owning `agentView.Connected` is
    `false`.
  - **Best-effort:** a failed control call shows its error inline and never logs the user out
    — only the existing fleet poll owns auth/401 handling. State convergence relies on the
    existing 1.5s fleet poll, which already refreshes proc state; no optimistic mutation.
- `styles.css`: button + confirm + inline-status styling.

The built SPA lives in committed `internal/dashboard/dist/`; `go build` embeds it without a
Node toolchain. Rebuild with `make ui` after any `web/src/` change.

## Testing

**Go:**

- `*server.Server.Control`: returns the agent's result when connected; returns an error when
  the agent is not connected (drives `FleetControl`'s `Unavailable`).
- HTTP handler (`httptest` + a fake `FleetController`):
  - happy path for `restart` and `stop` → 200 `{"ok":true}`, correct op/selector forwarded.
  - `400` for unknown `action`, missing `agent`, missing `selector`.
  - `502` when the fake controller returns an error.
  - `200 {"ok":false,"error":...}` passthrough when the controller returns `Ok:false`.
  - session guard: `401` without a valid cookie.
- Updated `Serve` call sites compile and the existing server/e2e tests still pass.

**Gate (must be green before finishing):**

```bash
go test ./... -race -count=1
gofmt -l .          # prints nothing
go vet ./...
make ui             # regenerate dist after web/src changes
go build -o marshal ./cmd/marshal
```

**Live demo (per project convention):** scratch `XDG_DATA_HOME`, server up with
`--http-listen`, enroll one agent running a couple of demo processes, log in, click
Restart/Stop and confirm the proc state changes in the next poll, exercise the inline-confirm
revert, hit a disconnected-agent case, then tear down and verify no orphan `marshal`
processes.

## Out of scope (carried forward)

- Per-instance targeting (the manager resolves at app granularity).
- `Start`-new (needs a spec-entry form / larger trust surface) and `Delete` (destructive).
- A persisted audit store (M14 logs a line only).
- Per-action authorization / multi-user roles (the dashboard is single-user).
- Optimistic UI state (we rely on the existing fleet poll to converge).

Still open from M11–M13 and unchanged by M14: in-memory sessions lost on restart;
`server passwd` / `token --rotate` not hot-reloaded; no login rate-limiting; self-signed cert
warning; single-user accounts.

## Concrete next step

Invoke the `writing-plans` skill to turn this design into a task-by-task implementation plan,
then execute it on a feature branch (`m14-controls`).
