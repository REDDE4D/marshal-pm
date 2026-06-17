package daemon

import (
	"context"
	"testing"
	"time"

	"marshal/internal/manager"
	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMergeBucketsSumsAndMaxes(t *testing.T) {
	a := []metricstore.Bucket{{TsMs: 1000, CpuAvg: 10, CpuMax: 15, MemAvg: 100, MemMax: 150}}
	b := []metricstore.Bucket{
		{TsMs: 1000, CpuAvg: 20, CpuMax: 12, MemAvg: 200, MemMax: 120},
		{TsMs: 2000, CpuAvg: 5, CpuMax: 5, MemAvg: 50, MemMax: 50},
	}
	got := metricstore.MergeBuckets([][]metricstore.Bucket{a, b})
	if len(got) != 2 {
		t.Fatalf("got %d buckets, want 2", len(got))
	}
	// Bucket 1000: avg summed across instances, max = max of maxes.
	if got[0].TsMs != 1000 || got[0].CpuAvg != 30 || got[0].CpuMax != 15 || got[0].MemAvg != 300 || got[0].MemMax != 150 {
		t.Fatalf("merged[0] = %+v", got[0])
	}
	if got[1].TsMs != 2000 || got[1].CpuAvg != 5 {
		t.Fatalf("merged[1] = %+v", got[1])
	}
}

func TestMetricsHistoryUnknownSelectorIsNotFound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openTempStore(t)
	srv := &Server{mgr: manager.New(ctx), mdb: st}
	_, err := srv.MetricsHistory(ctx, &pb.MetricsHistoryRequest{Selector: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestMetricsHistoryReturnsBuckets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := openTempStore(t)
	srv := &Server{mgr: manager.New(ctx), mdb: st}
	defer srv.mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitListOnline(t, srv, 1)

	// Seed two samples under the instance label, 1s apart, "now"-ish.
	now := time.Now().UnixMilli()
	_ = st.Append(now-2000, []metricstore.Sample{{Label: "a#0", Cpu: 10, Mem: 100}})
	_ = st.Append(now-1000, []metricstore.Sample{{Label: "a#0", Cpu: 30, Mem: 300}})

	resp, err := srv.MetricsHistory(ctx, &pb.MetricsHistoryRequest{
		Selector: "a", SinceMs: int64(time.Hour / time.Millisecond), BucketMs: 1000,
	})
	if err != nil {
		t.Fatalf("MetricsHistory: %v", err)
	}
	if len(resp.GetBuckets()) == 0 {
		t.Fatalf("got no buckets, want >= 1")
	}
}

func openTempStore(t *testing.T) *metricstore.Store {
	t.Helper()
	st, err := metricstore.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
