# Handoff — stable IDs, multi-target selectors, `restart --update-env`

**Date:** 2026-07-01
**Branch:** `feature/stable-ids-selectors-envreload` (off `dev`; not yet merged)

## Current state

Three features implemented, tested (TDD), and demoed end-to-end. All commits are on the
feature branch. `dev`/`main` are untouched. Full `go test ./... -race -count=1` is green,
`go vet ./...` clean, `gofmt -l .` clean (the one file gofmt lists lives under a stale
`.claude/worktrees/…` checkout — not our code).

Spec: `docs/superpowers/specs/2026-07-01-stable-ids-selectors-envreload-design.md`
Plan: `docs/superpowers/plans/2026-07-01-stable-ids-selectors-envreload.md`
SDD ledger: `.superpowers/sdd/progress.md` (all 10 tasks marked complete, each review clean).

### Commits (in order)
- `c9a16ec` feat(config): persist daemon-assigned app ID in dump.json
- `474eb57` feat(manager): reuse persisted app IDs, assign next-free otherwise
- `76ad715` test(manager): cover ID migration; changelog for stable IDs
- `bae5a68` feat(cli): expandSelectorArgs for multi-target/comma selectors
- `40d5357` feat(cli): multi-target stop/delete/reset via runSelector
- `8717092` feat(cli): restartCmd with multi-target and --update-env flag scaffold
- `f9137e2` feat(manager): UpdateEnv RPC + in-place env swap with restart
- `51ac649` feat(daemon): UpdateEnv handler persists refreshed env, skips absent apps
- `6d5d0c9` feat(cli): implement restart --update-env env reload

## What changed and why

### 1. Stable app IDs (Group 1)
`config.App` gained `ID int` (`yaml:"-" json:"id,omitempty"`) — persisted in `dump.json`,
never read from user YAML. `manager.Add` now honors a positive, non-colliding `app.ID`,
else assigns `maxAppID()+1`, advances `nextID`, and writes the id back onto the stored spec
(`internal/manager/manager.go`, helpers `maxAppID`/`idTaken`). Previously IDs came from a
monotonic in-memory counter that reset/drifted every (re-)add, so `restart <id>` was
unreliable and IDs renumbered on every daemon restart.

**Automatic migration:** a pre-upgrade `dump.json` has no `id`, so the first daemon start
after upgrade assigns contiguous `1..N` by load order and persists them. Stable thereafter.

### 2. Multi-target selectors (Group 2)
`stop`/`restart`/`delete`/`reset` now take multiple targets and comma lists. New CLI helper
`expandSelectorArgs` (comma-split → `targetsFromArg` → flatten → de-dup; `all` short-circuits)
and `runSelector` (loops the per-target RPC; multi-target/config-file expansion warns and
continues with a non-zero exit; a single explicit target still fails hard). `selectorCmd`
switched to `cobra.MinimumNArgs(1)`. `restart` moved into its own `restartCmd()` builder
(it also carries `--update-env`). Purely CLI-side — no daemon/proto change for this group.

### 3. `restart --update-env` (Group 3)
New `UpdateEnv` daemon RPC (`proto/marshal/v1/daemon.proto`, regenerated `internal/pb`):
`UpdateEnvRequest { repeated AppSpec apps }` — only name+env read. `manager.UpdateEnv(name,
env)` swaps `a.spec.Env` and restarts instances in place (mirrors `Restart`'s locking; keeps
the app id and restart history). `server.UpdateEnv` updates each present app, skips absent
ones, persists via `store.Save(mgr.Specs())`. CLI `runRestartUpdateEnv` re-reads the config
file(s) (which resolve `env_file` into `env`), sends the RPC, and warns for apps in the file
that aren't running. Bare-selector + `--update-env` errors (needs a `marshal.yaml`).

## How to build / run / test

```bash
make build                       # stamps version from git describe
go test ./... -race -count=1     # full suite (green)
go vet ./... && gofmt -l .       # clean

# new usage
marshal restart 2 3              # multi-target
marshal delete 2,3               # comma list
marshal restart app.yaml --update-env   # reload env in place, preserve id
```

## Live demo performed (2026-07-01)

Isolated scratch daemon (`XDG_DATA_HOME=/tmp/marshal-demo/data`, 3 demo apps). Verified:
stable IDs survive `marshal kill` + auto-respawn (dump.json persisted 1/2/3 → same after);
`restart 1 3`, forgiving `restart 2 99` (warn + non-zero exit), `delete 2,3` comma; env
reload `hello-v1`→`hello-v2` with id preserved; bare-name `--update-env` rejected. Torn down
via `marshal kill` (scratch env) — no orphans; the standing launchd daemon (pid 899) was
untouched. NB: the demo daemon is only identifiable by its `XDG_DATA_HOME` env, not argv —
always tear down with `XDG_DATA_HOME=<scratch> marshal kill`, never a broad pkill.

Note: `marshal start` does NOT persist by itself; `dump.json` is written by `marshal save`
or any mutate op (restart/stop/delete/reset). This is the existing PM2-style `save` model,
unchanged by this work — but it means the stable-ID demo needs a `save` (or a mutate) before
killing the daemon, or there is nothing to resurrect.

## Known issues / minor findings (→ address in final review or follow-up)

- **Cobra prints full usage on a partial-batch failure.** `restart 2 99` returns an error, so
  cobra dumps the command usage/help after the table. Pre-existing behavior for any RunE
  error; consider `SilenceUsage: true` on these commands for cleaner batch output.
- `runRestartUpdateEnv` warns for absent apps by iterating a map → non-deterministic stderr
  order when multiple apps are absent. Harmless; sort if a test ever asserts order.
- `server.UpdateEnv` `continue`s on ANY `mgr.UpdateEnv` error (spec-mandated skip-on-absent).
  Fine given the manager's current error surface; add a code comment if that surface grows.
- Stale doc comment on `manager.opMu` lists `Add/Stop/Restart/Delete/StopAll` but not
  `UpdateEnv`.
- Task 2 `maxAppID` uses a local var named `max` (shadows the Go 1.21+ builtin); vet clean.

## Deferred (out of scope, noted in spec)

- Fleet/dashboard parity for env reload (`marshal fleet …`, dashboard button) — local daemon
  only this pass.
- Full-spec reload (cmd/args/cwd/instances/limits) — `--update-env` is env-only; other fields
  still need `delete` + `start`.
- Multi-target for `describe` / `logs` — single-target for now.

## Concrete next step

1. Run the final whole-branch code review (in progress at handoff time).
2. Decide merge shape: one PR for the whole branch, or split into three (stable-ids /
   selectors / env-reload) per the spec — all three groups are independently mergeable.
3. Merge into `dev` (`--no-ff`); cut a release when ready (minor bump — three user-facing
   features).
4. On the live box (89.163.150.187, tgbot): after the new binary is deployed, the first
   `systemctl --user restart marshal` migrates `dump.json` to contiguous IDs `1..N` and they
   stay stable — resolving the original "can't restart by ID" complaint.
