# Marshal — M24: Commit & push (the write half of the file browser) — Design

**Date:** 2026-06-20
**Status:** Approved (brainstorming complete), ready for an implementation plan.
**Builds on:** M21 git-deploy, M22 managed git credentials, M23 read-only file browser.
Read those specs/handoffs first — this milestone reuses their deploy/credential/browse seams
wholesale.

---

## 1. Goal

A dashboard user can **edit, create, delete, and rename** files of a git-deployed app and have
each change **committed and pushed to origin** using the app's M22-managed credential. Because
redeploy does `git reset --hard FETCH_HEAD`, an edit is only durable once it is on origin —
**pushing is the whole point of this milestone**. M23 is the read half; this is the write half.

Applying a pushed change to the *running* process remains the existing **Redeploy** action — see
the decisions below.

## 2. Decisions (from brainstorming)

1. **Scope:** full CRUD — edit / create / delete / rename files. (No multi-file commits.)
2. **Commit model:** **per-operation atomic commit + push.** Every operation is its own commit,
   pushed immediately. The working tree is always clean and matches origin — nothing for a
   redeploy to silently wipe. (Rejected: staged batch commits, which reintroduce divergent
   working-tree state.)
3. **Apply to running app:** **commit + push only.** The running process is untouched until the
   user clicks the existing **Redeploy** button. No surprise rebuild/restart on every save.
4. **Commit author:** **derived from the credential** — `user.name = <credential username>`,
   `user.email = <username>@marshal.local`. Falls back to a fixed identity
   (`Marshal` / `marshal@localhost`) when the app has no credential. Set **inline** with
   `-c user.name=… -c user.email=…`; never mutate the clone's git config.
5. **Commit message:** **auto default, user-editable on save.** The editor's Save dialog shows an
   editable field pre-filled with a sensible default (`Update <path>`). Create/delete/rename use
   their auto defaults silently (`Create <path>`, `Delete <path>`, `Rename <from> → <to>`).
6. **Push failure:** **transactional rollback.** Capture HEAD before the operation; on any
   commit/push failure, `git reset --hard <preSHA>` (plus surgical removal of a create/rename
   leftover) so the visible working tree always matches what is durable on origin. **Never
   force-push.**

## 3. Architecture overview

Reuses the M23 seam end to end — **no new RPC, no new connection.** One additive proto op carries
a CRUD mutation from dashboard → server → agent; the agent performs the git work in the app's clone
root and pushes to origin with the M22 credential.

```
Browser (FileBrowser.tsx)
  └─ PUT/DELETE .../file , POST .../rename  (JSON, requireSession)
       └─ dashboard files.go: resolve credential name → GitCredential, build CommitRequest
            └─ controller.Control(agent, ControlOp_Commit)   [existing fleet TLS link]
                 └─ daemon command.go: case ControlOp_Commit
                      └─ deploy.Deployer.Commit(...)  [concurrency guard + src/cred resolution]
                           └─ deploy/mutate.go: confine → apply FS change → git add/rm/mv
                                → inline-identity commit → push → transactional rollback on failure
```

## 4. The mutation core — `internal/deploy/mutate.go` (security-critical)

### 4.1 Path confinement for writes — `confineNew`

M23's `confine` calls `filepath.EvalSymlinks(full)`, which **errors on a non-existent path** — so
it cannot validate a create or rename **destination**. Add a sibling:

- `confineNew(root, rel) (string, error)` — lexical containment check (as `confine`), then resolve
  the **deepest existing ancestor** with `EvalSymlinks`, verify *that* stays inside the real root,
  then re-append the not-yet-existing tail. Closes the symlinked-parent-dir escape vector for new
  paths. Returns generic, non-path-leaking errors, exactly like `confine`.

`edit` and `delete` keep using the existing `confine` (the target must already exist). `create` and
`rename`-destination use `confineNew`.

### 4.2 `.git/` guard

Reject any operation whose confined relative path has `.git` as its first path component
(`cannot modify .git`). Mutating `.git/config`, `.git/HEAD`, etc. would corrupt the clone.

### 4.3 Branch gate

Commit+push only makes sense onto a branch. Run `git symbolic-ref -q --short HEAD`; if HEAD is
detached (app deployed from a tag or a raw commit SHA), reject with
`deployment is not on a branch (read-only)`. A normal branch deployment stays on its branch across
redeploys (`reset --hard FETCH_HEAD` moves the branch ref, it does not detach), so it stays
writable.

### 4.4 `Commit` flow (`kind ∈ {edit, create, delete, rename}`)

