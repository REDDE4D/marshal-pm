package fleet

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"marshal/internal/fleetauth"
	"marshal/internal/pb"
	"marshal/internal/server"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

func TestBuildHello(t *testing.T) {
	h := buildHello("web-1", "v0.1.0")
	if h.GetAgentName() != "web-1" || h.GetMarshalVersion() != "v0.1.0" {
		t.Fatalf("name/version wrong: %+v", h)
	}
	if h.GetOs() != runtime.GOOS || h.GetArch() != runtime.GOARCH {
		t.Fatalf("os/arch wrong: got %s/%s", h.GetOs(), h.GetArch())
	}
	if hn, _ := os.Hostname(); h.GetHostname() != hn {
		t.Fatalf("hostname = %q, want %q", h.GetHostname(), hn)
	}
	if h.GetHostBootUnix() < 0 {
		t.Fatalf("boot unix negative: %d", h.GetHostBootUnix())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

func snap() []*pb.ProcInfo { return []*pb.ProcInfo{{Name: "api", State: "online"}} }

func TestClientHelloAndPeriodicPush(t *testing.T) {
	reg := server.NewRegistry(server.WithOfflineAfter(time.Hour))
	dir := t.TempDir()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert, fp, err := server.LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	auth, secrets, err := server.LoadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	srv := server.NewServer(reg, nil, nil, auth)
	go func() { _ = server.Serve(sctx, lis, srv, cert) }()

	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	c := New(lis.Addr().String(), "web-1", "test", snap,
		WithTLS(tlsCfg),
		WithInterval(20*time.Millisecond), WithBackoff(10*time.Millisecond, 40*time.Millisecond),
		WithAuth("", secrets.EnrollToken, func(string) error { return nil }))
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go c.Run(cctx)

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetAgentName() == "web-1" && ag[0].GetConnected() && len(ag[0].GetProcs()) == 1
	})
}

func TestClientReconnectsWhenServerStartsLate(t *testing.T) {
	// Reserve an address, then free it so the server is initially down.
	lis0, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis0.Addr().String()
	_ = lis0.Close()

	// Generate a cert and auth before binding so we know the fingerprint and tokens.
	dir := t.TempDir()
	cert, fp, err := server.LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	auth, secrets, err := server.LoadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}

	c := New(addr, "web-1", "test", snap,
		WithTLS(tlsCfg),
		WithInterval(20*time.Millisecond), WithBackoff(10*time.Millisecond, 40*time.Millisecond),
		WithAuth("", secrets.EnrollToken, func(string) error { return nil }))
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go c.Run(cctx) // retries against a dead address

	time.Sleep(60 * time.Millisecond)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("could not rebind %s: %v", addr, err)
	}
	reg := server.NewRegistry(server.WithOfflineAfter(time.Hour))
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	srv2 := server.NewServer(reg, nil, nil, auth)
	go func() { _ = server.Serve(sctx, lis, srv2, cert) }()

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetConnected()
	})
}

// fakeFleetServer is a minimal in-process Fleet server for testing metric shipping.
// It replies to Hello with HelloAck{LastMetricTsMs: ackWatermark} and records
// all received MetricSamples.
type fakeFleetServer struct {
	pb.UnimplementedFleetServer
	ackWatermark    int64
	ackLogWatermark int64

	mu      sync.Mutex
	samples []*pb.MetricSample
	lines   []*pb.LogShipLine
}

func (f *fakeFleetServer) Connect(stream pb.Fleet_ConnectServer) error {
	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch m := msg.GetMsg().(type) {
		case *pb.AgentMessage_Hello:
			_ = m
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: f.ackWatermark, LastLogTsMs: f.ackLogWatermark},
			}})
		case *pb.AgentMessage_Metrics:
			f.mu.Lock()
			f.samples = append(f.samples, m.Metrics.GetSamples()...)
			f.mu.Unlock()
		case *pb.AgentMessage_Logs:
			f.mu.Lock()
			f.lines = append(f.lines, m.Logs.GetLines()...)
			f.mu.Unlock()
		}
	}
}

func (f *fakeFleetServer) sawSample(tsMs int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.samples {
		if s.GetTsMs() == tsMs {
			return true
		}
	}
	return false
}

func (f *fakeFleetServer) sawLine(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.lines {
		if l.GetText() == text {
			return true
		}
	}
	return false
}

