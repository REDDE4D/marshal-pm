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

// rpc runs a single daemon RPC with its own short timeout.
func rpc[T any](t *testing.T, name string, fn func(context.Context) (T, error)) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := fn(ctx)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

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

	// start
	rpc(t, "Start", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
			{Name: "svc", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 2},
		}})
	})
	waitState(t, c, "svc", "online", 2)

	// stop -> stopped
	rpc(t, "Stop", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Stop(ctx, &pb.Selector{Target: "svc"})
	})
	waitState(t, c, "svc", "stopped", 2)

	// restart -> online again
	rpc(t, "Restart", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Restart(ctx, &pb.Selector{Target: "svc"})
	})
	waitState(t, c, "svc", "online", 2)

	// save + delete + resurrect restores the app
	rpc(t, "Save", func(ctx context.Context) (*pb.Ack, error) {
		return c.Save(ctx, &pb.Empty{})
	})
	rpc(t, "Delete", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Delete(ctx, &pb.Selector{Target: "svc"})
	})
	list := rpc(t, "List", func(ctx context.Context) (*pb.ProcList, error) {
		return c.List(ctx, &pb.Empty{})
	})
	if len(list.GetProcs()) != 0 {
		t.Fatalf("after delete: %d procs, want 0", len(list.GetProcs()))
	}
	rpc(t, "Resurrect", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Resurrect(ctx, &pb.Empty{})
	})
	waitState(t, c, "svc", "online", 2)
}

func TestDaemonKillStopsDaemon(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "marshal-kill")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	t.Setenv("XDG_DATA_HOME", base)

	st, err := store.New()
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	ctx, cancelDaemon := context.WithCancel(context.Background())
	defer cancelDaemon()
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		_ = daemon.Run(ctx, st)
	}()

	c, conn := dialReady(t, st)
	defer conn.Close()

	ack := rpc(t, "Kill", func(ctx context.Context) (*pb.Ack, error) {
		return c.Kill(ctx, &pb.Empty{})
	})
	if !ack.GetOk() {
		t.Fatalf("Kill ack not ok: %+v", ack)
	}

	select {
	case <-daemonDone:
		// daemon serve loop returned — success
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not exit within 3s after Kill")
	}
}
