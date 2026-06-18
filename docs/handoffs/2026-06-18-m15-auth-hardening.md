# M15 Dashboard Auth Hardening — Handoff

**Date:** 2026-06-18
**Branch:** `m15-auth-hardening` (NOT yet merged to main)
**Gate:** green — `go test ./... -race -count=1` passes (19 packages), `gofmt -l .` silent,
`go vet ./...` clean, `go build -o marshal ./cmd/marshal` succeeds. (No `make ui` needed —
no `web/src/` changes; the 429 surfaces through the existing login error path.)

---

## Current state

M15 is complete (pending merge). It hardens the dashboard/server auth surface along the three
axes that had been carried as "open" since M11–M14:

1. **Session persistence** — dashboard logins now survive a server restart.
2. **Login rate-limiting** — repeated failed logins are locked out (429 + `Retry-After`).
3. **Auth hot-reload** — a running server picks up `server passwd` / `token --rotate` changes
   without a restart.

No proto, agent, or manager changes. Work is confined to `internal/dashboard` and
`internal/server`.

Design spec: `docs/superpowers/specs/2026-06-18-marshal-dashboard-m15-auth-hardening-design.md`.
Implementation plan: `docs/superpowers/plans/2026-06-18-marshal-dashboard-m15-auth-hardening.md`.

Branch commits (newest first):

```
a8c79e4 fix(dashboard,server): bound limiter map, refresh mtime on save, tidy tests
ac42b6e feat(server): hot-reload auth.json via mtime-gated poll
7fc34b5 feat(dashboard): rate-limit failed logins with 429 + Retry-After
495b153 feat(dashboard): per-(user,IP) login lockout limiter
0a702bd feat(dashboard): persist sessions to <dataDir>/sessions.json
92c3a1c feat(dashboard): disk-backed session store keyed by token hash
```

(Branched from `6fe7b33` on `main`, which already carries the M15 spec + plan commits.)

---

## What was built

### 1. Session persistence (`internal/dashboard/session.go`, `handlers.go`, `server.go`; commits 92c3a1c, 0a702bd)

- The session store map is **re-keyed by token hash** (`hashSessionToken` = hex SHA-256).
  `create` returns the plaintext token (the cookie value); the store keeps only its hash, so
  a plaintext token never lives in memory or on disk — the same posture as the password hash.
- `newSessionStore(ttl, now, path)` — when `path` is non-empty, the map is **loaded at
  construction** (dropping entries already past `expiry`) and **persisted on every mutation**
  (`create`/`delete`/sweep) via an atomic tmp-file + rename write at mode `0600`. A missing
  file is fine; a corrupt file logs and starts empty (no panic).
- `<dataDir>/sessions.json` is threaded `ServeDir → dashboard.Serve → newHandler →
  newSessionStore`. **Empty path ⇒ pure in-memory, no file I/O** — `NewHandler`'s signature is
  unchanged, so the ~18 existing handler tests are untouched.

### 2. Login rate-limiting (`internal/dashboard/limiter.go`, `handlers.go`; commits 495b153, 7fc34b5)

