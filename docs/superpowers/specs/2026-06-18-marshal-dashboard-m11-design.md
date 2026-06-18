# M11 — Web Dashboard (thin vertical slice): Design

**Date:** 2026-06-18
**Sub-project:** #4 (Web dashboard), milestone M11 — first slice.
**Depends on:** M7–M10 (central server, metric/log storage, command channel, auth/TLS),
all merged to `main`.

## 1. Goal

Stand up the web dashboard as an HTTP/TLS layer on the existing server binary, proving the
whole **serve-UI + JSON-API + username/password session** stack end-to-end with the least
risk. M11 is intentionally a **thin vertical slice**:

- Username/password login → server-side session.
- Serve an embedded single-page app (SPA).
- One read-only data view: a **live process list across all hosts** (auto-refresh).

Deferred to later milestones: metric charts (M12), live log tailing (M13), process controls
(M14), and multi-user accounts (M13+).

The dashboard is **off by default** — it only starts when `--http-listen` is passed. An agent
or a server run without that flag is completely unaffected. Standalone agent mode (no
`server:` block) never touches any of this.

## 2. What the dashboard builds on (all already on `main`)

The server is a single binary holding live state in memory and history on disk:

- `server.Registry.List() []*pb.AgentState` — live fleet state (agents, online/offline via
  `WithOfflineAfter`, last-seen, their `ProcInfo` processes). **M11's only data source.**
- `server.AuthStore` over `auth.json` (0600, atomic tmp+rename, rollback on save failure) —
  already holds hashed enroll/admin tokens + the per-agent registry. **The natural home for
  dashboard credentials.**
- TLS cert/key (`LoadOrCreateCert`) — already terminated by the server; reused for HTTPS.
- (Later milestones will additionally read `stores` (metrics), `logStores` (logs), and the
  `broker`/`FleetControl` path. M11 does not need them.)

## 3. Architecture & package layout

A new **`internal/dashboard/`** Go package serves HTTP over TLS on a **separate listener/port**
from gRPC, reusing the same cert. The handler holds **direct in-process references** to
`*server.Registry` and `*server.AuthStore` — no gRPC loopback.

```
internal/dashboard/
  server.go     // Serve(ctx, addr, reg, auth, cert): http.Server with route mux + auth middleware
  session.go    // in-memory session store: token -> {user, expiry}, mutex + background sweep
  handlers.go   // login, logout, session, fleet handlers
  embed.go      // //go:embed dist  -> http.FileServer over the built SPA
  dist/         // built React assets (committed, so `go build ./...` needs no Node)
web/            // Vite + React + TypeScript source (repo root)
                //   node_modules gitignored; vite build outDir -> ../internal/dashboard/dist
```

**Separate-port rationale:** gRPC stays on `:9000` untouched; the dashboard runs on its own
`--http-listen` addr (e.g. `:9001`) with the same `cert.pem`. This avoids cmux/protocol-sniffing
complexity and keeps each package single-purpose. The minor cost (a second port to expose) is
trivial for a self-hosted tool.

## 4. HTTP API, auth & sessions

All `/api/*` routes **except `/api/login`** require a valid session cookie; missing/invalid →
`401`. Server-side sessions, per the architecture spec.

| Route | Method | Behavior |
|---|---|---|
| `/api/login` | POST | `{user, pass}` JSON → PBKDF2 verify (constant-time) → mint session, set cookie → `200` on success, `401` on bad creds |
| `/api/logout` | POST | invalidate the session, clear the cookie → `204` |
| `/api/session` | GET | returns `{user}` so the SPA knows if it is logged in on load → `200` / `401` |
| `/api/fleet` | GET | JSON view of `Registry.List()` — per agent: name, online bool, last-seen; per proc: name, status, pid, uptime, restarts |
| `/*` (non-API GET) | GET | serve the embedded asset if it exists, else fall back to `index.html` (client-side SPA routing) |

### Cookie & session

- Cookie `marshal_session`: `HttpOnly` + `Secure` + `SameSite=Strict`. Value is a random
  256-bit token (crypto/rand, base64url). Expiry 24h (fixed, non-sliding for simplicity).
- `SameSite=Strict` is the CSRF defense: the API is JSON-only and accepts no HTML form posts,
  so a cross-site request cannot forge a state change with the cookie attached.
- Session store: `map[token]struct{user string; expiry time.Time}` guarded by a mutex, with a
  background goroutine sweeping expired entries. Sessions live **in memory only** → they are
  lost on server restart and the user re-logs in. Acceptable for v1.

### Password storage

