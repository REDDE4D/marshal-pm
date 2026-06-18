# M15 — Dashboard Auth Hardening — Design

**Date:** 2026-06-18
**Status:** approved (brainstorm) — ready for implementation plan
**Milestone:** M15 (auth hardening)

## Summary

Harden the dashboard/server auth surface along three independent axes, all carried over as
"open" from the M11–M14 deferred lists:

1. **Session persistence** — dashboard logins survive a server restart (today they live in an
   in-memory map and are lost on every bounce).
2. **Login rate-limiting** — throttle/lock out repeated failed logins to resist online
   password guessing (today `/api/login` runs an unbounded PBKDF2 verify per attempt).
3. **Auth hot-reload** — a running server picks up `server passwd` and `token --rotate`
   changes without a restart (today `auth.json` is loaded once in `ServeDir` and never
   re-read).

No proto, agent, or manager changes. The work is confined to `internal/dashboard`
(`session.go`, new `limiter.go`, `handlers.go`), `internal/server` (`auth.go`, `server.go`
wiring), and tests.

## Background (verified in code)

- **AuthStore** (`internal/server/auth.go:50`) holds, behind a `sync.Mutex`, the enroll/admin
  token hashes, the enrolled-agent map, and the dashboard `Users` map
  (PBKDF2-HMAC-SHA256, 600k iters, per-user 16-byte salt, constant-time verify).
  `LoadOrInitAuth(dir)` reads `<dir>/auth.json` (`0600`, atomic tmp+rename write). The same
  `*AuthStore` backs both the dashboard login (`VerifyDashboardUser`) and the gRPC
  interceptors (`unaryAuth`/`streamAuth` → `verifyAdmin`/`authAgent`/`verifyEnroll`), which
  run on the hot path of every agent RPC.
- **Sessions** (`internal/dashboard/session.go`) are a `map[string]session` keyed by the
  plaintext base64 token; `session{user, expiry}`. Created on login, validated by the
  `requireSession` middleware, swept hourly. Purely in-memory.
- **Login** (`internal/dashboard/handlers.go:99`, `POST /api/login`): decode `{User,Pass}` →
  `VerifyDashboardUser` (401 on miss) → `sessions.create(user)` → set `marshal_session`
  cookie (`HttpOnly`, `Secure`, `SameSite=Strict`). No attempt tracking.
- **Wiring**: `ServeDir` (`internal/server/server.go:355`) loads auth once, builds a shared
  `*Server`, and launches `dashboard.Serve(...)` in a goroutine under the server context.
  The dashboard package today has **no `dataDir` awareness**.
- Marshal serves direct TLS (no reverse proxy), so `http.Request.RemoteAddr` is a
  trustworthy client identifier — no `X-Forwarded-For` spoofing to defend against.
- The project is **stdlib-only** (plus gRPC/protobuf); no file-watcher dependency today.

## 1. Session persistence

Keep the existing **server-side** store (preserves instant logout/revocation) but back it
with disk, storing **only token hashes** at rest.

- **Re-key the in-memory map by token hash** (hex SHA-256), not the plaintext token:
  - `create(user)` → generate 256-bit random token, compute `hash = sha256hex(token)`, store
    `session{user, expiry}` under `hash`, persist, return the **plaintext** token (set as the
    cookie value, exactly as today).
  - `validate(tok)` / `delete(tok)` hash the incoming `tok` and look up by hash.
  - Net effect: a plaintext session token never lives in memory or on disk — only in the
    user's cookie. Same defense-in-depth posture as the password hashing.
- **Persistence format:** `sessions.json` — a JSON object `{ "<hash>": {"user","expiry"} }`,
  written atomically (tmp file + rename, `0600`) on every mutation (`create`, `delete`,
  sweep). Single-user scale ⇒ a full rewrite per mutation is negligible.
- **Load at startup:** read `sessions.json` if present, **dropping entries already past
  `expiry`**. Missing/corrupt file ⇒ start empty (log on corrupt).
- **Path threading:** the session store gains a persistence path. `ServeDir` passes
  `<dataDir>/sessions.json` down through `dashboard.Serve` → `newHandler` →
  `newSessionStore(ttl, now, path)`. **An empty path ⇒ pure in-memory, no file I/O** — this
  preserves today's behavior for the existing dashboard tests and any embedding without a
  data dir. (Same backward-compatible "nil/empty disables it" pattern M14 used for the new
  `controller` argument.)

## 2. Login rate-limiting

