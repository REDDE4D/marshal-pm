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
