# M17 Login-Attempt Audit Log — Handoff

**Date:** 2026-06-18
**Branch:** `m17-login-audit` (ready to merge; see "Concrete next step")
**Gate:** green — `go test ./... -race -count=1` passes (20 packages, incl. the new
`internal/audit`), `gofmt -l .` silent, `go vet ./...` clean, `go build -o marshal ./cmd/marshal`
succeeds. No `web/src/` changes (CLI-only surface).

---

## Current state

M17 is complete (pending merge). It adds a durable, disk-bounded record of dashboard login
attempts and a CLI to read it — closing the M15-era open item *"no login-attempt audit log
(the limiter is in-memory only)."*

Work touches a new leaf package `internal/audit`, the dashboard (`internal/dashboard`), the
server wiring (`internal/server/server.go`, one call site), and the CLI (`cmd/marshal`). No
proto, agent, or manager changes.

Design spec: `docs/superpowers/specs/2026-06-18-marshal-dashboard-m17-login-audit-log-design.md`.
Implementation plan: `docs/superpowers/plans/2026-06-18-marshal-dashboard-m17-login-audit-log.md`.

Branch commits (newest first):

```
df6ff58 feat(cli): add 'marshal server audit' to view login attempts
fb415b0 feat(dashboard): record login attempts to the audit log
39362a0 feat(audit): append-only rotating login-attempt log + reader
36375dc docs: M17 login-audit-log implementation plan
9f2ac18 docs: M17 login-audit-log design spec
```

(Branched from `36375dc` on `main`.)

---

## What was built

### 1. `internal/audit` — leaf package (commit 39362a0)

A self-contained package that imports neither `dashboard` nor `server` (so both can depend on
it cycle-free).

- `Event{Time time.Time; User, IP, Outcome string}` (JSON tags `time,user,ip,outcome`).
  **Passwords are never stored** — the struct has no password field.
- Outcome constants: `OutcomeSuccess = "success"`, `OutcomeInvalid = "invalid_credentials"`,
  `OutcomeRateLimited = "rate_limited"`. `DefaultMaxBytes = 5 MiB`.
- `Log` writer (`audit.go`): `New(path, maxBytes)` + `Record(ev)`. `Record` is nil-safe (a nil
  `*Log` is a no-op), concurrency-safe (a mutex spans stat→rotate→open→append), and marshals
  outside the lock. **Rotation:** when the file reaches `maxBytes` it `rename`s to `path+".1"`
  (overwriting any prior `.1`) and starts fresh — disk bounded to ~2× cap, recent history
  preserved, current write never lost. File mode `0600`. **All I/O errors are logged and
  swallowed** — auditing must never break login.
- `Read(path, ReadOptions{Limit, FailuresOnly})` (`read.go`): reads `.1` then the current file
  (chronological), skips corrupt/blank lines, treats a missing file as empty (no error),
  applies `FailuresOnly` (drop successes) then `Limit` (last N).

### 2. Dashboard recording (commit fb415b0)

- `handler` gained `audit *audit.Log` (nil ⇒ disabled). `newHandler` takes an `auditPath`
  (8th arg) and builds the writer only when non-empty. **`NewHandler`'s exported signature is
  unchanged** (passes `""`), so the ~18 existing handler tests run with auditing disabled.
- `login` records exactly one event per exit path, with `clientIP(r)` and the submitted
  `body.User`: the `rate_limited` event fires in the 429 branch **before**
  `VerifyDashboardUser` (no verify-timing side channel), `invalid_credentials` on verify
  failure, `success` after the session is minted.
- `dashboard.Serve` gained `auditPath`; `internal/server/server.go` passes
  `<dataDir>/login-audit.log`.

### 3. `marshal server audit` CLI (commit df6ff58)

New subcommand alongside `passwd`/`token`/`agent`. Flags: `--data-dir`, `--limit N`
(default 50, most recent N), `--failures` (exclude successes). Reads via `audit.Read` and
prints `TIME  OUTCOME  USER  IP` oldest→newest; an empty log prints "no login attempts
recorded" to stderr and exits 0.

---

## Build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .          # must print nothing
go vet ./...
marshal server audit --data-dir <dir> [--limit N] [--failures]
```

Login attempts on a running server are appended to `<dataDir>/login-audit.log` immediately;
`marshal server audit` reads them (no server needed — it reads the file directly).

---

## Review outcome

Per-task reviews (fresh reviewer each): Task 1 ✅/Approved, Task 2 ✅/Approved, Task 3
✅/Approved. Final whole-branch review (opus): **READY TO MERGE** — no Critical/Important
issues. It verified end-to-end that no password or secret can reach the log, the
`rate_limited` record fires before verify (no timing side channel), `Record` is nil-safe and
locks correctly, rotation bounds disk without losing the current write, and the reader
tolerates corrupt/partial/missing files. All known Minors were triaged **accept**.

### Deferred / known issues (Minor — accepted)

- The CLI prints tab-delimited columns (one `%-19s`-padded outcome field), not `text/tabwriter`
  fixed-width, and has no header row. Readable; a cosmetic follow-up. (Consistent with the
  project's "function over design" stance.)
- `audit.Read` uses `os.IsNotExist` (pre-1.13 idiom) rather than `errors.Is(fs.ErrNotExist)`.
- On a rare `rename` failure during rotation the file keeps growing past the cap — deliberate
  (errors swallowed so login never breaks); could carry a clarifying comment.

### Out of scope (per the spec, still open)

- Dashboard `/api/audit` endpoint + UI to view the log in-browser (natural follow-up).
- gRPC agent-auth attempt auditing (this is dashboard logins only).
- Configurable cap / multi-file retention beyond one `.1`; tamper-evidence; alerting; geo/IP.

---

## Live-demo result (2026-06-18, scratch `/tmp/marshal-m17-demo`, server `:19370`/`:19371`)

Verified end-to-end against a real running server (no agent — auth-layer only):

1. Password set while down; server started with `--http-listen`; no-cookie `/api/fleet` → 401.
2. `admin/hunter2` login → 200, recorded `success`.
3. Five `admin/wrong` logins → 401 each (recorded `invalid_credentials`); the 6th → 429
   (recorded `rate_limited`).
4. `login-audit.log` is mode `-rw-------` (0600).
5. `marshal server audit` printed all 7 rows oldest→newest with correct outcomes/user/IP;
   `--failures` excluded the `success` row (6 rows); `--limit 2` printed only the two most
   recent.
6. Teardown: server stopped, scratch removed; `pgrep -fl marshal` shows only the user's
   pre-existing daemon (pid 84457), untouched. No demo orphans.

---

## Concrete next step

1. **Merge `m17-login-audit` to `main`** via the `finishing-a-development-branch` skill (final
   whole-branch review: READY TO MERGE; all Minors accepted).
2. **Next milestone: M18 — server-side log search** in the dashboard (the last item in the
   current program of work; needs its own brainstorm → spec → plan → TDD). Candidate scope:
   search/filter across stored fleet logs from the dashboard and/or a `fleet logs --grep` CLI.
