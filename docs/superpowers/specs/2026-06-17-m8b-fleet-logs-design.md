# M8b — Fleet Log Storage (design)

**Date:** 2026-06-17
**Status:** approved (brainstorm)
**Builds on:** M8 (central-server metric storage) — this is the log analog.

---

## 1. Goal & scope

The central server already persists per-agent **metrics**. M8b adds the same for
**logs**: the daemon ships captured stdout/stderr lines to the central server over the
existing `Fleet.Connect` stream, the server persists them per-agent in SQLite, and a new
`marshal fleet logs <agent> <app>` command queries stored history.

**In scope:**

- `internal/logstore` — per-agent SQLite log record store (Approach B: timestamped records).
- Proto: `LogBatch` agent message, `HelloAck.last_log_ts_ms` watermark, `FleetLogsHistory` RPC.
- Daemon: a `logs.Registry` fleet-tap that fans in all sinks' lines, and `fleet.Client`
  log shipping (watermark backlog + live buffered flush).
- Server: per-agent `logstores`, `Connect` handling of `LogBatch`, `FleetLogsHistory`
  handler, log pruning in the retention goroutine.
- CLI: `marshal fleet logs <agent> <app|label>` (backfill-only, last-N lines).

**Out of scope (YAGNI):**

- Live `--follow` over the fleet (server fan-out of live lines to a watching CLI).
- Full 7-day file-based reconnect backfill — reconnect backfill is bounded by the
  in-memory ring (~1000 lines), not the rotated files.
- Server-side numeric-ID selector resolution (name / label-prefix only, mirroring metrics).
- Auth / TLS (deferred to M10).

---

## 2. Data flow

```
process stdout/stderr
   → logs.Sink (ring + rotated files)              [exists]
   → Registry fleet-tap (fan-in all sinks,
       including sinks created later)              [new]
   → fleet.Client: buffer, flush LogBatch
       on the existing 2s push tick                [new]
   → server.Connect handler (LogBatch branch)      [new]
   → logstore (per-agent SQLite)                   [new]
   → FleetLogsHistory RPC → marshal fleet logs     [new]
```

The normal path is live streaming. The watermark + ring backlog only exist to fill the
gap created by a disconnect/reconnect.

---

## 3. `internal/logstore` (mirrors `internal/metricstore`)

One SQLite DB per agent at `<data-dir>/agents/<name>/logs.db`.

```sql
CREATE TABLE log_line (
  ts     INTEGER NOT NULL,   -- unix ms
  label  TEXT NOT NULL,      -- "app#instance"
  stderr INTEGER NOT NULL,   -- 0 / 1
  text   TEXT NOT NULL
);
CREATE INDEX idx_log_label_ts ON log_line(label, ts);
```

Types and API (parallel to `metricstore`):

```go
type Line struct {        // input for Append
  TsMs   int64
  Label  string
  Stderr bool
  Text   string
}

type StoredLine struct {  // query result
  TsMs   int64
  Label  string
  Stderr bool
  Text   string
}

// Stream filter for Tail / queries.
type StreamFilter int
const (
  StreamAny    StreamFilter = iota // both stdout and stderr
  StreamStdout                     // stderr = 0 only
  StreamStderr                     // stderr = 1 only
)

type Store struct{ /* db *sql.DB */ }

func Open(path string) (*Store, error)
func (s *Store) Append(lines []Line) error
func (s *Store) Tail(label string, limit int, filter StreamFilter) ([]StoredLine, error) // last `limit` lines for one label, ts ascending
func (s *Store) MaxTs() (int64, error)            // SELECT max(ts) FROM log_line (0 if empty)
func (s *Store) Labels() ([]string, error)        // distinct labels
func (s *Store) Prune(beforeMs int64) (int64, error)
func (s *Store) Close() error
```

**Multi-instance merge:** the server resolves a selector to one or more labels
(e.g. `app` → `app#0`, `app#1`), calls `Tail` for each, then merges the results by
timestamp and applies the overall line limit. This is the log analog of
`metricstore.MergeBuckets`. The merge helper (merge N ts-ascending `[]StoredLine`,
keep the newest `limit`) lives in `logstore` so server and any future local use share it:

