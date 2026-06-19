# Marshal — M22: Marshal-managed git credentials — Handoff

**Date:** 2026-06-19
**Branch:** `m22-managed-git-credentials` (all work done, reviewed, live-demoed; ready to merge `--no-ff` to `main`).
**Read the M21 handoff `2026-06-19-m21-git-deploy.md` first** for the deploy architecture this builds on.

---

## TL;DR

A dashboard user can now store a git **HTTPS personal-access token** in Marshal and deploy
from a **private** repo without the agent host being git-authed. The token is stored **once on
the server**, encrypted at rest (AES-256-GCM), and **pushed to the chosen agent inside the
deploy/redeploy op** over the existing TLS fleet link. The agent injects it into `git
clone`/`fetch` via a throwaway **GIT_ASKPASS** helper — the token never appears in argv, the
clone URL, the per-app log, `dump.json`, or the audit log. The deploy persists only the
credential **name** (in `config.GitSource.Credential`); redeploy re-resolves it server-side, so
the server holds no app→credential mapping.

Built spec → plan → 9-task subagent-driven TDD (each task reviewed) → whole-branch review (opus,
"ready to merge") → live demo (caught + fixed one real bug). Spec:
`docs/superpowers/specs/2026-06-19-managed-git-credentials-design.md`. Plan:
`docs/superpowers/plans/2026-06-19-managed-git-credentials.md`.

## What changed this session

- **New package `internal/credstore`** — file-backed AES-256-GCM store (stdlib only).
  `Open/Put/Get/List/Delete`; `credentials.json` (0600, ciphertext+nonce only); master key from
  `MARSHAL_MASTER_KEY` (base64, 32 bytes) or auto-generated `<dataDir>/master.key` (0600).
  Atomic temp-write+rename flush; `Put` upserts (rotate); `List` returns metadata only.
- **Proto** (`daemon.proto`/`fleet.proto`): `GitSource.credential=5` (name), `ProcInfo.credential=12`,
  new `GitCredential{username,token}`, `DeployRequest.credential=2`, `RedeployRequest.credential=2`.
  All additive — an M21 agent/`dump.json` without credentials still works.
- **config**: `config.GitSource.Credential string` (name only; persisted in `dump.json`).
- **deploy**: `Runner.Run` gained an `env []string` param (`ExecRunner` appends to `os.Environ()`
  only when non-nil). `deploy.Credential{Username,Token}`; `Start(app, cred)`/`Redeploy(name, cred)`.
  `fetch` builds a temp **GIT_ASKPASS** script (0700, removed via defer), sets
  `GIT_ASKPASS/MARSHAL_GIT_USER/MARSHAL_GIT_TOKEN/GIT_TERMINAL_PROMPT=0`, and rewrites the clone
  URL to embed the **username only**. Build step gets **nil** env (no credential during build).
  **Inherited git credential helpers are disabled** (`-c credential.helper=`) on managed-credential
  git ops — see the bug below.
- **daemon**: `command.go` passes the proto credential to the deployer; `convert.go` maps the
  credential name into `config.GitSource`; `manager.InstanceSnapshot.Credential` + `snapshotApp`
  stamping; `snapshotToProc` sets `ProcInfo.Credential`.
- **dashboard**: new `credentials.go` — `Credentials` interface + `GET/POST /api/credentials`,
  `DELETE /api/credentials/{name}` (behind `requireSession`; 503 when the store is disabled).
  `apps.go` resolves a credential name → attaches `GitCredential` to deploy/redeploy (unknown →
  400); `fleet.go` `procView.Credential` (the M21-lesson DTO-drop guard). `handlers.go`/`server.go`
  thread `creds`; `internal/server/server.go` `ServeDir` opens credstore (typed-nil handled:
  feature disabled if `Open` fails, ordinary deploys still work).
- **web**: `api.ts` `CredentialMeta` + `listCredentials/createCredential/deleteCredential`,
  `GitSource.credential`, `redeploy(agent,name,credential?)`, `Proc.credential`. `AddAppModal`
  git-mode credential `<select>`. New `Credentials.tsx` (list/add/delete; token is write-only
  `type=password`). `ProcessCard` passes `proc.credential` on redeploy. Wired into `App`/`router`.

