# Marshal Dashboard M17 — Login-Attempt Audit Log — Design

**Date:** 2026-06-18
**Status:** approved (pending implementation)
**Scope:** new `internal/audit` package; `internal/dashboard` (writer wiring); `internal/server`
+ `cmd/marshal` (CLI viewer). No proto, agent, or manager changes.

## Problem

After M15, repeated failed dashboard logins are rate-limited, but the limiter is
**in-memory only** — nothing about who attempted to log in, from where, or with what
outcome survives a restart. An operator has no durable record to answer "was this account
sprayed?" or "did someone log in successfully at 3am?". M17 adds a persisted, bounded
audit log of dashboard login attempts and a CLI to read it.

## Goal

Record every dashboard login attempt — success, bad credentials, or rate-limited rejection —
to a durable, disk-bounded, append-only file, and provide a `marshal server audit` command
to view recent entries. Auditing must never break or slow the login path.

## Non-goals

- Dashboard API/UI for the audit log (CLI-only this milestone).
- gRPC agent-auth attempts (this is dashboard logins only).
- Geo/IP enrichment, alerting, or SIEM export.
- Tamper-evidence (signing/hash-chaining) — out of scope for a self-hosted v1.

## Architecture

A new **leaf utility package `internal/audit`** owns the on-disk format, the rotating
writer, and the reader. The dashboard imports it as the writer (one record per login
outcome); `cmd/marshal` imports it as the reader (the CLI). Because it is a leaf with no
imports of `dashboard`/`server`, both can depend on it without a cycle or layering
violation.

### Event schema

One JSON object per line (JSONL):

```go
type Event struct {
    Time    time.Time `json:"time"`    // UTC; RFC3339 on disk via encoding/json
    User    string    `json:"user"`    // username as submitted (may be empty/attacker-controlled)
    IP      string    `json:"ip"`      // source IP, port stripped
    Outcome string    `json:"outcome"` // one of the Outcome constants below
}

const (
    OutcomeSuccess  = "success"
    OutcomeInvalid  = "invalid_credentials"
    OutcomeRateLimited = "rate_limited"
)
```

**Passwords are never recorded** — only the submitted username. The file holds usernames
and IPs, so it is written mode `0600`.

### Writer — `audit.Log`

```go
func New(path string, maxBytes int64) *Log
func (l *Log) Record(ev Event)
```

- `Record` is safe for concurrent use (a `sync.Mutex`). It marshals `ev` to JSON, appends a
  newline, and writes to `path` opened `O_APPEND|O_CREATE|O_WRONLY`, `0600`.
- **Rotation:** before writing, if the current file size is `>= maxBytes`, rotate —
  `os.Rename(path, path+".1")` (overwriting any existing `.1`), then create a fresh `path`.
  This bounds disk to ~2× `maxBytes`, preserves recent history, and never rewrites mid-file.
- **Default cap:** `maxBytes` = 5 MiB (a `DefaultMaxBytes` const; the dashboard passes it).
- **Errors are non-fatal:** a marshal/open/write/rotate failure is logged via `log.Printf`
  and swallowed. Auditing must never fail a login. A nil `*Log` (disabled) makes `Record` a
  no-op.

### Reader — `audit.Read`

```go
type ReadOptions struct {
    Limit        int  // 0 = all; else the most recent N events
    FailuresOnly bool // exclude OutcomeSuccess
}
func Read(path string, opts ReadOptions) ([]Event, error)
```

- Reads `path+".1"` (if present) then `path`, concatenating in chronological order (oldest
  first). A missing current/rotated file is not an error; a missing *both* returns an empty
  slice.
- Parses line by line, **skipping any corrupt/blank line** (an audit reader must tolerate a
  partially written tail line).
- Applies `FailuresOnly` (drop successes), then `Limit` (keep the last N after filtering).

### Dashboard wiring

- `handler` gains a field `audit *audit.Log` (nil ⇒ disabled).
- `login` records exactly one event on each exit path, using `clientIP(r)` for the IP and
  the submitted `body.User`:
  - rate-limited (the 429 branch, before `VerifyDashboardUser`) → `OutcomeRateLimited`
  - verify fails → `OutcomeInvalid`
  - success → `OutcomeSuccess`
- `newHandler(..., auditPath string)` constructs the writer when `auditPath != ""`, else
  leaves it nil. `ServeDir → dashboard.Serve → newHandler` threads
  `<dataDir>/login-audit.log` and `audit.DefaultMaxBytes`. **`NewHandler`'s exported
  signature is unchanged** — it passes no audit path, so the ~18 existing handler tests run
  with auditing disabled and are untouched. (Same pattern as M15 session persistence.)

### CLI — `marshal server audit`

New subcommand registered alongside `passwd`/`token`/`agent` in the server command tree.

- Flags: `--data-dir` (defaults to the standard server data dir), `--limit N` (default 50,
  most recent N), `--failures` (exclude successes).
- Resolves the path to `<dataDir>/login-audit.log`, calls `audit.Read`, and prints aligned
  columns `TIME  OUTCOME  USER  IP`, oldest→newest (newest at the bottom, like tailing).
  `TIME` is the event time in local time, RFC3339. An empty log prints a friendly
  "no login attempts recorded" line to stderr and exits 0.

## Data flow

```
POST /api/login ─┬─ locked?      → 429 + audit.Record{rate_limited}
                 ├─ verify fail? → 401 + audit.Record{invalid_credentials}
                 └─ success      → 200 + audit.Record{success}
                                          │ append (rotate at 5 MiB → .1)
                                          ▼
                          <dataDir>/login-audit.log  ──read──  marshal server audit
```

## Error handling

- Writer: all I/O errors logged + swallowed; login proceeds regardless.
- Reader/CLI: a corrupt line is skipped, not fatal. A missing file ⇒ empty result (CLI prints
  the friendly empty message). A genuine read error (e.g. permission) is returned and the CLI
  exits non-zero with the error.

## Testing (TDD)

`internal/audit`:
1. Record→Read round-trip: three events, read back in order with correct fields.
2. Rotation: with a tiny `maxBytes`, enough records create `.1`; the current file restarts;
   total entries across both files are preserved and bounded.
3. Corrupt-line tolerance: a garbage line between valid ones is skipped by `Read`.
4. `FailuresOnly` excludes successes; `Limit` returns the last N after filtering.
5. Missing files: `Read` of a nonexistent path returns an empty slice, no error.

`internal/dashboard`:
6. `login` writes exactly one event with the correct outcome for each of success / bad
   credentials / rate-limited (using a temp-dir audit path), and writes nothing when the
   writer is disabled (nil).

`cmd/marshal`:
7. `server audit` renders recorded entries and respects `--limit` / `--failures`.

Gate: `go test ./... -race -count=1`, `gofmt -l .` silent, `go vet ./...` clean, `go build`.
Then the live-demo convention (drive real logins — good, bad, and a lockout — then
`marshal server audit`), and a handoff doc.

## Out of scope / deferred

- Dashboard `/api/audit` endpoint + UI (natural follow-up).
- Configurable cap / multi-file retention beyond one `.1`.
- gRPC agent-auth audit; alerting; tamper-evidence.
