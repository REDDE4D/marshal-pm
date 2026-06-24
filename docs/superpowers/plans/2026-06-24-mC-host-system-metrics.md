# M-C Host System Metrics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add per-host current-value gauges — CPU%, load average, memory, network I/O rate — collected on the agent, pushed with the periodic `StateSnapshot`, stored on `AgentState`, and served via `/api/fleet`.

**Architecture:** A new `internal/hostmetrics` sampler reads gopsutil host stats and returns a `*pb.HostMetrics`, holding delta state to turn cumulative network counters into a bytes/sec rate. The daemon pulls it on each snapshot push via a new `fleet.WithHost` option; the server stores the latest on the registry's `AgentState`; the dashboard exposes a nested `host` JSON object the SPA renders on the agent band. Point-in-time only — no time-series, no new endpoint.

**Tech Stack:** Go 1.26, gopsutil/v3 (`cpu`/`load`/`mem`/`net`), protobuf (`make proto`), React/TypeScript SPA (`make ui`).

## Global Constraints

- **TDD:** failing test first, then implementation. `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .` (empty) before finishing.
- **Point-in-time only:** no metricstore table, no host charts, no new endpoint. Host metrics live on `AgentState`, refreshed each agent push (~interval).
- **Network I/O is a rate** (bytes/sec) from counter deltas across ticks, NOT cumulative since-boot totals.
- **Aggregate only:** whole-host CPU% (`cpu.Percent(0,false)[0]`), summed network across NICs (`net.IOCounters(false)[0]`).
- **Best-effort sampling:** each gopsutil call is guarded; a failure leaves that field zero and never fails the whole sample. `Sample()` never returns nil.
- **Net-rate edge cases:** first reading → 0 (no prior counters); non-positive elapsed → 0; counter reset (negative delta) → 0 (never a huge number).
- **Proto changes are additive:** new `HostMetrics` message; `StateSnapshot.host = 2`; `AgentState.host = 11`. Regenerate `internal/pb` with `make proto` (never hand-edit `*.pb.go`).
- **Changelog:** add an `[Unreleased]` entry as part of the work.
- **Commit trailer:** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- **Branch:** `mC-host-metrics` (already created off `dev`; the design spec is committed at `8cab6e9`).

---

## File Structure

- `proto/marshal/v1/fleet.proto` + `internal/pb/*` — `HostMetrics`, `StateSnapshot.host`, `AgentState.host` (Task 1).
- `internal/hostmetrics/sampler.go` (new) + test — the host sampler + pure `rate` helper (Task 2).
- `internal/fleet/client.go` — `HostFunc`, `WithHost`, `pushSnapshot` sets `Host` (Task 3).
- `internal/server/registry.go` + `internal/server/server.go` — store host on the entry, emit in `List()`, pass it at the snapshot call site (Task 4).
- `internal/daemon/server.go` — construct the sampler, wire `WithHost` (Task 5).
- `internal/dashboard/fleet.go` — `hostView`, `agentView.Host`, mapping (Task 6).
- `web/src/api.ts` + `web/src/Overview.tsx` + embedded bundle — type + agent-band render (Task 7).
- `CHANGELOG.md` + whole-branch verification (Task 8).

---

### Task 1: Proto — `HostMetrics` message + snapshot/state fields

**Files:**
- Modify: `proto/marshal/v1/fleet.proto` (`StateSnapshot` ~55; `AgentState` ~61-72)
- Regenerate: `internal/pb/fleet.pb.go` via `make proto`

**Interfaces:**
- Produces: `pb.HostMetrics` with fields `CpuPercent float64`, `Load1/Load5/Load15 float64`, `MemTotal/MemUsed uint64`, `MemUsedPct float64`, `NetRxBps/NetTxBps float64` and matching `Get*` getters; `pb.StateSnapshot.Host *HostMetrics` (`GetHost`); `pb.AgentState.Host *HostMetrics` (`GetHost`).

- [ ] **Step 1: Add the message and fields**

In `proto/marshal/v1/fleet.proto`, add the new message (place it just above `message StateSnapshot`):

