package memguard

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/metrics"
)

func TestGuardFiresAfterThreshold(t *testing.T) {
	var restarted []string
	g := New(func(name string) { restarted = append(restarted, name) }, nil)
	g.SetLimit("web", 100)
	over := map[string]metrics.Sample{"web#0": {Mem: 200}}
	under := map[string]metrics.Sample{"web#0": {Mem: 50}}

	g.Check(over) // 1
	g.Check(over) // 2
	if len(restarted) != 0 {
		t.Fatalf("fired early: %v", restarted)
	}
	g.Check(over) // 3 -> fire
	if len(restarted) != 1 || restarted[0] != "web" {
		t.Fatalf("want [web], got %v", restarted)
	}

	g.Check(over) // breach cleared on fire; counting restarts
	g.Check(over)
	if len(restarted) != 1 {
		t.Fatalf("fired too soon after reset: %v", restarted)
	}

	g.Check(under) // drop under limit resets the counter
	g.Check(over)
	g.Check(over)
	if len(restarted) != 1 {
		t.Fatalf("under-limit did not reset: %v", restarted)
	}
	g.Check(over) // 3rd consecutive -> fire again
	if len(restarted) != 2 {
		t.Fatalf("want 2 fires total, got %v", restarted)
	}
}

func TestGuardNoLimitNeverFires(t *testing.T) {
	n := 0
	g := New(func(string) { n++ }, nil)
	for i := 0; i < 5; i++ {
		g.Check(map[string]metrics.Sample{"web#0": {Mem: 1 << 40}})
	}
	if n != 0 {
		t.Fatalf("fired without a configured limit: %d", n)
	}
}

func TestGuardRemoveDropsState(t *testing.T) {
	n := 0
	g := New(func(string) { n++ }, nil)
	g.SetLimit("web", 100)
	over := map[string]metrics.Sample{"web#0": {Mem: 200}}
	g.Check(over)
	g.Check(over)
	g.Remove("web")
	g.Check(over) // would have been the 3rd, but limit + breach are gone
	if n != 0 {
		t.Fatalf("fired after Remove: %d", n)
	}
}