// fakeServerHandle wraps a fakeFleetServer with its TLS listening address and
// client TLS config so tests can dial securely.
type fakeServerHandle struct {
	*fakeFleetServer
	addr   string
	tlsCfg *tls.Config
}

func newFakeServer(t *testing.T) *fakeServerHandle {
	t.Helper()
	cert, fp, err := server.LoadOrCreateCert(t.TempDir(), "", "")
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
	fs := &fakeFleetServer{}
	serverCreds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	gs := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterFleetServer(gs, fs)
	t.Cleanup(func() { gs.GracefulStop() })
	go func() { _ = gs.Serve(lis) }()
	return &fakeServerHandle{fakeFleetServer: fs, addr: lis.Addr().String(), tlsCfg: tlsCfg}
}

func TestClientSeedsWatermarkFromAckAndBackfills(t *testing.T) {
	// Fake server: replies to Hello with HelloAck{LastMetricTsMs: 5000},
	// records received MetricBatches.
	fs := newFakeServer(t)
	fs.ackWatermark = 5000

	metrics := func(since int64) []*pb.MetricSample {
		// Local "history": one row at 4000 (already on server), one at 6000 (new).
		all := []*pb.MetricSample{
			{TsMs: 4000, Label: "api#0", Cpu: 1, Mem: 1},
			{TsMs: 6000, Label: "api#0", Cpu: 2, Mem: 2},
		}
		var out []*pb.MetricSample
		for _, s := range all {
			if s.TsMs > since {
				out = append(out, s)
			}
		}
		return out
	}

	c := New(fs.addr, "web-1", "test", func() []*pb.ProcInfo { return nil },
		WithTLS(fs.tlsCfg),
		WithInterval(20*time.Millisecond), WithMetrics(metrics))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// Within a couple ticks the server should have received only the 6000 row
	// (5000 watermark filters out 4000), and never re-send it.
	waitFor(t, func() bool {
		return fs.sawSample(6000) && !fs.sawSample(4000)
	})
	cancel()
}

func TestClientExecutesCommandAndRepliesResult(t *testing.T) {
	// Stub Fleet server: ack hello, push one restart command, capture the reply.
	gotReply := make(chan *pb.CommandResult, 1)
	srv := &cmdStubServer{gotReply: gotReply}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cert, fp, err := server.LoadOrCreateCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	serverCreds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	gs := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterFleetServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	executed := make(chan string, 1)
	c := New(lis.Addr().String(), "web-1", "test",
		func() []*pb.ProcInfo { return nil },
		WithTLS(tlsCfg),
		WithInterval(20*time.Millisecond),
		WithCommands(func(cmd *pb.Command) *pb.ControlResult {
			executed <- cmd.GetOp().GetRestart().GetTarget()
			return &pb.ControlResult{Ok: true, Procs: []*pb.ProcInfo{{Name: "api"}}}
		}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	select {
	case target := <-executed:
		if target != "api" {
			t.Fatalf("executed target = %q, want api", target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("command was never executed")
	}
	select {
	case reply := <-gotReply:
		if !reply.GetResult().GetOk() || reply.GetResult().GetProcs()[0].GetName() != "api" {
			t.Fatalf("reply = %v, want ok with api proc", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("result was never received by the server")
	}
}

// cmdStubServer acks Hello, sends one restart Command, and captures the reply.
type cmdStubServer struct {
	pb.UnimplementedFleetServer
	gotReply chan *pb.CommandResult
}

func (s *cmdStubServer) Connect(stream pb.Fleet_ConnectServer) error {
	// First inbound message is Hello.
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{HelloAck: &pb.HelloAck{}}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_Command{Command: &pb.Command{
		RequestId: 1,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "api"}}},
	}}}); err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if r := msg.GetResult(); r != nil {
			s.gotReply <- r
			return nil
		}
	}
}

