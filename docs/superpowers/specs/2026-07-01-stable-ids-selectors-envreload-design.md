# Design: stable app IDs, multi-target selectors, and `restart --update-env`

**Date:** 2026-07-01
**Status:** Approved (design); ready for implementation planning
**Branch:** `feature/stable-ids-selectors-envreload` (off `dev`)

## Motivation

Three related pain points surfaced while operating the live agent:

1. **`restart` does not pick up env changes.** Editing `env:` / `env_file:` in a
   `marshal.yaml` and running `marshal restart <name>` re-launches from the daemon's
   *stored* spec. The daemon only ever persists the **resolved** env map, never the file
   path, so a plain restart cannot re-read the file. Applying an env change today requires
   `marshal delete` + `marshal start`, which loses the app's ID and restart history.

2. **App IDs are unstable and non-contiguous.** IDs come from a monotonic in-memory
   counter (`m.nextID++`) that is never persisted. Every (re-)add — daemon restart,
   `start`, `resurrect` — reassigns IDs from load order, and runtime re-adds make the
   counter drift. Observed on the live box: IDs `1, 21, 22, 23, …` where `marshal ls`
   shows an ID but `restart <id>` / `describe <id>` return `no app matching "<id>"` for
   any value the user reasonably guesses.

3. **No multi-target selectors.** `stop` / `restart` / `delete` / `reset` are declared
   `cobra.ExactArgs(1)`, so `marshal restart 2 3` is rejected and `2,3` is not parsed.

By-ID resolution itself is **not** broken ([`manager.go` `resolve`](../../../internal/manager/manager.go)
matches `strconv.Atoi(sel)` against `a.id`); the felt breakage is a symptom of (2).

## Scope

In scope (three independent chunks, shipped as three PRs off `dev`):

- **B2 — Stable, persistent IDs** (foundational; do first).
- **B1 — Multi-target selectors** for `stop` / `restart` / `delete` / `reset`.
- **A — `restart --update-env`** env reload.

Out of scope (deferred, noted for follow-up):

- Full-spec reload (cmd/args/cwd/instances/limits) — `--update-env` refreshes **env only**.
- Fleet/dashboard equivalents (`marshal fleet …`, dashboard buttons) for env reload — the
  first pass targets the **local daemon** CLI path only.
- Multi-target for `describe` / `logs` — single-target is fine there for now.
- Rolling / zero-downtime restart — `--update-env` uses the existing all-at-once restart.

## Current architecture (relevant pieces)

- `cmd/marshal/control.go`
  - `startCmd()` → `config.Load` (resolves `env_file` into `Env`) → `appToSpec` → `Start` RPC.
  - `selectorCmd(use, short, call)` builds `stop`/`restart`/`delete`/`reset`; `ExactArgs(1)`;
    `targetsFromArg` expands a `.yaml`/`.yml` path to app names, else returns `[arg]`.
- `internal/daemon/server.go` — `Start` → `doStart` → `mgr.Add`; mutations persist via
  `s.store.Save(s.mgr.Specs())`. `Resurrect` and boot-load also funnel through `mgr.Add`.
- `internal/manager/manager.go`
  - `Add`: `m.nextID++`; `ma := &managedApp{id: m.nextID, …}`; ignores any incoming id.
  - `Restart`: stop instances, then re-`startInstance` from `a.spec` (ID preserved).
  - `resolve`: `all` → integer id match → name match.
  - `Specs()`: returns each `a.spec` (for `store.Save`).
- `internal/config/config.go` — `App` struct; `EnvFile` is `yaml:"env_file" json:"-"`
  (loaded from YAML, merged into `Env`, not persisted).
- `internal/store/store.go` — `dump.json` is a JSON array of `config.App`;
  `Save`/`Load` round-trip whatever fields are JSON-tagged.