```go
func MergeTail(series [][]StoredLine, limit int) []StoredLine
```

**Append ordering:** lines are appended oldest-first within a batch for crash-safe
commits (a partial commit leaves a consistent prefix), matching the metric store.

---

## 4. Proto additions (`proto/marshal/v1/fleet.proto`)

```protobuf
message HelloAck {
  int64 last_metric_ts_ms = 1;  // existing
  int64 last_log_ts_ms    = 2;  // new: server's stored high-water-mark for logs
}

message LogShipLine {
  int64  ts_ms  = 1;
  string label  = 2;   // "app#instance"
  bool   stderr = 3;
  string text   = 4;
}

message LogBatch {
  repeated LogShipLine lines = 1;
}

message AgentMessage {
  oneof msg {
    Hello         hello    = 1;  // existing
    StateSnapshot snapshot = 2;  // existing
    MetricBatch   metrics  = 3;  // existing
    LogBatch      logs     = 4;  // new
  }
}

message FleetLogsHistoryRequest {
  string    agent_name = 1;
  string    selector   = 2;  // app name or "name#instance" label
  int32     lines      = 3;  // backfill count
  LogStream stream     = 4;  // reuse existing LogStream enum (unspecified = merged)
}

message FleetLogsHistoryResponse {
  repeated LogLine lines = 1;  // reuse existing LogLine message {name, instance_id, stderr, line}
}

service Fleet {
  // ... existing RPCs ...
  rpc FleetLogsHistory(FleetLogsHistoryRequest) returns (FleetLogsHistoryResponse);
}
```

If `LogLine` / `LogStream` are not visible from `fleet.proto` (different file, same
`marshal.v1` package — they should be importable), define fleet twins of identical shape;
prefer reuse.

**Watermark semantics (identical to metrics):** the server derives the watermark from
`logstore.MaxTs()` and returns it in `HelloAck.last_log_ts_ms`. The agent ships ring lines
with `ts > watermark`. The same-millisecond edge case — a line sharing the exact ms of the
last stored line could be dropped on reconnect — is accepted, matching the existing metric
watermark behavior.

---

## 5. Daemon side

> **Implementation refinement (2026-06-17, accepted):** the plan replaces the
> subscribe-the-channel mechanism below with a **pull-based** `LogsFunc(sinceTsMs)`
> that reads the ring each 2s push tick — the exact seam the metrics path uses. It
> meets the same constraints (ring-bounded backfill, no new local store, no write
> amplification) with no `Sink` changes, no tap, and no goroutines. A line becomes
> queryable on the server up to one tick (~2s) later, which is immaterial for a
> backfill-only feature. Sections 5.1–5.2 are retained for context; see
> `docs/superpowers/plans/2026-06-17-m8b-fleet-logs.md` for the implemented design.

### 5.1 `logs.Registry` fleet-tap

`logs.Registry` gains a tap that fans in lines from **all** sinks, including sinks created
later when new instances start (so coverage is not frozen at connect time):

```go
type LabeledLine struct {
  Label  string
  Ts     time.Time
  Stderr bool
  Text   string
}

// SubscribeFleet returns a merged ring backfill (across current sinks, lines with
// ts after `since`) plus a live channel that receives every line from every sink —
// present and future. `cancel` detaches the tap and releases resources.
func (r *Registry) SubscribeFleet(since time.Time) (backlog []LabeledLine, ch <-chan LabeledLine, cancel func())
```

Implementation: the Registry keeps a set of active fleet taps. Each `Sink` forwards every
captured line to the registry's taps (in addition to its per-sink ring/subscribers). When
a new sink is created via `For`, it is wired to the existing taps. Backlog is built by
merging each current sink's ring backfill filtered to `ts > since`.

### 5.2 `fleet.Client` log shipping

`fleet.Client` gains an option mirroring `WithMetrics`:

