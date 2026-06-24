# M-C · Host system metrics — design spec

**Date:** 2026-06-24
**Milestone:** M-C (third data milestone of the dashboard program; see
`2026-06-23-dashboard-program-roadmap.md`). Medium, additive.
**Branch:** `mC-host-metrics` off `dev`.

## Goal

Add current-value **host** gauges per agent — CPU%, load average, memory, network I/O
rate — collected on the agent, pushed with the existing periodic `StateSnapshot`, stored
on `AgentState`, and served via `/api/fleet` for the dashboard's cluster cells.
**Point-in-time only** (no time-series, no new endpoint); one set of values per host,
refreshed each agent interval (~5s).

## Decisions (locked in brainstorming)

1. **Point-in-time gauges**, not time-series. Host metrics live on `AgentState`, refreshed
   each tick — exactly where M-B's host metadata and the per-process gauges already sit. No
   metricstore table, no host charts, no bucketing.
2. **Network I/O as a rate** (bytes/sec), computed from gopsutil's cumulative counters via
   deltas across ticks — consistent with CPU% (also a rate) and useful as a live cell. Not
   cumulative since-boot totals.
3. **Aggregate only** — whole-host CPU% (not per-core), summed network across NICs (not
   per-NIC).
4. **Rides the existing push** — `HostMetrics` is added to `StateSnapshot`, not a new
   message stream or endpoint.

## Field set — new `HostMetrics` proto message

| Field          | Type     | Source (gopsutil/v3)                         |
|----------------|----------|----------------------------------------------|
| `cpu_percent`  | `double` | `cpu.Percent(0, false)[0]` — aggregate, delta-based |
| `load1`        | `double` | `load.Avg().Load1`                           |
| `load5`        | `double` | `load.Avg().Load5`                           |
| `load15`       | `double` | `load.Avg().Load15`                          |
| `mem_total`    | `uint64` | `mem.VirtualMemory().Total`                  |
| `mem_used`     | `uint64` | `mem.VirtualMemory().Used`                   |
| `mem_used_pct` | `double` | `mem.VirtualMemory().UsedPercent`            |
| `net_rx_bps`   | `double` | Δ`net.IOCounters(false)[0].BytesRecv` ÷ Δt   |
| `net_tx_bps`   | `double` | Δ`net.IOCounters(false)[0].BytesSent` ÷ Δt   |

All best-effort: a failing subsystem leaves its field(s) zero and never fails the whole
sample.

## Component changes

### 1. Host sampler — new package `internal/hostmetrics`

One responsibility: produce a `*pb.HostMetrics` for the current instant. Holds the delta
state needed for rates:

- **Net rate:** retains the previous `BytesRecv`/`BytesSent` counters and the timestamp of
  the previous sample. `rate = (now - prev) / elapsedSeconds`. The **first** sample has no
  prior counters → rates are `0`; it stores the baseline for next time. Guards against a
  zero/negative elapsed and against counter resets (negative delta → 0).
- **CPU%:** `cpu.Percent(0, false)` returns the utilization since the previous call;
  gopsutil keeps that state internally. The sampler **primes** it once at construction (a
  discarded initial call) so the first real sample is a true delta, mirroring how the
  per-process `Sampler` primes its CPU handles.
- **Load / memory:** stateless point reads each sample.

Shape:

```go
package hostmetrics

type Sampler struct { /* prev net counters + prev time */ }

func NewSampler() *Sampler            // primes cpu.Percent
func (s *Sampler) Sample() *pb.HostMetrics
```

Best-effort: each gopsutil call is guarded; an error leaves that field zero. `Sample()`
never returns nil (returns a `*pb.HostMetrics`, zeroed where data was unavailable).

### 2. Proto — `proto/marshal/v1/fleet.proto`

New message + two additive fields:

```proto
message HostMetrics {
  double cpu_percent  = 1;
  double load1        = 2;
  double load5        = 3;
  double load15       = 4;
  uint64 mem_total    = 5;
  uint64 mem_used     = 6;
  double mem_used_pct = 7;
  double net_rx_bps   = 8; // bytes/sec received
  double net_tx_bps   = 9; // bytes/sec sent
}

message StateSnapshot {
  repeated ProcInfo procs = 1;
  HostMetrics host = 2;        // M-C: current host gauges (nil if agent has none)
}

message AgentState {
  // … existing fields 1–10 …
  HostMetrics host = 11;       // M-C: latest host gauges
}
```

