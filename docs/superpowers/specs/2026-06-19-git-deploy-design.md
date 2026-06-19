# Design: Deploy an app from git (clone → build → run, with redeploy)

**Date:** 2026-06-19
**Status:** Approved (brainstorm) — ready for implementation plan
**Milestone:** M21 — git deploy + redeploy (host-credential auth)

## Summary

Add a `git` app source to the dashboard "add an app" flow (M20 left the
`source.type` discriminator in place for exactly this). A user gives a repo URL and an
optional build command; the chosen agent **clones** the repo, **builds** it, and **runs**
the result as a normal supervised app. Because a clone + build can take minutes, the
deploy is **asynchronous**: `POST /api/apps` returns *accepted* immediately and the deploy
state (`cloning → building → online | failed`) rides the existing ~2s fleet heartbeat so
the dashboard card shows live progress. Git-sourced apps also gain a **redeploy** action
(fetch latest on the pinned ref → rebuild → restart in place, swapping only on a
successful build).

The run step is identical to a command app: a git-deployed app is a normal `config.App`
that *also* carries a `Source`. The new machinery is a single agent **deployer** component
that performs clone/build out of band and then hands the resolved spec to the existing
start chain:

```
dashboard POST /api/apps {source:{type:"git",...}}
  → ControlOp_Deploy{AppSpec+GitSource}
  → agent handleFleetCommand → deployer.Start(app)   // returns "accepted" fast
       └─ goroutine: git clone → build (output → per-app log sink)
            └─ on success → doStart(resolvedSpec) + store.Save   // existing chain
  fleetSnapshot() = mgr.List()  +  deployer.Snapshots()          // synthetic deploy entries
```

## Goals

- Deploy a **new** app from a git repo on a chosen connected agent: clone → optional/auto
  build → run, with the same core + advanced app-spec fields as a command app.
- Show **live deploy status** (`cloning`/`building`/`failed`) on the dashboard card via the
  existing fleet poll, with full clone+build output in the existing per-app log view.
- **Redeploy** an existing git app: fetch latest on the pinned ref, rebuild, restart in
  place — leaving the running version untouched if the build fails.
- Survive agent restarts: a deployed git app re-runs from its existing checkout on boot
  (no re-clone/rebuild), and its `Source` is retained so redeploy can fetch+rebuild later.

## Non-goals (deferred)

- **Marshal-managed credentials** — secret storage, encryption-at-rest, a credentials UI,
  and per-deploy git-env injection. This is the **next milestone**; it layers onto the
  same "agent runs git" seam without rework. This milestone relies on the agent host's own
  git credentials (SSH keys / credential helper / token-in-URL).
- Dockerfile / buildpacks; multi-step build pipelines.
- Deploy history / rollback to a previous build.
- Webhook- or schedule-triggered auto-redeploy.
- A dashboard file manager + in-browser editor (a separately flagged future idea).

## Architecture

### Wire contract (proto)

`daemon.proto`:

```proto
message GitSource {            // mirrors a new config.App.Source
  string repo   = 1;          // clone URL (required)
  string ref    = 2;          // branch / tag / sha; empty → repo default branch
  string build  = 3;          // build command; empty → auto-detect
  string subdir = 4;          // optional: build/run from this subdir of the checkout
}

message AppSpec {
  ... existing fields 1..10 ...
  optional GitSource source = 11;   // nil for command apps
}

message ProcInfo {
  ... existing fields 1..9 ...
  string source = 10;   // "command" | "git" — drives the redeploy button
  string detail = 11;   // short status summary for deploy entries, e.g. "build exited 1"
}
```

`fleet.proto`:

```proto
message DeployRequest   { AppSpec app    = 1; }   // git source rides on the AppSpec
message RedeployRequest { string  target = 1; }   // app name

message ControlOp {
  oneof op {
    Selector       stop     = 1;
    Selector       restart  = 2;
    Selector       delete   = 3;
    StartRequest   start    = 4;
    DeployRequest  deploy   = 5;   // new
    RedeployRequest redeploy = 6;  // new
  }
}
```

The `Deploy`/`Redeploy` ops return a `ControlResult` like every other op, but the agent
returns it **immediately** after admitting the deploy (no procs yet) — `ok:true` =
accepted, `ok:false` = rejected (e.g. duplicate name, missing repo). The actual clone/build
runs in the background and reports via the heartbeat. No transport change.

### Agent deployer (new package, held by daemon `Server`)

Owns an in-memory map `app-name → deployState{phase, detail}` and one background goroutine
per active deploy. Phases: `cloning`, `building`, `failed` (terminal until cleared);
success removes the entry (real instances take over).

`handleFleetCommand` gains two fast-returning cases:

```
case *pb.ControlOp_Deploy:    err = s.deployer.Start(v.Deploy.GetApp())
case *pb.ControlOp_Redeploy:  err = s.deployer.Redeploy(v.Redeploy.GetTarget())
```

**`Start(app)` (initial deploy):**
1. Reject fast (`ok:false "app \"X\" already exists"`) if an app of that name is already
   running (per `mgr`) or already mid-deploy. Reject if `repo` is empty.
2. Record `phase=cloning`. Spawn goroutine.
3. Goroutine:
   - Resolve deploy dir `<dataRoot>/deploys/<name>` (fresh for initial deploy).
   - Get stdout/stderr log writers from `logs.Registry.WriterPair("<name>#0")` (the
     `#0` instance label, so output lands in the app's existing log view via the same
     `FleetLogsHistory` selector even before any real instance exists).
   - `git clone --branch <ref> <repo> <dir>` (or clone + checkout when `ref` is a sha).
     Output → log writers.
   - `phase=building`: determine the build command (explicit `build`, else auto-detect —
     see below); run it via `sh -c` in `<dir>/<subdir>`. Output → log writers. Non-zero
     exit → `phase=failed{detail}`, stop (app not added).
   - **Success:** build the resolved `config.App` (cmd/args as given, `cwd` defaulting to
     `<dir>/<subdir>`, instances/env/restart/etc. preserved, `Source` retained), call the
     existing `doStart([spec])` + `store.Save(mgr.Specs())`, then delete the deploy entry.

