# M13 — Live log tailing in the dashboard (design)

**Date:** 2026-06-18
**Status:** approved (brainstorm complete) — pending implementation plan
**Milestone:** M13 (next dashboard milestone after M12 metric charts)

## Goal

Surface a process's captured stdout/stderr logs in the web dashboard, tailing new
lines as they arrive. This is the log analog of M12: M12 surfaced the server's stored
CPU/mem history; M13 surfaces the server's stored log lines.

## What already exists (the starting line)

- **Capture & storage are done.** Agents push log batches to the server
  (`AgentMessage_Logs` → `Server.storeLogBatch`), persisted per-agent in SQLite
  (`internal/logstore`). `logstore.Store` supports `Tail(label, limit, filter)`,
  `MergeTail` across instances, `MaxTs`, `Labels`, `Prune`, and a `StreamFilter`
  (stdout / stderr / any).
- **The server already serves one-shot log queries** over gRPC
  (`Server.FleetLogsHistory`): newest-N lines for a selector, merged across instances,
  stream-filtered. The CLI `marshal fleet logs` and `marshal logs -f` use it.
- **What's missing is the dashboard surface.** There is no `/api/logs` HTTP endpoint
  and no log UI. M12 established the exact pattern to mirror: a session-guarded JSON
  endpoint backed by a structural interface (`MetricsHistory`, implemented by
  `*server.stores`), polled from React in a click-to-expand per-process detail panel.

## Key decisions (from brainstorming)

1. **Transport: poll with a cursor.** No SSE / server-push. The dashboard is already
   entirely poll-based (fleet every 2s, metrics every 10s); logs follow that model. A
   "live" feel comes from a ~1.5s poll that fetches only lines newer than a cursor. No
   new transport, no new dependency.
2. **Placement: a tab in the existing M12 detail panel.** The per-process expandable
   panel gains a **Charts | Logs** toggle. Per-process scope (an app name / selector,
   merged across its instances), exactly like the metrics detail panel.
3. **Controls in scope:** stream filter (all/stdout/stderr), auto-scroll with
   pause-on-scroll-up, backfill depth selector, and client-side substring search.

## The cursor — rowid, not timestamp

Each SQLite row in `log_line` has an implicit monotonic `rowid`. The cursor is the
**rowid**, not `ts`. This:

- sidesteps the same-millisecond drop/duplicate problem a `ts`-based cursor would have
  (many lines can share one millisecond);
- naturally merges instances in true insertion order with a single query;
- stays monotonic and safe with no `AUTOINCREMENT`, because `Prune` deletes the
  *oldest* (smallest) rowids — the max always grows, so a cursor never collides with a
  later-reused rowid.

A client holding a cursor whose rows have since been pruned still works: `rowid >
cursor` simply returns the surviving newer lines.

## Server changes

### `internal/logstore` — new `Since` query (TDD)

`StoredLine` gains a field:

```go
type StoredLine struct {
    RowID  int64 // NEW — monotonic cursor key
    TsMs   int64
    Label  string
    Stderr bool
    Text   string
}
```

New method (existing `Tail` is the regression guard; behavior of `Tail` is unchanged):

```go
// Since returns lines for the given labels with rowid > afterRowID, ascending by
// rowid, plus the max rowid returned (the next cursor). When afterRowID <= 0 it
// returns the newest `limit` lines instead (backfill), still ascending by rowid,
// with the cursor set to the max rowid returned. limit <= 0 means no limit.
// Returns (nil, afterRowID, nil) when there are no matching lines, so the caller's
// cursor never goes backwards.
func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter) ([]StoredLine, int64, error)
```

- Follow path (`afterRowID > 0`): `SELECT rowid, ts, label, stderr, text FROM log_line
  WHERE label IN (...) AND rowid > ? [stream filter] ORDER BY rowid [LIMIT ?]`.
- Backfill path (`afterRowID <= 0`): newest `limit` across the labels
  (`ORDER BY rowid DESC LIMIT ?`), then reversed to ascending; cursor = max rowid.
- The single cross-label query supersedes the per-label `MergeTail` dance for the
  incremental case (`MergeTail` and `Tail` stay for the existing gRPC one-shot path).

### `internal/server` — `logStores.Since` wrapper

`*server.logStores` gains a method that resolves the selector to labels (exact match or
`selector#` prefix, identical to `FleetLogsHistory`) and calls `Store.Since`:

```go
func (s *logStores) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error)
```

- Unknown agent (`!has(agent)`): returns `(nil, 0, nil)` — graceful empty, no error
  (mirrors how `/api/metrics` treats an unknown agent as empty rather than 404).

### `internal/dashboard` — `/api/logs` endpoint

