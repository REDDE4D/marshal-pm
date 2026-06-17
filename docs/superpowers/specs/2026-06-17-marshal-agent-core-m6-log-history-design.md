# M6 — Log history & retention (design)

**Status:** approved (brainstormed 2026-06-17)
**Milestone:** M6, second milestone of sub-project #2 (metrics & log pipeline)
**Predecessor:** M5 (metric history) — merged to `main` at `5101380`.

## 1. Goal

Today Marshal captures per-instance output to rotated files **and** a 1000-line in-memory
ring, but `marshal logs` only ever backfills from the ring — so history is capped at ~1000
lines and **lost on daemon restart**, even though the rotated files on disk still hold it.
Logs also grow without an age bound or compression.

M6 makes log history **deep and durable** (read back from the files on disk), adds **retention +
compression**, adds a **per-stream view**, and fixes two carried correctness bugs. It is the
log-side counterpart to M5's metric history and completes sub-project #2's local-insights goal.

Scope (all approved):
1. Deep backfill from rotated `.out`/`.err` files (beyond the ring; survives restart).
2. Retention by age + gzip compression of aged segments.
3. Per-stream `logs --stdout` / `--stderr`.
4. Two carried bug fixes: unbounded partial-line buffer in `Sink`; backfill→subscribe race in `logs -f`.

## 2. Approach decision

**Approach A — keep raw byte files; per-stream backfill is exact, merged-deep is best-effort.**

The persisted `.out.log`/`.err.log` files store **raw process bytes with no per-line
timestamps** (timestamps live only in the in-memory ring). This is deliberately preserved: the
files stay externally `tail`/`grep`-able, and it is the storage model the fleet architecture spec
endorses ("logs in rotated files"). The consequence — accepted — is that history read back from
disk has each stream's own chronological order but **cannot perfectly interleave stdout and
stderr by time** once it is older than the live ring.

Rejected:
- **B — timestamped line-records in the files.** Perfect interleave at any depth and forward-
  compatible with the central server's log-records stream, but the files stop being raw process
  output, and it needs a writer/parser + format-migration story. Deferred to the central-server
  work (sub-project #3), where structured log records are the natural home.
- **C — SQLite log store / timestamp sidecar.** Contradicts the spec's explicit file-based
  decision and duplicates storage.

## 3. Architecture

```
  process stdout/stderr ─raw bytes─▶ Sink.write ─┬─▶ lumberjack files (.out/.err, rotated, .gz, age-deleted)
                                                 ├─▶ ring (1000 lines, with Ts) ──┐ exact merged-recent
                                                 └─▶ live subscribers              │
                                                                                  │
  marshal logs ──gRPC LogRequest{stream}──▶ daemon.Logs ── backfill routing ──────┤
        --stdout/--stderr ─────────────────────────────▶ filetail (read files) ──┘ exact per-stream / deep / cold
        (merged default) ──────────────────────────────▶ ring if it satisfies N, else filetail (best-effort merge)
        -f/--follow ───────────────────────────────────▶ Sink.SubscribeWithRing (atomic snapshot+register)
```

### 3.1 Retention & compression (`logs.Policy`, `logs.Sink`, `logs.Registry`)

A value type carries the policy:

```go
type Policy struct {
    MaxSizeMB  int  // rotate threshold (lumberjack MaxSize)
    MaxBackups int  // rotated files kept (lumberjack MaxBackups)
    MaxAgeDays int  // delete rotated files older than this (lumberjack MaxAge); 0 = no age limit
    Compress   bool // gzip rotated files (lumberjack Compress)
}
```

The default policy (was: 10 MB × 5 backups, no age, no compression) becomes
**`{MaxSizeMB: 10, MaxBackups: 5, MaxAgeDays: 14, Compress: true}`**. Retention and compression
are entirely lumberjack's job — set `MaxAge` and `Compress` on the two `lumberjack.Logger`s; no
new prune goroutine is needed (contrast M5, which needed an explicit prune loop for SQLite).

Configuration follows the M5 metric-retention pattern — **global default + per-app override**:
- The daemon default is the hardcoded `Policy`, overridable via a `WithLogRetention(Policy)`
  `Option` (used by tests).
- Per-app override in `marshal.yaml`:

  ```yaml
  apps:
    - name: api
      cmd: ./api
      logs: { max_size_mb: 50, max_backups: 10, max_age_days: 30, compress: true }
  ```

The `Registry` holds `default Policy` plus `policies map[string]Policy` keyed by **app name**.
`Registry.For(label)` strips the `#idx` suffix from the label, looks up the app's policy (falling
back to the default), and passes it to `newSink`. The daemon pushes each app's policy into the
registry when it applies config.

### 3.2 Deep backfill (`logs/filetail.go`, new)

A focused, dependency-light unit:

```go
// fileBackfill returns up to n lines from one stream's on-disk segments,
// newest-first segments scanned until n lines are gathered, oldest-to-newest within the result.
func fileBackfill(dir, label string, stderr bool, n int) ([]Line, error)
```

Behaviour:
- Globs the stream's segments: the active `<label>.<out|err>.log` plus rotated
  `<label>.<out|err>-*.log` and `<label>.<out|err>-*.log.gz`.
- Orders newest→oldest. Lumberjack names rotated files with an embedded
  `2006-01-02T15-04-05.000` timestamp, so filenames sort lexicographically by time; the active
  file is newest.
- Reads segments (each ≤ `MaxSizeMB`, default 10 MB — cheap to read whole) until `n` lines are
  collected, transparently gunzipping `.gz` segments.
- Returns `Line`s with `Stderr` set and a **zero `Ts`** (the wire protocol does not send
  timestamps today, so display is unaffected).
- Tolerates a segment disappearing mid-read (a concurrent rotation can rename/remove a file) —
  treat `os.IsNotExist` as "skip this segment", never an error.

