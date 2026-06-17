# Marshal Central Server — M8 (metric records up + server-side storage) — Design

**Date:** 2026-06-17
**Status:** Approved (design); plan to follow.
**Sub-project:** #3 — Central server / fleet aggregation (second milestone, M8).
**Depends on:** M7 (agent↔server connection + live fleet state). Builds on sub-project #2
(local metrics & log pipeline, M5–M6).

## 1. Context

Marshal is a free, self-hosted process manager built bottom-up toward a fleet manager
(architecture spec: `docs/superpowers/specs/2026-06-16-fleet-process-manager-architecture-design.md`).
M7 delivered the first slice of the central server: an agent dials the server over an
agent-initiated gRPC stream and pushes process-state snapshots; the server keeps **in-memory**
fleet state; `marshal fleet ps` reads it. M7 transport is plaintext and unauthenticated by design.

M8 adds the **data plane upward, for metrics**: the agent streams its raw CPU/mem samples up,
the server **persists** them (SQLite), and serves history back. This is the first time the
central server has durable storage.

The roadmap originally bundled metrics **and** logs into M8. During this brainstorm it was
**split** to keep each milestone thin and independently shippable (mirroring how M5/M6 split
metrics from logs locally):

- **M8 (this design)** — metric records up + server-side SQLite + `marshal fleet metrics`.
- **M8b** — log records up + server-side log files (the deferred Approach-B timestamped log
  records giving perfect cross-stream interleave in deep history) + `marshal fleet logs`.
- **M9** — downward control plane (`restart`/`stop`/`start <app> on <host>` with acks).
- **M10** — auth & TLS hardening (bootstrap token, per-agent identity, TLS everywhere).

## 2. Goal & scope

**Goal:** one Marshal server stores per-instance metric history for every connected agent and
serves it back. An agent ships raw CPU/mem samples from its local `metricstore` over the existing
`Fleet.Connect` stream; the server persists them per-agent (SQLite) and answers history queries
via a new `FleetMetricsHistory` RPC behind `marshal fleet metrics`. Live `cpu`/`mem` also begin
appearing in `marshal fleet ps`.

**In scope (M8):**
- A `MetricBatch` message on the existing `Fleet.Connect` agent→server stream.
- Server-side per-agent SQLite storage, reusing the existing `internal/metricstore` package.
- A new `FleetMetricsHistory` RPC and `marshal fleet metrics <agent> <selector>` CLI.
- Gap-free server history via watermark-driven backfill on reconnect (see §3).
- Live `cpu`/`mem` populated in `marshal fleet ps`.
- Two folded-in deferred fixes from the M7 handoff (empty agent name; clean-shutdown log noise).

**Out of scope (deferred):**
- Log records up + server-side log files + Approach-B interleave (**M8b**).
- Downward control commands (**M9**).
- TLS, bootstrap tokens, agent identity/authorization (**M10**) — **M8 transport stays plaintext
  and unauthenticated, trust-all**, like M7.
