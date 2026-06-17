package fleet_test

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/pb"
	"marshal/internal/server"

	"google.golang.org/grpc"
)

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
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	go func() { _ = server.Serve(sctx, lis, reg, nil, nil) }()

	c := fleet.New(lis.Addr().String(), "web-1", "test", snap,
		fleet.WithInterval(20*time.Millisecond), fleet.WithBackoff(10*time.Millisecond, 40*time.Millisecond))
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

	c := fleet.New(addr, "web-1", "test", snap,
		fleet.WithInterval(20*time.Millisecond), fleet.WithBackoff(10*time.Millisecond, 40*time.Millisecond))
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
	go func() { _ = server.Serve(sctx, lis, reg, nil, nil) }()

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

// newFakeServer starts a gRPC server backed by fakeFleetServer and returns it
// with its listening address populated.
type fakeServerHandle struct {
	*fakeFleetServer
	addr string
}

func newFakeServer(t *testing.T) *fakeServerHandle {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeFleetServer{}
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, fs)
	t.Cleanup(func() { gs.GracefulStop() })
	go func() { _ = gs.Serve(lis) }()
	return &fakeServerHandle{fakeFleetServer: fs, addr: lis.Addr().String()}
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

	c := fleet.New(fs.addr, "web-1", "test", func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithMetrics(metrics))
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

	c := fleet.New(fs.addr, "web-1", "test", func() []*pb.ProcInfo { return nil },
		fleet.WithInterval(20*time.Millisecond), fleet.WithLogs(logs))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	waitFor(t, func() bool { return fs.sawLine("fresh") && !fs.sawLine("old") })
	cancel()
}
