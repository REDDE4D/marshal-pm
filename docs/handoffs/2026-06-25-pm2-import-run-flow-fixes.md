# Handoff — PM2 import & run-flow fixes (2026-06-25)

## Current state

Branch **`fix-pm2-import-run-flow`** (off `dev`), not yet merged. Four real-world
issues hit while a user imported an 11-app PM2 `ecosystem.config.js` (a Telegram
admin-bot fleet) and ran it under Marshal. All tests green
(`go test ./... -race`), `go vet` clean, `gofmt` clean.

Commits on the branch (oldest → newest):
1. `fix(pm2import): diagnose ESM ecosystem files instead of "no apps found"`
2. `fix(pm2import): bake an absolute cwd into every imported app`
3. `feat(cli): stop/restart/delete by yaml file + bordered, colored process table`
4. (help-text + changelog for the local-vs-fleet clarification)

`CHANGELOG.md` `[Unreleased]` is updated for all of it.

## What changed this session and why

### 1. Importer: ESM ecosystem files (`internal/pm2import/load.go`)
`marshal import pm2` evaluates `.js/.cjs` files via `node -e 'JSON.stringify(require(...))'`.
If the project's `package.json` has `"type":"module"`, node treats `ecosystem.config.js`
as ESM and silently drops the CommonJS `module.exports`, returning `{}` → the
importer reported the opaque `no apps found`. Now `checkEvalResult` detects the
empty/`default`-only export and tells the user to rename to `.cjs`; `nodeEval`
also folds node's stderr into the error so a throwing config (missing `.env`,
etc.) shows its real cause instead of `exit status 1`.

### 2. Importer: absolute cwd (`internal/pm2import/convert.go`, `load.go`)
PM2 resolves a relative `script` against the ecosystem file's dir and defaults
`cwd` to it. Marshal copied the (often empty) `cwd` verbatim, so `cmd: node`,
`args: [src/index.js]` with an empty cwd inherited the **daemon's** working dir
(under launchd that's `$HOME`) → `Error: Cannot find module '/home/tgbot/src/index.js'`.
`Load` now records `Ecosystem.BaseDir` (abs dir of the file) and `Convert`'s
`resolveCwd` makes every app's cwd absolute: absent → base dir; relative (e.g.
`./dashboard-next`) → joined onto base; absolute → untouched. With no base dir
(parseJSON used directly) cwd is left as-is, so non-Load callers are unchanged.

### 3. CLI: stop/restart/delete by yaml + table (`cmd/marshal/control.go`)
- `selectorCmd` used to pass its arg straight through as a selector, so
  `marshal stop marshal.yaml` searched for an app named `marshal.yaml`. New
  `targetsFromArg` expands a `.yaml/.yml` path to the app names it defines
  (fromFile=true → a missing app warns instead of aborting; a single literal
  selector still fails hard).
- `printProcs` now draws a box-drawing table via `renderProcTable`, colorizing
  the STATE column on a TTY (green online / red errored|stopped / yellow else).
  `isTerminal` gates color so pipes/files stay plain. Shared by list/start/stop/
  restart/describe/resurrect.

### 4. Dashboard reporting — NOT a bug, a topology mismatch (help text only)
Root cause fully traced. There are **two separate supervision contexts with
separate stores**:
- `marshal server startup --self-enroll <yaml>` (`cmd/marshal/selfenroll.go`)
  runs server + dashboard + an in-process agent whose store is
  `<serverData>/agent`. That agent supervises the yaml's apps and reports to the
  dashboard. It is a *different daemon* than the local one.
- `marshal start` / `marshal list` use the **default** store + an auto-spawned
  local daemon that is **not** enrolled with any server.
So the self-enrolled app shows in the dashboard but not in `marshal list`, and
`marshal start` apps show in `marshal list` but never in the dashboard. The
intended way to put apps on the enrolled agent is
`marshal fleet start <agent> <marshal.yaml>` (`cmd/marshal/fleet.go:316`); the
agent name is the host's hostname (`marshal fleet ps` lists it). The fleet
client itself is wired only once at daemon startup from the persisted server
config + token (`internal/daemon/server.go:327`) — there is no live reload.

Only a help-text clarification was added to `marshal start` (local-vs-fleet +
pointer to `marshal fleet start`). **A deeper fix is an open design decision —
see Deferred.**

## Build / run / test
```
make build
go test ./... -race -count=1
go vet ./... && gofmt -l .
```
Manual dogfood used a scratch `XDG_DATA_HOME=/tmp/marshal-demo-cli`:
`marshal start marshal.yaml` → `marshal list` (table) → `marshal stop marshal.yaml`
(stops all) → `marshal kill`. No orphans.

## Deferred / open decisions
- **Issue 1 deeper fix (needs the maintainer's call):** options are (a) leave as
  docs-only (current); (b) make `marshal start` detect a configured server and
  hint/route to the fleet; (c) unify so `marshal list` also shows enrolled-agent
  apps; (d) a `marshal fleet start` convenience that defaults the agent to the
  local hostname. Not started.
- `interpreter: none` with a relative `cmd` (e.g. `./run.sh`) still won't resolve
  against cwd because Go's `exec` looks up a relative argv[0] against the process
  cwd, not `cmd.Dir`. Not hit by node/python apps; left alone.
- `printFleet` (`marshal fleet ps`) still uses plain tabwriter — not upgraded to
  the new table.

## Concrete next step
Decide the issue-1 direction (above), then merge `fix-pm2-import-run-flow` → `dev`
(`--no-ff`). The user's immediate unblock: re-run `marshal import pm2
ecosystem.config.cjs` (note `.cjs`) with the new binary so apps get absolute
cwds, then `marshal fleet start <hostname> /home/tgbot/adminbot/marshal.yaml`
against the running self-enroll server to get them into the dashboard.