- A REST API and the dashboard (sub-project #4).
- Live config/policy hot-reload.

## 3. Locked decisions (from this brainstorm)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Milestone scope | **Metrics only**; logs split to M8b | Keeps the milestone thin and independently useful; mirrors the local M5/M6 split. |
| Disconnect handling | **Backfill on reconnect** (gap-free server history) | The agent already holds a complete local history; replaying the gap keeps the server's copy contiguous. Chosen over best-effort (which leaves outage-shaped gaps). |
| Sample sourcing | **Watermark-driven pull from local `metricstore`** | The fleet client ships local rows newer than the server's acknowledged high-water-mark. **Live push and backfill are the same mechanism** — no separate live/backfill code paths and no boundary to keep gap-free. Chosen over a live-sampler tap + separate backfill query, and over shipping pre-aggregated buckets (which would rob the server of re-bucketing at query time). |
| Sample fidelity | **Raw samples up; server re-buckets at query time** | Matches the local `metricstore` model; a dashboard/CLI can ask for any bucket width later. |
| Server storage layout | **Per-agent SQLite file**, reusing `metricstore` unchanged | `<data-dir>/agents/<agent>/metrics.db`. Agent isolation; maximal reuse (the `label` column stays `app#instance`, no schema change). Fleet scale is tens of hosts, so per-agent DB files are fine. |
| High-water-mark | **Derived, not bookkept** | `last_metric_ts_ms` = `SELECT max(ts)` from the agent's store. No separate watermark state on the server; the store *is* the source of truth, so the agent self-heals on reconnect. |
| History read protocol | **`FleetMetricsHistory` gRPC on the same server**, reusing `daemon.proto`'s `MetricBucket`/`MetricsHistoryResponse` | Same single-protocol, DRY approach as M7's `ProcInfo` reuse; the richer dashboard REST API is sub-project #4. |

## 4. The gRPC contract (`proto/marshal/v1/fleet.proto`)

Extends the existing `Fleet` service and messages; reuses `daemon.proto`'s metric response types
(same proto package `marshal.v1`, same Go package `pb`).

```proto
service Fleet {
  rpc Connect(stream AgentMessage) returns (stream ServerMessage);
  rpc ListFleet(ListFleetRequest) returns (ListFleetResponse);
  rpc FleetMetricsHistory(FleetMetricsHistoryRequest) returns (MetricsHistoryResponse); // NEW
}

message AgentMessage {
  oneof msg {
    Hello hello = 1;
    StateSnapshot snapshot = 2;
    MetricBatch metrics = 3;           // NEW
  }
}

message HelloAck {
  int64 last_metric_ts_ms = 1;         // NEW: server's stored high-water-mark for this agent (0 = none)
}

// One stored sample row, flattened so it maps 1:1 to a metricstore row.
message MetricSample {
  int64  ts_ms = 1;
  string label = 2;                    // "app#instance"
  double cpu   = 3;
  int64  mem   = 4;
}
message MetricBatch { repeated MetricSample samples = 1; }

message FleetMetricsHistoryRequest {
  string agent_name = 1;
  string selector   = 2;               // name or id within that agent, resolved like local metrics
  int64  since_ms   = 3;               // lookback window; server queries ts >= now - since_ms
  int64  bucket_ms  = 4;               // bucket width; 0 = server auto-picks (~60 buckets)
}
// Response reuses daemon.proto's MetricsHistoryResponse { repeated MetricBucket buckets }.
```

Notes:
- `MetricBatch.samples` is **flattened** (each sample carries its own `ts_ms`) so it maps directly
  to a `metricstore` row; a batch may span multiple sample timestamps (e.g. a reconnect backfill).
- `ServerMessage` is unchanged in M8 — the downstream direction is still reserved for M9.
- Proto regenerated via the existing `go generate ./internal/pb` (protoc on PATH).

## 5. Agent side (`internal/fleet` + `internal/metricstore`)

**New `metricstore` accessor.** `SamplesSince(tsMs int64) ([]TimestampedSample, error)` returns raw
rows with `ts > tsMs`, ordered by `ts` ascending, where
`TimestampedSample = { TsMs int64; Label string; Cpu float64; Mem uint64 }`. (The server adds one
more accessor, `MaxTs`, in §6; `Append`/`Query`/`Prune` are unchanged and reused on both ends.)

**Fleet client watermark.** The client gets an injected
`MetricsSince func(tsMs int64) []*pb.MetricSample` (the daemon wires it to the local `metricstore`).
It holds an in-memory `watermark int64`:

1. On (re)connect, after `Hello`: read `HelloAck.last_metric_ts_ms` and set `watermark` to it.
2. On each interval tick (the same ticker that drives the M7 snapshot push): call
   `MetricsSince(watermark)`; if non-empty, send a `MetricBatch`. On a successful `Send`, advance
   `watermark` to the max `ts_ms` shipped.
3. The immediate first push after connect ships everything newer than the ack — i.e. the outage
   gap — then steady state ships one tick's worth.

Because the server's `max(ts)` is authoritative and re-read on every reconnect, the agent
**self-heals**: even if a `Send` succeeded but the server crashed before the insert was durable,
the next `HelloAck` resets the agent's watermark to the server's true max.

**Resilience.** Server-down behaviour is unchanged from M7: the agent keeps sampling and storing
locally; a server outage only logs (with the §8 `context.Canceled` fix) and backs off. Backfill
depth is bounded by the local metric retention window (default 7 days) — an outage longer than that
loses the pre-retention tail from the server's copy. Documented, accepted.

## 6. Server side (`internal/server`)

- `marshal server` gains a `--data-dir` flag (default `$XDG_DATA_HOME/marshal-server`, falling back
  to `$HOME/.local/share/marshal-server` consistent with the agent store's resolution). The server
  is **no longer purely in-memory**.
- **Per-agent SQLite reusing `metricstore` unchanged:** `<data-dir>/agents/<agent>/metrics.db`.
  Stores are opened lazily on first contact from an agent and held open for the server's lifetime
  (fleet scale is small). The agent name is sanitised to a safe path segment before use.
- **On `MetricBatch`:** group the samples by `ts_ms` and `Append` each group **in ascending ts
  order** to that agent's store (the existing `metricstore.Append(tsMs, []Sample)` takes one ts with
  many samples, so the server groups the flattened batch back by ts). Each `Append` is one atomic
  transaction, and groups are committed oldest-first, so `max(ts)` always reflects a fully-committed
  prefix — making it a valid watermark even if a later group in the same batch fails.
- **On `Hello`:** look up (or open) the agent's store, compute `SELECT max(ts)` (a new tiny
  `metricstore.MaxTs() (int64, error)` accessor, returning 0 when empty), and return it in
  `HelloAck.last_metric_ts_ms`.
