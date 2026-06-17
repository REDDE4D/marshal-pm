package server

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// fakeConnectStream is a minimal in-memory Fleet_ConnectServer for tests.
type fakeConnectStream struct {
	grpc.ServerStream
	ctx    context.Context
	recv   []*pb.AgentMessage
	sent   []*pb.ServerMessage
	recvAt int
}

func (f *fakeConnectStream) Context() context.Context { return f.ctx }
func (f *fakeConnectStream) Send(m *pb.ServerMessage) error {
	f.sent = append(f.sent, m)
	return nil
}
func (f *fakeConnectStream) Recv() (*pb.AgentMessage, error) {
	if f.recvAt >= len(f.recv) {
		return nil, io.EOF
	}
	m := f.recv[f.recvAt]
	f.recvAt++
	return m, nil
}

func startServer(t *testing.T, reg *Registry) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Serve(ctx, lis, reg) }()
	return lis.Addr().String()
}

func TestConnectRejectsEmptyName(t *testing.T) {
	srv := NewServer(NewRegistry(), newStores(t.TempDir()))
	st := &fakeConnectStream{ctx: context.Background(), recv: []*pb.AgentMessage{
		{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: ""}}},
	}}
	err := srv.Connect(st)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Connect with empty name err = %v, want InvalidArgument", err)
	}
}

func TestConnectAcksWatermarkAndStoresBatch(t *testing.T) {
	ss := newStores(t.TempDir())
	srv := NewServer(NewRegistry(), ss)

	// Pre-seed one sample so the second connect sees a non-zero watermark.
	pre, _ := ss.get("web-1")
	_ = pre.Append(5000, []metricstore.Sample{{Label: "api#0", Cpu: 1, Mem: 1}})

	st := &fakeConnectStream{ctx: context.Background(), recv: []*pb.AgentMessage{
		{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: "web-1"}}},
		{Msg: &pb.AgentMessage_Metrics{Metrics: &pb.MetricBatch{Samples: []*pb.MetricSample{
			{TsMs: 6000, Label: "api#0", Cpu: 12, Mem: 100},
			{TsMs: 6000, Label: "api#1", Cpu: 8, Mem: 80},
			{TsMs: 7000, Label: "api#0", Cpu: 20, Mem: 200},
		}}}},
	}}
	if err := srv.Connect(st); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// First sent message is the HelloAck carrying the watermark (5000).
	ack := st.sent[0].GetHelloAck()
	if ack == nil || ack.GetLastMetricTsMs() != 5000 {
		t.Fatalf("HelloAck watermark = %v, want 5000", st.sent[0])
	}
	// The batch landed: max(ts) is now 7000.
	store, _ := ss.get("web-1")
	if mx, _ := store.MaxTs(); mx != 7000 {
		t.Fatalf("after batch MaxTs = %d, want 7000", mx)
	}
}

func dialFleet(t *testing.T, addr string) pb.FleetClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewFleetClient(conn)
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

func TestFleetMetricsHistory(t *testing.T) {
	ss := newStores(t.TempDir())
	srv := NewServer(NewRegistry(), ss)
	st, _ := ss.get("web-1")
	now := time.Now().UnixMilli()
	_ = st.Append(now-2000, []metricstore.Sample{{Label: "api#0", Cpu: 10, Mem: 100}, {Label: "api#1", Cpu: 5, Mem: 50}})
	_ = st.Append(now-1000, []metricstore.Sample{{Label: "api#0", Cpu: 30, Mem: 300}})

	// Unknown agent → NotFound.
	if _, err := srv.FleetMetricsHistory(context.Background(), &pb.FleetMetricsHistoryRequest{AgentName: "ghost", Selector: "api"}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown agent err = %v, want NotFound", err)
	}

	resp, err := srv.FleetMetricsHistory(context.Background(), &pb.FleetMetricsHistoryRequest{
		AgentName: "web-1", Selector: "api", SinceMs: (time.Hour).Milliseconds(), BucketMs: 1000,
	})
	if err != nil {
		t.Fatalf("FleetMetricsHistory: %v", err)
	}
	if len(resp.GetBuckets()) == 0 {
		t.Fatal("expected buckets for api across both instances")
	}
}

func TestServerConnectListAndOffline(t *testing.T) {
	reg := NewRegistry(WithOfflineAfter(time.Hour))
	addr := startServer(t, reg)
	cl := dialFleet(t, addr)

	stream, err := cl.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: "web-1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{Snapshot: &pb.StateSnapshot{Procs: []*pb.ProcInfo{{Name: "api", State: "online"}}}}}); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetConnected() && len(ag[0].GetProcs()) == 1
	})

	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && !ag[0].GetConnected()
	})

	// ListFleet over the wire reflects the same offline state.
	resp, err := cl.ListFleet(context.Background(), &pb.ListFleetRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetAgents()) != 1 || resp.GetAgents()[0].GetConnected() {
		t.Fatalf("agents = %+v", resp.GetAgents())
	}
}
