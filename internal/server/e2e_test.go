package server

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/fleetauth"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func e2eDialFleet(t *testing.T, addr, fingerprint string) *grpc.ClientConn {
	t.Helper()
	tlsCfg, err := fleetauth.ClientTLS(fingerprint, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func waitForHistory(t *testing.T, conn *grpc.ClientConn, agent, selector string, wantBuckets int, adminToken string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		authedCtx := metadata.AppendToOutgoingContext(ctx, "marshal-token", adminToken)
		resp, err := pb.NewFleetClient(conn).FleetMetricsHistory(authedCtx, &pb.FleetMetricsHistoryRequest{
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

func waitForLogs(t *testing.T, conn *grpc.ClientConn, agent, selector string, wantLines int, adminToken string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		authedCtx := metadata.AppendToOutgoingContext(ctx, "marshal-token", adminToken)
		resp, err := pb.NewFleetClient(conn).FleetLogsHistory(authedCtx, &pb.FleetLogsHistoryRequest{
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

	// Get the fingerprint before starting ServeDir (LoadOrCreateCert is idempotent).
	_, fp, err := LoadOrCreateCert(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}

	// Init auth before ServeDir so we have the plaintext tokens.
	// ServeDir calls loadOrInitAuth internally — idempotent once auth.json exists.
	_, secrets, err := loadOrInitAuth(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	enrollToken := secrets.EnrollToken
	adminToken := secrets.AdminToken

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
	go func() { _ = ServeDir(ctx1, lis1, dataDir, "", "") }()

	c1 := fleet.New(lis1.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithTLS(tlsCfg),
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logsFn),
		fleet.WithAuth("", enrollToken, func(string) error { return nil }))
	cctx1, ccancel1 := context.WithCancel(context.Background())
	go c1.Run(cctx1)

	conn1 := e2eDialFleet(t, lis1.Addr().String(), fp)
	waitForLogs(t, conn1, "web-1", "api", 2, adminToken)
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
	go func() { _ = ServeDir(ctx2, lis2, dataDir, "", "") }()

	c2 := fleet.New(lis2.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithTLS(tlsCfg),
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logsFn),
		fleet.WithAuth("", enrollToken, func(string) error { return nil }))
	cctx2, ccancel2 := context.WithCancel(context.Background())
	defer ccancel2()
	go c2.Run(cctx2)

	conn2 := e2eDialFleet(t, lis2.Addr().String(), fp)
	defer conn2.Close()
	// After reconnect the watermark is seeded from the persisted max(ts), so the
	// client ships only the gap line — total 3 lines proves backfill works.
	waitForLogs(t, conn2, "web-1", "api", 3, adminToken)

	// Prove backfill shipped only the gap line: exactly 3 lines, no duplicates.
	ctxF, cancelF := context.WithTimeout(context.Background(), time.Second)
	defer cancelF()
	authedCtxF := metadata.AppendToOutgoingContext(ctxF, "marshal-token", adminToken)
	final, err := pb.NewFleetClient(conn2).FleetLogsHistory(authedCtxF, &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 100,
	})
	if err != nil {
		t.Fatalf("final FleetLogsHistory: %v", err)
	}
	got := final.GetLines()
	if len(got) != 3 {
		t.Fatalf("final history = %d lines, want exactly 3 (a resend would duplicate)", len(got))
	}
	wantTexts := []string{"line1", "line2", "line3"}
	for i, w := range wantTexts {
		if got[i].GetLine() != w {
			t.Fatalf("line[%d] = %q, want %q", i, got[i].GetLine(), w)
		}
	}
}

func TestE2EFleetControlRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	auth, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := NewRegistry()
	go func() { _ = Serve(ctx, lis, reg, nil, nil, cert, auth) }()

	// Real agent client whose command handler echoes the selector back.
	c := fleet.New(lis.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithTLS(tlsCfg),
		fleet.WithInterval(20*time.Millisecond),
		fleet.WithCommands(func(cmd *pb.Command) *pb.ControlResult {
			return &pb.ControlResult{Ok: true, Procs: []*pb.ProcInfo{
				{Name: cmd.GetOp().GetRestart().GetTarget(), State: "online"},
			}}
		}),
		fleet.WithAuth("", secrets.EnrollToken, func(string) error { return nil }))
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go c.Run(cctx)

	// Wait until the agent is connected (registry reflects its live session).
	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetConnected()
	})

	conn := e2eDialFleet(t, lis.Addr().String(), fp)
	defer conn.Close()
	rctx, rcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer rcancel()
	authedRctx := metadata.AppendToOutgoingContext(rctx, "marshal-token", secrets.AdminToken)
	resp, err := pb.NewFleetClient(conn).FleetControl(authedRctx, &pb.FleetControlRequest{
		AgentName: "web-1",
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "api"}}},
	})
	if err != nil {
		t.Fatalf("FleetControl: %v", err)
	}
	if !resp.GetResult().GetOk() || resp.GetResult().GetProcs()[0].GetName() != "api" {
		t.Fatalf("result = %v, want ok with api proc", resp.GetResult())
	}
}