- `proto/marshal/v1/daemon.proto` — `Start/Stop/Restart/Delete/List/Describe/Save/
  Resurrect/Kill/Reset/Flush/…`. Regenerated into `internal/pb`.

---

## Feature B2 — Stable, persistent IDs

### Change

1. **`config.App` gains an internal ID field:**
   ```go
   // ID is the daemon-assigned stable identifier, persisted in dump.json and
   // reused across restarts/resurrect. It is never read from user YAML.
   ID int `yaml:"-" json:"id,omitempty"`
   ```
   `yaml:"-"` keeps it out of the user-facing config; `json:"id,omitempty"` persists it in
   `dump.json`. `omitempty` means pre-upgrade dumps (no `id`) unmarshal to `ID == 0`.

2. **`mgr.Add` honors a persisted ID, else assigns the next free one:**
   - If `app.ID > 0`: use it as `ma.id` (reuse across restart).
   - Else: `ma.id = maxExistingID + 1` (or `1` if none).
   - Always advance `m.nextID` to `max(m.nextID, ma.id)` so later runtime adds never collide.
   - Set `ma.spec.ID = ma.id` so `Specs()` → `Save` persists the assignment.
   - Duplicate-ID defense: if an incoming `app.ID` already exists in `m.apps` (corrupt dump),
     fall back to `maxExistingID + 1` rather than creating a collision.

3. **No `store` change** — `dump.json` already round-trips JSON-tagged fields.

### Automatic migration

Existing `dump.json` has no `id`. On the first daemon start after upgrade, every loaded app
has `ID == 0` → `Add` assigns contiguous **1..N** by load order → `Save` persists them.
Thereafter IDs are stable across restart/resurrect. The live box's sparse `1,21,22,…`
collapses to `1..12` on the next daemon restart and stays fixed. No manual step required.

### Consequences

- Gaps after `delete` are allowed (PM2-like): deleting ID 3 leaves a hole; the next new app
  takes `max+1`, not a backfilled 3. Stability across restarts is the goal, not compaction.
- Fleet/dashboard inherit stable IDs automatically (same `snapshotApp` ID).

### Tests (TDD)

- Add with `ID == 0` assigns 1, then 2, then 3 …
- Add with `ID == 5` reuses 5; a subsequent zero-ID add assigns 6 (advances `nextID`).
- Load(apps without id) → Add loop yields contiguous 1..N; `Specs()` now carries those IDs;
  a round-trip through `Save`/`Load` preserves them.
- Duplicate incoming ID falls back to `max+1` (no collision).
- `resolve("2")` finds the app whose persisted ID is 2 after a simulated restart.

---

## Feature B1 — Multi-target selectors

### Change

- `stop` / `restart` / `delete` / `reset`: `ExactArgs(1)` → `MinimumNArgs(1)`.
- Build the target list from **all** args: split each on commas, resolve each element via the
  existing `targetsFromArg` (name / ID / `.yaml` path), flatten, and de-duplicate preserving
  order. `all` anywhere short-circuits to the single `all` target.
- Loop the existing per-target selector RPC; aggregate the returned `ProcList` into one table.
- **No proto/daemon change** — purely CLI-side expansion.

### Not-found behavior

- **Multiple targets** (2+ resolved, or a config-file expansion): a target that errors
  (unknown name/ID, or not-running) prints `marshal: <target>: <err>` to stderr and the loop
  continues; the command exits non-zero if any target failed. This generalizes today's
  config-file "warn and keep going".
- **A single explicit target** still fails hard (unchanged behavior).

### Refactor note

`restart` needs both multi-target and the `--update-env` flag (Feature A), so it moves out of
the shared `selectorCmd` into its own `restartCmd()` builder. `stop` / `delete` / `reset` keep
using `selectorCmd`, which gains the multi-target loop.

### Tests (TDD)

