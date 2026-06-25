# Handoff — Security / concurrency / leak review + hardening (2026-06-25)

## Current state

- Branch: **`fix/security-hardening-review`** off `dev` (NOT merged, NOT pushed).
- Two commits on top of `ae47f80` (v0.3.0):
  - `c97f42e` fix(security): harden git deploy inputs, dump.json perms, registry memory bound
  - `cc44bb1` feat(server): audit gRPC auth failures (#3, audit-only)
- `go test ./... -race -count=1` → **all 27 packages pass**, no data races. `gofmt`/`go vet` clean.
- Live demo (server + scratch agent) **PASS** (see below). Scratch torn down; only the user's
  standing launchd daemon (`clock`) remains.

## What this session was

An in-depth review focused on **security, race conditions, memory leaks** (4 parallel review
agents + a full `-race` run), followed by fixes. The review surfaced findings across auth/crypto,
injection, concurrency, and resource lifecycle. Key outcome: **2 of the flagged "bugs" did not
survive verification** and were deliberately NOT "fixed" (see below) — TDD/`-race` caught them.

## What changed and why

### Fixed (real, verified)
1. **Git deploy argument injection** — `internal/config/config.go` `GitSource.Validate()` (new,
   exported) rejects `repo`/`ref` with a leading `-` (git would read `--upload-pack=…` as a flag →
   RCE) and `subdir` that is absolute or escapes via `..`. Called from `config.validate()` **and**
   at the deploy sink `deploy.Start`/`Redeploy` (`internal/deploy/deployer.go`) — important because
   `store.Load()` does **not** re-validate, so a tampered `dump.json` would otherwise bypass the
   parse-time gate. Tests: `config_test.go` (`TestValidateRejectsDangerousGitSource`,
   `TestValidateAcceptsSafeGitSource`), `deployer_test.go` (`TestStartRejectsDangerousSource`,
   `TestRedeployRejectsDangerousSource`).
2. **`dump.json` `0644` → `0600`** — `internal/store/store.go`. It serializes app `Env` (secrets).
   Test: `store_test.go` `TestSaveDumpIsPrivate`.
3. **Registry unbounded growth** — `internal/server/registry.go` new `Registry.Evict(cutoff)`;
   wired into the existing 10-min prune loop in `server.go` with the same 7-day retention already
   used for store rows. Bounds the in-memory fleet map under churning/ephemeral agent names.
   Tests: `registry_test.go` (`TestRegistryEvict*`).
4. **gRPC auth-failure auditing (#3, audit-only)** — the fleet interceptors
   (`internal/server/interceptor.go`) now record failed admin/agent/enroll token attempts via
   `AuthStore.recordAuthFailure` (source class in `User` as `fleet:admin|agent|enroll`, peer IP,
   `invalid_credentials`). Previously gRPC auth failures left **no record at all**. The `*audit.Log`
   is now created once in `server.Run()` (unconditionally, so it works with the dashboard off),
   attached via `AuthStore.SetAuditLog`, and **shared** with the dashboard (single writer on
   `login-audit.log`). `dashboard.Serve`/`newHandler` now take `*audit.Log` instead of an audit
   path string. Surfaced by `marshal server audit`. Test: `interceptor_test.go`
   `TestInterceptorAuditsAuthFailures`.

### Deliberately NOT fixed (did not survive verification)
- **Registry "publish-then-mutate" data race** (a reviewer rated it High): does **not reproduce**
  under `-race`, even with an aggressive concurrent `Update`+`List`+marshal test. `Update`/`List`
  serialize on `r.mu`; the producer passes a fresh per-message slice it never mutates; protobuf-go
  marshal is safe for concurrent reads. Added `registry_test.go`
  `TestRegistryListConcurrentWithUpdate` as a permanent regression guard instead of a fake fix.
- **Fleet receiver goroutine "leak"** (`internal/fleet/client.go`): traced — the goroutine
  terminates after one in-flight command (`conn.Close()` errors `Recv()`; `recvErr` is cap-1 so the
  final send never blocks). Bounded, not an unbounded leak (unless a command handler blocks forever,
  which would be the handler's bug). No change.

### Active gRPC lockout (#3 second half) — intentionally deferred
Per the review decision, only **audit logging** was added, not an active per-IP lockout on gRPC
auth. A lockout is a behavior change that could disrupt a legitimate fleet reconnecting after a
botched token rotation; left as a recommendation.

## How to build / run / test

```bash
make build                                  # stamps version from git tags
go test ./... -race -count=1                # all green
go vet ./... && gofmt -l .                  # clean
```

## Live demo performed (PASS)

Scratch `XDG_DATA_HOME=/tmp/marshal-demo/...`, server on `:9000/:9001`:
- Set password + captured fingerprint while server down; started server.
- `marshal fleet ps --token wrong-token` → `error: invalid admin token`; repeated.
- `marshal server audit --failures` → two `invalid_credentials  fleet:admin  ::1` rows
  (these records did not exist before this change).
- Started a scratch daemon app with `DB_PASSWORD` env; `marshal save`; `dump.json` is `-rw-------`
  and contains `DB_PASSWORD` (proving why 0600 matters).
- Teardown: `marshal kill` (scratch data dir) + killed the demo server by PID + `rm -rf` scratch.
  Confirmed only the standing launchd daemon (`clock`) remained. **No broad pkill** (the user runs
  a standing daemon).

## Deferred / known issues (from the review, not yet addressed)

These were flagged but not implemented this session — candidates for a follow-up branch:
- **gRPC active lockout** on repeated auth failures (the deferred half of #3).
- **Login lockout keyed on attacker-controlled `user+ip`** (`dashboard/handlers.go`) — no per-IP
  cap; rotating the username field mints fresh limiter buckets. Add an independent per-IP counter.
- **`MARSHAL_MASTER_KEY` from env** (`secretbox.go`) — readable via `/proc/<pid>/environ`; consider
  restricting to explicit opt-in.
- **`authAgent` O(n) short-circuiting loop** (`server/auth.go`) — weak timing oracle on the
  agent token; make it a deterministic `map[tokenHash]name` lookup.
- **SSRF in notify channels** (`notify/channels/*`) — operator-controlled URLs, no internal-IP
  allowlist, redirects on, no client timeout. By-design for operators today.
- **`ssh-keygen` writes plaintext key to a temp file** (`credstore.go`) before sealing — generate
  ed25519 in-process instead.

## Concrete next step

Decide whether to **merge `fix/security-hardening-review` into `dev`** (`--no-ff`) now, or to first
extend the branch with one or more of the deferred items above (most natural next: the per-IP login
lockout, and the `authAgent` constant-time map lookup — both small). The `[Unreleased]` CHANGELOG
section already has Added/Security/Fixed entries for the four landed changes.
