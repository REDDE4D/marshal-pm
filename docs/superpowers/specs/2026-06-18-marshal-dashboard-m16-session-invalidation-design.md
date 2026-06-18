# Marshal Dashboard M16 — Session Invalidation on Credential Change — Design

**Date:** 2026-06-18
**Status:** approved (pending implementation)
**Scope:** `internal/dashboard`, `internal/server` (auth only). No proto, agent, or manager changes.

## Problem

After M15, dashboard sessions are persisted and a running server hot-reloads
`auth.json` (so `server passwd` takes effect within ~3s without a restart). But
sessions are **independent of the password**: changing a user's password does
*not* invalidate that user's active dashboard sessions. An attacker who captured
a session cookie keeps access even after the password is rotated in response. This
milestone closes that gap.

## Goal

When a dashboard user's credential changes, every session minted under the *old*
credential is invalidated on its next authenticated request — precisely, without
disturbing other users' sessions or reacting to unrelated admin/enroll token
rotations.

## Non-goals

- Real-time push logout (sessions die on their *next request*, within the existing
  validate path — no websocket/eventing).
- Invalidating sessions on anything other than a dashboard *credential* change
  (admin/enroll token rotation is explicitly out of scope — those are not
  dashboard-user credentials).
- A user-facing "log out all other sessions" control (no per-user session listing).

## Mechanism: credential stamp

Each dashboard credential already carries a fresh random 16-byte salt, regenerated
on every `SetDashboardUser`. A credential *generation* is therefore uniquely
identified by a fingerprint over its stored secret:

```
stamp(user) = hex( SHA-256( PBKDF2 || "." || Salt || "." || Iter ) )
```

Properties:
- Changes **iff** the password changes (new salt ⇒ new PBKDF2 ⇒ new stamp).
- Stable across reads, restarts, and hot-reloads (derived purely from stored fields).
- Opaque outside the server package; the dashboard treats it as an unkeyed string.
- Reveals nothing useful: it is a hash of an already-hashed secret plus a salt.

## Components & changes

### 1. `server.AuthStore.DashboardCredentialStamp` (new method)

```go
// DashboardCredentialStamp returns an opaque fingerprint of user's current
// dashboard credential, or ok=false if the user has no credential. The
// fingerprint changes whenever the password is (re)set.
func (a *AuthStore) DashboardCredentialStamp(user string) (string, bool)
```

- Computed under `a.mu` (same lock discipline as `VerifyDashboardUser`), so it
  reflects hot-reloaded data race-free.
- Unknown user ⇒ `("", false)`.

### 2. `dashboard.Authenticator` interface (extended)

```go
type Authenticator interface {
    VerifyDashboardUser(user, password string) bool
    DashboardCredentialStamp(user string) (string, bool)
}
```

`*server.AuthStore` satisfies it. This is the only signature change to a shared
interface; the dashboard `handler` already holds an `auth Authenticator`.

### 3. `dashboard.session` + `sessionStore` (extended, stays credential-agnostic)

- `session` gains a field: `Stamp string \`json:"stamp"\``.
- `create(user, stamp string)` stores the stamp alongside `User`/`Expiry`.
- `validate(tok)` returns `(user, stamp string, ok bool)` — it still only checks
  existence and expiry; it does **not** know what the stamp means.

The session store never imports an auth concept — it stores and returns an opaque
string. The credential comparison lives in the handler, which already has `auth`.

### 4. `handler.login` and `handler.requireSession` (wiring)

- **login** — after `VerifyDashboardUser` succeeds, fetch
  `stamp, _ := h.auth.DashboardCredentialStamp(user)` and call
  `h.sessions.create(user, stamp)`. (Verify already guarantees the user exists, so
  `ok` is true here; defensively, an empty stamp would simply force re-login.)
- **requireSession** — `user, stamp, ok := h.sessions.validate(cookie)`; if `!ok`,
  401. Then `cur, exists := h.auth.DashboardCredentialStamp(user)`; if `!exists` or
  `cur != stamp`, drop the session (`h.sessions.delete(cookie)`) and return 401.
  Otherwise proceed as today.

## Data flow

```
password change (server passwd)  ──hot-reload (~3s)──▶  AuthStore serves NEW stamp
                                                                │
old session cookie ──▶ requireSession ──▶ validate (live, not expired) ──▶ user,oldStamp
                                                                │
                            DashboardCredentialStamp(user) = newStamp ≠ oldStamp ──▶ 401 + delete
```

## Persistence & back-compat

- `sessions.json` entries gain a `"stamp"` field (atomic 0600 write, unchanged
  mechanism).
- Sessions persisted **before** this upgrade decode with an empty `Stamp`. An empty
  stamp never equals a real (non-empty) stamp, so those sessions force exactly one
  re-login after the upgrade — the secure default.
- `NewHandler` keeps its current signature; the ~18 existing handler tests are
  untouched except where they construct a fake `Authenticator` (which gains the new
  method — see Testing).

## Error handling

- `DashboardCredentialStamp` on an unknown/deleted user ⇒ `ok=false` ⇒
  `requireSession` treats the session as invalid (a deleted user cannot retain a
  live session). This also covers the future user-deletion case for free.
- A login race where the password changes between `VerifyDashboardUser` and
  `DashboardCredentialStamp` mints a session under the *new* stamp (the new
  password the user just authenticated against is the source of truth) — no stale
  session is created.

## Testing (TDD)

Unit (`internal/dashboard`):
1. Session survives normal validate when the stamp is unchanged.
2. Changing the stamp (simulated via a fake `Authenticator` returning a new value)
   invalidates the session on the next request (401) and removes it from the store.
3. A user that the authenticator reports as gone (`ok=false`) is invalidated.
4. A pre-upgrade persisted session (empty stamp) is invalidated against a real stamp.
5. Existing login/logout/limiter tests still pass with the extended fake.

Unit (`internal/server`):
6. `DashboardCredentialStamp` returns stable output across calls, a *different*
   value after `SetDashboardUser` with a new password, and `ok=false` for an
   unknown user.

Integration (`internal/server` serve test or e2e): login → request OK → change
password on disk → `Reload` → same cookie now 401.

Gate: `go test ./... -race -count=1`, `gofmt -l .` silent, `go vet ./...` clean,
`go build`. Then the live-demo convention (CLI + dashboard end-to-end on a scratch
data dir), and a handoff doc.

## Out of scope / deferred

- Real-time/push logout; per-user active-session listing; "log out everywhere" UI.
- Sliding-window session renewal (TTL semantics unchanged from M15).
- Rate-limiter persistence and the login-attempt audit log (separate milestone).
