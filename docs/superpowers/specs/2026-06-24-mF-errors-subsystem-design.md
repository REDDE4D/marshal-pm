# M-F · Errors / Exceptions subsystem — design

**Date:** 2026-06-24
**Program:** dashboard data-first roadmap (`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`).
**Milestone:** M-F (largest of the data milestones; B/C/D/E/G already merged to `dev`).
**Branch (to create):** `mF-errors-subsystem` off `dev`.
**Status:** design approved in brainstorming; ready for an implementation plan.

## Goal

Turn the fleet's raw stderr into a deduplicated **error-signature ledger** so the dashboard can
answer "what's breaking, how often, where, and since when." Today only
`logstore.ErrorCounts` exists (per-process stderr counts over the last 5 minutes) — there is no
grouping, no history beyond a count, and no fleet-wide view.

M-F ships the **backend (signature derivation + `/api/errors`)** plus a **minimal transitional
Errors page**, consistent with how M-B–M-E shipped (data + transitional UI; the polished
"Instrument" treatment lands in **M-A**).

## Key architectural finding (why this is lighter than the roadmap assumed)

The roadmap framed M-F as "reuse M-E's **agent-side** event-store pattern." That assumption is
obsolete: **the server already mirrors every agent's stderr** into its own per-agent SQLite
logstore (`internal/server/logstores.go`), and the dashboard already reads error counts from it
(`logstore.ErrorCounts` → `/api/logstats`). So error-signature grouping can be done **entirely
server-side, compute-on-read**, from data that is already present.

Consequences:
- **No** proto changes, **no** agent changes, **no** fleet-snapshot plumbing, **no** new persisted
  store, **no** ingest-path coupling, **no** migration/backfill.
- Server log retention is **7 days** (prune in `internal/server/server.go`,
  `retentionMs = 7*24h`). That bounds every query, so the worst-case scan is small and a separate
  materialized store is unnecessary (premature optimization; see *Deferred*).

## Approach (chosen)

**Compute-on-read, server-side.** A new **pure** package `internal/errsig` holds all the logic
(classification, normalization, signature hashing, source-location extraction, aggregation). On each
`/api/errors` request the dashboard handler enumerates agents, pulls the window's stderr from the
server logstore, runs `errsig.Aggregate`, and returns JSON.

Rejected alternatives:
- **Materialized server-side `sigstore`** (normalize at ingest, persist a signatures table): faster
  reads but adds a store, schema, dedup/upsert logic, bucket maintenance, and a backfill — too many
  moving parts for an occasionally-viewed page bounded to 7 days. Documented scale-path only.