```proto
// HostMetrics — M-C: current-value host gauges (point-in-time, per agent).
message HostMetrics {
  double cpu_percent  = 1; // aggregate host CPU utilization %
  double load1        = 2;
  double load5        = 3;
  double load15       = 4;
  uint64 mem_total    = 5; // bytes
  uint64 mem_used     = 6; // bytes
  double mem_used_pct = 7;
  double net_rx_bps   = 8; // bytes/sec received (rate)
  double net_tx_bps   = 9; // bytes/sec sent (rate)
}
```

Change `StateSnapshot` to carry host metrics:

```proto
message StateSnapshot {
  repeated ProcInfo procs = 1; // ProcInfo from daemon.proto
  HostMetrics host = 2;        // M-C: current host gauges (nil if none)
}
```

Add field 11 to `AgentState` (after `host_boot_unix = 10;`):

```proto
  HostMetrics host = 11; // M-C: latest host gauges
```

- [ ] **Step 2: Regenerate the Go bindings**

Run: `make proto`
Expected: succeeds; `internal/pb/fleet.pb.go` defines the `HostMetrics` type and the new `Host` fields.

- [ ] **Step 3: Verify build + getters exist**

Run: `go build ./... && grep -c 'func (x \*HostMetrics) GetNetRxBps\|func (x \*HostMetrics) GetCpuPercent\|func (x \*StateSnapshot) GetHost\|func (x \*AgentState) GetHost' internal/pb/fleet.pb.go`
Expected: build succeeds; grep prints `4`.

- [ ] **Step 4: Commit**

```bash
git add proto/marshal/v1/fleet.proto internal/pb/
git commit -m "feat(proto): add HostMetrics to StateSnapshot and AgentState

New HostMetrics message (cpu/load/mem/net-rate); StateSnapshot.host (2)
carries it on the periodic push; AgentState.host (11) stores the latest.
Regenerated internal/pb via make proto.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Host sampler — `internal/hostmetrics`

**Files:**
- Create: `internal/hostmetrics/sampler.go`
- Test: `internal/hostmetrics/sampler_test.go`

**Interfaces:**
- Consumes: `pb.HostMetrics` (Task 1); gopsutil `cpu`/`load`/`mem`/`net`.
- Produces: `hostmetrics.NewSampler() *Sampler`; `(*Sampler).Sample() *pb.HostMetrics` (never nil; zeroed where data unavailable; net rates 0 on first call).

- [ ] **Step 1: Write the failing test**

Create `internal/hostmetrics/sampler_test.go`:

```go
package hostmetrics

import (
	"testing"
	"time"
)

func TestRate(t *testing.T) {
	t0 := time.Unix(1000, 0)
	// Normal: 1000 bytes over 2s = 500 B/s each direction.
	rx, tx := rate(
		netCounters{rx: 100, tx: 200, t: t0},
		netCounters{rx: 1100, tx: 1200, t: t0.Add(2 * time.Second)},
	)
	if rx != 500 || tx != 500 {
		t.Fatalf("rate = (%v, %v), want (500, 500)", rx, tx)
	}
	// First reading: prev has zero time -> 0, 0.
	if rx, tx := rate(netCounters{}, netCounters{rx: 1100, tx: 1200, t: t0}); rx != 0 || tx != 0 {
		t.Fatalf("first-reading rate = (%v, %v), want (0, 0)", rx, tx)
	}
	// Counter reset: cur < prev -> 0, not a huge number.
	if rx, _ := rate(netCounters{rx: 1000, t: t0}, netCounters{rx: 5, t: t0.Add(time.Second)}); rx != 0 {
		t.Fatalf("reset rate rx = %v, want 0", rx)
	}
	// Non-positive elapsed -> 0.
	if rx, _ := rate(netCounters{rx: 0, t: t0}, netCounters{rx: 1000, t: t0}); rx != 0 {
		t.Fatalf("zero-dt rate rx = %v, want 0", rx)
	}
}

