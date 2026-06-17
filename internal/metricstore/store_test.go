package metricstore

import (
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAppendQueryRoundTrip(t *testing.T) {
	st := openTemp(t)
	if err := st.Append(1000, []Sample{{Label: "a#0", Cpu: 10, Mem: 100}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Append(2000, []Sample{{Label: "a#0", Cpu: 20, Mem: 200}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(got), got)
	}
	if got[0].TsMs != 1000 || got[1].TsMs != 2000 {
		t.Fatalf("bucket timestamps = %d,%d want 1000,2000", got[0].TsMs, got[1].TsMs)
	}
}

func TestBucketAggregation(t *testing.T) {
	st := openTemp(t)
	// Two samples in the same 1000ms bucket [2000,3000): cpu 10 & 30, mem 100 & 300.
	if err := st.Append(2000, []Sample{{Label: "a#0", Cpu: 10, Mem: 100}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Append(2500, []Sample{{Label: "a#0", Cpu: 30, Mem: 300}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d buckets, want 1", len(got))
	}
	b := got[0]
	if b.TsMs != 2000 || b.CpuAvg != 20 || b.CpuMax != 30 || b.MemAvg != 200 || b.MemMax != 300 {
		t.Fatalf("bucket = %+v, want ts=2000 cpuAvg=20 cpuMax=30 memAvg=200 memMax=300", b)
	}
}

func TestQueryRespectsSinceAndLabel(t *testing.T) {
	st := openTemp(t)
	if err := st.Append(1000, []Sample{{Label: "a#0", Cpu: 1, Mem: 1}, {Label: "b#0", Cpu: 9, Mem: 9}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := st.Append(5000, []Sample{{Label: "a#0", Cpu: 2, Mem: 2}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := st.Query(QueryReq{Label: "a#0", SinceMs: 3000, BucketMs: 1000})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 || got[0].TsMs != 5000 {
		t.Fatalf("got %+v, want single bucket at 5000 (since filter + label filter)", got)
	}
}

func TestPrune(t *testing.T) {
	st := openTemp(t)
	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 1, Mem: 1}})
	_ = st.Append(5000, []Sample{{Label: "a#0", Cpu: 2, Mem: 2}})
	n, err := st.Prune(3000)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}
	got, _ := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if len(got) != 1 || got[0].TsMs != 5000 {
		t.Fatalf("after prune got %+v, want only ts=5000", got)
	}
}

func TestReopenPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 7, Mem: 70}})
	_ = st.Close()

	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	got, _ := st2.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 1000})
	if len(got) != 1 || got[0].CpuAvg != 7 {
		t.Fatalf("after reopen got %+v, want cpuAvg=7", got)
	}
}

func TestQueryRejectsZeroBucket(t *testing.T) {
	st := openTemp(t)
	if _, err := st.Query(QueryReq{Label: "a#0", SinceMs: 0, BucketMs: 0}); err == nil {
		t.Fatal("expected error for zero bucket width")
	}
}