- **Agent-side store + fleet push** (the roadmap's original framing): redundant now that the server
  already has all stderr; would add proto messages and ship a list over the snapshot. Don't.

## Components

### `internal/errsig` (new, pure — no DB, no I/O)

The brain of the feature; isolated so it is trivially table-driven testable.

- `IsError(text string) bool` — **level heuristic.** Returns `false` for lines carrying a recognized
  **info/warn/debug** marker (`level=info`, `level=debug`, `[INFO]`, `[WARN]`, `DEBUG`, `WARNING`,
  `WARN`, …, case-insensitive). Returns `true` for everything else on stderr — including lines with
  **no** recognizable level (stderr default) and lines with an explicit **error** marker (`ERROR`,
  `ERR`, `FATAL`, `PANIC`, `panic:`, `Exception`, `Traceback (most recent call last):`,
  `level=error`, …). Markers live in small, documented, tweakable tables.
- `Normalize(text string) string` — the **Standard** ruleset, applied in this order, then whitespace
  collapse + lowercase:
  1. strip a leading timestamp (RFC3339 / `YYYY-MM-DD HH:MM:SS(.fff)` / syslog-ish / bracketed).
  2. hex addresses & `0x…` & long hex runs → `<hex>`.
  3. UUIDs → `<uuid>`.
  4. IPv4(`:port`?) / `host:port` → `<addr>`.
  5. quoted strings (`"…"`, `'…'`, backtick) → `<str>`.
  6. file paths (`/abs/...`, `./rel/...`, `C:\…`, optional trailing `:line`) → `<path>`.
  7. durations/sizes (`12ms`, `1.5s`, `4KiB`) and remaining standalone integers/floats → `<num>`.
  Result is the canonical signature text.
- `Signature(text string) string` — `hex(sha256(Normalize(text)))[:12]`. Stable id used as the
  ledger key and the API `id`.
- `Source(window []string) string` — **best-effort** `file:line`. Scans the matched line plus a
  small following window (default 5 lines) for the first match of: Go/C-style `path/file.ext:NNN`
  (ext in a known set), Python `File "X", line N`, generic `at <path>:<line>`. Returns `""` when
  nothing matches. Runs **before** path normalization on the raw text.
- `Aggregate(lines []Line, sinceMs, nowMs int64, nBuckets int) Result` — **pure fold** over a
  `(tsMs, label, text)` stream (ascending by `(label, ts)`), producing the cluster totals and the
  signature list. `Line{TsMs int64; Label, Text string; Agent string}`.

`Result` / `Signature` shape (Go side; JSON below):
- `Cluster{ Errors int; Signatures int; AffectedProcs int; LastErrorUnix int64 }`
- per signature: `Id, Sample, Source, Agent, Proc string; Affected []string; Count int;
  FirstUnix, LastUnix int64; Buckets []int` — `Buckets` has exactly `nBuckets` entries spanning
  `[sinceMs, nowMs]`. Signatures sorted **count desc, then LastUnix desc**.

**Signature identity:** keyed on the **normalized message, fleet-wide**. The same error across N
instances/agents collapses to one row; `Affected` lists the distinct proc labels, `Agent`/`Proc`
hold a representative (most-recent) origin. *(Note: a generic message like "connection refused" can
merge across unrelated apps; the `Affected` list keeps this transparent and an `agent=` filter
scopes it. Adding the agent to the key is a future option if over-merging is observed — flagged,
not built.)*

### `internal/logstore` — one new read method

```go
// StderrSince returns stderr lines for the given labels with ts >= sinceMs,
// ordered by (label, ts) so each proc's lines are contiguous and time-ordered
// (required for Source's following-line window). limit <= 0 means no limit.
func (s *Store) StderrSince(labels []string, sinceMs int64) ([]StoredLine, error)
```
Mirrors the existing `ErrorCounts` query style (`stderr = 1 AND ts >= ?`, `label IN (…)`), but
returns the line text + ts, ordered `label, ts`.

### `internal/server` — agent-level wrapper

Add to `logStores` an agent-scoped helper that resolves the agent's labels and calls
`StderrSince`, mirroring the existing `ErrorCounts(agent, sinceMs)` wrapper:
```go
func (s *logStores) StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error)
```
Unknown agent → `(nil, nil)`.

### `internal/dashboard` — the endpoint

- Extend the existing `LogsHistory` interface (same `*server.logStores` dependency) with
  `StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error)`.
- New handler `GET /api/errors`, registered like the other read endpoints
  (`h.requireSession(h.errors)`).
- The handler enumerates agents via `h.lister.List()` (optionally a single `agent=`), pulls each
  agent's stderr since the window start, tags each line with its agent, runs `errsig.Aggregate`,
  and writes JSON. Cluster stats reflect the **selected range**.

## Data flow

```
agent stderr ──(existing log stream)──> server per-agent logstore (7-day SQLite)
                                                   │
GET /api/errors?range&agent ── handler ── lister.List() ── per agent: StderrSince(window)
                                                   │            │ tag with agent name
                                                   └── errsig.Aggregate(lines, since, now, 24)
                                                                  └── JSON (cluster + signatures)
```

## API

`GET /api/errors?range=24h&agent=<optional>` — `range ∈ {24h, 7d, all}`; `all` clamps to the
7-day retention. Invalid/missing range → `24h`. Auth: session-gated like every read endpoint.

```jsonc
{
  "range": "24h",
  "since": 1750000000000,        // window start, unix ms
  "now":   1750086400000,        // server now, unix ms
  "cluster": {
    "errors":          1240,     // total error occurrences in window
    "signatures":      7,        // distinct signatures
    "affected_procs":  3,        // distinct proc labels with >= 1 error
    "last_error_unix": 1750086390 // most recent error ts (unix sec); 0 if none
  },
  "signatures": [
    {
      "id":         "9f2a1c4e8b0d",
      "sample":     "panic: runtime error: invalid memory address or nil pointer dereference",
      "source":     "worker.go:142",          // "" when none extracted
      "agent":      "edge-1",                  // representative (most recent) origin
      "proc":       "api#0",
      "affected":   ["api#0", "api#1"],        // distinct proc labels
      "count":      980,
      "first_unix": 1750001200,
      "last_unix":  1750086390,
      "buckets":    [0,0,3,12,40,...]          // exactly 24 ints across [since, now]
    }
  ],
  "truncated": false              // true only if the maxScan guard tripped
}
```
Signatures sorted **count desc, then last_unix desc**. `buckets` length is a fixed constant
(`24`) regardless of range, so the UI renders a consistent bar-sparkline.

**Guard:** a `maxScan` cap bounds the worst-case line pull per request; if hit, the response sets
`"truncated": true` (surface, never silently drop — *no silent caps*).

## Transitional UI (minimal; M-A delivers the styled page)

- `web/src/router.ts`: add `{ name: "errors" }` for `#/errors`.
- `web/src/Errors.tsx` (new): range selector (`24h · 7d · all`); a cluster line
  (Errors · Signatures · Affected procs · Last error); a `<table>` of signatures — mono truncated
  `sample`, `count`, last-seen (existing `ago()`), affected-proc count, `source`, and a tiny inline
  bar-sparkline from `buckets` (parallel to `Sparkline.tsx`). Loading / empty / error states.
- `web/src/api.ts`: `getErrors(range, agent?)` typed fetch + the `ErrorsResponse` types.
- A nav link to `#/errors` from the Overview header (no red count badge yet — M-A polish).

Functional, not styled-to-spec — enough to demo the data end-to-end in the browser. Rebuild the
embedded bundle with `make ui` and commit it.

## Error handling

- Unknown/absent agent, no store on disk, empty fleet → `200` with zero cluster + empty
  `signatures` (mirrors `logstats`).
- Invalid/missing `range` → default `24h`; `all` clamps to 7-day retention.
- Store read error → `500 "internal error"` (mirrors `logs`).
- `maxScan` guard → `"truncated": true`, partial result still returned.
- Frontend: network failure surfaced in the page error state, not swallowed; no console errors;
  page wrapped so a render error can't blank the app.

## Testing (TDD; subagent-driven per task)

- `internal/errsig` (the bulk): table-driven tests for `IsError` (explicit error markers,
  info/warn/debug exclusion, unknown→error), each `Normalize` rule (timestamps, hex/addr, uuid,
  ipaddr, quotes, paths, nums) + idempotence, `Signature` stability (variants collapse; genuinely
  distinct stay distinct), `Source` (Go panic, Python traceback, generic, none), `Aggregate`
  (grouping, counts, `Affected` set dedup, bucket distribution + boundaries, cluster totals,
  empty input, sort order).
- `internal/logstore`: `StderrSince` — stderr-only, `since` boundary, label `IN` filter,
  `(label, ts)` ordering.
- `internal/server`: `logStores.StderrSince` wrapper — label resolution, unknown agent → empty.
- `internal/dashboard`: `/api/errors` handler against a fake source — range parsing, single-agent
  vs fleet-wide aggregation, JSON shape, empty/unknown, truncation flag, auth gating.
- Full gates before finishing: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .`,
  `make build`. Rebuild UI (`make ui`).

## Deferred / out of scope (M-A or later)

- Styled "Instrument" Errors page, nav **red count badge**, per-signature drill-down / live-log
  modal (all M-A).
- **Agent-keyed** signatures (only if fleet-wide over-merging is observed).
- A **materialized `sigstore`** (the scale-path if compute-on-read ever hurts; unnecessary under
  the 7-day bound).
- Persisting error history beyond the 7-day log retention.

## Definition of done

- `internal/errsig` + `StderrSince` (logstore & server) + `/api/errors` + transitional Errors page,
  all TDD, all gates green, bundle rebuilt & committed.
- `CHANGELOG.md` `[Unreleased]` Added entry.
- Whole-branch opus review clean (no Critical/Important).
- Live demo: an app emitting varied stderr (Go panic + Python-style traceback + "connection
  refused" with varying IPs/ports) → variants collapse to single signatures, counts climb,
  source-loc shows for the panic, cluster + ledger render in the browser; scratch torn down by data
  dir, no orphans.
- Handoff `docs/handoffs/2026-06-24-mF-errors-subsystem.md`; merge `--no-ff` to `dev`.
