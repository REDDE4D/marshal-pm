package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func e2eDialFleet(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func waitForHistory(t *testing.T, conn *grpc.ClientConn, agent, selector string, wantBuckets int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := pb.NewFleetClient(conn).FleetMetricsHistory(ctx, &pb.FleetMetricsHistoryRequest{
			AgentName: agent, Selector: selector, SinceMs: time.Hour.Milliseconds(), BucketMs: 1000,
		})
		cancel()
		if err == nil && len(resp.GetBuckets()) >= wantBuckets {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("history for %s/%s never reached %d buckets", agent, selector, wantBuckets)
}

func waitForLogs(t *testing.T, conn *grpc.ClientConn, agent, selector string, wantLines int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := pb.NewFleetClient(conn).FleetLogsHistory(ctx, &pb.FleetLogsHistoryRequest{
			AgentName: agent, Selector: selector, Lines: 100,
		})
		cancel()
		if err == nil && len(resp.GetLines()) >= wantLines {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("logs for %s/%s never reached %d lines", agent, selector, wantLines)
}

func TestE2ELogsIngestAndBackfill(t *testing.T) {
	dataDir := t.TempDir()
	base := time.Now().UnixMilli()

	var mu sync.Mutex
	local := []*pb.LogShipLine{
		{TsMs: base - 2000, Label: "api#0", Text: "line1"},
		{TsMs: base - 1000, Label: "api#0", Text: "line2"},
	}
	logsFn := func(since int64) []*pb.LogShipLine {
		mu.Lock()
		defer mu.Unlock()
		var out []*pb.LogShipLine
		for _, l := range local {
			if l.TsMs > since {
				out = append(out, l)
			}
		}
		return out
	}

	// --- leg 1: serve, connect, ship the first two lines ---
	lis1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = ServeDir(ctx1, lis1, dataDir) }()

	c1 := fleet.New(lis1.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logsFn))
	cctx1, ccancel1 := context.WithCancel(context.Background())
	go c1.Run(cctx1)

	conn1 := e2eDialFleet(t, lis1.Addr().String())
	waitForLogs(t, conn1, "web-1", "api", 2)
	conn1.Close()
	ccancel1()
	cancel1()
	lis1.Close()

	// --- leg 2: add a gap line, restart server on SAME dir, reconnect ---
	mu.Lock()
	local = append(local, &pb.LogShipLine{TsMs: base, Label: "api#0", Text: "line3"})
	mu.Unlock()

	lis2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = ServeDir(ctx2, lis2, dataDir) }()

	c2 := fleet.New(lis2.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logsFn))
	cctx2, ccancel2 := context.WithCancel(context.Background())
	defer ccancel2()
	go c2.Run(cctx2)

	conn2 := e2eDialFleet(t, lis2.Addr().String())
	defer conn2.Close()
	// After reconnect the watermark is seeded from the persisted max(ts), so the
	// client ships only the gap line — total 3 lines proves backfill works.
	waitForLogs(t, conn2, "web-1", "api", 3)
}

func TestE2EMetricsIngestAndBackfill(t *testing.T) {
	dataDir := t.TempDir()

	// Use time.Now()-relative timestamps so samples fall inside the 1h query window.
	base := time.Now().UnixMilli()

	// local "store" the agent ships from, strictly-newer-than semantics.
	var mu sync.Mutex
	local := []*pb.MetricSample{
		{TsMs: base - 2000, Label: "api#0", Cpu: 10, Mem: 100},
		{TsMs: base - 1000, Label: "api#0", Cpu: 20, Mem: 200},
	}
	metricsFn := func(since int64) []*pb.MetricSample {
		mu.Lock()
		defer mu.Unlock()
		var out []*pb.MetricSample
		for _, s := range local {
			if s.TsMs > since {
				out = append(out, s)
			}
		}
		return out
	}

	// --- leg 1: serve, connect, ship base-2000 and base-1000 ---
	lis1, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx1, cancel1 := context.WithCancel(context.Background())
	go func() { _ = ServeDir(ctx1, lis1, dataDir) }()

	c1 := fleet.New(lis1.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metricsFn))
	cctx1, ccancel1 := context.WithCancel(context.Background())
	go c1.Run(cctx1)

	// Query the server until 2 buckets are present.
	conn1 := e2eDialFleet(t, lis1.Addr().String())
	waitForHistory(t, conn1, "web-1", "api", 2)
	conn1.Close()
	ccancel1()
	cancel1()
	lis1.Close()

	// --- leg 2: simulate a gap row, restart server on SAME dir, reconnect ---
	mu.Lock()
	local = append(local, &pb.MetricSample{TsMs: base, Label: "api#0", Cpu: 30, Mem: 300})
	mu.Unlock()

	lis2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = ServeDir(ctx2, lis2, dataDir) }()

	c2 := fleet.New(lis2.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metricsFn))
	cctx2, ccancel2 := context.WithCancel(context.Background())
	defer ccancel2()
	go c2.Run(cctx2)

	conn2 := e2eDialFleet(t, lis2.Addr().String())
	defer conn2.Close()
	// After reconnect, the server's history must include ts=base (3 buckets at 1s).
	// The server seeds the client's watermark from its persisted max(ts), so the
	// client sends only the gap row (ts=base), proving backfill works.
	waitForHistory(t, conn2, "web-1", "api", 3)
}
