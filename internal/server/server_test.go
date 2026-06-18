package server

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"marshal/internal/fleetauth"
	"marshal/internal/logstore"
	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
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

// newTLSTestServer starts a TLS Fleet server in the background and returns
// (addr, fingerprint, secrets). The server is shut down when t ends.
func newTLSTestServer(t *testing.T, reg *Registry) (addr, fingerprint string, secrets *InitSecrets) {
	t.Helper()
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	auth, sec, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Serve(ctx, lis, reg, nil, nil, cert, auth) }()
	return lis.Addr().String(), fp, sec
}

func TestConnectRejectsEmptyName(t *testing.T) {
	srv := NewServer(NewRegistry(), newStores(t.TempDir()), nil, nil)
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
	srv := NewServer(NewRegistry(), ss, nil, nil)

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

func dialFleet(t *testing.T, addr, fingerprint string) pb.FleetClient {
	t.Helper()
	tlsCfg, err := fleetauth.ClientTLS(fingerprint, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
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
	srv := NewServer(NewRegistry(), ss, nil, nil)
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
	addr, fp, secrets := newTLSTestServer(t, reg)
	cl := dialFleet(t, addr, fp)

	// Connect stream requires the enroll token.
	enrollCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("marshal-enroll", secrets.EnrollToken))
	stream, err := cl.Connect(enrollCtx)
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

	// ListFleet over the wire requires admin token.
	listCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("marshal-token", secrets.AdminToken))
	resp, err := cl.ListFleet(listCtx, &pb.ListFleetRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetAgents()) != 1 || resp.GetAgents()[0].GetConnected() {
		t.Fatalf("agents = %+v", resp.GetAgents())
	}
}

func TestFleetLogsHistorySelectorMergeAndFilter(t *testing.T) {
	dir := t.TempDir()
	ls := newLogStores(dir)
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls, nil)

	srv.storeLogBatch("web-1", []*pb.LogShipLine{
		{TsMs: 1, Label: "api#0", Stderr: false, Text: "o0"},
		{TsMs: 2, Label: "api#1", Stderr: true, Text: "e1"},
		{TsMs: 3, Label: "api#0", Stderr: false, Text: "o0b"},
	})

	// selector "api" resolves both api#0 and api#1, merged ascending by ts.
	resp, err := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 10,
	})
	if err != nil {
		t.Fatalf("FleetLogsHistory: %v", err)
	}
	if len(resp.GetLines()) != 3 {
		t.Fatalf("got %d lines, want 3", len(resp.GetLines()))
	}
	if resp.GetLines()[0].GetLine() != "o0" || resp.GetLines()[1].GetLine() != "e1" {
		t.Fatalf("merge order wrong: %+v", resp.GetLines())
	}
	if resp.GetLines()[1].GetName() != "api" || resp.GetLines()[1].GetInstanceId() != 1 {
		t.Fatalf("label parse wrong: %+v", resp.GetLines()[1])
	}

	// stderr filter
	respErr, _ := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "web-1", Selector: "api", Lines: 10, Stream: pb.LogStream_LOG_STREAM_STDERR,
	})
	if len(respErr.GetLines()) != 1 || respErr.GetLines()[0].GetLine() != "e1" {
		t.Fatalf("stderr filter = %+v, want [e1]", respErr.GetLines())
	}
}

func TestFleetLogsHistoryUnknownAgent(t *testing.T) {
	ls := newLogStores(t.TempDir())
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls, nil)
	_, err := srv.FleetLogsHistory(context.Background(), &pb.FleetLogsHistoryRequest{
		AgentName: "ghost", Selector: "api", Lines: 10,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

// FleetControl against an agent with no live session is Unavailable.
func TestFleetControlNotConnected(t *testing.T) {
	srv := NewServer(NewRegistry(), nil, nil, nil)
	_, err := srv.FleetControl(context.Background(), &pb.FleetControlRequest{
		AgentName: "ghost",
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "api"}}},
	})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("FleetControl on absent agent err = %v, want Unavailable", err)
	}
}

