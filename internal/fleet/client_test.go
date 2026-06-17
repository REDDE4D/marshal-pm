package fleet_test

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/pb"
	"marshal/internal/server"
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
	go func() { _ = server.Serve(sctx, lis, reg) }()

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
	go func() { _ = server.Serve(sctx, lis, reg) }()

	waitFor(t, func() bool {
		ag := reg.List()
		return len(ag) == 1 && ag[0].GetConnected()
	})
}