New file `internal/dashboard/limiter.go`.

- `loginLimiter` keyed on the **(username, source-IP) pair** (`net.SplitHostPort` of
  `RemoteAddr`; fall back to the raw value if it has no port). Injectable `now func() time.Time`
  for tests, mirroring `sessionStore`.
- Per-key state: consecutive-failure count and `lockedUntil`.
  - **On failed verify:** increment; once failures **≥ 5**, set `lockedUntil = now + backoff`.
  - **Backoff:** starts at **1 minute**, **doubles** on each subsequent lockout for that key,
    **capped at 15 minutes**.
  - **On successful login:** delete the key (full reset).
- **Enforcement in `login`:** check the limiter **first**. If the key is currently locked,
  return **429** with a `Retry-After` header (seconds until unlock) **without** running
  `VerifyDashboardUser` — saves the PBKDF2 cost and avoids a verify-timing side channel.
  Otherwise verify; on failure record the failure (and return the existing 401), on success
  reset.
- Stale entries (unlocked, last activity older than the max backoff) are pruned opportunistically
  (e.g. on access / a lightweight sweep). In-memory only — losing limiter state on restart is
  acceptable (worst case an attacker's lockout clears on a server bounce, which they don't control).

## 3. Auth hot-reload

- `AuthStore.Reload()` (new, `internal/server/auth.go`): under the existing `mu`, `os.Stat`
  the file; if `ModTime` is unchanged since the last successful load, return immediately
  (cheap no-op — this is what makes the poll affordable). On change, read + `json.Unmarshal`
  into a fresh `authData` and swap it in; record the new mtime. A read or unmarshal error
  **keeps the current in-memory data** and logs once (atomic-rename writes guarantee we never
  observe a half-written file). `LoadOrInitAuth` records the initial mtime so the first poll
  is a no-op.
- A **background poll goroutine** in `ServeDir` ticks every **~3 s**, calling `auth.Reload()`,
  bound to the server `ctx` (stops on shutdown). Because the dashboard and the gRPC
  interceptors share the one `*AuthStore`, both pick up `passwd`/`token --rotate` changes
  within one poll interval — no hot-path disk reads.

## Testing (TDD — failing test first for each)

- **session_test.go**: persist→reload roundtrip (new store at same path sees the session);
  entries past expiry dropped on load; `validate`/`delete` work via hash; logout removes the
  entry from disk; empty path writes **no** file; corrupt file ⇒ empty store, no panic.
- **limiter_test.go**: locks after 5 fails; success before lock resets the counter; lock
  expires after backoff; backoff doubles and caps; distinct (user,IP) keys are independent.
- **handlers_test.go / server_test.go**: locked key ⇒ 429 + `Retry-After`, and
  `VerifyDashboardUser` is **not** called while locked; a second IP for the same user is not
  locked; an integration test logs in, rebuilds the handler against the same `sessions.json`,
  and confirms the cookie still validates (the restart-survival guarantee).
- **auth_test.go**: `Reload` picks up a password written to disk by `SetDashboardPassword`;
  unchanged mtime ⇒ no reparse (assert via a sentinel / not-reloaded marker); a corrupt file
  keeps the prior good data.

All gated by `go test ./... -race -count=1`, `gofmt -l .` (silent), `go vet ./...`, and
`go build -o marshal ./cmd/marshal`. No `make ui` needed unless `web/src/` changes (it should
not — the 429 surfaces through the existing login error path; a brief "too many attempts /
try again" message may be a small UI nicety but is **not required** for this milestone).

## Explicitly out of scope (deferred)

- **Invalidating live sessions on password/token change** — sessions are independent of the
  password today; killing active sessions on a hot-reloaded change is a natural follow-up but
  adds a coupling not required here.
- Self-signed cert browser warning; multi-user accounts / roles; encrypting `sessions.json`
  beyond hashing; multi-instance / shared-session deployments; login CAPTCHA.

## Concrete parameters (single source of truth)

| Parameter | Value |
|---|---|
| Session token | 256-bit random, base64url; stored as `sha256` hex |
| Session file | `<dataDir>/sessions.json`, `0600`, atomic tmp+rename |
| Session TTL | 24 h (unchanged) |
| Lockout threshold | 5 consecutive failures per (user, IP) |
| Backoff | 1 min, ×2 per repeat, cap 15 min |
| Locked response | HTTP 429 + `Retry-After` (seconds) |
| Hot-reload poll | ~3 s mtime check, no-op when unchanged |
