# Marshal — M25: SSH deploy keys — Handoff

**Date:** 2026-06-21
**Branch:** `m25-ssh-deploy-keys` (all work done, reviewed, live-demoed; ready to merge `--no-ff` to `main`).
**Read the M22 handoff `2026-06-19-m22-managed-git-credentials.md` first** (the credential store + per-op
push mechanism this extends) and the M24 handoff `2026-06-20-m24-commit-push.md` (the push path SSH now also drives).

---

## TL;DR

A dashboard user can now deploy, redeploy, and edit/push a git app from an **SSH-authenticated** repo,
as a second credential **type** alongside M22's HTTPS token. Marshal **generates** an ed25519 keypair
server-side (`ssh-keygen`), seals the private key at rest (AES-256-GCM, reusing the M22 credstore), and
shows the user the **public key** to register as a deploy key. On the **first deploy** the server scans
the repo host (`ssh-keyscan`, **TOFU-once**), pins the host key into the credential, and pushes
`{private_key, known_hosts, kind:SSH}` per-op over the existing TLS fleet link. The agent writes the key +
pin to short-lived **0600** temp files and routes every git op through `GIT_SSH_COMMAND` with
`StrictHostKeyChecking=yes` — so clone, fetch, **and** M24 push all work through one mechanism, and the
agent **never** does `accept-new` (the server is the single trust root). This also closed M24's deferred
"credential-on-push not exercised live" gap, on the SSH path.

Built spec → plan → 7-task subagent-driven TDD (each task reviewed; 3 had a small fix loop) → whole-branch
review (opus, "ready to merge — clean") → 3 hardening fixes → live demo (full fleet over a real SSH remote,
incl. the host-key-rejection). Spec: `docs/superpowers/specs/2026-06-20-ssh-deploy-keys-design.md`. Plan:
`docs/superpowers/plans/2026-06-20-ssh-deploy-keys.md`.

## What changed this session

- **Proto** (`proto/marshal/v1/fleet.proto`): `GitCredential` += `private_key=3`, `known_hosts=4`,
  `kind=5`; new `enum CredentialKind {CRED_HTTPS=0, CRED_SSH=1}`. All additive — `kind` defaults
  `CRED_HTTPS`, so an M22/M24 agent ignoring the new fields still works. Regen via `go generate ./internal/pb`.
