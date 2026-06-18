# M13 Live Log Tailing — Handoff

**Date:** 2026-06-18
**Branch:** `m13-logs` (NOT yet merged to main)
**Gate:** green — `go test ./... -race -count=1` passes (19 packages), `gofmt -l .` silent,
`go vet ./...` clean, `make ui` builds, `go build -o marshal ./cmd/marshal` embeds the dist
(rebuild is reproducible — clean working tree after `make ui`).

---

## Current state

M13 is complete (pending merge). The dashboard's expandable per-process detail panel (from
M12) now has a **Charts | Logs** tab toggle. The Logs tab tails the process's captured
stdout/stderr by polling a new cursor-based endpoint every 1.5s, with stream-filter
(all/stdout/stderr), backfill-depth (100/500/1000), and client-side substring search.

No new transport (poll only, like fleet/metrics), no new dependency, no new storage — this
surfaces the server's existing per-agent log store (`internal/logstore`, SQLite) to the
browser via `GET /api/logs`.

Design spec: `docs/superpowers/specs/2026-06-18-marshal-dashboard-m13-log-tailing-design.md`.
Implementation plan: `docs/superpowers/plans/2026-06-18-marshal-dashboard-m13-log-tailing.md`.

Branch commits (newest first):

```
7b6eab2 feat(dashboard): live log tailing tab in the detail panel
f4ef794 feat(dashboard): GET /api/logs cursor-based tail endpoint
6c833c0 feat(server): logStores.Since selector wrapper
6013096 feat(logstore): rowid-cursor Since query for tailing
```

(Branched from `a882e61` on `main`, which already carries the M13 spec + plan commits.)

---

## What was built

### Task 1 — `internal/logstore/store.go`

`StoredLine` gained a `RowID int64` field, and a new cursor query:

```go
func (s *Store) Since(labels []string, afterRowID int64, limit int, filter StreamFilter) ([]StoredLine, int64, error)
```

The cursor is the SQLite **rowid**, not `ts` — this avoids the same-millisecond
drop/duplicate problem and gives a true insertion-order merge across instance labels in one
query (`label IN (?,...) AND rowid > ? ORDER BY rowid`). It is monotonic because `Prune`
deletes the *oldest* (smallest) rowids, so a held cursor never points into a hole. When
`afterRowID <= 0` it returns the newest `limit` lines ascending (backfill); otherwise lines
with `rowid > afterRowID` ascending (follow). Returns the max rowid as the next cursor; an
empty result returns the unchanged cursor. `Tail`/`MergeTail` are untouched (still used by
the gRPC one-shot path).

### Task 2 — `internal/server/logstores.go`

`*logStores.Since(agent, selector, afterRowID, limit, filter)` resolves the selector to
labels (exact match or `selector#` prefix, identical to `FleetLogsHistory`) and delegates to
`Store.Since`. Unknown agent → `(nil, 0, nil)`; known agent with no matching labels →
`(nil, afterRowID, nil)`.

### Task 3 — `internal/dashboard/logs.go` (new) + wiring

New session-guarded route `GET /api/logs`:
- `?agent=X&selector=Y&stream=all|stdout|stderr&limit=<n>` → backfill: newest `limit` lines.
- `&after=<cursor>` → follow: only lines newer than the cursor.
- `stream` defaults to `all` (invalid → `all`); `limit` default **500**, cap **5000**,
  invalid/≤0 → 500; `after` absent/invalid/negative → 0; missing agent or selector → **400**.
- JSON envelope (object, because of the cursor):
  `{"cursor":<int64>,"lines":[{ts,name,instance,stderr,text}]}`. `name`/`instance` from
  splitting the `name#idx` label.
- Unknown agent → `200 {"cursor":0,"lines":[]}`.

The dashboard defines a `LogsHistory` interface (satisfied structurally by `*server.logStores`
— no import cycle; dashboard imports only the leaf `logstore` package). `newHandler` /
`NewHandler` / `Serve` gained a `LogsHistory` parameter (third, after `metrics`); `ServeDir`
passes the in-scope `ls` into `dashboard.Serve(ctx, httpAddr, reg, ss, ls, auth, cert)`. All
six existing `NewHandler` test call sites were updated.

### Tasks 4–6 — `web/src/` (one commit)

- `api.ts`: `LogLine`/`LogsResponse` types + `getLogs(agent, selector, {stream, limit, after})`
  (throws on 401, like `getMetricsForProc`).
- `LogView.tsx` (new): presentational monospace pane; stderr lines styled distinctly;
  auto-scrolls to the newest line and **pauses when the user scrolls up**, with a
  "Jump to latest" button to resume. Client-side substring search filters the buffer.
