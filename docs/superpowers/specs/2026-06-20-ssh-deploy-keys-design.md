# M25 — SSH deploy keys — design spec

**Date:** 2026-06-20
**Status:** approved (brainstorm), ready for implementation plan
**Builds on:** M22 managed git credentials (`2026-06-19-managed-git-credentials-design.md`) and
M24 commit & push (`2026-06-20-commit-push-design.md`). Read those first — this is the second
credential *type* alongside the M22 HTTPS personal-access-token.

---

## 1. Goal

Let a dashboard user deploy (and edit/push, per M24) a git app from a repo that authenticates
with an **SSH deploy key**, without the agent host being SSH-authed. Marshal **generates** the
keypair server-side, stores the private key encrypted at rest, and shows the user the **public
key** to register as a deploy key on their repo. The private key only ever leaves the server over
the existing TLS fleet link, into agent memory + a short-lived `0600` temp file, for one git op.

This reuses M22's "server owns the credential, pushes it per-op, agent stays stateless" model and
M24's `GIT_SSH_COMMAND`-covers-all-ops reality (so clone, fetch, **and** push all work through one
mechanism). It also closes M24's deferred "credential-on-push not exercised live" gap, on SSH.

## 2. Decisions (from the brainstorm)

1. **Key provenance: Marshal generates.** ed25519, **no passphrase** (deploy-key convention; the
   key is already sealed at rest under the master key). Pasting an existing private key is **out of
   scope**.
2. **Host key verification: TOFU, pinned on first use, then strict.** The agent **never** runs
   `accept-new` — it always verifies strictly against a server-supplied pin.
3. **Pin storage: server-side, with the credential.** Consistent with M22 (the server owns
   credential trust state); survives Forget/re-clone; shared across agents; centrally visible.
4. **Pin capture: the server scans (`ssh-keyscan`).** The server is the single trust root — it
   learns the host key once (TOFU) and pushes it down; the agent can never silently trust a key the
   server did not pin. Assumes the server can reach the git host's SSH port. (Agent-side
   capture-and-report-back was considered and rejected as more plumbing for this cut.)
5. **Push parity: included.** SSH plumbs through `GIT_SSH_COMMAND`, which applies to every git
   network op, so M24 edit/create/delete/rename push works on SSH-deployed apps with no extra work.
6. **Key generation: server shells out to `ssh-keygen -t ed25519 -N ""`** (guaranteed
   OpenSSH-compatible private-key format; no new Go dependency vs. adding `golang.org/x/crypto/ssh`).
   This is the lean; the plan may revisit if shelling out on the server proves awkward.

## 3. Data model — `internal/credstore`

The existing `entry` already carries a `Type` field (today always `"https-token"`). SSH adds type
`"ssh-key"`:

- `Cipher` / `Nonce` — the **encrypted private key** (OpenSSH format), sealed exactly as the HTTPS
  token is today (AES-256-GCM under the server master key).
- `PublicKey` *(new, plaintext — it is public)* — the `ssh-ed25519 …` authorized_keys line, shown
  in the dashboard so the user can register it as a deploy key.
- `KnownHosts` *(new, plaintext)* — the pinned host-key line(s) from `ssh-keyscan`; empty until the
  first deploy scans it.
- `Username` — `"git"` (the SSH user in `git@host`).

New methods (HTTPS `Put`/`Get`/`Delete`/`List` untouched except `Meta` gaining `PublicKey`):

- `Generate(name) (publicKey string, err error)` — mint an ed25519 keypair, seal the private key,
  store `Type:"ssh-key"`, `Username:"git"`, the public key, empty `KnownHosts`; return the public
  key. Upsert semantics match `Put` (re-generate rotates).
- `GetKey(name) (privateKey, knownHosts string, ok bool, err error)` — decrypt the private key and
  return it with the current pin.
- `SetKnownHosts(name, line string) error` — record the pin after the server's first scan.
- `Meta` gains `PublicKey` (empty for https-token rows). `List` therefore exposes the public key
  but **never** the private key.

## 4. Proto & fleet flow — `GitCredential`

Additive only (an M22/M24 agent that ignores the new fields still works; default `kind` = HTTPS):

```proto
message GitCredential {
  string username    = 1;
  string token       = 2;          // HTTPS only
  string private_key = 3;          // SSH only (sealed in transit by TLS; never persisted on agent)
  string known_hosts = 4;          // SSH only — the server-pinned host key
  CredentialKind kind = 5;         // default HTTPS keeps old agents working
}
enum CredentialKind { CRED_HTTPS = 0; CRED_SSH = 1; }
```

The secret rides per-op over the TLS link and lives only in agent memory + a temp file for the
duration of one git op — same invariant as M22. `DeployRequest`, `RedeployRequest`, and
`CommitRequest` already carry a `GitCredential`; no new request fields.

## 5. Server-side scan & pin (the trust root) — dashboard credential resolution

When the dashboard resolves an `ssh-key` credential for a deploy / redeploy / commit:

