# M16 Dashboard Session Invalidation — Handoff

**Date:** 2026-06-18
**Branch:** `m16-session-invalidation` (ready to merge; see "Concrete next step")
**Gate:** green — `go test ./... -race -count=1` passes (19 packages), `gofmt -l .` silent,
`go vet ./...` clean, `go build -o marshal ./cmd/marshal` succeeds. No `web/src/` changes
(the 401 surfaces through the existing session-validation path; no UI work).

---

## Current state

M16 is complete (pending merge). It closes the M15-era open item *"invalidating live
sessions when the password/token changes."* When a dashboard user's password changes, that
user's existing dashboard sessions are invalidated on their next authenticated request —
precisely (only that user; unrelated admin/enroll token rotations do nothing) and surviving
a restart.

Work is confined to `internal/server` (auth) and `internal/dashboard`. No proto, agent, or
manager changes.

Design spec: `docs/superpowers/specs/2026-06-18-marshal-dashboard-m16-session-invalidation-design.md`.
Implementation plan: `docs/superpowers/plans/2026-06-18-marshal-dashboard-m16-session-invalidation.md`.

This session also landed the two M15 follow-up Minors first (already on `main`, commit
`32e648e`): `Reload()` now reads/parses `auth.json` off `a.mu` (swap re-checked under the
lock), and `TestReloadCorruptKeepsOldData` bumps mtime with `os.Chtimes` to avoid a
coarse-granularity flake.

Branch commits (newest first):

```
0f50f2a test(dashboard): assert empty-stamp (pre-upgrade) session is invalidated
3bd3b04 test(server): session dies after password change + reload (e2e)
346f9e7 feat(dashboard): invalidate sessions on credential-stamp change
c775b19 feat(server): add DashboardCredentialStamp for session invalidation
94c4d62 docs: M16 session-invalidation implementation plan
48dd0d2 docs: M16 session-invalidation design spec
```

(Branched from `94c4d62` on `main`, which already carries the M16 spec + plan + M15-cleanup merge.)

---

## What was built — the "credential stamp"

The mechanism is a per-user **credential stamp**: a dashboard credential already carries a
fresh random salt regenerated on every `SetDashboardUser`, so a credential *generation* is
uniquely fingerprinted as

```
stamp(user) = hex( SHA-256( PBKDF2 || "." || Salt || "." || Iter ) )
```

It changes iff the password changes, is stable across reads/restarts/hot-reloads, and is
opaque outside the server package.

### 1. `server.AuthStore.DashboardCredentialStamp` (commit c775b19, `internal/server/auth.go`)

`func (a *AuthStore) DashboardCredentialStamp(user string) (string, bool)` — computes the
stamp under `a.mu` (same lock discipline as `VerifyDashboardUser`, so it reflects
hot-reloaded data race-free), copying the user record out before hashing. Unknown user ⇒
`("", false)`.

### 2. Dashboard enforcement (commit 346f9e7, `internal/dashboard/{handlers,session}.go`)

- `Authenticator` interface gained `DashboardCredentialStamp(user) (string, bool)`.
  `*server.AuthStore` satisfies it.
- `session` gained a `Stamp string \`json:"stamp"\`` field. `create(user, stamp)` records it;
  `validate(tok)` now returns `(user, stamp, ok)`. **The session store stays
  credential-agnostic** — it stores/returns an opaque string; the comparison lives in the
  handler.
- `login` stamps the new session with the user's current stamp.
- `requireSession` calls `validate`, then `DashboardCredentialStamp(user)`; if the user is
  gone (`!exists`) or `cur != stamp`, it deletes the dead session and returns 401.

### 3. Back-compat

Pre-upgrade `sessions.json` entries decode with an empty `Stamp`. An empty stamp never
equals a real one, so those sessions force exactly one re-login after the upgrade — the
secure default. `NewHandler`'s exported signature is unchanged.

### 4. Tests

