# Design: Add an app via the dashboard

**Date:** 2026-06-19
**Status:** Approved (brainstorm) — ready for implementation plan
**Milestone:** M20 — dashboard "add an app" (true start flow)

## Summary

Add the ability to create and launch a **new** app from the redesigned ("Signal")
dashboard. This is the *true* `Start` op — distinct from M19's start button, which only
issues a `Restart` to revive an already-stopped/errored process. The user fills a form,
the dashboard sends a structured app spec to a new endpoint, and the server routes it
through the existing `ControlOp_Start` chain.

The backend start chain already exists end-to-end:

```
agent handleFleetCommand (ControlOp_Start)
  → doStart(specs)
  → appSpecToConfig(spec)   // proto → config.App, parses kill_timeout, validates
  → manager.Add(app)        // dup-name check, spawns instances
  → startInstance → supervisor.Instance.Run → proc.Start
  → store.Save              // app persists across daemon restarts
```

So this milestone is a **thin UI + routing layer**: one new dashboard endpoint, a
request→`AppSpec` mapping, and a modal form in the web client. **No proto change, no
agent change.**

## Goals

- Create a new app on a chosen connected agent from the dashboard, with the core +
  advanced fields of the app spec.
- Surface validation and dispatch errors (bad input, duplicate name, agent unreachable)
  back to the user in the modal.
- Leave the API shape forward-compatible with a future **full git support** milestone
  (deploy from a repo) without building any git logic now.

## Non-goals (deferred)

- The actual git source — only the `source.type` discriminator lands now (see
  Forward-compatibility).
- Per-app log-retention fields in the form (`max_size_mb`, `max_backups`, `max_age_days`,
  `compress`) — rarely set, sane defaults exist.
- Editing an existing app; one-click port open.
- Changing the card "start" button (stays restart-of-stopped).

## Architecture & data flow

```
AddAppModal (web)
  → POST /api/apps  { agent, source: { type:"command", ...appspec } }
  → dashboard h.apps:
        validate presence (agent, source.type, name, cmd)
        switch source.type:
          "command" → build *pb.AppSpec
          else      → 400 "unsupported source type"
        op = ControlOp_Start{ StartRequest{ Apps:[]*pb.AppSpec{spec} } }
        h.controller.Control(ctx, agent, op)        // existing path
  → agent doStart → appSpecToConfig → manager.Add → supervisor → proc.Start → store.Save
  → 200 {ok:true} | 400 {ok:false,error} | 502 {ok:false,error}
→ modal closes, overview refetches, new app card appears
```

The authoritative validation (restart mode, `kill_timeout` duration parse, `instances` ≥
0, **duplicate name**) remains in the existing `appSpecToConfig` / `config.Prepare` /
`manager.Add` path; its error string is surfaced verbatim. The dashboard handler only does
light presence checks before dispatch.

## API contract — `POST /api/apps` (new, session-guarded)

Why a dedicated endpoint instead of extending `POST /api/control`: `/api/control` is
selector-based (act on an existing app by name); app *creation* carries a full spec and a
*source*. A separate endpoint keeps the two concerns clean and is the natural place to add
a git source later without overloading a control verb.

Request body:

```json
{
  "agent": "dev-1",
  "source": {
    "type": "command",
    "name": "web",
    "cmd": "/usr/bin/node",
    "args": ["server.js"],
    "cwd": "/srv/app",
    "instances": 2,
    "env": { "PORT": "3000" },
    "restart": "always",
    "max_restarts": 16,
    "kill_timeout": "5s"
  }
}
```

- `agent` (required): target connected agent name.
- `source.type` (required): today only `"command"`. Unknown/`"git"` → `400 {ok:false,
  error:"unsupported source type"}`.
- `command` source fields map 1:1 to `pb.AppSpec`. Only `name` and `cmd` are required;
  the rest are optional and fall through to backend defaults (`instances`→1,
  `restart`→always, `max_restarts`→16, `kill_timeout`→5s).

Responses (mirrors `/api/control`, reuses the gRPC-error→HTTP mapping):

- `200 {ok:true}` — app created and launching.
- `400 {ok:false, error}` — missing field, unsupported source type, or validation /
  duplicate-name error from the start chain.
- `401` — no/invalid session.
- `502 {ok:false, error}` — target agent not connected / unreachable.

## Backend changes (Go)

- **`internal/dashboard/apps.go`** (new): `h.apps` handler — decode body, presence-check,
  switch on `source.type`, map `command` → `*pb.AppSpec`, build `ControlOp_Start`, call
  `h.controller.Control` (same 10s timeout as `control`). Audit log line consistent with
  `control`, e.g. `dashboard: add app <name> -> <agent> by <user>: ok=<bool>`.
- **`internal/dashboard/handlers.go`**: register
  `mux.HandleFunc("POST /api/apps", h.requireSession(h.apps))`.
- No changes to `daemon`, `manager`, `proc`, `supervisor`, `server`, or any `.proto`.

## Frontend changes (web/, Signal tokens)

- **`api.ts`**: `addApp(agent, source): Promise<ControlResult>` posting to `/api/apps`;
  a `CommandSource` type. Same never-throws contract as `control()`.
- **`AddAppModal.tsx`** (new):
  - Target-agent dropdown sourced from `GET /api/fleet` (connected agents only — includes
    proc-less agents, so a fresh agent can get its first app). Auto-select when exactly
    one; require an explicit pick when several; disable submit + show a hint when none
    connected.
  - Required: `name`, `cmd`.
  - Common: `args` (entered as a space/newline list), `cwd`, `instances`.
  - **Advanced** (collapsed by default): `env` (key/value rows), `restart`
    (select: always / on-failure / no), `max_restarts`, `kill_timeout`.
  - Client-side required-field gating; backend errors shown in an in-modal error banner.
  - Styled with existing Signal design tokens — no new `styles.css`, no plain styles.
- **Overview**: `+ Add app` button in the header opens the modal; on success → close,
  refetch overview, new card appears.

## Forward-compatibility: future full git support

The `source` discriminator is the only concession to the future git milestone. Today the
handler accepts `{type:"command", ...}`. When git lands, a `{type:"git", repo, branch,
build, ...}` variant is added as a new `case` in the handler's switch and a new tab/mode in
the modal — existing `command` callers are unaffected. No git cloning, building, or
validation is implemented now.

## Testing (TDD)

- **Go — `internal/dashboard/apps_test.go`** (fake controller asserting the dispatched op):
  - happy path → builds `ControlOp_Start` with the expected `AppSpec` field values.
  - unknown `source.type` → 400.
  - missing required field (`agent` / `name` / `cmd`) → 400.
  - validation / duplicate-name error from controller → 400 with error surfaced.
  - agent unreachable → 502.
  - unauthenticated (no session) → 401.
- **Web** — focused `AddAppModal` component test: required-field gating, agent-dropdown
  default (one agent) and empty (no agents) states, and the POST payload shape — consistent
  with existing web component tests.

## Build / verify

```bash
go test ./... -race -count=1
gofmt -l . ; go vet ./...
make ui          # web/ builds into internal/dashboard/dist (embedded)
```

Then the live-demo dance (per CLAUDE.md): scratch `XDG_DATA_HOME`, server on :9001 with a
connected agent, Vite dev proxy, add an app through the modal in-browser, confirm the card
appears and the process is online; tear down and confirm no orphans (`pgrep -fl marshal`).
