# Marshal Dashboard M18 — Server-Side Log Search — Design

**Date:** 2026-06-18
**Status:** approved (pending implementation)
**Scope:** `internal/logstore`, `internal/server`, `internal/dashboard`, `web/src`,
`proto` + `internal/pb`, `cmd/marshal`. No agent or manager changes.

## Problem

Marshal stores fleet logs server-side in per-agent SQLite (`internal/logstore`), but there
is **no text search anywhere in the stack**. The only filtering is by identifier
(agent/selector), stream (stdout/stderr), and count. The dashboard has a search box
(`web/src/LogView.tsx`), but it filters purely client-side over the ≤5000 lines already in the
browser — it cannot see older history still on disk. This milestone adds a single
case-insensitive **substring** search, pushed into SQLite so it scans the full stored
history, surfaced on both the dashboard and the `fleet logs` CLI.

## Goal

Let an operator find log lines containing a literal substring across an agent+selector's full
stored history, from the dashboard (`/api/logs?q=`) and the CLI (`fleet logs --grep`), with
one shared, efficient match implementation.

## Decisions

- **Match semantics:** case-insensitive literal substring, implemented as SQLite
  `text LIKE '%needle%' ESCAPE '\'`. SQLite `LIKE` is case-insensitive for ASCII — matching
  the current client-side box. No regex, no FTS5 (each would add a separate code path or an
  index migration for marginal benefit).
- **One core, two surfaces:** the filter lives in `internal/logstore`; the dashboard and the
  gRPC/CLI path both call into it. No duplicated matching logic.
- **Per agent+selector:** search is still scoped to one agent and one selector (app or
  `name#instance`), exactly like log fetching today. No cross-agent search.

## Non-goals

- Regex / FTS / fuzzy matching; match highlighting in the UI; time-range filters; multi-agent
  search; persisting search history. (All deferred.)

## Architecture

### 1. Core — `internal/logstore`

A new helper and a new argument on the two query methods:

```go
// escapeLike makes s a literal for a LIKE pattern: backslash-escapes \, %, _.
func escapeLike(s string) string

func (s *Store) Tail(label string, limit int, filter StreamFilter, text string) ([]StoredLine, error)
func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter, text string) ([]StoredLine, int64, error)
```

- When `text == ""`, behavior is unchanged (no `LIKE` clause appended).
- When `text != ""`, the SQL gains `AND text LIKE ? ESCAPE '\'` with parameter
  `"%" + escapeLike(text) + "%"`. The match happens in SQLite over all rows for the
  resolved labels — full-history search, not a post-filter on a fetched window.
- `MergeTail` and the `(label, ts)` index are unchanged; substring search is a scan within
  the already index-narrowed label set (acceptable for a self-hosted log store; FTS is the
  documented future optimization).

`internal/server/logstores.go`: `logStores.Since(agent, selector, afterRowID, limit, filter, text)`
gains the `text` argument and forwards it to `store.Since`.

### 2. Dashboard surface

- `dashboard.LogsHistory` interface gains the `text string` parameter on `Since`:
  ```go
  Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error)
  ```
  `*server.logStores` satisfies it after the core change.
- `/api/logs` reads a new optional `q` query parameter and passes it to `Since`. All other
  params (`agent`, `selector`, `after`, `limit`, `stream`) and the JSON response shape
  (`logsView{Cursor, Lines}`) are unchanged.
- **Frontend (`web/src`):**
  - `api.ts` `getLogs` gains a `q` option, added to the query string.
  - `Fleet.tsx`: the existing `logSearch` state is lifted server-side. When `logSearch`
    changes, reset the cursor to `0`, clear `logLines`, and backfill the newest N **matching**
    lines; subsequent polls send `q` so the live tail is filtered too. (Debounce the input by
    ~250 ms so each keystroke does not refetch.)
  - `LogView.tsx`: drop the client-side `lines.filter(...)` substring filter — the server is
    now authoritative. `LogView` renders whatever the server returned.

### 3. CLI / gRPC surface

- **Proto** (`proto/marshal/v1/fleet.proto`): add `string grep = 5;` to
  `FleetLogsHistoryRequest`. Regenerate `internal/pb`.
- **Server** (`FleetLogsHistory`): pass `req.GetGrep()` into `store.Tail`'s new `text`
  argument. An empty `grep` preserves today's behavior.
- **CLI** (`cmd/marshal/fleet.go`): add `--grep <text>` to `fleet logs`; set
  `FleetLogsHistoryRequest.Grep`. Help text: "only lines containing this substring
  (case-insensitive)".

## Data flow

```
dashboard:  GET /api/logs?...&q=needle ─▶ LogsHistory.Since(...,text) ─▶ logStores.Since ─▶ store.Since
                                                                                              │ SQL: ... AND text LIKE '%needle%' ESCAPE '\'
CLI:  fleet logs <agent> <sel> --grep needle ─▶ gRPC FleetLogsHistory{Grep} ─▶ store.Tail(...,text) ─┘
```

## Error handling

- A `q`/`grep` containing `%` or `_` is treated literally via `escapeLike` — no SQL-injection
  surface (parameterized query) and no accidental wildcard.
- Empty `q`/`grep` ⇒ no filtering (identical to current behavior).
- No new error conditions; the search is just an added `WHERE` term.

## Testing (TDD)

`internal/logstore`:
1. `Tail`/`Since` with a `text` filter return only matching lines; case-insensitivity holds.
2. `escapeLike`: a needle containing `%`/`_`/`\` matches those characters literally, not as
   wildcards.
3. Empty `text` ⇒ unchanged result set (regression guard).

`internal/server`:
4. `logStores.Since` forwards `text`; `FleetLogsHistory` with `Grep` set returns only matching
   lines (and unset `Grep` is unchanged).

`internal/dashboard`:
5. `/api/logs?q=` returns only matching lines (via a fake `LogsHistory` asserting the `text`
   arg is threaded, plus an end-to-end check through `Since`).

`cmd/marshal`:
6. `fleet logs --grep` sets `FleetLogsHistoryRequest.Grep` (flag wiring).

Frontend: verified in the live browser demo (type a substring → matching lines from full
history appear; clearing restores the live tail).

Gate: `go test ./... -race -count=1`, `gofmt -l .` silent, `go vet ./...` clean, `go build`,
and `make ui` (frontend builds). Then the live demo (CLI `--grep` + the dashboard search box
in-browser) and a handoff.

## Out of scope / deferred

- Regex/FTS5; match highlighting; time-range and multi-agent search.
- An FTS5 index as a performance optimization for very large stores (substring `LIKE` is a
  table scan within the label-narrowed set; fine for self-hosted scale, revisit if needed).
