package server

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

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
