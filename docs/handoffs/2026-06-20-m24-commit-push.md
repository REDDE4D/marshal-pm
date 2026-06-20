# Marshal — M24: Commit & push (write half of the file browser) — Handoff

**Date:** 2026-06-20
**Branch:** `m24-commit-push` (all work done, reviewed, live-demoed; ready to merge `--no-ff` to `main`).
**Read the M23 handoff `2026-06-19-m23-file-browser.md` first** (the read half this builds on) and the M22 handoff `2026-06-19-m22-managed-git-credentials.md` (the credential/push mechanism reused here).

---

## TL;DR

A dashboard user can now **edit, create, delete, and rename** files of a git-deployed app. Each
operation is **its own commit, pushed to origin** (per-operation atomic) using the app's M22-managed
credential. Because redeploy does `git reset --hard FETCH_HEAD`, **pushing is what makes an edit
durable** — that's the milestone. The per-app Files card's CodeMirror viewer is now **editable**
(text, non-oversize files), with **Save & push** (editable commit message), **New file**, and per-row
**Rename**/**Delete** (delete behind a confirm). Commit+push is **decoupled from redeploy**: a pushed
edit is applied to the running process only on the next **Redeploy**.

Security core is the heart: `confineNew` confines not-yet-existing create/rename paths (defeats
symlinked-parent escape); a `.git/` guard blocks clone-corruption; a branch gate rejects detached-HEAD
deployments; staging is **targeted** (`git add -- <path>`, never `-A`) so build artifacts never get
committed; the token rides only `GIT_ASKPASS` (never argv/URL/log/audit); and **every failure rolls
back transactionally** (`git reset --hard <preSHA>` + surgical leftover removal) so the working tree
always matches origin. **No force-push, ever.**

Reuses the M23 seam — **no new RPC, no new connection**: one additive `ControlOp` variant
(`commit=9`) + one additive `ControlResult` payload (`commit=6`). An M21/M22/M23 agent without it
still works.

Built spec → plan → 8-task subagent-driven TDD (each task reviewed) → whole-branch review (opus,
"merge with fixes") → fixes (caught a real bug, see below) → re-review (approved) → live demo. Spec:
`docs/superpowers/specs/2026-06-20-commit-push-design.md`. Plan:
`docs/superpowers/plans/2026-06-20-commit-push.md`.

## What changed this session

- **Proto** (`proto/marshal/v1/fleet.proto`): `enum CommitKind {EDIT=0,CREATE=1,DELETE=2,RENAME=3}`;
  `CommitRequest{app,kind,path,new_path,content,message,credential}`; `CommitResult{sha,branch}`.
  `ControlOp` += `commit=9`; `ControlResult` += `commit=6`. All additive. Regen via
  `go generate ./internal/pb`.
- **`internal/deploy/mutate.go`** (new) — the security-critical core:
  - `confineNew(root, rel)`: like M23's `confine`, but resolves the **deepest existing ancestor**
    via `EvalSymlinks` and re-checks containment, so a create/rename **destination** that doesn't
    exist yet can't escape via a symlinked parent. (M23's `confine` rejects non-existent paths, so it
    can't validate a new path — hence the sibling.) `confine` still used for edit/delete (must exist).
  - `isGitInternal(rel)`: rejects any path whose first component is `.git`.
  - `(*Deployer).mutateAndPush(dir, src, cred, kind, rel, newRel, content, message)`: branch gate
    (`git symbolic-ref -q --short HEAD`, detached → reject) → `.git` guard → capture `preSHA`
    (`rev-parse HEAD`) → apply FS change + **targeted** stage (`add -- rel` / `rm -- rel` /
    `mv -- rel newRel`) → inline-identity commit (`-c credential.helper= -c user.name= -c user.email=`)
    → push (`-c credential.helper= push <pushURL> HEAD:refs/heads/<branch>`, env from M22
    `gitCredEnv`; **no --force**) → on any failure `reset --hard preSHA` + `os.Remove` the one
    create/rename untracked leftover (no `git clean -fd`). `pushURL` = `origin` with no credential,
    `withUsername(src.Repo, cred.Username)` when credentialed. `gitIdentity` derives author from the
    credential username (`<user>` / `<user>@marshal.local`), fallback `Marshal`/`marshal@localhost`.
- **`internal/deploy/deployer.go`**: `(*Deployer).Commit(name, kind, rel, newRel, content, message,
  cred)` — resolves `host.Source(name)` + `Root(name)` (rejects unknown/non-git apps), takes the
  per-app `states` lock to be **mutually exclusive with deploy/redeploy in both directions** (sets a
  transient `phaseCommitting`, `defer clearState`), delegates to `mutateAndPush`. `phaseCommitting`
  is filtered out of `Snapshots()` so a brief write shows no phantom proc.
- **`internal/daemon/command.go`**: `case *pb.ControlOp_Commit:` after `Redeploy` — nil deployer →
  "deploy not supported"; maps the proto credential → `deploy.Credential`; calls `Commit`; returns
  `ControlResult{Ok, Commit}`.
- **`internal/dashboard/files.go`**: `PUT /api/fleet/{agent}/apps/{app}/file?path=[&create=1]` →
  edit (or **create** when `create=1` → `COMMIT_CREATE`); `DELETE .../file?path=` → delete;
  `POST .../apps/{app}/rename` (body `{from,to,message,credential}`) → rename. All behind
  `requireSession`. Each enforces a **1 MiB** content cap, rejects an **empty `path`** with 400,
  resolves the credential name via the existing `resolveCredential` (unknown → 400), and audit-logs
  agent/app/kind/path/sha/branch — **never the token**. Error mapping reuses M23's shared
  `fileControl`: transport → 503, op-rejected → 400, success → 200 `{sha, branch}`.
- **`internal/dashboard/handlers.go`**: registers the three new method-qualified routes after the
  M23 `GET .../file` route (Go 1.22 method patterns coexist on the same path).
- **web** (`web/`): `api.ts` `CommitResult` + `writeFile`/`createFile`/`deleteFile`/`renameFile`
  (`createFile` = PUT with `&create=1`). **`FileBrowser.tsx`** rewritten: CodeMirror **editable** for
  text, non-oversize files (truncated 1-MiB heads stay read-only — saving a head would truncate the
  file); **Save & push** with an editable commit-message field; **New file** (toolbar); per-row
  **Rename**/**Delete** (delete confirm); "Pushed `<sha>` to `<branch>`" toast; reactive error
  banner; a `credential` prop threaded into all write calls. `ProcessDetail.tsx` passes
  `credential={p?.credential}` (and gained `credential?` on its local proc type). `make ui` rebuilt
  the embedded `internal/dashboard/dist`.