Regenerate `internal/pb` via `make proto`.

### 3. Fleet client — `internal/fleet/client.go`

- New option `WithHost(fn func() *pb.HostMetrics)` (mirrors `WithMetrics`/`WithLogs`),
  storing `c.host`.
- `pushSnapshot` sets `Host: c.host()` when `c.host != nil`, else leaves it nil:

```go
func (c *Client) pushSnapshot(send func(*pb.AgentMessage) error) error {
	snap := &pb.StateSnapshot{Procs: c.snapshot()}
	if c.host != nil {
		snap.Host = c.host()
	}
	return send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{Snapshot: snap}})
}
```

### 4. Daemon wiring — `internal/daemon`

The daemon constructs a `hostmetrics.Sampler`, runs nothing extra on a ticker (the sampler
is pulled on demand), and passes `fleet.WithHost(func() *pb.HostMetrics { return hs.Sample() })`
when building the fleet client. Sampling on each `pushSnapshot` call gives the natural
~interval cadence and keeps the net-rate delta aligned to the push interval.

### 5. Server — `internal/server`

The `AgentMessage_Snapshot` case in `server.go` (around line 124-126) currently calls
`s.reg.Update(name, m.Snapshot.GetProcs())`. M-C stores the host metrics alongside the procs.
Preferred shape: extend the registry's `Update(name, procs, host)` (single call site) to also
take `m.Snapshot.GetHost()` and stash it on the agent entry (`e.host`); `Registry.List()`
(registry.go ~108-112) then adds `Host: e.host` to the emitted `AgentState`. (A separate
`SetHost` setter is an equally valid alternative; the plan picks one.) No other server change —
`List()` already returns the full `AgentState`, so host metrics flow to `/api/fleet`.

### 6. Dashboard `/api/fleet` — `internal/dashboard/fleet.go`

`agentView` gains a nested `host` object:

```go
type hostView struct {
	CPUPercent float64 `json:"cpu_percent"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	MemTotal   uint64  `json:"mem_total"`
	MemUsed    uint64  `json:"mem_used"`
	MemUsedPct float64 `json:"mem_used_pct"`
	NetRxBps   float64 `json:"net_rx_bps"`
	NetTxBps   float64 `json:"net_tx_bps"`
}
// agentView gains: Host *hostView `json:"host,omitempty"`
```

`fleetView` maps it from `a.GetHost()` when non-nil (omitted when the agent reported none).

### 7. Web — `web/src/api.ts` + the agent band component

- `api.ts`: an `AgentHost` type and `host?: AgentHost` on `Agent`.
- The agent header/band (where M-B renders hostname/os/arch) gains a minimal host line:
  `cpu N% · load a/b/c · mem U/T (P%) · ↓X/s ↑Y/s`, formatting bytes/sec with a helper and
  omitting cleanly when `host` is absent. **Minimal transitional surfacing** — M-A delivers
  the real cluster-cell visual treatment. Rebuild the embedded bundle (`make ui`).

## Testing (TDD per layer)

- **hostmetrics:** two successive `Sample()` calls with injected/fake counters → correct
  bytes/sec; first sample → 0 rates; counter reset (negative delta) → 0, not a huge number;
  a stubbed failing subsystem leaves its field zero while others populate. (Where gopsutil
  can't be faked cleanly, assert real-host invariants: `mem_total > 0`, `cpu_percent >= 0`,
  rates `>= 0`.)
- **fleet client:** `pushSnapshot` includes `Host` when `WithHost` set, nil otherwise.
- **server:** applying a snapshot with host metrics updates `AgentState.Host`.
- **dashboard:** `/api/fleet` JSON carries the `host` object with correct values; omitted
  when nil.

## Edge cases / non-goals

- **Load average** unavailable off Unix → zeros (Marshal targets darwin/linux).
- **First tick / reconnect:** net rate 0 until a second sample establishes a delta.
- **Counter reset / wrap:** negative delta clamped to 0.
- **Non-goals:** no host time-series or charts; no new endpoint; no per-core/per-NIC
  breakdown; no swap, disk, or temperature (possible later milestone); no change to the
  per-process metric path or metricstore.

## Next step

Write the implementation plan (writing-plans), then build on `mC-host-metrics`, TDD per
layer, with a `CHANGELOG.md` `[Unreleased]` entry, handoff, and a live demo.