Stored in `auth.json` under a **`users` map** (forward-compatible with M13+ multi-user — adding
users is then an additive change, not a migration):

```jsonc
"users": {
  "admin": { "pbkdf2": "<base64 hash>", "salt": "<base64>", "iter": 600000 }
}
```

- Hash: **PBKDF2-HMAC-SHA256**, ~600,000 iterations (OWASP guidance), 16-byte random salt,
  32-byte derived key. Uses Go 1.24+ stdlib `crypto/pbkdf2` — **no new dependency** (the repo
  is on Go 1.26). Verification is constant-time (`crypto/subtle.ConstantTimeCompare`),
  consistent with how `internal/fleetauth` already compares token hashes.
- New `AuthStore` methods: `SetDashboardUser(user, password)` and
  `VerifyDashboardUser(user, password) bool`, following the existing atomic tmp+rename +
  rollback-on-failure save pattern.

## 5. Data wiring & CLI

- `server.ServeDir(...)` gains an `httpAddr` parameter (empty string = dashboard disabled).
  When non-empty, after the registry/stores/auth are built it starts
  `dashboard.Serve(ctx, httpAddr, reg, auth, cert)` in a goroutine alongside the gRPC server;
  both share `ctx` for graceful shutdown. (Threaded as an explicit parameter, not a
  `RegOption`, since `RegOption` configures the `Registry`, not the serve topology.)
- `cmd/marshal/server.go`: new `--http-listen` flag (e.g. `:9001`), passed through to
  `ServeDir`. Default empty (disabled).
- New CLI command **`marshal server passwd [--user admin]`**: prompts for a password with no
  terminal echo and a confirmation, then calls `AuthStore.SetDashboardUser`. Defaults the
  username to `admin`.
- First-run/server-start hint: when `--http-listen` is set but no dashboard user exists, the
  server logs `dashboard: no user set — run 'marshal server passwd'`.

## 6. Frontend & build

- **Vite + React + TypeScript** SPA in `web/`. M11 screens:
  - **Login page** — username + password form, POSTs `/api/login`.
  - **Fleet view** — a table of agents and their processes (agent name + online/offline badge;
    per process: name, status, pid, uptime, restart count). Polls `/api/fleet` every ~2s. On any
    `401`, redirects to the login page.
- `vite build` emits to `internal/dashboard/dist/`. That directory is **committed** so
  `go build ./...` never requires Node (matches CLAUDE.md's "go build just works"). A `make ui`
  target (and documented `npm --prefix web run build`) rebuilds it when the frontend changes.
  `web/node_modules` is gitignored.

## 7. Testing

TDD, Go-side via `net/http/httptest`:

- **Handlers:** `/api/login` success sets a cookie; bad creds → `401`. `/api/fleet` without a
  cookie → `401`; with a valid session → `200` and registry JSON (seed the registry with one
  agent). `/api/logout` clears the session (subsequent `/api/fleet` → `401`). Non-API unknown
  path serves `index.html` (SPA fallback).
- **Session store:** create/validate/expire/sweep units; expired token rejected.
- **AuthStore:** `SetDashboardUser` then `VerifyDashboardUser` roundtrip; wrong password fails;
  PBKDF2 salt is per-user-random (two sets of the same password differ); atomic-save rollback on
  a forced write failure.

Frontend automated tests are **deferred** to a later milestone. M11's UI is verified by a manual
smoke run (the `run` skill): build, set a password, start the server with `--http-listen`, log in,
see a live agent appear in the table.

## 8. Deferred / known issues

1. **Self-signed cert → browser TLS warning.** Expected for v1 (the server auto-generates a
   self-signed cert). Operators supply a real cert via `--tls-cert`/`--tls-key`.
2. **In-memory sessions** are lost on server restart (re-login). Persistent/signed sessions are
   future work.
3. **Single user only.** Multi-user accounts (a populated `users` map + add/remove CLI) land in
   M13+; the schema is already shaped for it.
4. **No login rate-limiting / lockout.** Noted for a later hardening pass.
5. **Committed `dist` build artifacts.** Pragmatic so the Go build stays Node-free; rebuilt via
   `make ui`.
6. **Out of scope for M11:** metric charts (M12), live log tailing (M13), process controls (M14).

## 9. Milestone roadmap (dashboard sub-project)

| Milestone | Scope |
|---|---|
| **M11 (this spec)** | login + server-side sessions + serve SPA + live read-only process list |
| M12 | metric charts (CPU/mem/uptime/restarts over time) reading `stores` |
| M13 | live log tailing reading `logStores`; multi-user accounts |
| M14 | process controls (start/stop/restart) via `FleetControl` |