- `Fleet.tsx`: the detail panel gained the **Charts | Logs** tab toggle (Charts =
  windows + MetricChart, unchanged). The Logs tab renders stream/limit/search controls
  (each `e.stopPropagation()` so they don't toggle the row) + `<LogView>`. A dedicated
  1.5s logs poll runs **only while the Logs tab is open**, resets buffer+cursor on
  `[expanded, tab, logStream, logLimit]`, caps the client buffer at **5000 lines**, and is
  **best-effort** (a failed poll never logs the user out — only the fleet poll owns auth).
  The first fetch backfills (`after=0`); subsequent fetches use the returned cursor.
- `styles.css`: tab/control/log-pane styling.

The built SPA lives in the committed `internal/dashboard/dist/`; `go build` embeds it without
a Node toolchain. Rebuild with `make ui` after any `web/src/` change.

---

## Build / run / test

```bash
go build -o marshal ./cmd/marshal
go test ./... -race -count=1
gofmt -l .          # must print nothing
go vet ./...
make ui             # only when web/src/ changes; regenerates internal/dashboard/dist/
```

Run the dashboard exactly as in M11/M12 (password set while the server is **down**, then start
with `--http-listen`, then enroll). Open `https://localhost:<http-port>/`, log in, expand a
process row, switch to the **Logs** tab, and watch lines tail in.

---

## Live-demo result (2026-06-18, scratch `XDG_DATA_HOME=/tmp/marshal-m13-demo`, server `:9300`/`:9301`)

End-to-end verified against a real server with one enrolled agent (`demo-1`) running a chatty
process (`chatty`: stdout + stderr every 0.5s):

1. `GET /api/logs` without a cookie → **401**; after login → **200**.
2. Backfill `?agent=demo-1&selector=chatty` → **150 lines**, `cursor 150`, ascending, correct
   `name`/`instance`/`stderr`/`text` (stdout lines `stderr:false`, warnings `stderr:true`).
3. Follow `&after=150` (after a 3s wait) → **only 70 new lines**, cursor advanced 150→220, no
   overlap, both streams interleaved by insertion order — confirms the no-gap/no-duplicate
   tailing property.
4. `&stream=stderr&limit=6` → exactly **6 newest stderr-only** lines.
5. Unknown agent `?agent=ghost&...` → `200 {"cursor":0,"lines":[]}` (graceful).
6. SPA shell serves and references the freshly-built `assets/index-Yg3yjwk8.js` (HTTP 200,
   151711 bytes = the `make ui` output).
7. Teardown: scratch app deleted, scratch daemon + server stopped, scratch dir removed.

**Note (not an M13 issue):** the same pre-existing user daemon noted in the M12 handoff
(`/Users/sebastiankuprat/process manager/marshal daemon`, started 11:30:06, managing a
`clock` app in the **default** state dir) was still running and was left untouched — it is
the user's own process, not a demo orphan. No scratch (`/tmp/marshal-m13-demo`) processes
remain.

The browser pixel rendering itself was not screenshotted; it was verified indirectly by
confirming the exact `/api/logs` payloads the React components consume (above) and that the
embedded SPA shell + hashed asset serve. A visual browser pass is a reasonable optional
follow-up.

---

## Review outcome

Per-task reviews (one fresh reviewer each) + a final whole-branch review (most capable model):
**ready to merge, no Critical/Important findings.** The final review explicitly validated the
cross-layer correctness (rowid follow = no gap / no duplicate even under burst), confirmed no
SQL-injection risk in the dynamic `IN (?...)` query, and confirmed clean layering.

### Deferred / known issues (Minor — carried for a follow-up polish)

1. **Tab/filter state not reset when the expanded row changes** — open process A on the Logs
   tab, collapse, open process B → B opens on the Logs tab with A's stream/limit (the log
   *buffer* is correctly cleared, so no data bleed; purely a UX surprise). The only
   user-noticeable Minor. Fix: reset `tab`/`logStream`/`logLimit`/`logSearch` on `expanded`
   change, or key the detail panel on `expanded`.
2. **`LogView` uses `key={i}`** (array index) — harmless (rows hold no per-row state) but
   causes needless reconciliation after a buffer-cap trim. A stable key would need the client
   to surface `rowid` in `logLineView` (currently it exposes only ts/name/instance/stderr/text).
3. **`cursor = res.cursor || cursor`** uses `||` not `??` — safe with the current backend
   (a real cursor is always a positive rowid; both sides are 0 at start).
4. **Minor test-coverage gaps** — no direct test for `logStores.Since` known-agent/no-match;
   no HTTP-layer test for the `limit` cap / `stream=invalid` defaulting; no test for the
   `limit<=0` no-limit path in `Since`.

### Out of scope for M13 (per the spec)

SSE / true server-push streaming; server-side search/grep (search is client-side over the
capped buffer, so it can miss matches that scrolled out — worth a UI note); downloading /
exporting logs; persisting the open tab / filter / search across reload; ANSI color
rendering; a multi-process combined ("firehose") log view.

Carried over from M11/M12 and still open: in-memory sessions lost on restart; `server passwd` /
`token --rotate` not hot-reloaded; no login rate-limiting; self-signed cert warning;
read-only dashboard (no process controls); single-user accounts.

---

## Concrete next step

1. **Merge `m13-logs` to `main`** via the `finishing-a-development-branch` skill (final
   whole-branch review already passed: "ready to merge", no Critical/Important findings).
2. Optionally land the four Minor polish items above as a small follow-up commit (the
   tab-state reset is the only one a user would notice).
3. **M14** — the next dashboard milestone (candidates from the M11/M12/M13 deferred lists:
   process controls, auth hardening, or server-side log search). Pick one and brainstorm it.