- **`FleetMetricsHistory`:** resolve the agent's store, resolve `selector` to a label set the same
  way the local daemon does, call the existing `metricstore.Query`, and merge per-instance buckets
  with the existing daemon-side `mergeBuckets` helper (or a shared copy). Re-bucketing happens at
  query time.
- **Pruning:** a goroutine prunes each open agent store beyond retention (default 7 days,
  test-overridable), mirroring the daemon's existing prune loop.
- M7's in-memory registry (live `procs`/`connected`/`lastSeen`) is retained for `ListFleet`;
  storage is an added layer alongside it.

**New `metricstore` accessor (server-side):** `MaxTs() (int64, error)` — the high-water-mark.
Together with §5's `SamplesSince`, these are the only `metricstore` additions.

## 7. CLI & live values (`cmd/marshal/fleet.go`)

- **`marshal fleet metrics <agent> <selector> [--since 1h] [--bucket ...] [--server host:port]`** —
  dials the server's `FleetMetricsHistory` and renders with the **existing `printMetrics`** renderer
  used by local `marshal metrics` (DRY). `--server` defaults to `$MARSHAL_SERVER` then
  `localhost:9000`, like `fleet ps`. Unknown agent or unreachable server → clear error, non-zero exit.
- **Live `cpu`/`mem` in `marshal fleet ps`:** the daemon caches the sampler's latest tick (it already
  receives the per-label `map[string]metrics.Sample` via `SetOnTick`), and the `fleetSnapshot`
  adapter merges those values into each `ProcInfo` before the snapshot is pushed. Until the first
  tick, values are zero (unchanged rendering). This is the visible payoff of metrics flowing and is
  independent of the storage path.

## 8. Folded-in deferred fixes (from the M7 handoff)

- **Empty agent name.** The agent defaults its name to `"unknown"` when `os.Hostname()` fails and no
  `server.name` is configured. The server **rejects an empty `Hello.agent_name`** (closes the stream
  with an `InvalidArgument` error) so two nameless agents cannot collide on the `""` key and snapshots
  are never silently dropped.