func TestSampleRealHostInvariants(t *testing.T) {
	s := NewSampler()
	first := s.Sample()
	if first == nil {
		t.Fatal("Sample() returned nil")
	}
	if first.GetMemTotal() == 0 {
		t.Fatalf("mem_total = 0, want > 0 on a real host")
	}
	if first.GetCpuPercent() < 0 {
		t.Fatalf("cpu_percent = %v, want >= 0", first.GetCpuPercent())
	}
	// First sample has no prior net counters -> rates are 0.
	if first.GetNetRxBps() != 0 || first.GetNetTxBps() != 0 {
		t.Fatalf("first-sample net = (%v, %v), want (0, 0)", first.GetNetRxBps(), first.GetNetTxBps())
	}
	// Second sample: rates are non-negative (a real delta or 0).
	time.Sleep(20 * time.Millisecond)
	second := s.Sample()
	if second.GetNetRxBps() < 0 || second.GetNetTxBps() < 0 {
		t.Fatalf("second-sample net = (%v, %v), want >= 0", second.GetNetRxBps(), second.GetNetTxBps())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hostmetrics/ -run 'TestRate|TestSampleRealHostInvariants' -v`
Expected: FAIL — package/`rate`/`NewSampler` undefined (compile error).

- [ ] **Step 3: Write the implementation**

Create `internal/hostmetrics/sampler.go`:

```go
// Package hostmetrics samples current-value host gauges (CPU%, load average,
// memory, network I/O rate) via gopsutil, for the agent's periodic fleet push.
package hostmetrics

import (
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"

	"marshal/internal/pb"
)

// netCounters is one reading of the aggregate network byte counters.
type netCounters struct {
	rx, tx uint64
	t      time.Time
}

// rate returns rx/tx bytes-per-second between two counter readings. It returns
// 0 for the first reading (prev.t zero), non-positive elapsed, or a counter
// reset/wrap (negative delta) — never a spurious huge value.
func rate(prev, cur netCounters) (rxBps, txBps float64) {
	if prev.t.IsZero() {
		return 0, 0
	}
	dt := cur.t.Sub(prev.t).Seconds()
	if dt <= 0 {
		return 0, 0
	}
	return perSec(prev.rx, cur.rx, dt), perSec(prev.tx, cur.tx, dt)
}

func perSec(prev, cur uint64, dt float64) float64 {
	if cur < prev {
		return 0
	}
	return float64(cur-prev) / dt
}

// Sampler reads host gauges, retaining the previous net counters for the rate.
type Sampler struct {
	prevNet netCounters
}

// NewSampler builds a host sampler and primes cpu.Percent so the first real
// Sample() is a true delta rather than since-boot.
func NewSampler() *Sampler {
	_, _ = cpu.Percent(0, false) // prime the gopsutil CPU delta
	return &Sampler{}
}

// Sample returns the current host gauges. Best-effort: a failing subsystem
// leaves its field(s) zero. Never returns nil.
func (s *Sampler) Sample() *pb.HostMetrics {
	h := &pb.HostMetrics{}
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		h.CpuPercent = pct[0]
	}
	if la, err := load.Avg(); err == nil && la != nil {
		h.Load1, h.Load5, h.Load15 = la.Load1, la.Load5, la.Load15
	}
	if vm, err := mem.VirtualMemory(); err == nil && vm != nil {
		h.MemTotal, h.MemUsed, h.MemUsedPct = vm.Total, vm.Used, vm.UsedPercent
	}
	if io, err := net.IOCounters(false); err == nil && len(io) > 0 {
		cur := netCounters{rx: io[0].BytesRecv, tx: io[0].BytesSent, t: time.Now()}
		h.NetRxBps, h.NetTxBps = rate(s.prevNet, cur)
		s.prevNet = cur
	}
	return h
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/hostmetrics/ -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/hostmetrics/
git commit -m "feat(hostmetrics): host gauge sampler with net-rate deltas

CPU%/load/memory point reads + network bytes/sec from counter deltas
(0 on first read, reset, or non-positive elapsed). Best-effort; never
returns nil.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Fleet client — `WithHost` option + `pushSnapshot`

**Files:**
- Modify: `internal/fleet/client.go` (`Client` struct ~37-52; options region ~65-72; `pushSnapshot` ~245-249)
- Test: `internal/fleet/client_test.go`

**Interfaces:**
- Consumes: `pb.HostMetrics`, `pb.StateSnapshot.Host` (Task 1).
- Produces: type `HostFunc func() *pb.HostMetrics`; `fleet.WithHost(fn HostFunc) Option`; `pushSnapshot` sets `StateSnapshot.Host` when a host func is configured.

- [ ] **Step 1: Write the failing test**

Add to `internal/fleet/client_test.go` (the file is `package fleet`, so it can call the unexported `pushSnapshot`):

```go
func TestPushSnapshotIncludesHostWhenConfigured(t *testing.T) {
	// With WithHost: the sent snapshot carries the host metrics.
	c := New("", "agent", "v", func() []*pb.ProcInfo { return nil },
		WithHost(func() *pb.HostMetrics { return &pb.HostMetrics{CpuPercent: 42, MemTotal: 2048} }))
	var got *pb.AgentMessage
	if err := c.pushSnapshot(func(m *pb.AgentMessage) error { got = m; return nil }); err != nil {
		t.Fatalf("pushSnapshot: %v", err)
	}
	h := got.GetSnapshot().GetHost()
	if h == nil || h.GetCpuPercent() != 42 || h.GetMemTotal() != 2048 {
		t.Fatalf("host = %+v, want cpu=42 mem=2048", h)
	}

	// Without WithHost: host is nil.
	c2 := New("", "agent", "v", func() []*pb.ProcInfo { return nil })
	var got2 *pb.AgentMessage
	if err := c2.pushSnapshot(func(m *pb.AgentMessage) error { got2 = m; return nil }); err != nil {
		t.Fatalf("pushSnapshot: %v", err)
	}
	if got2.GetSnapshot().GetHost() != nil {
		t.Fatalf("host = %+v, want nil when WithHost not set", got2.GetSnapshot().GetHost())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/fleet/ -run TestPushSnapshotIncludesHostWhenConfigured -v`
Expected: FAIL — `WithHost` undefined (compile error).

- [ ] **Step 3: Add the `HostFunc` type, struct field, and option**

In `internal/fleet/client.go`, add the type near the other func types (after `CommandFunc`):

```go
// HostFunc returns the agent's current host gauges, or nil if unavailable.
type HostFunc func() *pb.HostMetrics
```

Add a `host HostFunc` field to the `Client` struct (after `commands CommandFunc`):

```go
	commands   CommandFunc
	host       HostFunc
```

Add the option (after `WithCommands`):

```go
// WithHost enables host-gauge shipping sourced from fn (sent with each snapshot).
func WithHost(fn HostFunc) Option { return func(c *Client) { c.host = fn } }
```

- [ ] **Step 4: Set `Host` in `pushSnapshot`**

In `internal/fleet/client.go`, replace `pushSnapshot`:

```go
func (c *Client) pushSnapshot(send func(*pb.AgentMessage) error) error {
	snap := &pb.StateSnapshot{Procs: c.snapshot()}
	if c.host != nil {
		snap.Host = c.host()
	}
	return send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{Snapshot: snap}})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/fleet/ -race -count=1`
Expected: PASS (new test plus all existing client tests).

- [ ] **Step 6: Commit**

```bash
git add internal/fleet/client.go internal/fleet/client_test.go
git commit -m "feat(fleet): WithHost option ships host gauges on each snapshot

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Server registry — store + emit host metrics

**Files:**
- Modify: `internal/server/registry.go` (`agentEntry` ~12-17; `Update` ~75-83; `List` ~108-119)
- Modify: `internal/server/server.go` (snapshot case ~124-126)
- Test: `internal/server/registry_test.go` (existing `Update` calls ~15, ~39; add a new test)

**Interfaces:**
- Consumes: `pb.HostMetrics`, `pb.StateSnapshot.GetHost()`, `pb.AgentState.Host` (Task 1).
- Produces: `Registry.Update(name string, procs []*pb.ProcInfo, host *pb.HostMetrics)`; `List()` emits `AgentState.Host`.

- [ ] **Step 1: Update existing call sites + add the failing test**

In `internal/server/registry_test.go`, update the two existing `Update` calls to the new 3-arg signature:
- line ~15: `reg.Update("web-1", []*pb.ProcInfo{{Name: "api", State: "online"}}, nil)`
- line ~39: `reg.Update("web-1", []*pb.ProcInfo{{Name: "api"}}, nil)`

Then add a new test:

```go
func TestRegistryStoresHostMetrics(t *testing.T) {
	reg := NewRegistry()
	reg.Open("h1")
	reg.Update("h1", nil, &pb.HostMetrics{CpuPercent: 12.5, MemTotal: 1000})
	got := reg.List()
	if len(got) != 1 {
		t.Fatalf("agents = %d, want 1", len(got))
	}
	h := got[0].GetHost()
	if h == nil || h.GetCpuPercent() != 12.5 || h.GetMemTotal() != 1000 {
		t.Fatalf("host = %+v, want cpu=12.5 mem=1000", h)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/server/ -run TestRegistryStoresHostMetrics -v`
Expected: FAIL — `Update` takes 2 args / `GetHost` returns nil (compile error on the new signature once you change the test calls, then assertion fail).

- [ ] **Step 3: Store host on the entry and in `Update`**

In `internal/server/registry.go`, add a field to `agentEntry` (after `meta AgentMeta`):

```go
	meta       AgentMeta
	host       *pb.HostMetrics
```

Change `Update` to accept and store host:

```go
// Update records a fresh snapshot (procs + host gauges) and bumps last-seen.
func (r *Registry) Update(name string, procs []*pb.ProcInfo, host *pb.HostMetrics) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.entry(name)
	e.procs = procs
	e.host = host
	e.streamOpen = true
	e.lastSeen = r.now()
}
```

- [ ] **Step 4: Emit host in `List()`**

In `internal/server/registry.go`, add `Host: e.host,` to the `&pb.AgentState{...}` literal in `List()` (after `HostBootUnix: e.meta.HostBootUnix,`):

```go
			HostBootUnix:   e.meta.HostBootUnix,
			Host:           e.host,
```

- [ ] **Step 5: Pass host at the server snapshot call site**

In `internal/server/server.go` (~line 126), update the `Update` call:

```go
				s.reg.Update(name, m.Snapshot.GetProcs(), m.Snapshot.GetHost())
```

- [ ] **Step 6: Run the server suite to verify all pass**

Run: `go test ./internal/server/ -race -count=1`
Expected: PASS (new test plus existing registry/server tests on the updated signature).

- [ ] **Step 7: Commit**

```bash
git add internal/server/registry.go internal/server/server.go internal/server/registry_test.go
git commit -m "feat(server): store and emit per-agent host metrics

Registry.Update takes host gauges from the snapshot; List() emits them
on AgentState.Host.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Daemon wiring — sample host metrics on the agent

**Files:**
- Modify: `internal/daemon/server.go` (sampler construction ~277; fleet-client build ~335-341)

**Interfaces:**
- Consumes: `hostmetrics.NewSampler` (Task 2); `fleet.WithHost` (Task 3).
- Produces: the running agent ships real host gauges with each snapshot. (No new unit test — this is construction/wiring glue verified by build and the live demo; the sampler and option are unit-tested in Tasks 2–3.)

- [ ] **Step 1: Construct the host sampler**

In `internal/daemon/server.go`, after `sampler := metrics.NewSampler(cfg.sampleInterval)` (~line 277), add:

```go
	hostSampler := hostmetrics.NewSampler()
```

Add the import `"marshal/internal/hostmetrics"` to the file's import block.

- [ ] **Step 2: Wire `WithHost` into the fleet client**

In `internal/daemon/server.go`, in the `fleet.New(...)` option list (~lines 339-341), add the host option (after `fleet.WithLogs(logsSince(reg)),`):

```go
				fleet.WithLogs(logsSince(reg)),
				fleet.WithHost(func() *pb.HostMetrics { return hostSampler.Sample() }),
				fleet.WithCommands(srv.handleFleetCommand))
```

- [ ] **Step 3: Verify the build compiles**

Run: `go build ./... && go vet ./internal/daemon/`
Expected: succeeds (no unused-import or type errors).

- [ ] **Step 4: Run the daemon suite (regression)**

Run: `go test ./internal/daemon/ -race -count=1`
Expected: PASS (unchanged behavior; wiring only).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go
git commit -m "feat(daemon): sample and ship host metrics with each snapshot

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Dashboard — expose host metrics in `/api/fleet`

**Files:**
- Modify: `internal/dashboard/fleet.go` (`agentView` ~24-35; `fleetView` ~57-68)
- Test: `internal/dashboard/fleet_test.go` (`TestFleetView` ~12-50)

**Interfaces:**
- Consumes: `pb.AgentState.GetHost()` and `pb.HostMetrics` getters (Task 1).
- Produces: `agentView.Host *hostView` JSON (`host`, omitempty) with fields `cpu_percent`, `load1/5/15`, `mem_total/used/used_pct`, `net_rx_bps/net_tx_bps`.

- [ ] **Step 1: Extend the failing test**

In `internal/dashboard/fleet_test.go`, within `TestFleetView`, add a `Host` to the `AgentState` literal (after `LastSeenUnix: 42,`):

```go
		LastSeenUnix: 42,
		Host: &pb.HostMetrics{CpuPercent: 7.5, Load1: 1.5, MemTotal: 4096, MemUsed: 1024, MemUsedPct: 25, NetRxBps: 100, NetTxBps: 200},
```

Then add assertions after the existing agent-view checks (after the `v[0].LastSeen != 42` block):

```go
	if v[0].Host == nil {
		t.Fatal("host view is nil, want populated")
	}
	if v[0].Host.CPUPercent != 7.5 || v[0].Host.Load1 != 1.5 {
		t.Fatalf("host cpu/load = %v/%v, want 7.5/1.5", v[0].Host.CPUPercent, v[0].Host.Load1)
	}
	if v[0].Host.MemTotal != 4096 || v[0].Host.MemUsed != 1024 || v[0].Host.MemUsedPct != 25 {
		t.Fatalf("host mem = %+v", v[0].Host)
	}
	if v[0].Host.NetRxBps != 100 || v[0].Host.NetTxBps != 200 {
		t.Fatalf("host net = %v/%v, want 100/200", v[0].Host.NetRxBps, v[0].Host.NetTxBps)
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dashboard/ -run TestFleetView -v`
Expected: FAIL — `v[0].Host` undefined (compile error).

- [ ] **Step 3: Add the `hostView` type and field**

In `internal/dashboard/fleet.go`, add the type (just above `type agentView struct`):

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
```

Add the field to `agentView` (after `HostBootUnix int64 ...`):

```go
	HostBootUnix   int64      `json:"host_boot_unix,omitempty"`
	Host           *hostView  `json:"host,omitempty"`
```

- [ ] **Step 4: Map it in `fleetView`**

In `internal/dashboard/fleet.go`, inside `fleetView`'s per-agent loop, before the `out = append(out, agentView{...})`, build the host view from the proto:

```go
		var host *hostView
		if h := a.GetHost(); h != nil {
			host = &hostView{
				CPUPercent: h.GetCpuPercent(),
				Load1:      h.GetLoad1(),
				Load5:      h.GetLoad5(),
				Load15:     h.GetLoad15(),
				MemTotal:   h.GetMemTotal(),
				MemUsed:    h.GetMemUsed(),
				MemUsedPct: h.GetMemUsedPct(),
				NetRxBps:   h.GetNetRxBps(),
				NetTxBps:   h.GetNetTxBps(),
			}
		}
```

Then add `Host: host,` to the `agentView{...}` literal (after `HostBootUnix: a.GetHostBootUnix(),`).

- [ ] **Step 5: Run the dashboard suite to verify all pass**

Run: `go test ./internal/dashboard/ -race -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/dashboard/fleet.go internal/dashboard/fleet_test.go
git commit -m "feat(dashboard): expose host metrics in /api/fleet

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Web — type + agent-band render + rebuild bundle

**Files:**
- Modify: `web/src/api.ts` (`Agent` type ~14-25)
- Modify: `web/src/Overview.tsx` (`agentMeta` ~19-29; agent-head render ~108-111)
- Rebuild: embedded SPA bundle via `make ui`

**Interfaces:**
- Consumes: `/api/fleet` JSON `host` object (Task 6).
- Produces: an `AgentHost` type + `host?` on `Agent`; a host-metrics line under the agent band. Transitional surfacing — M-A delivers the real cluster cells.

- [ ] **Step 1: Add the `AgentHost` type**

In `web/src/api.ts`, add the type (above the `Agent` type) and a field on `Agent`:

```ts
export type AgentHost = {
  cpu_percent: number;
  load1: number;
  load5: number;
  load15: number;
  mem_total: number;
  mem_used: number;
  mem_used_pct: number;
  net_rx_bps: number;
  net_tx_bps: number;
};
```

Add to the `Agent` type (after `host_boot_unix?: number;`):

```ts
  host?: AgentHost;
```

- [ ] **Step 2: Render a host line on the agent band**

In `web/src/Overview.tsx`, add a `hostMeta` helper and a byte-rate formatter (just after the `agentMeta` function, ~line 29):

```tsx
function fmtBps(bps: number): string {
  if (bps < 1024) return `${bps.toFixed(0)} B/s`;
  if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(1)} KB/s`;
  return `${(bps / (1024 * 1024)).toFixed(1)} MB/s`;
}

function hostMeta(a: Agent): string | null {
  const h = a.host;
  if (!h) return null;
  const gb = (b: number) => (b / (1024 * 1024 * 1024)).toFixed(1);
  return [
    `cpu ${h.cpu_percent.toFixed(0)}%`,
    `load ${h.load1.toFixed(2)}/${h.load5.toFixed(2)}/${h.load15.toFixed(2)}`,
    `mem ${gb(h.mem_used)}/${gb(h.mem_total)}gb (${h.mem_used_pct.toFixed(0)}%)`,
    `↓${fmtBps(h.net_rx_bps)} ↑${fmtBps(h.net_tx_bps)}`,
  ].join(" · ");
}
```

In the agent-head block (~line 108-111), render the host line under the existing `.seen` meta. Replace:

```tsx
            <span className="seen">{agentMeta(a)}</span>
```

with:

```tsx
            <span className="seen">{agentMeta(a)}</span>
            {hostMeta(a) && <span className="seen host-meta">{hostMeta(a)}</span>}
```

- [ ] **Step 3: Type-check the web sources**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no type errors.

- [ ] **Step 4: Rebuild the embedded bundle**

Run: `make ui`
Expected: succeeds; the embedded bundle under `internal/dashboard/dist` is regenerated.

- [ ] **Step 5: Verify the Go build still embeds cleanly**

Run: `go build ./...`
Expected: succeeds.

- [ ] **Step 6: Commit**

```bash
git add web/src/api.ts web/src/Overview.tsx internal/dashboard/dist
git commit -m "feat(dashboard): show host metrics on the agent band

Minimal transitional surfacing (cpu/load/mem/net line); M-A delivers the
real cluster-cell treatment. Rebuilt the embedded bundle.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

(If `git add internal/dashboard/dist` reports a different regenerated path, run `git status` and stage what `make ui` actually produced.)

---

### Task 8: Changelog + whole-branch verification

**Files:**
- Modify: `CHANGELOG.md` (`[Unreleased]` → `Added`)

- [ ] **Step 1: Add the changelog entry**

In `CHANGELOG.md`, under `## [Unreleased]` → `### Added` (first bullet):

```markdown
- **Host system metrics (M-C):** each agent now reports current-value host
  gauges — CPU%, load average (1/5/15), memory (used/total/percent), and
  network I/O rate (bytes/sec) — shipped with the periodic state push and shown
  on the agent band and in `/api/fleet`.
```

- [ ] **Step 2: Run the full verification suite**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l .`
Expected: all tests PASS; `go vet` clean; `gofmt -l .` prints nothing.

- [ ] **Step 3: Build the binary**

Run: `make build`
Expected: succeeds; `./marshal --version` prints a git-derived version.

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(mC): changelog entry for host system metrics

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 5: Final state check**

Run: `git log --oneline dev..HEAD && git status -s`
Expected: the M-C commits listed; working tree clean. Branch ready for code review → live demo → handoff → merge to `dev` (`--no-ff`).

---

## Post-plan steps (outside task checkboxes)

1. **Code review** — requesting-code-review (whole branch) before merge.
2. **Live demo** — per CLAUDE.md: scratch `XDG_DATA_HOME`, standard ports :9000/:9001, set password + rotate enroll token while the server is down, start the server, enroll an agent (`marshal start`), then confirm `/api/fleet` shows a real `host` object (cpu/load/mem real; net rate 0 on the first tick, then >0) and the agent band renders the host line. Tear down by data dir; verify no orphans (`pgrep -fl marshal`).
3. **Handoff** — `docs/handoffs/2026-06-24-mC-host-system-metrics.md`.
4. **Merge** `mC-host-metrics` → `dev` (`--no-ff`); delete the branch.
