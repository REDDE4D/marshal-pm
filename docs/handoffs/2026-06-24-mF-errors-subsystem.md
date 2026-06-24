# M-F · Errors / Exceptions subsystem — Handoff

**Date:** 2026-06-24
**Branch:** `mF-errors-subsystem` (off `dev` @ `946d6f3`). Reviewed + live-demo-verified;
**ready to merge → `dev` (`--no-ff`)**. `main` unchanged (still v0.2.0).

To resume: read this file. Spec: `docs/superpowers/specs/2026-06-24-mF-errors-subsystem-design.md`;
plan: `docs/superpowers/plans/2026-06-24-mF-errors-subsystem.md`; program roadmap:
`docs/superpowers/specs/2026-06-23-dashboard-program-roadmap.md`; SDD ledger (git-ignored):
`.superpowers/sdd/progress.md`.

## What M-F does

Turns the fleet's mirrored stderr into a deduplicated **error-signature ledger**, exposed at
`GET /api/errors` and surfaced on a minimal transitional **Errors page** (`#/errors`).

**Key architectural decision (lighter than the roadmap assumed):** the roadmap framed M-F as
"reuse M-E's *agent-side* event store." That was obsolete — the **server already mirrors every
agent's stderr** into per-agent SQLite logstores (`internal/server/logstores.go`). So M-F derives
signatures **compute-on-read, entirely server-side**, with **no proto / agent / persisted-store /
migration** changes. Server log retention is 7 days, which bounds every query.

Per signature: normalized-message id, representative raw sample, best-effort `file:line`, origin
agent/proc, distinct affected procs, count, first/last seen, and a 24-bucket occurrence trend.
Plus a cluster (errors, distinct signatures, affected procs, last error). Range `24h|7d|all`
(`all` clamps to 7-day retention); optional `agent=` filter; fleet-wide otherwise.

Design decisions (locked in brifestorming): **level-heuristic** filter (info/warn/debug excluded,
everything else on stderr is an error); **Standard** normalization (strip timestamps/ids/addrs/
paths/numbers → placeholders); **best-effort** source extraction; signature key = normalized
message **fleet-wide** (affected procs tracked as a set).

## What changed (12 commits, `580f073..47876b4`; code is `8b70c39..47876b4`)

- `internal/errsig/` (new, **pure** — no DB/I/O/goroutines/wall-clock):
  - `errsig.go` — `IsError` (level heuristic; anchored `reLevelPrefix` so a mid-message "info"
    doesn't false-exclude), `Normalize`, `Signature` (12-hex sha256), `Source` (best-effort
    file:line: Go/C `file.ext:NNN`, Python `File "x", line N`, generic `at path:line`),
    `isTraceHeader`. (8b70c39, 2f6b7dc, 3c23e0c, 47876b4)
  - `aggregate.go` — `Line`/`Sig`/`Cluster`/`Result` + `Aggregate(lines, since, now, nBuckets)`:
    pure fold → cluster + signature ledger (24 buckets, sorted count-desc then last-desc). (08a9c34)
- `internal/logstore/store.go` — `StderrSince(labels, sinceMs)`: stderr rows since, ordered
  `(label, ts)`. (1c08755)
- `internal/server/logstores.go` — `(*logStores).StderrSince(agent, sinceMs)` wrapper (mirrors
  `ErrorCounts`; unknown agent → nil). (9e60f46)
- `internal/dashboard/` — `LogsHistory` extended with `StderrSince`; new `errors.go`
  (`GET /api/errors` handler, view types, `rangeMs`, `errSparkBuckets=24`, `errMaxScan` truncation
  guard); route registered session-gated; fakes updated. (b4bf246)
- `web/src/` — `getErrors` + types (`api.ts`), `Errors.tsx` (range tabs, cluster line, signature
  table, inline `Sparkline`, loading/empty/error states), `#/errors` route (`router.ts`,
  `App.tsx`), nav button (`Overview.tsx`); embedded bundle rebuilt (`make ui`). (2273ce2)
- `CHANGELOG.md` `[Unreleased]` Added entry. (2273ce2)

## Quality gates