## Key decisions / non-obvious

- **Server-side store, pushed per-deploy** (matches server→agents topology). Secret transits the
  (already TLS) fleet link and lives in agent memory only for one clone/fetch.
- **HTTPS token only** this cut; **SSH deploy keys deferred** (own milestone).
- **GIT_ASKPASS + username-in-URL**, never URL-rewrite-with-token (would hit the per-app log).
- **Global, named credentials**, usable by any authenticated dashboard user (single-shared-admin
  model). Credential **name** rides the fleet snapshot so redeploy is server-stateless.
- **Credential audit via `log.Printf`** (name+username, never token) — the `audit.Log` type is
  login-specific (Time/User/IP/Outcome), so credential ops follow the M21 `dispatchApp` pattern.

## Live demo result (2026-06-19, scratch `/tmp/marshal-m22-demo`, server `:9000`/`:9001`)

Real fleet (server + agent `dev-1`) against a **local auth-required dumb-HTTP git remote**
(`octocat` / a Basic-Auth token; clone provably fails without creds):
- Created credential `gh-demo` → `201`; `GET /api/credentials` returned **no token**;
  `credentials.json` held only ciphertext+nonce; `master.key` was `0600`.
- Deployed the private repo selecting `gh-demo` → `cloning → online`; auth server logged
  **authenticated 200s** (the managed token drove the clone); **token absent from the per-app
  log and from the entire agent data dir**; fleet showed `privapp ... credential=gh-demo`.
- Rotated the token (+ the remote's accepted token) → redeploy fetched **v2** and restarted on it.
- `DELETE` → `204`, list empty, `credentials.json` `{}`; re-delete `404`; unauth `401`.
- Teardown clean; no orphan `marshal` processes (only the user's launchd daemon remained).

## Bug found & fixed by the live demo

**Inherited git credential helper replayed a stale token.** On a host with
`credential.helper=osxkeychain` (default on macOS; `libsecret`/`store` on Linux), the initial
clone **cached** the managed token in the OS keychain. After a token rotation, the helper
replayed the **old** token on redeploy *before* GIT_ASKPASS ran → `fatal: Authentication
failed`. Fix (commit `2c56f79`): run managed-credential git ops with `-c credential.helper=`
(empties the helper list) so the askpass token is authoritative and nothing is cached. Re-verified
live. **Lesson echoing M21:** the unit tests + whole-branch review all passed; only the real
end-to-end demo surfaced this.

## Known issues / deferred

- **Failed redeploy leaves a lingering `failed` deployer state** that blocks re-redeploy until the
  app is deleted/dismissed (pre-existing M21 redeploy behavior; the demo hit it once). Minor;
  consider auto-clearing a redeploy `failed` entry after a TTL or on next redeploy.
- **SSH deploy keys** — next credential-type milestone (key-file lifecycle, `GIT_SSH_COMMAND`,
  `known_hosts`).
- **Master-key rotation / re-encryption** of the whole store is not implemented.
- **Per-agent / per-user credential scoping** — deferred (no multi-tenant model yet).
- Minor review nits deferred (all non-blocking, in `.git/sdd/progress.md`): test reimplements
  `bytes.Contains`; a couple of untested error branches (`h.creds==nil` deploy path,
  `TestNoCredentialNoAskpass` nil-guard); web `Credentials.tsx refresh` not memoized;
  `deleteCredential` doesn't parse a server error body.
- **`deploy.Credential` is not redaction-safe under `%+v`** — no code prints it today, but a future
  `log.Printf("%+v", cred)` would leak; consider a redacting `String()`.

## How to build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1     # all 23 packages green
gofmt -l . ; go vet ./...        # silent / clean
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
```

## Concrete next step

1. **Merge `m22-managed-git-credentials` to `main`** (`--no-ff`, no remote) via
   `finishing-a-development-branch`.
2. Then a follow-up could be **SSH deploy keys** (the next credential type), or the deferred
   dashboard **file manager + in-browser editor** the user flagged.