- **Clean-shutdown log noise.** `internal/fleet/client.go` `Run` skips the
  `"fleet: connection to <addr> ended: <err>"` log line when `errors.Is(err, context.Canceled)` — a
  normal daemon stop no longer logs a spurious error.

## 9. Error handling

- **Agent:** all fleet-client errors (dial, stream drop, send) trigger backoff-and-retry; none affect
  local supervision. A `MetricBatch` send failure leaves the watermark un-advanced, so the rows
  re-ship on reconnect. Delivery is at-least-once, but **no duplicate rows result**: on reconnect the
  agent resets its watermark to the server's `max(ts)` (a fully-committed prefix per §6) and ships
  strictly newer rows, so any group the server already stored is never re-sent.
- **Server:** a malformed/half-open stream is dropped and the agent marked offline (M7 behaviour);
  other agents and reads are unaffected. A failed `Append` is logged; the agent re-ships on reconnect.
- **CLI:** unreachable `--server` or unknown agent → clear connection/lookup error and non-zero exit.

## 10. Testing (TDD, per project convention)

- **`internal/metricstore`:** `SamplesSince` (rows strictly after ts, ascending order, empty case);
  `MaxTs` (value with data, 0 when empty).
- **`internal/fleet` client:** watermark initialised from `HelloAck`; ships only rows newer than the
  watermark; advances watermark only on successful send; backfills a simulated outage gap on
  reconnect; no dup/gap at the live↔backfill boundary; standalone (no `server:` block) ships nothing.
- **`internal/server`:** per-agent store routing keyed by name; `max(ts)` returned in `HelloAck`;
  `MetricBatch` grouped-by-ts append; `FleetMetricsHistory` bucketing and selector resolution;
  pruning beyond retention; empty `Hello.agent_name` rejected.
- **End-to-end (in-process bufconn):** spin up a server with a temp data dir, connect a real agent,
  let it sample, assert `fleet metrics` returns buckets; then kill the server mid-run, restart it,
  and assert the agent backfills the gap so server history is contiguous.
- **CLI:** `fleet metrics` rendering (reusing the `printMetrics` tests' fixtures); unknown-agent and
  unreachable-server error paths.
- **Regression:** `context.Canceled` shutdown produces no log line.
- Full gate before finishing: `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (clean),
  `go build ./...`.

## 11. New / changed surfaces (summary)

New:
- `cmd/marshal/fleet.go` — add `marshal fleet metrics` subcommand (alongside M7's `fleet ps`).
- `internal/server/` — per-agent metric storage layer, `FleetMetricsHistory` handler, `--data-dir`.

Changed:
- `proto/marshal/v1/fleet.proto` (+ regenerated `internal/pb`) — `MetricBatch`/`MetricSample`,
  `HelloAck.last_metric_ts_ms`, `FleetMetricsHistory` RPC + request.
- `internal/metricstore/store.go` — add `SamplesSince` and `MaxTs`; `TimestampedSample` type.
- `internal/fleet/client.go` — `MetricsSince` injection, watermark, `MetricBatch` push, and the
  `context.Canceled` log fix; `New` signature/option for the metrics source.
- `internal/daemon/server.go` — cache the latest sampler tick; wire `MetricsSince` (local
  `metricstore` read) and the latest-tick merge into the `fleetSnapshot` adapter; agent-name default
  to `"unknown"`.
- `cmd/marshal/server.go` — `--data-dir` flag; construct the server with a data dir.

Unchanged: the `Daemon` proto service (no new RPC); M7's in-memory registry and `ListFleet`.

## 12. Architecture context

Sub-project #3 (central server), milestone progression:
M7 (connection + live state) ✅ → **M8 (metric records up + storage) — this design** →
M8b (log records up + storage) → M9 (downward control) → M10 (auth + TLS). Sub-project #4 (web
dashboard) builds on top.