```go
type LogSource func(since time.Time) (backlog []*pb.LogShipLine, ch <-chan *pb.LogShipLine, cancel func())

func (c *Client) WithLogs(src LogSource) Option
```

Connection flow additions (on top of the existing metrics flow):

1. After `HelloAck`, call `LogSource(watermark)` where `watermark = time.UnixMilli(ack.LastLogTsMs)`.
2. Send the backlog (`ts > watermark`) as one or more `LogBatch` messages.
3. Drain the live channel into an in-memory buffer; on each existing 2s push tick, flush
   the buffer as a `LogBatch` (skip if empty).
4. The buffer is **capped** (e.g. a few thousand lines). On overflow, drop oldest and keep
   a dropped-line counter (logged), so a noisy process cannot blow up daemon memory.
5. On disconnect, `cancel` the tap; re-subscribe fresh on reconnect with the new watermark.

### 5.3 Daemon wiring (`internal/daemon`)

Add a `logsSince`-style adapter that exposes the registry tap as a `fleet.LogSource`
(converting `logs.LabeledLine` → `*pb.LogShipLine`), and pass it via `WithLogs` when
constructing the fleet client, alongside the existing `WithMetrics`.

---

## 6. Server side

- New `logstores` type mirroring `stores` (`internal/server/logstores.go`): lazy per-agent
  `logstore.Store`, `sanitizeAgent` reuse, `get`, `has`, `closeAll`, `pruneAll`.
- `Connect` handler: on `AgentMessage.logs` (`LogBatch`) → convert to `[]logstore.Line`
  and `Append`. `HelloAck.last_log_ts_ms` is set from the agent's `logstore.MaxTs()`.
- `FleetLogsHistory` handler: open/get the agent store, resolve the selector to labels
  (name or label-prefix: `sel == l || strings.HasPrefix(l, sel+"#")`, mirroring metrics),
  `Tail` each label, `MergeTail` to the requested limit, apply the `stream` filter, and
  map to `[]*pb.LogLine` (parsing `name`/`instance_id` from the label).
- `ServeDir` retention goroutine: in addition to metric pruning, prune logstores older than
  **7 days** (same cadence and window as metrics).

---

## 7. CLI (`cmd/marshal/fleet.go`)

```
marshal fleet logs <agent> <app|label> [flags]
  --server   default $MARSHAL_SERVER or localhost:9000
  -n,--lines backfill count (default 15, matching local `marshal logs`)
  --stdout   only stdout
  --stderr   only stderr
```

Calls `FleetLogsHistory` and renders lines using the same formatting as local
`marshal logs` (name#instance prefix + stream marker as applicable).

---

## 8. Testing (TDD)

- **`logstore` unit tests:** `Append`/`Tail`/`MaxTs`/`Labels`/`Prune`; stream filtering;
  `MergeTail` across multiple labels with a limit; empty-store `MaxTs` → 0.
- **`logs.Registry` tap test:** fan-in across an existing sink and a sink created *after*
  `SubscribeFleet`; backlog filtered by `since`; `cancel` detaches cleanly (no goroutine
  leak, no send on closed channel).
- **Fleet e2e:** agent ships logs → server stores → `FleetLogsHistory` returns them in
  order; reconnect after producing more lines backfills from the ring since the watermark
  (no duplicates beyond the accepted same-ms edge).
- Gate before finishing: `gofmt -l .` silent, `go vet ./...`, `go build`, `go test ./... -race -count=1`.

---

## 9. Known limitations / deferred

1. **Reconnect backfill bounded by the ring (~1000 lines)**, not the 7-day file history.
   A long disconnect on a busy app loses lines beyond the ring. By design for M8b.
2. **Same-ms watermark edge:** a line sharing the exact millisecond of the last stored
   line may be dropped on reconnect (matches metrics).
3. **No live `--follow` over the fleet** — backfill-only this milestone.
4. **Selector is name / label-prefix only** server-side (no numeric-ID resolution).
5. **High-volume cap:** the shipping buffer drops oldest lines under sustained flooding
   (counter logged); this is a memory-safety tradeoff, not lossless delivery.
