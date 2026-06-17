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

func TestMergeBucketsAndAutoBucket(t *testing.T) {
	a := []Bucket{{TsMs: 1000, CpuAvg: 10, CpuMax: 15, MemAvg: 100, MemMax: 150}}
	b := []Bucket{{TsMs: 1000, CpuAvg: 5, CpuMax: 20, MemAvg: 50, MemMax: 60}}
	got := MergeBuckets([][]Bucket{a, b})
	if len(got) != 1 || got[0].CpuAvg != 15 || got[0].CpuMax != 20 || got[0].MemAvg != 150 || got[0].MemMax != 150 {
		t.Fatalf("MergeBuckets = %+v, want summed avgs + max maxes", got)
	}
	if w := AutoBucketMs(0, 600000); w != 600000 {
		t.Fatalf("AutoBucketMs explicit = %d, want 600000", w)
	}
	if w := AutoBucketMs(60000, 0); w != 1000 {
		t.Fatalf("AutoBucketMs auto-floored = %d, want 1000", w)
	}
	if w := AutoBucketMs(600000, 0); w != 10000 {
		t.Fatalf("AutoBucketMs auto = %d, want 10000 (600000/60)", w)
	}
}

func TestSamplesSinceMaxTsLabels(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if mx, err := st.MaxTs(); err != nil || mx != 0 {
		t.Fatalf("empty MaxTs = %d, %v; want 0, nil", mx, err)
	}

	_ = st.Append(1000, []Sample{{Label: "a#0", Cpu: 10, Mem: 100}})
	_ = st.Append(2000, []Sample{{Label: "a#0", Cpu: 20, Mem: 200}, {Label: "b#0", Cpu: 5, Mem: 50}})

	got, err := st.SamplesSince(1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("SamplesSince(1000) len = %d, want 2 (strictly newer than 1000)", len(got))
	}
	if got[0].TsMs != 2000 || (got[0].Label != "a#0" && got[0].Label != "b#0") {
		t.Fatalf("unexpected first row: %+v", got[0])
	}

	mx, err := st.MaxTs()
	if err != nil || mx != 2000 {
		t.Fatalf("MaxTs = %d, %v; want 2000, nil", mx, err)
	}

	labels, err := st.Labels()
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 || labels[0] != "a#0" || labels[1] != "b#0" {
		t.Fatalf("Labels = %v, want [a#0 b#0]", labels)
	}
}
