# Design: Marshal-managed git credentials (server-side HTTPS tokens)

**Date:** 2026-06-19
**Status:** Approved (brainstorm) — ready for implementation plan
**Milestone:** M22 — Marshal-managed git credentials
**Read first:** `2026-06-19-git-deploy-design.md` (M21 deploy architecture) and the M22
kickoff handoff `docs/handoffs/2026-06-19-m22-managed-git-credentials-kickoff.md`.

## Summary

Let a user store a git **HTTPS personal-access token (PAT)** *in Marshal* and have an agent
use it to clone/fetch a **private** repo at deploy time. Today (after M21) deploy auth is
purely the agent host's own git setup; this milestone adds a Marshal-managed credential so
private-repo deploys work on a host that is not itself git-authed.

The secret is stored **once on the server**, encrypted at rest, and **pushed to the chosen
agent inside the deploy/redeploy op** over the existing TLS fleet link. The agent uses it for
a single clone/fetch and never persists it. The deploy carries only the credential's **name**
(`config.GitSource.Credential`), persisted in `dump.json` so **redeploy** re-resolves the same
credential automatically.

This layers additively onto the M21 seam. The only breaking change to the deploy package is
adding an `env []string` parameter to the `deploy.Runner` interface (the choke point for
threading a per-deploy credential into git's environment).

```
Create:   dashboard POST /api/credentials {name,username,token}
            → credstore.Put → AES-256-GCM(token, masterKey) → <dataDir>/credentials.json (0600)

Deploy:   dashboard POST /api/apps {source:{type:"git", credential:"gh-ci", ...}}
            → server resolves "gh-ci": credstore.Get → decrypt token
            → ControlOp_Deploy{ AppSpec(GitSource.credential="gh-ci"), GitCredential{user,token} }
            → agent deployer.Start(app, cred)
                 └─ goroutine: write temp askpass (0700) + git env → git clone → build → doStart

Redeploy: persisted GitSource carries credential NAME "gh-ci" (no secret in dump.json)
            → server re-resolves+decrypts → ControlOp_Redeploy{target, GitCredential{...}}
```

## Goals

- Create / list / delete / **rotate** a named HTTPS-token credential in the dashboard; the
  token value is write-only (never returned by the API after creation).
- Encrypt credentials at rest with a server master key (auto-generated key file, env override).
- Select a stored credential by name when deploying a git app; the chosen agent clones/fetches
  the private repo using it.
- Redeploy a credential-using git app: re-resolve the persisted credential name on the server
  and re-push the secret — no re-entry by the user.
- **Never leak the token**: not in argv, the clone URL, the per-app log, `dump.json`, or the
  audit log.

## Non-goals (deferred)

- **SSH deploy keys** (key-file lifecycle, `GIT_SSH_COMMAND`, `known_hosts`) — a follow-up
  milestone. This cut is HTTPS-token only.
- **Per-agent or per-user scoping / ownership** — credentials are server-global, usable by any
  authenticated dashboard user (matches the current single-shared-admin model). A real
  multi-tenant ownership model is out of scope.
- **OS keychain** key storage; key rotation/re-encryption of the whole store.
- Credentials for the **build** step (e.g. private npm registry auth) — the credential is
  applied to git clone/fetch only.
- Dashboard file manager + in-browser editor (a separately flagged future idea).

## Trust boundary

A stored credential lets any authenticated dashboard user clone private code on an agent they
already control through the dashboard. This is acceptable under Marshal's current
single-shared-admin dashboard model and is the same blast radius an admin already has. All
`/api/credentials` routes require a dashboard session. The token transits the (already TLS)
fleet link and lives in agent process memory only for the duration of one clone/fetch.

## Architecture

### `internal/credstore` (new package)

A focused, self-contained encrypted store — deliberately *not* folded into the `server`
package next to `auth.go`, to keep the crypto and CRUD in one small testable unit.

```go
type Entry struct {
    Name      string // unique id, used as the credential reference
    Type      string // "https-token" (only type in this cut)
    Username  string // non-secret (e.g. a GitHub username or "x-access-token")
    CreatedAt int64
    // ciphertext + nonce held internally; never exported in List
}

type Store struct { /* path, masterKey, mu, in-memory map */ }

func Open(dir string) (*Store, error)              // loads/creates <dir>/credentials.json + master key
func (s *Store) Put(name, username, token string) error  // create or ROTATE (upsert by name)
func (s *Store) Get(name string) (username, token string, ok bool, err error) // decrypts
func (s *Store) List() []Entry                     // metadata only — NEVER plaintext token
func (s *Store) Delete(name string) bool
```

- **On-disk file** `<dataDir>/credentials.json` (0600): a JSON map `name → {type, username,
  nonce(base64), ciphertext(base64), created_at}`. The token is the only encrypted field.
- **Encryption**: AES-256-GCM via Go stdlib (`crypto/aes`, `crypto/cipher`, `crypto/rand`) —
  **no new dependency**, same stdlib-only style as `auth.go`. Fresh random nonce per `Put`.
  GCM authenticates the ciphertext, so a wrong key or tampered file yields a decrypt error,
  never silent garbage.
- **Master key**: 32 random bytes. Resolution order:
  1. `MARSHAL_MASTER_KEY` env var (base64 std, must decode to exactly 32 bytes) — lets an
     operator inject the key from a secret manager and keep it off disk.
  2. else `<dataDir>/master.key` (0600); auto-generated with `crypto/rand` on first `Open` if
     absent (zero-config default).
  A present-but-invalid `MARSHAL_MASTER_KEY` (bad base64 / wrong length) is a hard error from
  `Open` — see error handling.
- **Name validation**: reuse the existing app-name allowlist style
  (`^[A-Za-z0-9][A-Za-z0-9._-]*$`) so a name is safe as a map key and audit field.

The store is owned **server-side** and constructed from the server's data dir alongside the
other stores. The dashboard `handler` gets a reference to it (new dependency) so it can resolve
a credential name → secret when building a deploy/redeploy op.

### config

`config.GitSource` gains one field — the credential **name** only (non-secret), persisted in
`dump.json` so redeploy knows which credential to re-resolve:

```go
type GitSource struct {
    Repo       string `yaml:"repo"        json:"repo"`
    Ref        string `yaml:"ref"         json:"ref,omitempty"`
    Build      string `yaml:"build"       json:"build,omitempty"`
    Subdir     string `yaml:"subdir"      json:"subdir,omitempty"`
    Credential string `yaml:"credential"  json:"credential,omitempty"` // M22: credstore name
}
```

The token itself is **never** written to config/`dump.json`.

### Wire contract (proto)

`daemon.proto`:

```proto
message GitSource {           // existing repo/ref/build/subdir ...
  string credential = 5;      // M22: credstore name (non-secret), persisted for redeploy
}

message ProcInfo {            // existing fields 1..11 (incl. source=10, detail=11) ...
  string credential = 12;     // M22: credential name (non-secret) → drives redeploy resolution
}
```

`fleet.proto`:

```proto
message GitCredential {        // the secret, attached per-op, NEVER persisted on the agent
  string username = 1;
  string token    = 2;
}

message DeployRequest   { AppSpec app    = 1; GitCredential credential = 2; }  // +field 2
message RedeployRequest { string  target = 1; GitCredential credential = 2; }  // +field 2
```

No transport change: `Deploy`/`Redeploy` still return `ControlResult` immediately (accepted /
rejected); the clone/build still runs in the background and reports via the heartbeat.

### Agent deployer + runner

- **`deploy.Runner`** gains an `env` parameter (the one breaking change to the M21 seam):

  ```go
  Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error
  ```

  `ExecRunner` sets `cmd.Env = append(os.Environ(), env...)` (nil env → inherit only, today's
  behavior). All existing call sites pass `nil` except the credentialed clone/fetch.
- **`Deployer.Start` / `Redeploy`** gain a `cred` argument (`username, token`, empty when none).
  `runDeploy` threads it into `fetch` (clone/fetch only). The **build step gets no credential
  env** — limiting exposure and matching the non-goal.
- **Injection (GIT_ASKPASS)** when a credential is present, in `fetch`:
  1. Write a throwaway askpass script to a per-deploy temp dir (0700) that echoes
     `$MARSHAL_GIT_USER` when its prompt argument contains "Username", else `$MARSHAL_GIT_TOKEN`.
  2. Build the git env: `GIT_ASKPASS=<script>`, `MARSHAL_GIT_USER=<username>`,
     `MARSHAL_GIT_TOKEN=<token>`, `GIT_TERMINAL_PROMPT=0` (fail instead of hanging on a prompt).
  3. Rewrite the clone URL to embed the **username only** (`https://<user>@host/repo`) so git
     associates the askpass password with that user; the token stays out of the URL.
  4. Remove the temp script (and dir) after the clone/fetch returns (success or failure).

  The token therefore appears only in the child process env and on the askpass→git stdout pipe
  — never in argv, the URL, or the output piped to the per-app log.

### Server wiring (resolve + attach)

The dashboard `apps`/`redeploy` handlers resolve the credential before dispatch:

- **Deploy**: read `source.credential` from the request. If non-empty, `credstore.Get(name)`;
  unknown → `400`. Attach `GitCredential{username, token}` to the `DeployRequest`. `GitSource`
  on the wire/spec carries only the name.
- **Redeploy**: symmetric with deploy. The credential **name** is surfaced (non-secret) on the
  fleet snapshot — `ProcInfo` gains `credential` (daemon.proto field 12), stamped from the
  app's persisted `GitSource.Credential`, and **threaded through the dashboard `procView` DTO**
  (`internal/dashboard/fleet.go`) so the web client sees it. *(M21's live demo caught exactly
  this class of bug — `procView` silently dropped `source`/`detail`; the plan must add a test
  asserting `credential` survives `procView`.)* The web `redeploy()` sends `{agent, name,
  credential}`; the server resolves the name → secret and attaches it to `RedeployRequest`.
  This needs **no** server-side app-spec persistence — the server stays stateless about which
  app uses which credential.

### Dashboard HTTP

New `internal/dashboard/credentials.go`, all behind `requireSession`:

- `GET  /api/credentials` → `[{name, type, username, created_at}]` — **no token field, ever**.
- `POST /api/credentials` `{name, username, token}` → create or **rotate** (upsert by name);
  `201`/`200`. Validates name + non-empty token.
- `DELETE /api/credentials/{name}` → `204`; `404` if absent.

`apps.go` deploy/redeploy gain the resolve-and-attach step above. Error mapping mirrors the
existing control path (`401` no session, `400` bad input / unknown credential, `502` agent
unreachable, `ok:false` agent-rejected).

### Dashboard web

- A **Credentials** management view (Signal styling): list (name / username / created), an
  add/rotate form (name, username, token — token is a password field, write-only), delete.
- `AddAppModal` git mode gains a **credential** selector: a dropdown of existing credential
  names plus "none" (host credentials / public repo). Selected name is sent as
  `source.credential`.
- Redeploy needs no credential input — the server reuses the persisted name.
- `api.ts`: `listCredentials()`, `createCredential()`, `deleteCredential()`; `addApp` widened
  with an optional `credential` on the git source.

## Error handling

- Deploy/redeploy references an unknown credential → `400` / `ok:false "unknown credential"`,
  surfaced in the modal; nothing dispatched.
- `MARSHAL_MASTER_KEY` present but invalid (bad base64 / not 32 bytes) → `credstore.Open`
  returns an error; the server logs it and runs with the **credentials feature disabled**:
  `/api/credentials` and credentialed deploys return `503` with a clear message, while ordinary
  (no-credential) deploys keep working. The server does not crash over a key typo.
- Wrong key / tampered `credentials.json` → GCM auth failure surfaced as a decrypt error on
  `Get` (that one credential is unusable), not a panic.
- Duplicate/rotate is intentional (upsert), not an error.
- Missing `username` is allowed for providers that ignore it; missing `token` on create → `400`.

## Audit

Credential create/rotate/delete and credential-using deploys record the action + credential
**name** + username in the existing audit log — **never the token**. (The audit log already
exists for login; this extends it.)

## Testing

- **`credstore`** (Go TDD): encrypt/decrypt round-trip; `List` never exposes plaintext;
  wrong-key / tampered-ciphertext → error; `master.key` auto-gen has mode 0600; env override
  takes precedence over the file; invalid env key → `Open` error; `Put` rotation replaces the
  token; name validation.
- **`deploy`**: `Runner` env plumbing (fake runner records env); with a credential, the clone
  env contains `GIT_ASKPASS` + `MARSHAL_GIT_USER/TOKEN` and the token is **absent from argv**;
  clone URL gains the username; askpass script is written 0700 and removed after; build step
  receives **no** credential env; no-credential path unchanged.
- **dashboard**: credentials CRUD (`201`/`200` rotate/`204`/`404`), `401` without session,
  list response has no `token`; deploy with `credential` resolves + attaches secret to
  `DeployRequest`; unknown credential → `400`; redeploy re-resolves the name → attaches secret
  to `RedeployRequest`; `credential` survives the `procView` DTO (M21-lesson regression test);
  feature disabled (`503`) when the store failed to open.
- **Frontend**: `make ui` (`tsc -b`) — repo has no web test framework — plus the live demo.

## Live demo plan

On `:9000/:9001` (standing demo-port convention), set auth while the server is **down**, then:
create a credential in the dashboard for a **private** repo (a real GitHub PAT, or a local
auth-required git server); deploy that private repo selecting the credential and confirm
`cloning → building → online`; confirm the per-app **log shows no token**; rotate the token and
redeploy; delete the credential. Tear down (agent + server + Vite, scratch dir, launch.json) and
confirm no orphan `marshal` processes (`pgrep -fl marshal`).

## Open decisions resolved in brainstorm

- **Store location**: server-side, pushed to the agent per-deploy (matches server→agents topology).
- **Credential type**: HTTPS token (PAT) only for this cut; SSH keys deferred.
- **Encryption key**: auto-generated `<dataDir>/master.key` (0600) with `MARSHAL_MASTER_KEY`
  (base64) env override; AES-256-GCM, stdlib-only.
- **Injection**: `GIT_ASKPASS` helper + username-in-URL; token never in argv/URL/log.
- **Scoping**: server-global, named, selected at deploy; usable by any authenticated dashboard user.
- **Package**: new `internal/credstore` (not folded into `server`).
- **Rotation**: in-scope — `POST /api/credentials` upserts by name.
