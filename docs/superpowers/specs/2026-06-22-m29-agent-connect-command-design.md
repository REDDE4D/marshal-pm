# M29 — Agent Connect-Command Generator

**Date:** 2026-06-22
**Branch:** `m29-agent-connect-command` (off `dev`)
**Status:** design approved; spec under review

A dashboard affordance that generates a ready-to-run **shell one-liner** for connecting a new
agent host to this central server, removing the manual `server token --rotate enroll` +
`server fingerprint` + hand-written `marshal.yaml` dance.

Inserted ahead of the previously-planned coalescing (now M30) and Signal UI (now M31) work, at
the user's request.

---

## Motivation

Enrolling a new agent today is fully manual: an operator runs `marshal server token --rotate
enroll` and `marshal server fingerprint` on the server, then hand-writes a `marshal.yaml`
`server:` block with `address` + `token` + `fingerprint`. There is no dashboard helper. This
feature lets an authenticated operator click **Connect an agent** and copy a command that
brings a new host into the fleet.

## Key constraint (drives the whole design)

The server stores only the **hash** of the enroll token (`AuthStore.data.EnrollTokenHash`,
[internal/server/auth.go](../../internal/server/auth.go)). The plaintext exists for one moment
— when minted/rotated — then is discarded. So the dashboard **cannot display the current
token**; a "generate" action must **mint a fresh enroll token**. The enroll token is a single
shared bootstrap secret (agents receive their own per-agent token *after* bootstrapping), so
minting rotates/replaces it.

Doing the rotation **through the running server's `AuthStore`** (in-memory + persisted via
`Rotate("enroll")`) makes the new token immediately effective for enrollment — which sidesteps
the known caveat that a CLI `token --rotate` against on-disk state is not picked up by a
running server until restart.

## Goals

- An authenticated dashboard user can generate a copy-paste command that enrolls a new agent.
- The minted enroll token is immediately valid against the running server.
- Generating never disrupts already-enrolled agents.

## Non-goals

- Multiple concurrent or one-time enroll tokens (rejected: bigger `fleetauth` change; the
  single-shared-token model is sufficient — generating a new link supersedes a prior unused
  one).
- A new `marshal enroll` CLI verb (rejected for now: the one-liner writes a `marshal.yaml` and
  runs the existing `marshal start`, so no CLI surface is added).
- Any Signal/visual styling — function-first; the styling pass is the later UI milestone.

---

## 1. Backend endpoint — `internal/dashboard`

New route: `POST /api/fleet/connect-token`, registered behind `requireSession` (mirrors
`POST /api/control`).

Request body (JSON, all optional — used only to shape the non-secret default):
```json
{ "address": "host:9000", "name": "agent-2" }
```
`address`/`name` are not validated server-side and not required; the frontend owns them.

Handler behavior:
1. Mint a fresh enroll token via the server's existing rotator (`AuthStore.Rotate("enroll")`),
   returning the new plaintext.
2. Compute the server cert fingerprint via `fleetauth.Fingerprint(cert.Leaf/DER)`.
3. Compute `default_address` = request `Host` (host portion) + ":" + the fleet listen port.
4. Respond `200` with:
   ```json
   { "token": "<plaintext>", "fingerprint": "<hex sha256>", "default_address": "host:9000" }
   ```

The token is returned **once** and **never logged** (the handler logs the action, not the
secret). Errors from `Rotate` → `500` with a generic message.

### Plumbing
The dashboard handler is already constructed with `auth *AuthStore` and the TLS `cert`
([internal/server/server.go](../../internal/server/server.go) `dashboard.Serve(...)`). To keep
the handler decoupled and testable, introduce a small interface it depends on, e.g.:
```go
// EnrollMinter mints a fresh enroll token and exposes the server cert fingerprint
// and the fleet listen address used to build the default connect address.
type EnrollMinter interface {
    RotateEnrollToken() (string, error)
    Fingerprint() string
    FleetAddress() string // host:port the server listens on for agents, e.g. ":9000"
}
```
A thin adapter in `internal/server` satisfies it from `AuthStore` + `cert` + the fleet listen
address. The fleet listen address must be plumbed into the dashboard (it currently receives the
HTTP addr, not the gRPC addr) — pass it through `dashboard.Serve`.

Only the **port** of `FleetAddress()` is used for `default_address` (combined with the request
host); a bare `:9000` yields port `9000`.

## 2. Frontend — `web/src`

A **"Connect an agent"** control on the Overview (where fleet/agents are shown). Clicking it
reveals an inline panel:
- Editable **agent name** (default e.g. `agent`) and **address** (prefilled from the endpoint's
  `default_address` once generated, or from `window.location.hostname` before).
- A **Generate** button → `POST /api/fleet/connect-token` → renders the one-liner:
  ```
  cat > marshal.yaml <<'EOF'
  server:
    address: HOST:PORT
    name: AGENT_NAME
    token: <minted token>
    fingerprint: <fp>
  EOF
  marshal start marshal.yaml
  ```
- A **copy** button and a visible **warning**: "Shown once. Generating rotated the enroll
  token — any previously generated, unused command no longer works. Already-connected agents
  are unaffected."

The frontend composes the command string from the structured response (keeping command format
a display concern the later UI pass can restyle). `api.ts` gains a typed
`connectToken(address?, name?)` call and a `ConnectToken` response type. `make ui` regenerates
the embedded bundle (committed).

## 3. Security

- Session-gated (unauthenticated → `401`), consistent with other mutating dashboard endpoints.
- Token returned once in the response body; never written to server logs.
- Fingerprint and address are not secrets.
- Rotation supersedes only a prior unused enroll token; per-agent tokens of enrolled agents are
  untouched, so enrolled agents keep working.

## 4. Testing (TDD — failing test first)

Backend (`internal/dashboard`):
- `connect-token` with a session → `200`, body has non-empty `token`, `fingerprint`, and a
  `default_address` whose port matches the configured fleet port; the response is shaped as
  specified.
- The **minted token actually enrolls**: feeding the returned token to the auth verify path
  succeeds, and the **previous** enroll token is rejected (rotation took effect). Use a fake
  `EnrollMinter` whose `RotateEnrollToken` returns a known value, and assert the handler
  surfaces exactly that token + fingerprint + computed address. (The real rotate→verify round
  trip is covered by existing `internal/server/auth` tests; here we assert the handler wiring.)
- Unauthenticated request → `401` (no session).
- Token is not present in any logged output (assert via a captured log buffer if the handler
  logs).

Frontend: build succeeds; the live demo exercises the rendered command end-to-end.

## 5. Verification / live demo (after implementation)

Per the project convention: build, run a scratch server on `:9000`/`:9001`, log in, click
**Connect an agent**, copy the generated one-liner, and **actually run it on a second scratch
agent data-dir** to enroll a new agent — then confirm the agent appears in `server agent ls` /
the Overview. Also confirm an *old* generated command fails after a re-generate. Tear down by
data-dir + PID; preserve the standing launchd daemon; `pgrep -fl marshal` clean.

## Concrete next step

Invoke writing-plans to turn this into a task-by-task implementation plan, then execute via
subagent-driven TDD (the M27/M28 pattern).