1. Resolve target path(s) via `confine` / `confineNew`; apply the `.git/` guard; resolve the branch
   (gate). (Binary/oversize *editability* is gated in the UI and bounded by the dashboard's content
   cap — the core writes the client-supplied bytes; it does not re-sniff the prior file.)
2. **Capture `preSHA` = `git rev-parse HEAD`** *before* touching the working tree.
3. Apply the filesystem change and stage **only the affected path(s)** — never `git add -A`/`add .`
   (that would sweep the untracked build binary and other artifacts into the commit):
   - `edit`:   write content → `git add -- <path>`
   - `create`: `confineNew` → `MkdirAll` parent → write content → `git add -- <path>`
   - `delete`: `git rm -- <path>`
   - `rename`: `confineNew` dest → `git mv -- <from> <to>`
4. Commit with inline identity (never mutates clone config):
   `git -c credential.helper= -c user.name=<author> -c user.email=<email> commit -m <message>`.
5. Push (no `--force`, ever):
   `git -c credential.helper= push <withUsername(repo, cred.Username)> HEAD:refs/heads/<branch>`
   with the `GIT_ASKPASS` env from M22's `gitCredEnv` (token via env, never argv/URL). Pushing to
   an explicit `withUsername` URL (rather than `origin`) means the op works even when the app was
   originally cloned without a credential.
6. **Transactional rollback** on any failure in steps 3–5: `git reset --hard <preSHA>`, then
   `os.Remove` the single untracked leftover a `create`/`rename`-dest leaves behind. **No
   `git clean -fd`** — it would delete unrelated untracked build artifacts.
7. On success, return `*pb.CommitResult{Sha, Branch}`.

`withUsername`, `gitCredEnv`, and `gitArgs` (the `-c credential.helper=` wrapper) are reused
unchanged from M22's `deployer.go`.

### 4.5 Concurrency guard

A write must never race a deploy/redeploy (which does `reset --hard`). The `Commit` path uses the
deployer's existing per-app state lock (`d.mu` + `d.states`): refuse if the app is mid-deploy
(`app is deploying`), and mark a transient busy state for the duration of the git work so a
concurrent redeploy is refused too. Clear it (success or failure) when done.

## 5. Proto seam — `internal/pb` (all additive)

```proto
enum CommitKind { COMMIT_EDIT = 0; COMMIT_CREATE = 1; COMMIT_DELETE = 2; COMMIT_RENAME = 3; }

message CommitRequest {
  string app      = 1;
  CommitKind kind = 2;
  string path     = 3;   // target (edit/create/delete) or source (rename)
  string new_path = 4;   // rename destination only
  bytes  content  = 5;   // edit/create only
  string message  = 6;   // commit message (server fills the default)
  GitCredential credential = 7;
}
message CommitResult { string sha = 1; string branch = 2; }
```

`ControlOp` gains `CommitRequest commit = 9;`. `ControlResult` gains `CommitResult commit = 6;`.
Regenerate with `go generate ./internal/pb`. An older agent ignores the unknown op and returns the
default `unknown op type` error — additive and backward-compatible.

## 6. Daemon wiring — `internal/daemon/command.go`

New `case *pb.ControlOp_Commit:` mirroring the M23 `ListDir`/`ReadFile` cases:

- nil deployer → `ControlResult{Ok:false, Error:"deploy not supported"}`.
- Map the proto credential to `deploy.Credential`; call
  `s.deployer.Commit(app, kind, path, newPath, content, message, cred)`.
- Success → `ControlResult{Ok:true, Commit: res}`; rejection → `ControlResult{Ok:false, Error}`.

`Deployer.Commit(...)` is the thin public method that — exactly like M22's `Redeploy` — resolves
the dir (`d.dir(name)`, rejecting unknown apps / non-deployments) and the persisted git source
(`d.host.Source(name)` for repo/ref) **itself**, applies the concurrency guard, and delegates to the
`mutate.go` core. The daemon does not pre-resolve the source.

## 7. Dashboard endpoints — `internal/dashboard/files.go` (behind `requireSession`)

Mapped onto the M23 `file` resource by HTTP method (registered in `handlers.go`):

- `PUT /api/fleet/{agent}/apps/{app}/file?path=` — body `{content, message, credential}` →
  edit, or **create** when the leaf does not exist.
- `DELETE /api/fleet/{agent}/apps/{app}/file?path=` — body `{message, credential}` → delete.
- `POST /api/fleet/{agent}/apps/{app}/rename` — body `{from, to, message, credential}` → rename.

