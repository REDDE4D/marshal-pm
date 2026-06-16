package main

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"marshal/internal/client"
	"marshal/internal/daemon"
	"marshal/internal/pb"
	"marshal/internal/store"

	"google.golang.org/grpc"
)

// dialReady waits until the in-process daemon is listening, then connects.
// Waiting first guarantees client.Connect sees a live socket and dials rather
// than trying to auto-spawn a subprocess (which, under `go test`, would be the
// test binary, not the marshal CLI).
func dialReady(t *testing.T, st *store.Store) (pb.DaemonClient, *grpc.ClientConn) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("unix", st.SocketPath(), 100*time.Millisecond); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	c, conn, err := client.Connect(st)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c, conn
}

func waitState(t *testing.T, c pb.DaemonClient, target, state string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		list, err := c.Describe(ctx, &pb.Selector{Target: target})
		cancel()
		if err == nil {
			got := 0
			for _, p := range list.GetProcs() {
				if p.GetState() == state {
					got++
				}
			}
			if got >= n {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s x%d of %q", state, n, target)
}

func TestDaemonLifecycleE2E(t *testing.T) {
	// macOS caps unix socket paths at ~104 bytes; t.TempDir() is too long.
	// Use a short /tmp base instead.
	base, err := os.MkdirTemp("/tmp", "marshal-e2e")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("XDG_DATA_HOME", base)

	st, err := store.New()
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// Run the daemon in-process so teardown is hermetic.
	ctx, cancelDaemon := context.WithCancel(context.Background())
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		_ = daemon.Run(ctx, st)
	}()
	t.Cleanup(func() { cancelDaemon(); <-daemonDone })

	c, conn := dialReady(t, st)
	defer conn.Close()

	rpcCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// start
	if _, err := c.Start(rpcCtx, &pb.StartRequest{Apps: []*pb.AppSpec{
		{Name: "svc", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 2},
	}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitState(t, c, "svc", "online", 2)

	// stop -> stopped
	if _, err := c.Stop(rpcCtx, &pb.Selector{Target: "svc"}); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitState(t, c, "svc", "stopped", 2)

	// restart -> online again
	if _, err := c.Restart(rpcCtx, &pb.Selector{Target: "svc"}); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitState(t, c, "svc", "online", 2)

	// save + delete + resurrect restores the app
	if _, err := c.Save(rpcCtx, &pb.Empty{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := c.Delete(rpcCtx, &pb.Selector{Target: "svc"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, _ := c.List(rpcCtx, &pb.Empty{})
	if len(list.GetProcs()) != 0 {
		t.Fatalf("after delete: %d procs, want 0", len(list.GetProcs()))
	}
	if _, err := c.Resurrect(rpcCtx, &pb.Empty{}); err != nil {
		t.Fatalf("Resurrect: %v", err)
	}
	waitState(t, c, "svc", "online", 2)
}
