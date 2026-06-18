# M10 — Fleet Auth & TLS — Design

**Date:** 2026-06-17
**Status:** approved (brainstorm) — pending spec review
**Branch:** `m10-auth-tls`
**Depends on:** M9 (fleet command channel, merged at `ffe222d`)
**Architecture ref:** `2026-06-16-fleet-process-manager-architecture-design.md` §6 (Security)

---

## 1. Motivation & threat model

M9 turned the fleet control plane into something that can **mutate** remote hosts
(`fleet start/stop/restart/delete`). Today every fleet RPC is plaintext and
unauthenticated, and the server binds all interfaces: anyone who can reach `:9000` can
observe and control every host. M10 closes this.

**Goal:** fleet mode requires TLS + authentication on every RPC, with per-agent identity
and server-side authorization of operator actions.

**Threat model (v1, pragmatic).** We defend against an attacker on the network who can
reach the server port: they must not be able to read fleet state, impersonate an agent, or
issue control commands without a valid credential. We do **not** defend against an attacker
who already has read access to the server's data directory (if they own the box, they own
the secrets) — though we still hash tokens at rest as cheap defense-in-depth. CA-based PKI,
HA, and dashboard sessions are out of scope (the dashboard is sub-project #4; its
username/password sessions come with it).

**Standalone mode is unaffected.** An agent with no `server:` block never touches the Fleet
service; the local daemon/CLI path gains no auth and no TLS. M10 only hardens fleet mode.

## 2. Decisions (from brainstorm)

| # | Decision | Choice |
|---|----------|--------|
| Scope | How much of the security story | **Full**: TLS + agent token + operator token + server-side authorization |
| TLS trust | How agents/CLI trust the server | **Auto self-signed cert + fingerprint pinning**; override with operator-supplied cert/key (then trust via CA/system roots) |
| Agent identity | How agents get a credential | **Auto-enroll on first connect**: one shared enroll token → server mints a per-agent token |
| Operator auth | CLI authentication granularity | **Single admin token**, required on all operator RPCs |
| Transport policy | Plaintext fallback? | **None.** Fleet mode is TLS-and-auth-only; no `--insecure` flag |

## 3. The four mechanisms

### 3.1 TLS layer — server cert + fingerprint pinning

- On first run the server generates a self-signed cert+key, persists them to its data dir
  as `cert.pem` / `key.pem` (mode 0600), and prints the cert's SHA-256 fingerprint. A
  `marshal server fingerprint` subcommand reprints it.
- Operators who want a CA-signed cert drop in their own `cert.pem`/`key.pem` (or point
  `--tls-cert` / `--tls-key` at them); the server uses those instead of generating.
- Agents and the CLI connect over TLS but **pin the fingerprint**: the client TLS config
  sets `InsecureSkipVerify: true` with a custom `VerifyPeerCertificate` that compares the
  leaf certificate's SHA-256 to the pinned value. This avoids any dependence on system
  roots or hostnames and works against a bare LAN IP. (Constant-time compare on the
  fingerprint bytes.)
- BYO-cert path: if the operator supplied a CA-signed cert, the client trusts it via an
  explicitly provided `ca` file instead of pinning. Pinning is the default; CA trust is the
  opt-in. There is no implicit system-roots path — trust must always be explicit
  (`fingerprint` or `ca`), so the client can never silently trust an unexpected cert.

### 3.2 Agent enrollment & identity — auto-enroll

- The server holds one **enrollment token**, generated and printed on first run, stored
  hashed at rest.
- **First connect (enrollment):** an agent with no persisted credential presents
  `(enroll_token, requested_name)`. The server:
  1. validates the enroll token;
  2. registers the agent under `requested_name`, rejecting the name if it is already bound
     to a *different* credential;
  3. mints a random **per-agent token**, stores its hash in the agent registry, and returns
     the plaintext token to the agent in the `HelloAck`.
  The agent persists the minted token to its state dir as `fleet-token` (0600).
- **Subsequent connects (authentication):** the agent presents its per-agent token. The
  server resolves it to the bound identity. The **authenticated name** — not the
  self-asserted `Hello.agent_name` — is what the server trusts for routing, storage, and
  command targeting.
- **Revocation:** `marshal server agent rm <name>` deletes the registry entry; that agent's
  token stops authenticating. Rotating the enroll token (`marshal server token --rotate
  enroll`) does not disturb already-enrolled agents.

### 3.3 Operator authentication — single admin token

- The server generates and prints an **admin token** on first run (separate from the enroll
  token, hashed at rest).
- The CLI sends the admin token on every operator RPC: `ListFleet`, `FleetMetricsHistory`,
  `FleetLogsHistory`, `FleetControl`.

### 3.4 Server-side authorization

- A gRPC interceptor (unary **and** stream) enforces credentials *before* any handler runs:
  - operator RPCs require a valid **admin token**;
  - `Connect` requires either a valid **enroll token** (enrollment) or a valid **per-agent
    token** (authenticated reconnect).
- A request lacking a valid credential is rejected with `codes.Unauthenticated` (missing /
  malformed) or `codes.PermissionDenied` (recognized but not allowed), and **no command is
  ever routed**. This is the concrete "authorized server-side" guarantee:
  `FleetControl` cannot reach the broker without passing the interceptor.