## Key decisions / non-obvious

- **Per-operation atomic commit+push** (not staged batches): the working tree is always clean and
  matches origin, so a redeploy's `reset --hard` never silently wipes uncommitted edits.
- **Commit+push only; redeploy stays separate** — no surprise rebuild/restart on every save. The UI
  banner says "Redeploy to apply changes to the running app."
- **Transactional rollback** chosen over leaving committed-but-unpushed state: what the dashboard
  shows always equals what's durable on origin.
- **Author derives from the credential**, fallback fixed `Marshal`/`marshal@localhost`, set **inline**
  (`-c user.name/-c user.email`) so the clone's git config is never mutated.
- **Writes allowed only on a branch.** A tag/SHA deployment is detached-HEAD → rejected (you can't
  meaningfully push). Branch deployments stay on their branch across redeploys (`reset --hard
  FETCH_HEAD` moves the branch ref, doesn't detach), so they remain writable.
- **UI writability is reactive** (a deliberate, documented divergence from spec §8's "proactive
  read-only banner"): the editor is editable for text/non-oversize files, and a not-a-branch /
  push-rejected attempt surfaces the honest 400 banner with the tree rolled back. This keeps the M23
  read path (`browse.go`) git-free (no `git symbolic-ref` per listing). Flagged for the reviewer; if
  proactive gating is wanted later, add a cheap `writable` flag to the dir listing.

## Bug found & fixed (by the whole-branch review)

The opus whole-branch review caught a real one: **"New file" was wired to the edit path.** `onNewFile`
called `writeFile` → `PUT` → handler hardcoded `COMMIT_EDIT`, and the agent's EDIT branch uses
`confine` (rejects non-existent paths), so every create failed "not found" — the implemented-and-
tested `COMMIT_CREATE` core was unreachable from the UI. Every green test missed it because core tests
exercised `COMMIT_CREATE` directly and handler tests used a fake controller that never runs git. Fix
(`d4f6b68`): web `createFile` → `PUT …&create=1`; handler maps `create=1` → `COMMIT_CREATE`; added
`TestCreateFileEndpoint` + the integration lock `TestHandleFleetCommand_CommitCreate` (real clone,
brand-new path, through daemon→deploy→push) + empty-path 400 tests. `c3d07d6` makes the audit-log
label honest (create vs edit). **Lesson echoing M21/M22:** the end-to-end review/demo caught what
unit tests structurally could not.

## Live demo result (2026-06-20, scratch `/tmp/marshal-m24-demo`, server `:9000`/`:9001`)

Real fleet (server + agent `dev-1`) against a **bare `file://` remote**; deployed `browseapp` from it
on branch `main` (`source=git`, `online`). All via the dashboard HTTPS API with a session cookie:
- **Edit** `README.md` → `{branch:main, sha:0671856}`; remote `main:README.md` = `edited via dashboard`.
- **Create** `src/new.txt` (`create=1`) → `{sha:ad77238}`; on origin with the right content.
- **Delete** `src/app.txt` → `{sha:0ebf357}`; gone from origin.
- **Rename** `run.sh` → `start.sh` → `{sha:1ef3733}`; old path gone, new path present on origin.
- Remote log shows all four commits authored `Marshal <marshal@localhost>` (fallback identity; no
  credential needed for `file://`).
- **Redeploy preserves the edit**: after `POST /api/apps/redeploy`, the dashboard read of `README.md`
  still returns `edited via dashboard` (the pushed commit survived `reset --hard FETCH_HEAD`).
- **Path escape** `../../../../etc/passwd` → **400 `path escapes deploy root`**; **`.git/config` edit**
  → **400 `cannot modify .git`**.
- **Rollback**: advanced origin out-of-band from a second clone, then an edit → **400 `push rejected
  (origin moved or credential lacks write access)`**; the agent clone HEAD was **unchanged** (rolled
  back), `README.md` intact, `git status` clean (no residue).
- **UI shipped**: the dashboard serves the rebuilt bundle containing `Save & push`, `New file`,
  `Commit message`, `Redeploy to apply`, `Pushed …` — the editable Files card is live.
- Teardown by data dir only; the user's standing launchd daemon (pid 3119) was preserved; no orphan
  demo processes; scratch dir removed.
- (Note: a rendered in-browser **screenshot** was not captured this run — no Playwright/Chrome
  available locally — but the served bundle was confirmed to contain the editable UI and every
  endpoint it calls was verified end-to-end.)

## Known issues / deferred

- **Credential-on-push not exercised live**: `file://` ignores credentials, so the managed-token push
  path (`gitCredEnv` + `withUsername` + `-c credential.helper=`) was demoed only via the no-credential
  branch. It is covered by unit tests (push-URL/env construction) and reuses M22's already-demoed
  mechanism verbatim. A future demo against an **auth-required HTTP remote** would close this.
- **Proactive writability gating** deferred (see the reactive-UI decision above).
- **Multi-file (changeset) commits**, **branch creation/switching**, **PR creation**, **conflict/merge
  UI**, and **explicit empty-directory create/delete** are out of scope (a deep create path implicitly
  `MkdirAll`s).
- **Rename destination may contain `/`** (a cross-directory move) — safely confined by `confineNew`,
  but the UI doesn't restrict it; intended as a move, just undocumented in the prompt.
- Minor (logged in `.superpowers/sdd/progress.md` for triage): `confineNew` has one benign redundant
  loop guard; `ProcessDetail` uses a hand-written partial `Proc` type (drift-prone); `📁/📄` emojis are
  pre-existing from M23; `TestDeleteFileEndpoint` asserts kind but not path/app.
- M23 carry-overs unchanged: `.git/` still **listable** (just not writable); 404-vs-400 mapping;
  CodeMirror bundle size.

## How to build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1     # all packages green
gofmt -l . ; go vet ./...        # silent / clean
go generate ./internal/pb        # regenerate proto bindings (protoc + plugins on PATH)
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
```

Endpoints (behind dashboard session): `PUT /api/fleet/{agent}/apps/{app}/file?path=<rel>[&create=1]`,
`DELETE /api/fleet/{agent}/apps/{app}/file?path=<rel>`, `POST /api/fleet/{agent}/apps/{app}/rename`.

## Concrete next step

1. **Merge `m24-commit-push` to `main`** (`--no-ff`, no remote) via
   `superpowers:finishing-a-development-branch`.
2. Then: **SSH deploy keys** (the other deferred M22 follow-on — key lifecycle, `GIT_SSH_COMMAND`,
   `known_hosts`), or an **auth-required-remote demo** to close the credential-on-push gap, or
   **proactive UI writability** + the minor cleanups above.
