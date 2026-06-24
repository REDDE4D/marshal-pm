package eventstore

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "restarts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRollupsCountsWindowAndLast(t *testing.T) {
	s := open(t)
	// Two recent events for a#0, one old; one event for b#0.
	for _, e := range []struct {
		label string
		ts    int64
	}{
		{"a#0", 1000}, {"a#0", 2000}, {"a#0", 100}, {"b#0", 3000},
	} {
		if err := s.Record(e.label, e.ts); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	roll, err := s.Rollups(1000) // window = ts >= 1000
	if err != nil {
		t.Fatalf("rollups: %v", err)
	}
	if roll["a#0"].Count24h != 2 {
		t.Fatalf("a#0 count = %d, want 2 (ts 1000,2000; 100 excluded)", roll["a#0"].Count24h)
	}
	if roll["a#0"].LastMs != 2000 {
		t.Fatalf("a#0 last = %d, want 2000", roll["a#0"].LastMs)
	}
	if roll["b#0"].Count24h != 1 || roll["b#0"].LastMs != 3000 {
		t.Fatalf("b#0 = %+v, want {1, 3000}", roll["b#0"])
	}
}

func TestRollupsLastSetEvenWhenOutsideWindow(t *testing.T) {
	s := open(t)
	_ = s.Record("c#0", 500) // only event is older than the window
	roll, err := s.Rollups(1000)
	if err != nil {
		t.Fatalf("rollups: %v", err)
	}
	if roll["c#0"].Count24h != 0 || roll["c#0"].LastMs != 500 {
		t.Fatalf("c#0 = %+v, want {0, 500}", roll["c#0"])
	}
}

func TestPruneDeletesOld(t *testing.T) {
	s := open(t)
	_ = s.Record("a#0", 100)
	_ = s.Record("a#0", 5000)
	n, err := s.Prune(1000) // delete ts < 1000
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned = %d, want 1", n)
	}
	roll, _ := s.Rollups(0)
	if roll["a#0"].LastMs != 5000 || roll["a#0"].Count24h != 1 {
		t.Fatalf("after prune a#0 = %+v, want {1, 5000}", roll["a#0"])
	}
}
