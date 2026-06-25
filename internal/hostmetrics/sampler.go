// Package hostmetrics samples current-value host gauges (CPU%, load average,
// memory, network I/O rate) via gopsutil, for the agent's periodic fleet push.
package hostmetrics

import (
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"

	"github.com/REDDE4D/marshal-pm/internal/pb"
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