- `restart 2 3` targets both; aggregated table has both rows.
- `delete 2,3` (comma) parses to two targets.
- `restart 2 99` with 99 unknown: 2 restarts, warning for 99, non-zero exit.
- `restart 2` (single, unknown) fails hard.
- `all` combined with other args resolves to just `all`.
- De-dup: `restart 2 2` acts once.

---

## Feature A — `restart --update-env`

### Change

1. **New daemon RPC** in `daemon.proto`:
   ```proto
   rpc UpdateEnv(UpdateEnvRequest) returns (ProcList);
   message UpdateEnvRequest { repeated AppSpec apps = 1; }  // only name + env are read
   ```
   Regenerate `internal/pb`.

2. **`manager.UpdateEnv(name string, env map[string]string)`**: find the app (error if
   absent), set `a.spec.Env = env`, then stop + start its instances exactly as `Restart`
   does. ID and restart history are preserved (same `managedApp`).

3. **`server.UpdateEnv` handler**: for each incoming `AppSpec`, call `mgr.UpdateEnv(name, env)`;
   collect updated snapshots; **skip** apps that aren't present (collect their names for a CLI
   warning); persist via `s.store.Save(s.mgr.Specs())`; return the aggregated `ProcList`.

4. **CLI** (`restartCmd()` from B1) gains `--update-env` (bool):
   - **Default (flag unset):** unchanged multi-target restart.
   - **Flag set:** the argument(s) MUST include a config file. `config.Load` re-resolves
     `env_file` → `env`; build `[]AppSpec{name, env}`; call `UpdateEnv`. Apps in the file that
     aren't running get a per-app warning (mirrors config-file restart).
   - **Validation:** `--update-env` with only bare name/ID selectors (no config file) errors:
     `marshal restart --update-env requires a marshal.yaml path (the daemon cannot re-read
     env without the config file)`.

### Why a new RPC (not delete + start)

Delete + start would reassign the ID and wipe restart history and renumber the app.
`UpdateEnv` mutates the stored spec in place, preserving identity — and with B2's stable IDs,
the app keeps the *same* ID before and after.

### Tests (TDD)

- `manager`: `UpdateEnv` swaps `Env`, restarts instances, preserves `id`.
- `server`: `UpdateEnv` persists the new env (`Load` reflects it) and skips unknown apps.
- `cmd`: `--update-env` with a bare name errors; with a config file calls `UpdateEnv`; an app
  in the file but not running yields a warning, not a hard failure.

---

## Data flow (Feature A)

```
marshal restart marshal.yaml --update-env
  │  CLI: config.Load() re-resolves env_file into a fresh env map
  ▼
UpdateEnv RPC { apps: [{name, env}, …] }
  ▼
daemon: for each app → mgr.UpdateEnv(name, env)
  │        a.spec.Env = env   (id + restart history preserved)
  │        stop instances → start instances (all-at-once)
  ▼
store.Save(mgr.Specs())        # persists new env (and stable id from B2)
  ▼
ProcList → CLI prints "restarted (env refreshed)"
```

## Verification / demo (per project conventions)

Local scratch demo (`XDG_DATA_HOME=/tmp/marshal-demo/...`, standard ports):

1. **Stable IDs:** start 3 demo apps → note IDs 1,2,3 → restart the daemon → confirm IDs are
   still 1,2,3 (pre-fix they would renumber).
2. **Multi-target:** `marshal restart 1 3` and `marshal delete 2,3`; confirm aggregated output
   and forgiving behavior with an unknown ID.
3. **Env reload:** app that prints an env var on boot → edit `env_file` → `marshal restart
   config.yaml --update-env` → confirm new value in logs and that the app's ID is unchanged.

Tear the demo down and confirm no orphans (`pgrep -fl marshal`).

## Rollout note for the live agent

After these ship and the box's binary is updated, the **first** `systemctl --user restart
marshal` will migrate `dump.json` to contiguous IDs `1..12` and they will stay stable
thereafter — resolving the original "can't restart by ID" complaint.