- `internal/server`: `TestDashboardCredentialStamp` (stable / changes-on-new-password /
  unknown-user) and an e2e `TestDashboardSessionDiesAfterPasswordChange` that drives a real
  `AuthStore` through login → `SetDashboardPassword` + `Reload()` → 401 (added an
  `emptyLister{}` stub, since `fleetView(nil)` panics on the 200 branch).
- `internal/dashboard`: `TestSessionSurvivesUnchangedStamp`, `TestSessionDiesOnStampChange`,
  `TestSessionDiesWhenUserGone`, and `TestPreUpgradeEmptyStampInvalidated` (the empty-stamp
  back-compat path, added to close the final-review Minor).

---

## Build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .          # must print nothing
go vet ./...
```

Behavior: after `marshal server passwd` on a **running** server, that user's existing
dashboard cookies stop working within the ~3s hot-reload window; a fresh login with the new
password works. Other users' sessions and admin/enroll token rotations are unaffected.

---

## Review outcome

Per-task reviews (fresh reviewer each): Task 1 Spec ✅ / Approved, Task 2 Spec ✅ / Approved,
Task 3 Spec ✅ / Approved. Final whole-branch review (opus): **READY TO MERGE** — no
Critical/Important issues; it validated the lock discipline, the `Reload`-swap-under-`a.mu`
race-safety, the empty-stamp back-compat path, and the login-race safety (a delete racing
login records an empty stamp that dies on the next request). Its one Minor (a direct
empty-stamp test, spec Testing item #4) was fixed in `0f50f2a`. Remaining known Minors are
cosmetic and accepted: `DashboardCredentialStamp` uses `Lock` not `RLock` (matches the file's
existing pattern); a swallowed `http.NewRequest` error in a test helper.

---

## Live-demo result (2026-06-18, scratch `/tmp/marshal-m16-demo`, server `:19360`/`:19361`)

Verified end-to-end against a real running server (no agent — auth-layer only):

1. Password set while server down; server started with `--http-listen`.
2. `/api/fleet` without a cookie → 401; login `admin/hunter2` → 200; authenticated
   `/api/fleet` with the cookie → 200.
3. `marshal server passwd` → `newpass` on the **running** server (**pid unchanged**, no
   restart).
4. After waiting past the ~3s reload poll, the **same old cookie → 401** (session
   invalidated); old `hunter2` login → 401; new `newpass` login → 200; new cookie → 200.
5. Teardown: server stopped, scratch removed; `pgrep -fl marshal` shows only the user's
   pre-existing daemon (pid 84457), untouched. No demo orphans.

**Demo gotcha (carried from M15, still relevant):** don't poll `/api/login` with the *new*
password before the reload lands — failed logins can trip the rate-limiter and lock the
(user, IP). Wait past the poll, then attempt once. (This demo only reuses the cookie on
`/api/fleet`, which is not a login attempt, so the limiter never engaged.)

---

## Deferred / known issues

- `DashboardCredentialStamp` uses `Lock` not `RLock` (consistent with `VerifyDashboardUser` /
  `HasDashboardUser`; a file-wide `RLock` pass could be a future cleanup).
- Sessions die on their *next request* (within ~3s), not via push — no websocket/eventing.
  Intentional (spec non-goal).

### Still open (per the M15/M16 specs)

- **Persisted login-attempt audit log** — the rate-limiter is in-memory only; no record of
  attempts survives a restart. (Next milestone candidate — already scoped as a follow-up.)
- **Server-side log search** in the dashboard (larger; needs its own spec).
- Self-signed cert browser warning; multi-user accounts / roles; CAPTCHA; rate-limiter
  persistence; per-user active-session listing / "log out everywhere" UI.

---

## Concrete next step

1. **Merge `m16-session-invalidation` to `main`** via the `finishing-a-development-branch`
   skill (final whole-branch review: READY TO MERGE; the one Minor it raised is fixed).
2. **Next milestone:** the **persisted login-attempt audit log** (brainstorm → spec → plan →
   TDD), then **server-side log search**. Both already have task stubs tracked for this
   program of work.