- **`internal/credstore/credstore.go`**: the `entry`/`Meta` `Type` field (already present) now also carries
  `ssh-key`. New `entry.PublicKey` (plaintext — public) and `entry.KnownHosts` (plaintext pin). New methods:
  `Generate(name) (publicKey, err)` (mints ed25519 via a `var genKeypair` seam that shells to
  `ssh-keygen -t ed25519 -N ""`, seals the private key into `Cipher`), `GetKey(name) (priv, knownHosts, ok, err)`
  (guards `Type=="ssh-key"`), `SetKnownHosts(name, line)`. The AES seal/decrypt was refactored into shared
  `seal`/`openCipher` helpers (M22 `Put`/`Get` behavior identical). `Get` gained a `Type=="https-token"` guard
  (final-fix #3) so an ssh-key can never be decrypted via the HTTPS path. `List`/`Meta` expose the public key,
  never the private key.
- **`internal/deploy/deployer.go` + `mutate.go`**: `deploy.Credential` += `PrivateKey`, `KnownHosts`, `SSH bool`,
  a `httpsActive()` helper, and a **redacting `String()`** (no token/key in `%v`/`%+v`/`%s` — also closes a
  deferred M22 nit). `gitCredEnv` gained an **SSH branch** (prepended; HTTPS `GIT_ASKPASS` body unchanged):
  writes the private key (0600, trailing newline ensured) + pin to a temp dir, returns
  `GIT_SSH_COMMAND="ssh -i '<key>' -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile='<kh>'"`
  (paths single-quoted), `defer`-removes the dir. `fetch` and the `mutate.go` push switched
  `credActive := cred.Token != ""` → `cred.httpsActive()`, so `withUsername` URL-rewrite and `-c credential.helper=`
  are HTTPS-only; the SSH URL is used verbatim and auth comes from `GIT_SSH_COMMAND`.
- **`internal/daemon/command.go`**: new `credFromProto(*pb.GitCredential) deploy.Credential` (nil → zero value;
  `CRED_SSH` → `{Username, PrivateKey, KnownHosts, SSH:true}`; else `{Username, Token}`) replaces the three
  inline constructions (deploy/redeploy/commit).
- **`internal/dashboard/credentials.go`**: `Credentials` interface widened with `Generate`/`GetKey`/`SetKnownHosts`.
  `credentialReq` += `Type`. `createCredential` branches: `type:"ssh-key"` → `Generate` → 201 `{ok, public_key}`
  (500 on keygen failure — final-fix; never logs key material); else the existing HTTPS path. Empty name → 400 for both.
- **`internal/dashboard/apps.go` + `files.go` + `handlers.go`**: `resolveCredential(name, repoURL)` (new 2nd arg).
  For `ssh-key`: `GetKey`; if the pin is empty **and** a repoURL is available, derive host[:port] via `sshHostPort`,
  `h.scanHost(...)` (injectable seam, defaults to `ssh-keyscan`), persist via `SetKnownHosts`, attach
  `{Username:"git", PrivateKey, KnownHosts, Kind:CRED_SSH}`. If the pin is still empty afterward → a clear
  **"no pinned host key yet; deploy it first"** error (final-fix #1) instead of an empty `known_hosts`.
  `sshHostPort` parses scp-like (`git@host:path`) and `ssh://` forms, short-circuits `http(s)://` to `("","")`,
  and rejects hosts starting with `-` (final-fix #2, `ssh-keyscan` arg-injection guard). Deploy passes `g.Repo`;
  redeploy + the three commit callers pass `""` (pin already set from first deploy).
- **web** (`web/src/api.ts`, `Credentials.tsx`): `CredentialMeta.public_key?`; `createSSHCredential(name)`
  (POSTs `{name, type:"ssh-key"}`, returns `{ok, public_key}`). `Credentials.tsx` gained a **type toggle**
  (HTTPS token | SSH key); SSH mode shows **name only + "generate key"**, then renders the returned public key
  with a copy button + "Add this as a deploy key…" helper; list rows show `type` and (for ssh-key) the stored
  public key. `make ui` rebuilt the embedded `internal/dashboard/dist`.

## Key decisions / non-obvious

- **Marshal generates the keypair** (private key born on the server); pasting an existing key is **out of scope**.
  ed25519, **no passphrase** (deploy-key convention; sealed at rest under the master key anyway).
- **Server is the single trust root**: the server `ssh-keyscan`s and pins; the agent verifies **strictly** and
  never `accept-new`. So the agent can never silently trust a key the server didn't pin. Requires the **server**
  to reach the git host's SSH port (the considered alternative — agent-side capture-and-report-back — was rejected
  as more plumbing).
- **TOFU-once**: the scan fires only when the pin is empty **and** a repoURL is present — which only happens on the
  first deploy (redeploy/commit pass `""`). An already-pinned credential never re-scans.
- **`GIT_SSH_COMMAND` covers all git network ops**, so clone/fetch/push all work with one mechanism — M24 push over
  SSH came essentially for free.
- **Generation shells out to `ssh-keygen`** (guaranteed OpenSSH-format private key, no new Go dependency) rather
  than adding `golang.org/x/crypto/ssh`.

## Whole-branch review (opus) — verdict: ready to merge (clean)

All four cross-cutting security invariants verified end-to-end: (1) no private-key leak; (2) TOFU-once strict
pinning, server sole trust root; (3) additive proto / backward compat (HTTPS path gated via `httpsActive()`);
(4) nil build env. No Critical/Important. 3 hardening fixes taken (commit `080b466`): #1 pin-required error,
#2 `ssh-keyscan` leading-`-` host guard, #3 `credstore.Get` Type guard.

## Live demo result (2026-06-21, scratch `/tmp/marshal-m25-demo`, server `:9000`/`:9001`)

Real fleet (server + agent `dev-1`) against a **throwaway `sshd` on :2222** (own host key + scratch
`authorized_keys`; the user's `~/.ssh` and account were never touched) serving a bare repo over SSH:
- **Generate** an `ssh-key` credential via the dashboard API → got the public key; `credentials.json` held the
  **sealed** private key (cipher present, **zero** `PRIVATE KEY` plaintext); `GET /api/credentials` exposed only
  the public key.
- **Deploy** `sshapp` from `ssh://…@127.0.0.1:2222/…/app.git` with the credential → `cloning → online`. The clone
  delivered repo content (README read back via the file API). The server **pinned** the host key after first deploy
  (`known_hosts` populated, matches the scanned `127.0.0.1:2222` key). The private key was **absent** from the
  entire agent data dir; the clone's remote URL is a plain `ssh://` (no embedded secret).
- **M24 edit+push over SSH**: `PUT …/file?path=README.md` → `{branch:main, sha:8c76b1c}`; the commit landed on the
  bare-repo origin over SSH (verified `git show main:README.md`). **Closes M24's credential-on-push live gap.**
- **Host-key rejection (the security crux), end-to-end through the real agent**: rotated the throwaway sshd's host
  key (now ≠ the server's pin) and **redeployed** → agent failed strict verification: **"clone failed: exit status
  128"**, app left **online on the old version** (M24 transactional). Also proven at the mechanism level: a wrong
  pinned `known_hosts` → `git clone` rejects with "host key changed / verification failed" (rc=128).
- **Rendered UI** (Playwright/Chromium, screenshots captured): the `sshdeploy` ssh-key card with its public deploy
  key + copy/delete; the add-credential **type toggle**; the SSH-key form (name-only + "generate key"); and the
  fleet view showing `sshapp` online **and** the rejected MITM redeploy card.
- Teardown by data dir + pid only; the user's standing launchd daemon (pid 3119) preserved; `pgrep` shows no demo
  orphans; scratch dir removed.

## Known issues / deferred

- **Cosmetic UI polish (function-first per the design memory):** the new SSH-UI CSS classes (`cred-pubkey*`,
  `cred-type-toggle`) have no dedicated stylesheet rules (render functional but unstyled); the generated-public-key
  `<textarea>` lacks an `aria-label`. Fold into the eventual Signal/M19 styling pass.
- **Flaky test (NOT an M25 regression):** `cmd/marshal/TestRunSupervisesAndStops` has a tight 5s SIGINT-exit
  deadline that flakes under heavy machine load. Passes on base `25fdba7` and on-branch in isolation (2–5s); only
  failed during the contended parallel `go test ./...` sweep. M25 does not touch `cmd/marshal`. **Candidate fix:**
  bump that 5s deadline.
- **Out of scope (as designed):** pasting an existing private key; passphrase-protected keys; key rotation/regen UI
  (delete + recreate for now); editing/un-pinning a host key from the UI; RSA/ECDSA; SSH agent forwarding;
  server→git-host reachability fallback (agent-side capture).
- **Server reachability assumption:** the server must reach the git host's SSH port to scan. If a future deployment
  can't satisfy that, revisit agent-side capture-and-report-back.

## How to build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1     # all packages green (cmd/marshal flake under heavy load only — see above)
gofmt -l . ; go vet ./...        # silent / clean
go generate ./internal/pb        # regenerate proto bindings (protoc + plugins on PATH)
make ui                          # web/ → internal/dashboard/dist (tracked, embedded)
```

Endpoints (behind dashboard session): `POST /api/credentials {name, type:"ssh-key"}` → `{ok, public_key}`;
existing deploy/redeploy/commit endpoints resolve an ssh-key credential transparently.

## Concrete next step

1. **Merge `m25-ssh-deploy-keys` to `main`** (`--no-ff`, no remote) via
   `superpowers:finishing-a-development-branch`.
2. Then: the deferred **auth-required HTTPS-remote** demo (the only remaining M22-era gap, now that SSH push is
   proven), **proactive UI writability** + the cosmetic SSH-UI styling, or bump the `cmd/marshal` SIGINT deadline.
