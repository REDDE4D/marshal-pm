package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/eventstore"
	"github.com/REDDE4D/marshal-pm/internal/logs"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/metrics"
	"github.com/REDDE4D/marshal-pm/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{mgr: manager.New(ctx)}
	return srv, func() { srv.mgr.StopAll(); cancel() }
}

func sleepSpec(name string, n int32) *pb.AppSpec {
	return &pb.AppSpec{Name: name, Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: n}
}

func TestStartThenList(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	ctx := context.Background()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 2)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	list, err := srv.List(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Procs) != 2 {
		t.Fatalf("got %d procs, want 2", len(list.Procs))
	}
}

func TestStartDuplicateIsAlreadyExists(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	ctx := context.Background()
	req := &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}
	if _, err := srv.Start(ctx, req); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err := srv.Start(ctx, req)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("got %v, want AlreadyExists", err)
	}
}

func TestStopUnknownIsNotFound(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	_, err := srv.Stop(context.Background(), &pb.Selector{Target: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestStartInvalidSpecIsInvalidArgument(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	// Missing cmd fails config validation.
	_, err := srv.Start(context.Background(),
		&pb.StartRequest{Apps: []*pb.AppSpec{{Name: "x"}}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument", err)
	}
}

func TestListIncludesMetricsAfterSample(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := newTestRegistry(t)
	mgr := manager.New(ctx, manager.WithLogs(reg))
	sampler := metricsSampler(t)
	srv := &Server{mgr: mgr, logs: reg, metrics: sampler}
	defer mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// wait until online, then sample once
	waitListOnline(t, srv, 1)
	sampler.SampleOnce(srv.testInstances())

	list, err := srv.List(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Procs) != 1 || list.Procs[0].Mem == 0 {
		t.Fatalf("proc = %+v, want non-zero Mem", list.GetProcs())
	}
}

func newTestRegistry(t *testing.T) *logs.Registry {
	t.Helper()
	return logs.NewRegistry(t.TempDir())
}

func metricsSampler(t *testing.T) *metrics.Sampler {
	t.Helper()
	return metrics.NewSampler(time.Hour)
}

func (s *Server) testInstances() []metrics.Instance { return metricsSnapshot(s.mgr)() }

func waitListOnline(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := srv.List(context.Background(), &pb.Empty{})
		online := 0
		for _, p := range list.GetProcs() {
			if p.GetState() == "online" {
				online++
			}
		}
		if online >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d online", want)
}

func TestWithFleetPollIntervalSetsOption(t *testing.T) {
	var o runOptions
	WithFleetPollInterval(250 * time.Millisecond)(&o)
	if o.fleetPoll != 250*time.Millisecond {
		t.Fatalf("fleetPoll = %v, want 250ms", o.fleetPoll)
	}
}

func TestResetAndFlush(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := logs.NewRegistry(t.TempDir())
	es, err := eventstore.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer es.Close()
	srv := &Server{mgr: manager.New(ctx), logs: reg, estore: es}
	defer srv.mgr.StopAll()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Reset prunes the eventstore for the app's labels.
	if err := es.Record("a#0", 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Reset(ctx, &pb.Selector{Target: "a"}); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if r, _ := es.Rollups(0); r["a#0"].Count24h != 0 {
		t.Fatalf("eventstore not pruned: %+v", r["a#0"])
	}

	// Flush clears the ring for the app's labels.
	if _, err := reg.For("a#0").Writer(false).Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Flush(ctx, &pb.Selector{Target: "a"}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := len(reg.For("a#0").Backfill(0)); n != 0 {
		t.Fatalf("ring = %d after flush, want 0", n)
	}
}

func TestResetUnknownIsNotFound(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	_, err := srv.Reset(context.Background(), &pb.Selector{Target: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}