**`Redeploy(name)`:** look up the persisted `config.App` (must carry a `Source`); record a
transient `phase=building` entry alongside the still-online instances. Goroutine:
`git fetch` + checkout `ref` in the existing dir → rebuild → on success `mgr.Restart(name)`
(re-execs the same cmd from the same cwd, picking up the new binary) and drop the entry.
**On build failure the running app is untouched** — phase flips to `failed` (transient),
old version keeps serving.

The deployer runs git/build through an injected **runner interface**
(`Run(ctx, dir, name, args..., stdout, stderr) error`) so tests need no network or real
git.

### Build auto-detect

Small, documented table; an explicit `build` command always wins:

| Repo marker | Build command |
|---|---|
| `go.mod` present | `go build ./...` |
| `package.json` with a `build` script | `npm ci && npm run build` |
| `package.json` without a `build` script | `npm ci` |
| none of the above | (no build — run as-is) |

### Snapshot representation

`fleetSnapshot()` (internal/daemon/fleet.go) becomes:

```
procs = convert(mgr.List())            // real supervised instances
      + deployer.Snapshots()           // synthetic ProcInfo for in-flight / failed deploys
```

Synthetic entries: `Name=<app>`, `State=cloning|building|failed`, `Source="git"`,
`Detail=<summary>`, `Pid=0`, `InstanceId=0`. They appear only while an app has no real
instances (initial deploy) or transiently during a redeploy; once `doStart`/`Restart`
lands, the real instances report instead. Real instances of a git app carry `Source="git"`
(new `Source` field on the manager's `InstanceSnapshot`, read from the spec; stamped onto
`ProcInfo` in `snapshotToProc`) so the card shows the redeploy button.

### Persistence, restart, delete

- `config.App` gains `Source *GitSource` (with a matching JSON shape), so it persists in
  `dump.json`. On agent restart the app re-runs straight from its existing checkout (cmd +
  cwd) — **no re-clone/rebuild on boot**; `Source` is retained only so redeploy can
  fetch+rebuild later.
- A **failed deploy is never persisted** (only success calls `doStart`/`Save`). If the
  agent restarts mid-deploy, the in-memory entry is gone and the user re-triggers.
- **Delete** of a git app also clears the deployer entry and removes
  `<dataRoot>/deploys/<name>`.

### Dashboard

- **HTTP** (`internal/dashboard/apps.go`): `POST /api/apps` keeps the `source.type`
  switch. `"command"` → unchanged sync `ControlOp_Start` path. `"git"` → validate
  (`name` + `repo` required), build an `AppSpec` with `GitSource`, send `ControlOp_Deploy`,
  return `202 {ok:true,status:"deploying"}`. New `POST /api/apps/redeploy {agent,name}` →
  `ControlOp_Redeploy{target:name}` → `202`. Error mapping mirrors the existing control
  path (401/502/400; `ok:false` for agent-rejected).
- **Web** (`web/src/`): `AddAppModal` gains a **Command | From Git** toggle; git mode shows
  `repo` (required), `ref/branch` ("default branch" placeholder), `build` ("auto-detect"
  placeholder), `subdir` (advanced), plus the shared name/instances/env/restart/optional-cwd
  fields. Cards render `cloning… / building…` live and `failed: <detail>` in red with a
  dismiss (= Delete); git-sourced online cards get a **Redeploy** button. `api.ts`: extend
  `addApp` to send a git source; add `redeploy(agent, name)`.

## Error handling

- Missing `repo`/`name` → `400` at the dashboard.
- Duplicate name → agent `ok:false "app \"X\" already exists"`, surfaced in the modal.
- Clone/build failure → `failed` card with `detail`; full output in the log view; app not
  added. User dismisses (Delete) to clear.
- Redeploy build failure → old version stays up; `failed` shown transiently.
- Agent unreachable → `502` (existing mapping); no session → `401`.

## Testing

- **Go TDD** (the deployer runs git/build via an injected runner, so no network):
  - `deployer`: phase transitions `cloning→building→online`; build-fail → `failed`;
    duplicate / missing-repo reject; redeploy `fetch→rebuild→restart`; swap-only-on-success
    (build fail leaves the running app untouched); delete clears entry + dir.
  - build-command auto-detect table tests.
  - snapshot merge (synthetic entries) + `source` stamping on real instances.
  - `apps.go`: git → `ControlOp_Deploy` mapping + validation + `202`; redeploy endpoint →
    `ControlOp_Redeploy`; error mapping (400/401/502/`ok:false`).
- **Frontend:** `tsc -b` (in `make ui`) + a live in-browser demo (repo has no web test
  framework — per M15–M20 convention).

## Live demo plan

On `:9000/:9001` (standing demo-port convention), against a real connected agent:
deploy a small public repo with an explicit build command; deploy another relying on
auto-detect; trigger a build failure and confirm the output appears in the log view and
the card shows `failed`; redeploy a deployed app and confirm it picks up a new commit.
Tear down and confirm no orphan `marshal` processes.

## Open decisions resolved in brainstorm

- Dedicated `ControlOp_Deploy`/`Redeploy` ops (not overloading `Start`).
- Redeploy endpoint shape: `POST /api/apps/redeploy {agent,name}`.
- Auto-detect table deliberately tiny (Go + Node) for this milestone.
- Build runs through `sh -c` so compound commands (`npm ci && npm run build`) work.
