# Marshal — M22 kickoff: Marshal-managed git credentials — Handoff

**Date:** 2026-06-19
**Status:** NOT STARTED — this is a kickoff brief for a fresh session to begin the milestone.
**Branch:** none yet (start one: `m22-managed-git-credentials`).
**Read `2026-06-19-m21-git-deploy.md` first** for the full git-deploy context this builds on.

---

## What this milestone is

Add **Marshal-managed git credentials** so the dashboard can deploy from **private repos**
without relying on the agent host already being git-authed. Today (after M21) deploy auth is
purely the agent host's own git setup (SSH keys / credential helper / token-in-URL). This
milestone lets a user store a credential **in Marshal** and have the agent inject it into git's
environment per-deploy.

This was explicitly split out of M21 as the deliberate next step (host-creds first, managed-creds
next) because it is a sizable sub-project on its own: secret storage, encryption-at-rest, a
credentials UI, and per-deploy git-env injection.

## Where it plugs in (the seam already exists)

M21 built the deploy path so this layers on **additively**, no rework:

- The agent's `internal/deploy` package runs `git` via an injected `Runner`
  (`deploy.ExecRunner` in `internal/deploy/exec_runner.go`) — `exec.CommandContext` with a `dir`.
  Credential injection happens by setting the command's **environment / git config** before clone
  and fetch (e.g. `GIT_ASKPASS`, a `credential.helper`, `GIT_SSH_COMMAND` pointing at a key file,
  or a token rewritten into the remote URL). The `Runner` interface is the choke point to thread
  credentials through.
- `internal/deploy/deployer.go` `fetch()` issues `git clone` / `git fetch` — the place a
  per-deploy credential context must be active.
- `config.GitSource{repo,ref,build,subdir}` (`internal/config/config.go`) is the spec a deploy
  carries — a credential **reference** (e.g. a named credential id) would be added here, NOT the
  secret itself.
- Dashboard deploy entry: `POST /api/apps` git branch in `internal/dashboard/apps.go`
  (`deployOp` / `gitSource`). A credential selector would be added to the request + modal.

## Open design questions to resolve in brainstorming (do NOT skip brainstorm → spec → plan)

1. **Credential types**: HTTPS token (PAT) only first, or also SSH deploy keys? (Token-only is the
   smaller first cut; SSH key adds key-file handling + `GIT_SSH_COMMAND`.)
2. **Where secrets live**: on the **server** (central, one store, pushed to agents per-deploy) or on
   each **agent** (local to the host that uses them)? Marshal is server→agents; a server-side store
   that ships the secret to the agent inside the deploy command is the natural fit but means the
   secret transits the (TLS) fleet link and lives in agent memory during a deploy.
3. **Encryption-at-rest**: how is the store key derived/held (a master key from env/file, OS
   keychain, age/nacl secretbox)? This is the crux of "managed" vs. the M21 host-creds model.
4. **Injection mechanism**: `GIT_ASKPASS` helper script vs. ephemeral `credential.helper` vs.
   rewriting the token into the clone URL vs. `GIT_SSH_COMMAND` for keys. Must avoid leaking the
   secret into logs (M21 pipes clone/build output to the per-app log — a token in a URL would be
   logged; prefer askpass/helper that keeps it out of argv and output).
5. **UI/CRUD**: add/list/delete credentials in the dashboard; scoping (per-agent? global?);
   selecting one when deploying. Plus the audit-log treatment (don't log secret values).
6. **Auth model**: only an authenticated dashboard user can create/use credentials; confirm the
   trust boundary (a credential lets you clone private code on an agent you already control).

## Constraints carried from M21

- TDD, small focused packages; `go test ./... -race -count=1` green; `gofmt -l .` silent;
  `go vet ./...` clean before finishing.
- Feature work on a branch; co-author trailer `Co-Authored-By: Claude Opus 4.8 (1M context)
  <noreply@anthropic.com>`.
- No git remote (local-only repo) — merge `--no-ff` to `main` at the end.
- Web client has no test framework — verify with `make ui` (`tsc -b`) + a live demo.
- **Live demo on standard ports `:9000`/`:9001`** (Vite proxy target); set auth while the server is
  **down**; tear down + confirm no orphan `marshal` processes after.
- **Never log secret values.** Keep tokens/keys out of argv, audit lines, and the per-app log.

## How to build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l . ; go vet ./...
make ui
```

## Concrete next step (for the new session)

1. Read `docs/handoffs/2026-06-19-m21-git-deploy.md` (deploy architecture) + the M21 spec
   `docs/superpowers/specs/2026-06-19-git-deploy-design.md`.
2. **Brainstorm** the managed-credentials milestone (use the brainstorming skill) — resolve the open
   questions above, especially store location + encryption + injection mechanism + credential type
   scope. Then spec → plan → subagent-driven TDD → review → live demo, same as M21.
3. Do NOT start coding before the design is approved and written to
   `docs/superpowers/specs/YYYY-MM-DD-managed-git-credentials-design.md`.

## Further-future (not this milestone)

- Dashboard **file manager + in-browser editor** (browse/edit a deployed app's files) — flagged by
  the user; also saved as an auto-memory.