Each handler:
- Enforces the existing **1 MiB** content cap before dispatch.
- Resolves `credential` (name → `*pb.GitCredential` via the existing `resolveCredential`; unknown →
  400) and builds a `CommitRequest`.
- Dispatches via the shared `fileControl` helper; returns JSON `{sha, branch}` on success.
- **Error mapping (reuses M23):** transport → 503; op-rejected (confine escape, `.git/`,
  not-a-branch, push rejected, binary, unknown credential) → 400; success → 200.
- **Audit** per the M22 `log.Printf` pattern: agent + app + kind + path + sha — **never the token.**

DTOs are dashboard-owned JSON (the M21 DTO-drop guard); no raw `pb` message is serialized.

## 8. Web UI — `web/` (`FileBrowser.tsx`, `api.ts`)

- The CodeMirror viewer becomes **editable** when the app is writable: on a branch, and the file is
  text and not oversize. A **Save** button opens a small commit-message field pre-filled with the
  auto-default (`Update <path>`), editable → `PUT .../file`.
- Tree toolbar gains **New file** (prompt for a path → create). Each entry gets **Rename** and
  **Delete** actions; delete is behind a confirmation (destructive-action convention). Create /
  delete / rename use their auto messages silently.
- On success: a toast `Pushed <sha> to <branch>`, then refresh the listing / file.
- When not writable (detached HEAD, or the push is rejected for missing/insufficient credential),
  the editor stays **read-only** with an honest banner explaining why — no mystery disabled buttons.
- Binary / oversize files remain download-only (unchanged from M23).
- `api.ts` gains `writeFile` / `deleteFile` / `renameFile` and a `CommitResult` type.
- `make ui` rebuilds the embedded `internal/dashboard/dist` (tracked).

## 9. Error handling & security summary

- Generic, non-path-leaking errors from the core (M23 discipline).
- Layered path confinement: `Root` rejects app-name separators; the ServeMux `{app}` segment cannot
  span `/`; `confine`/`confineNew` do lexical + symlink-resolved containment; `.git/` guard on top.
- Transactional rollback guarantees the visible tree always matches origin; **no `--force`**.
- Targeted staging (`git add -- <path>` / `git rm` / `git mv`) — never `-A` — so build artifacts are
  never committed.
- Token handling unchanged from M22: `GIT_ASKPASS` env only; never in argv, URL, per-app log,
  `dump.json`, or the audit log. Inherited credential helpers disabled (`-c credential.helper=`).
- Concurrency guard serializes writes against deploy/redeploy.

## 10. Testing plan (TDD, per task)

- **`mutate_test.go` (centerpiece):** `confineNew` escape vectors (symlinked parent, `..` in tail,
  absolute path); `.git/` rejection; each kind's happy path against a **local `file://` remote**
  asserting the commit lands on origin; push-rejection → rollback leaves the tree at `preSHA` with
  no leftover and nothing on origin; detached-HEAD rejection; oversize edit rejection (content cap
  at the dashboard); targeted-staging (an untracked build artifact stays uncommitted after an edit commit); author
  identity derivation + fallback.
- **Daemon case tests:** mirror the M23 `ListDir`/`ReadFile` tests (nil deployer, unknown app,
  success payload).
- **Dashboard handler tests:** method routing, credential resolution (unknown → 400), 1 MiB cap,
  error mapping (503 / 400 / 200).
- Full `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (silent) before finishing.
- **Live demo** (per project convention): real fleet + auth-required `file://` remote on
  `:9000`/`:9001`; exercise edit/create/delete/rename end to end in the dashboard; verify the
  commit reaches origin, a subsequent **Redeploy** preserves the edit, push without a write-scoped
  credential rolls back cleanly, and the token never appears in any log or the agent data dir. Tear
  down by data dir (preserve the user's standing launchd daemon); confirm no orphan processes.

## 11. Out of scope / deferred

- Multi-file (changeset) commits — explicitly rejected (per-operation atomic).
- Auto-redeploy on save — rejected (commit+push only).
- Branch creation / switching, PR creation, conflict resolution / merge UI.
- Directory create/delete as first-class operations (a create with a deep path implicitly
  `MkdirAll`s; an explicit empty-dir or recursive-dir op is deferred).
- SSH deploy keys (separate M22 follow-on, unchanged by this milestone).
- M23 carry-overs unchanged: `.git/` is still *listable* (just not writable); 404-vs-400 mapping;
  CodeMirror bundle size.

## 12. Concrete next step

Invoke `writing-plans` to produce the task-by-task implementation plan
(`docs/superpowers/plans/2026-06-20-commit-push.md`), then execute it subagent-driven-TDD as in
M21–M23.