func TestE2EMetricsIngestAndBackfill(t *testing.T) {
	dataDir := t.TempDir()

	// Use time.Now()-relative timestamps so samples fall inside the 1h query window.
	base := time.Now().UnixMilli()

	// Get the fingerprint before starting ServeDir (LoadOrCreateCert is idempotent).
	_, fp, err := LoadOrCreateCert(dataDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}

	// Init auth before ServeDir so we have the plaintext tokens.
	// ServeDir calls loadOrInitAuth internally — idempotent once auth.json exists.
	_, secrets, err := loadOrInitAuth(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	enrollToken := secrets.EnrollToken
	adminToken := secrets.AdminToken

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
	go func() { _ = ServeDir(ctx1, lis1, dataDir, "", "") }()

	c1 := fleet.New(lis1.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithTLS(tlsCfg),
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metricsFn),
		fleet.WithAuth("", enrollToken, func(string) error { return nil }))
	cctx1, ccancel1 := context.WithCancel(context.Background())
	go c1.Run(cctx1)

	// Query the server until 2 buckets are present.
	conn1 := e2eDialFleet(t, lis1.Addr().String(), fp)
	waitForHistory(t, conn1, "web-1", "api", 2, adminToken)
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
	go func() { _ = ServeDir(ctx2, lis2, dataDir, "", "") }()

	c2 := fleet.New(lis2.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		fleet.WithTLS(tlsCfg),
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metricsFn),
		fleet.WithAuth("", enrollToken, func(string) error { return nil }))
	cctx2, ccancel2 := context.WithCancel(context.Background())
	defer ccancel2()
	go c2.Run(cctx2)

	conn2 := e2eDialFleet(t, lis2.Addr().String(), fp)
	defer conn2.Close()
	// After reconnect, the server's history must include ts=base (3 buckets at 1s).
	// The server seeds the client's watermark from its persisted max(ts), so the
	// client sends only the gap row (ts=base), proving backfill works.
	waitForHistory(t, conn2, "web-1", "api", 3, adminToken)
}

func TestOperatorRPCRequiresAdminToken(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	auth, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, lis, NewRegistry(), nil, nil, cert, auth) }()

	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// No token → Unauthenticated
	noAuthCtx, noAuthCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer noAuthCancel()
	_, err = pb.NewFleetClient(conn).ListFleet(noAuthCtx, &pb.ListFleetRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no token: got code %v, want Unauthenticated", status.Code(err))
	}

	// Valid admin token → success
	adminCtx, adminCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer adminCancel()
	authedCtx := metadata.AppendToOutgoingContext(adminCtx, "marshal-token", secrets.AdminToken)
	_, err = pb.NewFleetClient(conn).ListFleet(authedCtx, &pb.ListFleetRequest{})
	if err != nil {
		t.Fatalf("valid admin token rejected: %v", err)
	}
}
