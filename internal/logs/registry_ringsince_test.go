package logs

import (
	"testing"
	"time"
)

func TestRingSinceMergesAndFiltersIncludingFutureSinks(t *testing.T) {
	base := time.Now()
	tick := base
	reg := NewRegistry(t.TempDir())
	reg.now = func() time.Time { tick = tick.Add(time.Millisecond); return tick }

	// existing sink: two lines
	a := reg.For("api#0")
	_, _ = a.Writer(false).Write([]byte("a1\na2\n"))

	cut := tick.UnixMilli() // watermark after the two api#0 lines

	// a sink created AFTER we captured `cut` must still be covered
	b := reg.For("web#0")
	_, _ = b.Writer(true).Write([]byte("b1\n"))

	got := reg.RingSince(cut)
	if len(got) != 1 || got[0].Label != "web#0" || got[0].Text != "b1" || !got[0].Stderr {
		t.Fatalf("RingSince(cut) = %+v, want one web#0 stderr line b1", got)
	}

	all := reg.RingSince(0)
	if len(all) != 3 {
		t.Fatalf("RingSince(0) = %d lines, want 3", len(all))
	}
	// ascending by ts
	for i := 1; i < len(all); i++ {
		if all[i].Ts.Before(all[i-1].Ts) {
			t.Fatalf("RingSince(0) not ascending: %+v", all)
		}
	}
}