1. Parse the **host[:port]** from the repo URL. Handle both SSH URL forms:
   - scp-like `git@github.com:org/repo.git` → host `github.com`,
   - `ssh://git@host:443/org/repo.git` → host `host`, port `443` (e.g. GitHub's `ssh.github.com:443`).
2. If the credential has **no pin yet** (`KnownHosts` empty): run `ssh-keyscan [-p port] host`, store
   the result via `SetKnownHosts` (TOFU — happens exactly once per credential).
3. Attach `{private_key, known_hosts, kind: CRED_SSH, username: "git"}` to the outgoing
   `GitCredential`.

The `ssh-keyscan` invocation is behind an injectable seam (interface/func field) so tests fake it —
no real network in unit tests. HTTPS credentials skip all of this and resolve as today.

## 6. Agent-side SSH env — `gitCredEnv` branch (`internal/deploy`)

`deploy.Credential` is extended to carry the SSH material and a kind discriminator. `gitCredEnv`
branches on kind:

- **HTTPS** (`kind == CRED_HTTPS`, or empty/legacy): unchanged — GIT_ASKPASS helper +
  `MARSHAL_GIT_USER/TOKEN`, URL gets `withUsername`, `-c credential.helper=` disables inherited
  helpers.
- **SSH** (`kind == CRED_SSH`): write the private key to a temp file (`0600`) and the pin to a temp
  `known_hosts`; set
  `GIT_SSH_COMMAND="ssh -i <key> -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile=<kh>"`.
  The SSH URL is used **as-is** (no `withUsername`, no `credential.helper=` — those are HTTPS-only).
  `cleanup` removes the temp dir via `defer`, same contract as today.

`fetch` and `mutateAndPush` are otherwise unchanged — they already route all git ops through the env
`gitCredEnv` returns. Build step still gets **nil** env (no key during build).

## 7. Dashboard UX

`Credentials.tsx` gains a **type toggle** (HTTPS token | SSH key):

- **HTTPS** — unchanged (username + write-only token).
- **SSH** — name only. On create the server generates the keypair and the UI shows the **public
  key** with a copy button and: *"Add this as a deploy key on your repo (e.g. GitHub → Settings →
  Deploy keys)."* The public key remains viewable in the list afterward (not secret); the private
  key is never retrievable.

API:

- `GET /api/credentials` returns `type` and, for ssh-key rows, `public_key`. Never the private key.
- `POST /api/credentials` branches on `type`: `https-token` → today's path; `ssh-key` →
  `Generate(name)`, respond with the public key.
- `AddAppModal`'s existing credential `<select>` lists SSH credentials alongside HTTPS ones; the
  user points the repo field at an SSH URL.

**Usage note (documented, not enforced):** a GitHub deploy key is per-repo, so the natural pattern
is one SSH credential per repo.

## 8. Security properties (invariants to hold)

- Private key is **born on the server**, sealed AES-256-GCM at rest, and only ever leaves over the
  TLS fleet link into agent **memory + a 0600 temp file** that is `defer`-removed. Never in argv,
  the clone URL, the per-app log, `dump.json`, or the audit log.
- `List` / `GET /api/credentials` and `credentials.json` never expose the private key — only the
  public key and the pinned host key.
- The agent verifies host keys **strictly** against the server pin; `accept-new` never runs on the
  agent.
- Build step gets **nil** env (no key during build), same as M22.
- `deploy.Credential` now holds a private key → add a **redacting `String()`** so a stray `%+v`
  cannot leak it (also closes a deferred M22 nit).
- Credential audit (deploy/commit) logs name + kind, **never** the private key — same as M22's
  token handling.

## 9. Testing

TDD, failing test first, per task:

- **credstore:** `Generate` yields a valid, distinct keypair; the private key is sealed (not
  plaintext on disk); `GetKey` round-trips; `SetKnownHosts` persists; `List`/`Meta` expose the
  public key but never the private key; existing HTTPS entries still load and work.
- **deploy:** `gitCredEnv` SSH branch writes a `0600` keyfile + known_hosts and the correct
  `GIT_SSH_COMMAND`; cleanup removes them; HTTPS branch unchanged; the SSH URL is **not** rewritten.
- **server scan/pin:** host[:port] parsed from each SSH URL form; scan runs only when the pin is
  absent (TOFU-once); the pin is attached to the outgoing `GitCredential`. Scan behind a faked seam
  — no real network.
- **dashboard:** `POST type=ssh-key` returns a public key and persists no plaintext private key;
  `GET` exposes `public_key`, not the private key; the HTTPS path is regression-tested.
- **Live demo:** real fleet against an **SSH git remote** (local `sshd`/bare repo or a throwaway
  GitHub repo with the generated deploy key registered): clone, redeploy, and an M24 push, all over
  SSH; confirm the private key is absent from the agent data dir and logs; confirm a **wrong/MITM
  host key is rejected**. Closes M24's "credential-on-push not exercised live" gap on the SSH path.

## 10. Scope / non-goals

**In:** generate ed25519 deploy keys server-side; server-pinned strict host-key verification;
SSH clone/fetch/push parity; dashboard generate-and-show-public-key; redacting `deploy.Credential`.

**Out:** pasting an existing private key; passphrase-protected keys; key rotation/regeneration UI
(delete + recreate for now); editing or un-pinning a host key from the UI; RSA/ECDSA keys; SSH agent
forwarding; per-host multiple-pin management beyond what one `ssh-keyscan` returns; server→git-host
reachability fallback (agent-side capture) — revisit if a deployment needs it.
