# M-E · Restart history (rollups) — design spec

**Date:** 2026-06-24
**Milestone:** M-E (data milestone of the dashboard program; see
`2026-06-23-dashboard-program-roadmap.md`). Medium, additive.
**Branch:** `mE-restart-history` off `dev`.

## Goal

Per-process **restarts-in-24h** count and **last-restart timestamp**, surfaced on each
process row. Real restart events are recorded with accurate timestamps in a small
agent-side event store (SQLite, mirroring `metricstore`); the daemon ships the two rollups
on `ProcInfo` as point-in-time fields (like M-D's exit fields). Velocity is UI-derived. No
event shipping to the server, no queryable event ledger — those are deferred.

## Decisions (locked in brainstorming)

1. **Rollups, point-in-time** — `restarts_24h` (count of restart events in the trailing 24h)
   and `last_restart_unix` (timestamp of the most recent restart). NOT a full event ledger,
   NOT individual-event shipping or a `/api/restarts` endpoint.
2. **Supervisor emits real-timestamped events** via an injected hook (not server-derived from
   counter deltas, which is imprecise and breaks on daemon restart).
3. **Agent-side event store** (new `internal/eventstore`, SQLite like `metricstore`). The
   daemon computes the rollups from it when building the snapshot.
4. **7-day retention** — prune events older than 7 days (bounds the tiny DB; covers the 24h
   window). `last_restart_unix` is the max event ts within retention (a process stable for
   >7 days shows no recent restart, which is correct; lifetime count stays in the existing
   `restarts` field).
5. **Velocity is UI-derived** (e.g. `restarts_24h / 24` per hour) — no separate shipped field.
6. **Event payload is minimal**: `(ts_ms, label)` only. No exit code/reason on the event
   (that would only matter for the deferred ledger; YAGNI).

## Component changes

### 1. Event store — new `internal/eventstore` (mirrors `internal/metricstore`)

SQLite, one row per restart. Single responsibility, independently testable.

```sql
CREATE TABLE IF NOT EXISTS restarts (
  ts    INTEGER NOT NULL,  -- unix millis
  label TEXT    NOT NULL   -- "name#idx"
);
CREATE INDEX IF NOT EXISTS idx_restarts_label_ts ON restarts(label, ts);
```

API:

```go
type Rollup struct {
	Count24h int32 // restart events with ts >= sinceMs
	LastMs   int64 // max(ts) for the label (0 if none)
}

func Open(path string) (*Store, error)               // SetMaxOpenConns(1), busy_timeout, schema
func (s *Store) Record(label string, tsMs int64) error
func (s *Store) Rollups(sinceMs int64) (map[string]Rollup, error) // one grouped query, all labels
func (s *Store) Prune(beforeMs int64) (int64, error)
func (s *Store) Close() error
```

`Rollups` runs a single grouped query so one call serves a whole snapshot:

```sql
SELECT label,
       SUM(CASE WHEN ts >= ? THEN 1 ELSE 0 END) AS count24h,
       MAX(ts)                                  AS last_ms
FROM restarts
GROUP BY label;
```

(`Count24h` is clamped to int32; a label with only events older than `sinceMs` still
reports its `LastMs` with `Count24h = 0`.)

### 2. Supervisor hook — `internal/supervisor/instance.go`

`Instance` gains an `onRestart func()` field, set via a new `NewInstance` option
`WithOnRestart(fn func())`. The supervisor stays storage-agnostic.

In `handleExit`, the hook fires once per **genuine restart** — at the restart-accounting
point, right after `i.restarts++` (inside the `restart == true` path), before the
stability/backoff logic. It does NOT fire on a clean stop, an operator stop, or a
`RestartNo`/non-failure no-restart path. (It fires even on the cycle that then trips the
MaxRestarts cap into `StateErrored`, matching the existing `restarts` counter's semantics —
that final attempt was still a restart.)

```go
i.mu.Lock()
i.restarts++
// … unstable accounting …
i.mu.Unlock()
if i.onRestart != nil {
	i.onRestart()
}
```

### 3. Manager wiring — `internal/manager`

A new option `WithRestartSink(sink RestartSink)` where:

```go
type RestartSink interface{ Record(label string, tsMs int64) error }
```

When the manager builds each instance, it injects the per-instance hook capturing the
`"name#idx"` label the manager assigns that instance (the same label used for metrics):

```go
opts := []supervisor.Option{}
if m.restartSink != nil {
	label := label // the "name#idx" the manager computes for this instance
	opts = append(opts, supervisor.WithOnRestart(func() {
		_ = m.restartSink.Record(label, time.Now().UnixMilli())
	}))
}
inst := supervisor.NewInstance(spec, policyFor(app), opts...)
```

(The exact label variable is whatever the manager already computes when constructing the
instance's `InstanceSnapshot.Label`; the plan pins it. `NewInstance` gains a variadic
`...Option` so existing callers are unaffected.)

The `eventstore.Store` satisfies `RestartSink` (its `Record` matches). The label captured is
the same `"name#idx"` used for metrics, so rollups align with `ProcInfo` rows.

### 4. Proto — `proto/marshal/v1/daemon.proto`

`ProcInfo` (currently fields 1–16) gains:

```proto
int32 restarts_24h      = 17; // restart events in the trailing 24h
int64 last_restart_unix = 18; // unix seconds of the most recent restart (0 = none in retention)
```

Regenerate `internal/pb` via `make proto`.

### 5. Daemon — `internal/daemon`

- Open the event store at a new `restarts.db` in the daemon data dir (a `RestartsDBPath()`
  helper on the store, alongside the existing `MetricsDBPath()`), and wire it as the
  manager's restart sink (`manager.WithRestartSink(estore)`).
- Periodic prune: a lightweight goroutine (or fold into the existing sampler tick) calls
  `estore.Prune(now - 7d)` occasionally.
- When building `ProcInfo` (`snapshotToProc` via `fleetSnapshot`/`procList`), fetch the
  rollups once per snapshot: `rollups, _ := estore.Rollups(now - 24h)`, then set
  `restarts_24h` and `last_restart_unix` (seconds) per label from `rollups[label]`. Default
  zero when absent.

### 6. Dashboard `/api/fleet` + web

- `procView` gains `restarts_24h` (int32) and `last_restart_unix` (int64, `omitempty`);
  `fleetView` maps them from the `ProcInfo` getters.
- Web (`api.ts` + `ProcessCard.tsx`): show `restarts 24h` and `last restart <relative>`
  (e.g. "12m ago"), with velocity derived (`restarts_24h / 24` per hour) where useful.
  **Minimal transitional surfacing** — M-A delivers the real treatment. Rebuild the embedded
  bundle (`make ui`).

## Testing (TDD per layer)

- **eventstore:** `Record` then `Rollups` — events inside the 24h window count, older ones
  don't (but still set `LastMs`); `LastMs` is the max ts; `Prune` deletes old rows and
  returns the count; multiple labels are grouped independently.
- **supervisor:** with a `WithOnRestart` hook, the hook fires once per restart on a
  crash-restart cycle; it does NOT fire on a clean exit / operator stop / `RestartNo`.
- **convert/daemon:** `snapshotToProc` (or the merge path) populates `restarts_24h` and
  `last_restart_unix` from the rollup.
- **dashboard:** `/api/fleet` JSON carries the two fields.

## Edge cases / non-goals

- **No events yet / stable process:** `restarts_24h = 0`, `last_restart_unix = 0` → UI shows
  "0" / "—". The lifetime `restarts` counter (existing) is unaffected.
- **Daemon restart:** the event store persists across daemon restarts (on-disk SQLite), so
  the 24h window survives; the in-memory cumulative `restarts` counter still resets as today
  (unchanged, out of scope).
- **Retention vs last-restart:** a process that last restarted >7 days ago shows
  `last_restart_unix = 0` (pruned). Acceptable — that's a healthy, long-stable process.
- **Non-goals:** no agent→server event shipping; no server-side event store; no
  `/api/restarts` endpoint or restart-history list; no per-event exit detail; no change to
  the metricstore/logstore schemas.

## Next step

Write the implementation plan (writing-plans), then build on `mE-restart-history`, TDD per
layer, with a `CHANGELOG.md` `[Unreleased]` entry, handoff, and a live demo.