// TestClientEnrollmentAndPersistence verifies the full enrollment round-trip:
// the client sends marshal-enroll when token=="", persists the minted token from
// HelloAck.AgentToken, then switches to marshal-token on the next connect.
func TestClientEnrollmentAndPersistence(t *testing.T) {
	// enrollServer simulates a fleet server that:
	//   - accepts an enroll token via marshal-enroll metadata
	//   - returns a minted per-agent token in HelloAck.AgentToken
	//   - accepts subsequent connections using marshal-token
	const mintedToken = "minted-per-agent-token-abc123"
	const enrollToken = "the-enroll-token"

	enrollSrv := &enrollStubServer{
		enrollToken: enrollToken,
		mintedToken: mintedToken,
	}
	dir := t.TempDir()
	cert, fp, err := server.LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tlsClientCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverCreds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	gs := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterFleetServer(gs, enrollSrv)
	t.Cleanup(func() { gs.GracefulStop() })
	go func() { _ = gs.Serve(lis) }()

	// Phase 1: enroll — persist must be called with the minted token.
	var persistMu sync.Mutex
	var persistedToken string
	persist := func(tok string) error {
		persistMu.Lock()
		persistedToken = tok
		persistMu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := New(lis.Addr().String(), "web-1", "test", snap,
		WithTLS(tlsClientCfg),
		WithInterval(20*time.Millisecond),
		WithBackoff(10*time.Millisecond, 40*time.Millisecond),
		WithAuth("", enrollToken, persist))
	go c.Run(ctx)

	// Wait until the server recorded an enrollment and the persist callback ran.
	waitFor(t, func() bool {
		enrollSrv.mu.Lock()
		enrollSeen := enrollSrv.enrollSeen
		enrollSrv.mu.Unlock()
		persistMu.Lock()
		got := persistedToken
		persistMu.Unlock()
		return enrollSeen && got != ""
	})
	persistMu.Lock()
	got := persistedToken
	persistMu.Unlock()
	if got != mintedToken {
		t.Fatalf("persisted token = %q, want %q", got, mintedToken)
	}
	cancel()

	// Phase 2: reconnect using the minted token — server must accept it.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	c2 := New(lis.Addr().String(), "web-1", "test", snap,
		WithTLS(tlsClientCfg),
		WithInterval(20*time.Millisecond),
		WithBackoff(10*time.Millisecond, 40*time.Millisecond),
		WithAuth(mintedToken, "", func(string) error { return nil }))
	go c2.Run(ctx2)

	waitFor(t, func() bool {
		enrollSrv.mu.Lock()
		defer enrollSrv.mu.Unlock()
		return enrollSrv.agentTokenSeen
	})
}

// enrollStubServer is a fake fleet server that handles enrollment and per-agent
// token auth for TestClientEnrollmentAndPersistence.
type enrollStubServer struct {
	pb.UnimplementedFleetServer
	enrollToken string
	mintedToken string

	mu             sync.Mutex
	enrollSeen     bool
	agentTokenSeen bool
}

func (s *enrollStubServer) Connect(stream pb.Fleet_ConnectServer) error {
	// Check incoming metadata for enroll or per-agent token.
	md, _ := metadata.FromIncomingContext(stream.Context())
	enrollVals := md.Get("marshal-enroll")
	agentVals := md.Get("marshal-token")

	var sendAgentToken string
	s.mu.Lock()
	if len(enrollVals) > 0 && enrollVals[0] == s.enrollToken {
		s.enrollSeen = true
		sendAgentToken = s.mintedToken
	} else if len(agentVals) > 0 && agentVals[0] == s.mintedToken {
		s.agentTokenSeen = true
	}
	s.mu.Unlock()

	// Drain until Hello then send HelloAck (with optional AgentToken).
	for {
		msg, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if msg.GetHello() != nil {
			return stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{AgentToken: sendAgentToken},
			}})
		}
	}
}

func TestClientShipsLogsAndSeedsLogWatermark(t *testing.T) {
	fs := newFakeServer(t)
	fs.ackLogWatermark = 5000

	logs := func(since int64) []*pb.LogShipLine {
		all := []*pb.LogShipLine{
			{TsMs: 4000, Label: "api#0", Text: "old"},   // already on server
			{TsMs: 6000, Label: "api#0", Text: "fresh"}, // new
		}
		var out []*pb.LogShipLine
		for _, l := range all {
			if l.TsMs > since {
				out = append(out, l)
			}
		}
		return out
	}

	c := New(fs.addr, "web-1", "test", func() []*pb.ProcInfo { return nil },
		WithTLS(fs.tlsCfg),
		WithInterval(20*time.Millisecond), WithLogs(logs))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, func() bool { return fs.sawLine("fresh") && !fs.sawLine("old") })
	cancel()
}