- New structural interface, satisfied by `*server.logStores` (no import cycle: the
  dashboard imports only the leaf `logstore` package for the `StreamFilter` type and
  line shape):

  ```go
  type LogsHistory interface {
      Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error)
  }
  ```

- Wired through `newHandler` / `NewHandler` / `Serve` exactly as `MetricsHistory` was;
  `server.ServeDir` passes the in-scope `ls` (`*logStores`) alongside `ss`.

- Route (session-guarded, like `/api/metrics`):
  `GET /api/logs?agent=X&selector=Y&stream=all|stdout|stderr&limit=<n>[&after=<cursor>]`
  - `stream` defaults to `all`; unknown values fall back to `all`.
  - `limit` parsed via the existing `parseMs`-style helper; default **500**, capped at
    a sane max (e.g. 5000) to bound a single response.
  - `after` absent / invalid → `0` (backfill).
  - Response **envelope** (object, not a bare array, because of the cursor):

    ```json
    { "cursor": 4213,
      "lines": [ { "ts": 1718700000123, "name": "app", "instance": 0,
                   "stderr": false, "text": "starting up" } ] }
    ```
  - `name`/`instance` come from `splitLabel(label)` (reused from the gRPC path).
  - Unknown agent → `200 {"cursor":0,"lines":[]}`.

## React UI (`web/src/`)

- **`LogView.tsx` (new)** — presentational log pane: monospace lines, stderr styled
  distinctly (dimmed / red). Auto-scrolls to the newest line; **pauses auto-scroll when
  the user scrolls up** and shows a "Jump to latest" affordance that resumes.
- **`Fleet.tsx`** — the M12 detail panel gains a **Charts | Logs** tab toggle. Charts
  are unchanged. The Logs tab renders the controls + `LogView`.
- **Logs tab controls:**
  - **stream filter** — all / stdout / stderr;
  - **backfill depth** — 100 / 500 / 1000 (default 500);
  - **client-side search** — substring filter applied over the buffered lines (does not
    hit the server).
- **Logs poll** — a dedicated `~1.5s` poll that runs **only while the Logs tab is open**
  inside an expanded panel. Cleaned up on tab-switch / panel collapse / unmount.
  **Best-effort:** a failed logs poll never logs the user out (only the fleet poll owns
  auth, as in M12). Changing the stream filter or backfill depth **resets the buffer +
  cursor** and refetches from scratch.
- **Client buffer cap** — the in-memory line buffer is capped (**5000 lines**); oldest
  lines drop off so a long-running follow cannot grow unbounded.
- **`api.ts`** — gains `getLogs(agent, selector, { stream, limit, after })` returning
  `{ cursor, lines }`; throws on 401 like `getMetricsForProc`.
- The built SPA is committed under `internal/dashboard/dist/`; rebuild with `make ui`
  after any `web/src/` change so `go build` embeds it.

## Defaults (locked in)

- Poll interval: **1.5s**
- Default backfill: **500 lines**
- Backfill options: **100 / 500 / 1000**
- Client buffer cap: **5000 lines**
- Server `limit` cap: **5000**

## Testing

- **`logstore`**: `Since` — backfill (newest N, ascending, cursor = max rowid); follow
  (`rowid > cursor` only); stream filtering; multi-instance merge ordering by rowid;
  empty result returns the unchanged cursor; cursor safety after a `Prune`. Existing
  `Tail` tests guard the unchanged one-shot path.
- **`dashboard`**: `/api/logs` — backfill vs follow (`after` forwarded to `Since`);
  `stream` param mapped; `limit` default + cap; unknown agent → `200` empty; session
  guard (401 without cookie). Modeled on `metrics_test.go` with a `fakeLogs`.
- **`server`**: `logStores.Since` selector matching (exact + `selector#` prefix) and
  unknown-agent empty. Existing `TestFleetLogsHistory` guards the shared store.
- Gate before finishing: `go test ./... -race -count=1`, `gofmt -l .` silent,
  `go vet ./...`, `make ui` builds, `go build -o marshal ./cmd/marshal`.

## Out of scope (deferred)

- SSE / true server-push streaming.
- Server-side search / grep (search is client-side over the buffer).
- Downloading / exporting logs.
- Persisting the open tab / selected filter / search across page reload.
- ANSI color-code rendering.
- A multi-process combined ("firehose") log view.

## Live demo (per project convention)

After implementation + handoff: scratch `XDG_DATA_HOME`, set password while server is
down, start with `--http-listen`, enroll an agent running a chatty demo process, open
the dashboard, expand the process row, switch to the Logs tab, and confirm: backfill
populates, new lines tail in within ~1.5s, the stream filter and search work,
auto-scroll pauses on scroll-up, and unknown/empty selectors render gracefully. Tear
down and confirm no orphan `marshal` processes remain.