- TDD throughout (subagent-driven: fresh implementer + spec/quality reviewer per task). All
  per-task reviews clean.
- Two reviewer-found fixes landed in-loop: `2f6b7dc` (Task-1 Important — bare-word info/warn
  markers false-excluded mid-message errors → anchored prefix regex).
- Final whole-branch review (opus): **READY TO MERGE**, no Critical/Important. One worthwhile Minor
  fixed: `9c9ceb0` (Source stack-window could bleed across agents sharing a proc label → break on
  agent too).
- **Live demo also exposed a within-proc Source bleed** (a plain "connection refused" stole the
  `worker.go:142` frame from a panic printed 2 lines later). Fixed: `47876b4` — Source lookahead is
  now **gated on `isTraceHeader`** (only Go panics / Python tracebacks / "exception" lines scan
  following lines; plain one-line errors use a window of just themselves). Locking tests added.
- `go test ./... -race -count=1` green (incl. `errsig`); `go vet` clean; `gofmt -l .` empty;
  `make build` ok (`v0.1.0-124-g47876b4`); `make ui` builds with 0 TS errors.
- **Live demo PASS** (scratch `/tmp/marshal-mF-demo`, real server :9000 / dashboard :9001): a
  `demo-agent` ran an `erratic.sh` emitting varied stderr (info heartbeat, connection errors with
  varying IPs/ports, Go panic + frame, Python traceback + frame). `/api/errors?range=24h` showed:
  connection variants **collapsed to one signature** (count climbing 42→313→408), **info lines
  excluded** (cluster errors = 6 sigs × N, no heartbeat), source `—` for the connection/ValueError
  lines and `worker.go:142`/`main.py:88` for the panic/traceback. Agent filter, `7d`, and
  `bogus`→`24h` canonicalization all 200. In-browser (`#/errors`, Playwright) the cluster + ledger
  + trend sparkline rendered (screenshot shown to user). Teardown by data dir; standing launchd
  daemon (PID 899) preserved; no orphans.

## Deferred / known limitations (not bugs — M-A or later)

- **Line-oriented v0:** stack-trace continuation lines (the raw `\t/srv/.../worker.go:142` frame,
  the `  File "main.py"...` line) each become their **own** signature rather than folding into the
  preceding error. They carry a correct self-source, but the ledger is noisier than ideal. Real
  multi-line grouping is **M-A** (the styled Errors page) territory.
- `reNum` collapses integer-only differences, so e.g. **HTTP 404 vs 500** merge into one signature
  (documented v0 trade-off; `affected`/`sample` keep it transparent; **agent-keyed signatures** and
  a **materialized `sigstore`** remain the documented future levers if needed).
- IPv6 hosts aren't address-normalized (variants still collapse consistently; cosmetic).
- Transitional UI only: `Sparkline` is a polyline not a true bar-sparkline; `.warn` CSS class for
  the truncation banner is undefined (unstyled); inline style on the page `<h1>`. **M-A** restyles
  every page, so these are intentionally left.
- `errsig` test hygiene: `Aggregate` representative-field semantics (Sample=first, Agent/Proc=most
  recent) are correct by code but not directly asserted; `notice:` level + `reSrcAt` paths
  untested. Low value to add now.

## Build / run / test

```bash
make proto   # regenerate internal/pb (NOT needed for M-F — no proto changes)
make ui      # rebuild embedded SPA bundle (commit it)
make build
go test ./... -race -count=1 && go vet ./... && gofmt -l .
```

## Next step

Merge `mF-errors-subsystem` → `dev` (`--no-ff`), delete the branch. **After M-F, only M-A remains**
in the program: the full "Marshal Instrument" redesign of every page, now backed by all the real
data from M-B…M-F (agent/host metadata, host + extended process metrics, restart history, control
additions, and this errors subsystem). Spec: `2026-06-23-dashboard-redesign-design.md`; prototype
`.superpowers/brainstorm/46891-1782222731/content/demo3.html`. Also consider whether to **cut a
release** of the accumulated `[Unreleased]` data/control milestones (M-B…M-G + M-F) before or
after M-A — decide release cadence then.