The `Sink` exposes it as a method (it knows its own `dir`/`label`):
`func (s *Sink) FileBackfill(stderr bool, n int) ([]Line, error)`.

### 3.3 Backfill routing (`internal/daemon/logs.go`)

The `Logs` handler reads the `stream` selector and routes backfill with a single rule that never
mixes ring and file sources (so no line is dropped or duplicated):

- **Per-stream** (`LOG_STREAM_STDOUT` / `LOG_STREAM_STDERR`): for each resolved instance, call
  `FileBackfill(stderr, n)`; concatenate across instances; trim to `n`. Exact within a single
  stream of a single instance; cross-instance order is best-effort (also untimestamped on disk).
- **Merged** (`LOG_STREAM_UNSPECIFIED`):
  - If the ring holds ≥ `n` lines → serve the last `n` from the ring (exact `Ts` interleave). No
    disk read. Covers the common case.
  - Else (`n` exceeds the ring, or the ring is cold after a restart) → serve from files, last `n`
    per stream concatenated — cross-stream order **best-effort, documented**.

### 3.4 Per-stream proto & CLI surface

```proto
enum LogStream {
  LOG_STREAM_UNSPECIFIED = 0;  // merged (default)
  LOG_STREAM_STDOUT      = 1;
  LOG_STREAM_STDERR      = 2;
}
message LogRequest {
  string    target = 1;
  int32     lines  = 2;
  bool      follow = 3;
  LogStream stream = 4;  // M6
}
```

Regenerate `internal/pb/daemon{,_grpc}.pb.go` via `go generate ./internal/pb`. The CLI `logs`
command gains mutually-exclusive `--stdout` / `--stderr` flags (both unset → merged) mapping to the
enum. `-n/--lines` and `-f/--follow` are unchanged; existing `lines` depth is what selects the file
tier — no new depth field.

## 4. Bug fixes

### 4.1 Unbounded partial-line buffer

In `Sink.write`, a stream that never emits `\n` grows `outPart`/`errPart` without bound. Add
`const maxLineBytes = 64 * 1024`: after appending bytes, if the partial buffer has no newline and
has reached the cap, flush it as a synthetic `Line` (emit + reset). Raw bytes still tee to the file
unchanged. **Documented behaviour:** a single logical line longer than 64 KiB is split at the cap.

### 4.2 Backfill→subscribe race in `logs -f`

`Logs` currently reads backfill and *then* subscribes; a line emitted in that gap is lost, and the
live/backfill boundary can duplicate. Add an atomic primitive that holds `s.mu` for **both** the
ring snapshot and the subscriber registration:

```go
func (s *Sink) SubscribeWithRing(n int) (backfill []Line, live <-chan Line, cancel func())
```

Because snapshot and registration share the lock, every line lands in exactly one of
{backfill, live} — no gap, no duplicate. The follow path uses this; deeper file backfill (strictly
older history) is read separately and cannot race the live boundary. The non-follow path keeps the
plain ring/file read.

## 5. Testing (TDD)

- **`filetail`**: synthesize active + rotated + `.gz` segments → assert last-N ordering, gunzip,
  N spanning multiple segments, and tolerance of a segment deleted mid-read.
- **`sink`**: partial-line cap splits at 64 KiB; `SubscribeWithRing` loses/dupes nothing under a
  concurrent emitter (race test); policy fields reach the `lumberjack.Logger`s.
- **`registry`**: per-app policy selected by app name; default fallback.
- **`config`**: `logs:` block parses; absent block / zero fields fall back to the default policy.
- **daemon `logs`**: stream filter routes to the right file(s); merged exact-from-ring vs deep
  falls-back-to-files; multi-instance.
- **CLI**: `--stdout`/`--stderr` mutual exclusion; correct enum mapping.
- Full gate: `go build ./...`, `go vet ./...`, `gofmt -l .` (empty), `go test ./... -race -count=1`.
- Real-binary smoke: write > 1000 lines, restart the daemon, `logs -n 2000` served from disk;
  `--stderr` shows only stderr; confirm `.gz` rotation and age-based deletion.

## 6. Out of scope (deferred)

- Live policy hot-reload on **existing** sinks: a changed per-app policy applies to sinks created
  after the change / after the next daemon restart, not retroactively.
- Perfect cross-stream interleave in deep/cold history (Approach B; left to the central-server
  log-records work in sub-project #3).
- Per-segment time-merge across streams.
- A max total disk budget across all apps (only per-app size/backups/age today).

## 7. Files touched

New:
- `internal/logs/filetail.go` (+ `filetail_test.go`).

Changed:
- `internal/logs/sink.go` — `Policy`, lumberjack `MaxAge`/`Compress`, partial-line cap,
  `SubscribeWithRing`, `FileBackfill`.
- `internal/logs/registry.go` — default + per-app policy map; policy selection in `For`.
- `internal/config/config.go` — `App.Logs *LogRetention` block (pointer fields for correct default fallback).
- `internal/daemon/server.go` — `WithLogRetention` Option; push default + per-app policy to the registry.
- `internal/daemon/logs.go` — stream filter + ring-vs-file backfill routing; atomic follow.
- `internal/daemon/convert.go` — read the retention block out of `AppSpec` into `config.App.Logs`.
- `proto/marshal/v1/daemon.proto` — `LogStream` enum + `LogRequest.stream`; **`LogRetention` message + `AppSpec.logs`** so a per-app override survives the CLI→daemon `start` hop (otherwise `appToSpec`/`appSpecToConfig` silently drop it). Regenerate `internal/pb`.
- `cmd/marshal/control.go` — write `a.Logs` into `AppSpec` in `appToSpec`; `--stdout`/`--stderr` flags.