- `loginLimiter` keyed on the **(username, source-IP) pair** (`clientIP` strips the port from
  `RemoteAddr`; Marshal serves direct TLS so there's no XFF to consult). Injectable `now` for tests.
- **5 consecutive failures ⇒ lockout.** Backoff starts at **1 min and doubles** (1→2→4→8) capped
  at **15 min**; a successful login **resets** the key.
- `login` checks the limiter **first**: a locked key returns **429 + `Retry-After`** *without*
  calling `VerifyDashboardUser` (skips PBKDF2, avoids a verify-timing side channel). In-memory
  only (losing limiter state on restart is acceptable).

### 3. Auth hot-reload (`internal/server/auth.go`, `server.go`; commits ac42b6e, a8c79e4)

- `AuthStore` gained an unexported `mtime` field. `Reload()` `stat`s `auth.json`; if `ModTime`
  is unchanged (compared with `.Equal`, not `==`) it's a cheap no-op. On change it re-reads,
  unmarshals into a fresh `authData`, normalizes nil maps, and swaps `a.data` **under the same
  `a.mu`** the verify methods hold (so the gRPC interceptors and the dashboard both see the new
  data, race-free). A read/parse error keeps the current data and returns the error.
- `ServeDir` starts `go auth.ReloadLoop(ctx, 3*time.Second)`, bound to the server context.
- `save()` now refreshes `a.mtime` after its rename, so a server-originated write (enroll/
  rotate/passwd) doesn't trigger a redundant re-parse on the next poll.

### Final-review fix (commit a8c79e4)

The whole-branch review flagged an **unbounded-growth** risk: `pruneLocked` only evicted
*previously-locked* entries, so an attacker spraying random usernames (≤4 fails each, never
locking) could grow the limiter map without bound. Fixed by evicting **never-locked** entries
on `lastSeen` idle > cap, while keeping previously-locked entries evicted on `lockUntil`-expiry
> cap. The naive "evict on `lastSeen` only" approach was rejected — it re-breaks
`TestLimiterBackoffDoublesAndCaps` by pruning a still-escalating entry on the final cap cycle.
Same commit also: `save()` mtime refresh, removed a dead `time` import suppressor, and made
`TestSessionEmptyPathNoFile` non-vacuous.

---

## Build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .          # must print nothing
go vet ./...
```

Run the dashboard as before (password set while the server is **down**, then start with
`--http-listen`). New behavior: logins now survive a restart; `marshal server passwd` /
`token --rotate` take effect within ~3s on a **running** server (no restart); and repeated bad
logins lock the (user, IP) for 1–15 min.

---

## Live-demo result (2026-06-18, scratch `/tmp/marshal-m15-demo`, server `:9360`/`:9361`)

Verified end-to-end against a real server (no agent needed — M15 is auth-layer only):

1. **Session persistence:** login `admin` → 200; `/api/fleet` with cookie → 200; on-disk
   `sessions.json` held a **hashed** key (`{"<sha256>":{"user":"admin","expiry":...}}`) with no
   plaintext token; killed the server and started a fresh process → the **same cookie still
   returned 200** (no re-login).
2. **Hot-reload:** changed the password to `newpass` via `server passwd` while the server kept
   running (PID unchanged); after the ~3s poll, `admin/newpass` → 200 and the old `hunter2` → 401.
3. **Rate-limiting:** 5 bad logins → 401 each; the 6th → **429 with `Retry-After: 59`**.
4. **Teardown:** demo servers stopped, scratch dir removed; `pgrep -fl marshal` showed only the
   user's pre-existing daemon (pid 84457, default state dir), left untouched. No demo orphans.

**Demo gotcha worth remembering:** the limiter and hot-reload interact — polling the login
endpoint with the *new* password before the reload lands generates failed logins that can lock
the account. Wait quietly past the poll interval, then attempt the login once.

---

## Review outcome

Per-task reviews (fresh reviewer each, Spec ✅ + Quality Approved on all five) + a final
whole-branch review (opus): **READY TO MERGE — WITH FIXES**, all of which were applied in
a8c79e4 and re-reviewed clean (no new issues). The final review validated token-hash-at-rest
(plaintext only in the cookie), atomic `0600` writes, limiter-before-verify with no verify
while locked, the `Reload` swap under the shared `a.mu` (race-clean), and back-compat.

### Deferred / known issues (Minor — carried for a follow-up)

1. **`Reload` reads the file under `a.mu`** (plan-prescribed) — contends with the gRPC verify
   hot path *only* on the rare poll where `auth.json` actually changed (mtime-gated), on a tiny
   file. Idiomatic alternative: read outside the lock, re-check mtime inside before swapping.
2. **`TestReloadCorruptKeepsOldData`** corrupts via `os.WriteFile` (not atomic rename) → a
   theoretical mtime-granularity flake on coarse filesystems (HFS+/ext4 1 s). Safe on this
   machine's APFS. Could harden with `os.Chtimes`.

### Out of scope for M15 (per the spec, still open)

- **Invalidating live sessions when the password/token changes** — sessions are independent of
  the password today; a hot-reloaded change does not kill active dashboard sessions. Natural
  next follow-up.
- Self-signed cert browser warning; multi-user accounts / roles; encrypting `sessions.json`
  beyond hashing; multi-instance / shared-session deployments; login CAPTCHA; no login attempt
  *audit log* (the limiter is in-memory only).

---

## Concrete next step

1. **Merge `m15-auth-hardening` to `main`** via the `finishing-a-development-branch` skill
   (final whole-branch review passed "ready to merge, with fixes"; fixes are applied + re-reviewed).
2. Optionally land the two Minor follow-ups above, and/or **session-invalidation-on-password-change**,
   as a small follow-up.
3. **M16** — next dashboard milestone. Candidates from the still-open lists: session
   invalidation on credential change; a persisted login-attempt audit log; server-side log
   search; or extending the control surface with Start/Delete (needs its own spec).