## 4. Wire format

Tokens travel in **gRPC metadata**, not proto messages — auth stays orthogonal to the data
model. Metadata keys:

- `marshal-token` — the per-agent token (on `Connect` reconnect) or the admin token (on
  operator RPCs).
- `marshal-enroll` — the enrollment token, sent only on a first-time `Connect`.

For the `Connect` stream, metadata is attached at stream open; for unary operator RPCs,
per-RPC.

**Only one proto change:** `HelloAck` gains `string agent_token = 3;` — the minted per-agent
token returned on successful enrollment (empty on a normal authenticated reconnect).
`Hello.agent_name` remains the *requested* name; the server simply stops trusting it as
identity once a credential is bound.

```proto
message HelloAck {
  int64  last_metric_ts_ms = 1;
  int64  last_log_ts_ms    = 2;
  string agent_token       = 3; // M10: minted per-agent token (set only on enrollment)
}
```

## 5. Config & on-disk layout

### Agent (`config.ServerConfig`)

New fields:

```yaml
server:
  address: host:9000
  name: dev-1
  token: <enrollment-token>      # pasted once by the operator; used only until enrolled
  fingerprint: <sha256-hex>      # pinned server cert fingerprint
  ca: /path/to/ca.pem            # optional; BYO-cert path instead of pinning
```

- The **minted per-agent token is never in YAML.** It lives in the state dir
  (`store.Dir()`) as `fleet-token` (0600).
- Connect logic: if `fleet-token` exists, authenticate with it; otherwise enroll using
  `token` and persist whatever `HelloAck.agent_token` returns.
- Validation: in fleet mode (`Server != nil`), exactly one of `fingerprint` or `ca` must be
  set, and `token` is required unless a `fleet-token` already exists.

### Server data dir (`$XDG_DATA_HOME/marshal-server`)

- `cert.pem`, `key.pem` — TLS material (generated or operator-supplied).
- `auth.json` (0600) — `{ enroll_token_hash, admin_token_hash, agents: { <name>:
  { token_hash, enrolled_at } } }`.
- First run generates all three secrets, writes them, and prints the enroll token, admin
  token, and fingerprint **once**.

### CLI

`resolveServer` is extended to a `resolveServerAuth` that resolves `--server` / `--token` /
`--fingerprint` from flags → env (`MARSHAL_SERVER`, `MARSHAL_TOKEN`, `MARSHAL_FINGERPRINT`)
→ `~/.config/marshal/cli.yaml`.

## 6. New CLI surface

| Command | Purpose |
|---------|---------|
| `marshal server fingerprint` | Reprint the server cert's SHA-256 fingerprint |
| `marshal server token [--rotate enroll\|admin]` | Show, or rotate, the enroll/admin token |
| `marshal server agent ls` | List enrolled agents (`name`, `enrolled_at`) |
| `marshal server agent rm <name>` | Revoke an agent (delete its registry entry) |

## 7. Migration / transport policy

Fleet mode is **TLS-and-auth-only — no plaintext fallback** (spec §6: "all transport TLS").
There is no released userbase to migrate and the M9 smoke setup is disposable. No
`--insecure` escape hatch is added — such flags quietly become the default in real deploys.
Existing tests that dial insecure are migrated to an in-process TLS+token harness (a helper
that boots a server with a known cert/token pair).

## 8. Testing (TDD per phase)

**Unit**
- Fingerprint verification: match accepts, mismatch rejects.
- Token hashing/verify: round-trip, wrong token rejected, constant-time compare.
- Enrollment: first connect mints + persists `fleet-token`; reconnect authenticates with it;
  name already bound to a different credential is rejected; bad/empty enroll token rejected.
- Interceptor: each RPC class accepts the correct credential and rejects the wrong one /
  none (`Unauthenticated` vs `PermissionDenied`).

**E2E**
- Extend the existing real-stream round-trip test to run over TLS with enrollment, then
  assert an unauthenticated `FleetControl` is rejected **before** reaching the broker.

## 9. Implementation phases (one spec, one branch `m10-auth-tls`)

Each phase is green (`go test ./... -race`, `gofmt`, `go vet`) before the next.

1. **TLS transport + fingerprint pinning** — server cert gen / load, client pin verifier;
   no auth yet. Migrate the dial sites and test harness to TLS.
2. **Auth interceptor + admin token** — server secret generation/storage (`auth.json`),
   unary+stream interceptor, admin token on operator RPCs.
3. **Agent enrollment + per-agent identity** — `HelloAck.agent_token`, agent registry,
   first-connect mint + persist, reconnect auth, server trusts the authenticated name.
4. **Server CLI subcommands + config wiring + docs** — `server fingerprint/token/agent`,
   `ServerConfig` fields + validation, CLI auth resolution, handoff doc.

## 10. Open questions / deferred

- **Dashboard sessions** (username/password) — deferred to the dashboard sub-project (#4).
- **Read vs admin token tiers** — deferred; single admin token for now.
- **Token rotation for per-agent tokens** — revoke + re-enroll covers it; explicit rotate
  is future work.
- **Audit log of operator commands** — still deferred (carried from M9).
- **Best-effort `FleetControl` timeout semantics** — unchanged from M9.