// TestEnrollAndAuthenticatedIdentity is the enrollment e2e test required by
// Task 13. It verifies two properties:
//  1. A Connect with marshal-enroll metadata + Hello{AgentName:"dev-1"} receives a
//     non-empty HelloAck.AgentToken (the server minted a per-agent token).
//  2. A second Connect with marshal-token = that minted token and
//     Hello{AgentName:"anything"} registers state under "dev-1" (the
//     authenticated name), NOT "anything" — verified via ListFleet.
func TestEnrollAndAuthenticatedIdentity(t *testing.T) {
	reg := NewRegistry(WithOfflineAfter(time.Hour))
	addr, fp, secrets := newTLSTestServer(t, reg)

	tlsCfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// --- Step 1: Enroll "dev-1", expect a minted token in HelloAck ---
	enrollCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("marshal-enroll", secrets.EnrollToken))
	stream1, err := pb.NewFleetClient(conn).Connect(enrollCtx)
	if err != nil {
		t.Fatalf("Connect (enroll): %v", err)
	}
	if err := stream1.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: "dev-1"}}}); err != nil {
		t.Fatalf("Send Hello (enroll): %v", err)
	}
	ack1, err := stream1.Recv()
	if err != nil {
		t.Fatalf("Recv HelloAck (enroll): %v", err)
	}
	helloAck1 := ack1.GetHelloAck()
	if helloAck1 == nil {
		t.Fatalf("first recv is not HelloAck: %T", ack1.GetMsg())
	}
	mintedToken := helloAck1.GetAgentToken()
	if mintedToken == "" {
		t.Fatal("HelloAck.AgentToken is empty; server did not mint a token on enrollment")
	}
	// Close enrollment stream.
	_ = stream1.CloseSend()

	// --- Step 2: Authenticate with minted token; send Hello{AgentName:"anything"} ---
	// The server must register state under "dev-1" (the authenticated name),
	// ignoring the self-asserted "anything".
	authCtx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("marshal-token", mintedToken))
	stream2, err := pb.NewFleetClient(conn).Connect(authCtx)
	if err != nil {
		t.Fatalf("Connect (auth): %v", err)
	}
	if err := stream2.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Hello{Hello: &pb.Hello{AgentName: "anything"}}}); err != nil {
		t.Fatalf("Send Hello (auth): %v", err)
	}
	_, err = stream2.Recv()
	if err != nil {
		t.Fatalf("Recv HelloAck (auth): %v", err)
	}
	// Send a snapshot so the server has state to list.
	if err := stream2.Send(&pb.AgentMessage{Msg: &pb.AgentMessage_Snapshot{Snapshot: &pb.StateSnapshot{
		Procs: []*pb.ProcInfo{{Name: "worker", State: "online"}},
	}}}); err != nil {
		t.Fatalf("Send Snapshot: %v", err)
	}

	// Wait for the registry to reflect the agent.
	waitFor(t, func() bool {
		for _, ag := range reg.List() {
			if ag.GetAgentName() == "dev-1" && ag.GetConnected() {
				return true
			}
		}
		return false
	})

	// Verify no agent named "anything" was registered.
	agents := reg.List()
	for _, ag := range agents {
		if ag.GetAgentName() == "anything" {
			t.Fatalf("server registered agent under self-asserted name %q; should have used %q", "anything", "dev-1")
		}
	}

	_ = stream2.CloseSend()
}

func TestConnectStoresLogBatchAndAcksWatermark(t *testing.T) {
	dir := t.TempDir()
	ls := newLogStores(dir)
	defer ls.closeAll()
	srv := NewServer(NewRegistry(WithOfflineAfter(time.Hour)), nil, ls, nil)

	srv.storeLogBatch("web-1", []*pb.LogShipLine{
		{TsMs: 1000, Label: "api#0", Stderr: false, Text: "l1"},
		{TsMs: 2000, Label: "api#0", Stderr: true, Text: "l2"},
	})

	st, _ := ls.get("web-1")
	if mx, _ := st.MaxTs(); mx != 2000 {
		t.Fatalf("MaxTs = %d, want 2000", mx)
	}
	got, _ := st.Tail("api#0", 10, logstore.StreamAny)
	if len(got) != 2 || got[0].Text != "l1" || got[1].Text != "l2" {
		t.Fatalf("stored = %+v, want l1,l2", got)
	}
}
