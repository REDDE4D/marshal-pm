//go:build e2e_fleet

package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/daemon"
	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/server"
	"github.com/REDDE4D/marshal-pm/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// recordingFleetServer is a minimal in-test TLS Fleet server. It records the
// agent name from each Hello and every process label seen in a snapshot, plus a
// count of currently-open Connect streams so the test can observe enroll
// (stream opens) and unenroll (stream drops).
type recordingFleetServer struct {
	pb.UnimplementedFleetServer

	mu          sync.Mutex
	helloNames  []string
	procLabels  map[string]bool
	openStreams int
	enrollSeen  bool
}

func (s *recordingFleetServer) Connect(stream pb.Fleet_ConnectServer) error {
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if v := md.Get("marshal-enroll"); len(v) > 0 && v[0] != "" {
			s.mu.Lock()
			s.enrollSeen = true
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	s.openStreams++
	if s.procLabels == nil {
		s.procLabels = map[string]bool{}
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.openStreams--
		s.mu.Unlock()
	}()

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
			s.mu.Lock()
			s.helloNames = append(s.helloNames, m.Hello.GetAgentName())
			s.mu.Unlock()
			// Mint a per-agent token so the client persists it and the
			// handshake completes normally.
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{AgentToken: "minted-token"},
			}})
		case *pb.AgentMessage_Snapshot:
			s.mu.Lock()
			for _, p := range m.Snapshot.GetProcs() {
				s.procLabels[p.GetName()] = true
			}
			s.mu.Unlock()
		}
	}
}

func (s *recordingFleetServer) sawHello(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.helloNames {
		if n == name {
			return true
		}
	}
	return false
}

func (s *recordingFleetServer) sawProc(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procLabels[name]
}

func (s *recordingFleetServer) openCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openStreams
}

// fleetServerHandle bundles a recording server with its listening address and
// the cert fingerprint a client must pin.
type fleetServerHandle struct {
	*recordingFleetServer
	addr        string
	fingerprint string
}

func startFakeFleetServer(t *testing.T) *fleetServerHandle {
	t.Helper()
	cert, fp, err := server.LoadOrCreateCert(t.TempDir(), "", "")
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fs := &recordingFleetServer{procLabels: map[string]bool{}}
	creds := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	gs := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterFleetServer(gs, fs)
	t.Cleanup(func() { gs.Stop() })
	go func() { _ = gs.Serve(lis) }()
	// Sanity check the fingerprint is a valid pin.
	if _, err := fleetauth.ClientTLS(fp, ""); err != nil {
		t.Fatalf("client TLS from fingerprint: %v", err)
	}
	return &fleetServerHandle{recordingFleetServer: fs, addr: lis.Addr().String(), fingerprint: fp}
}

// waitForCond polls cond until true or the deadline elapses (generous to avoid
// flakes under -race), failing with msg otherwise.
func waitForCond(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within 5s: %s", msg)
}

// TestEnrollE2E proves the real daemon fleet client connects on enroll and
// streams the host's apps to a server, then drops the stream on unenroll.
func TestEnrollE2E(t *testing.T) {
	fs := startFakeFleetServer(t)

	// macOS caps unix socket paths at ~104 bytes; t.TempDir() is too long, so
	// use a short /tmp base for the daemon's store.
	base, err := os.MkdirTemp("/tmp", "marshal-enroll")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	st := store.NewAt(base)

	// Run the real daemon in-process with a fast fleet poll so enroll/unenroll
	// are picked up quickly.
	ctx, cancelDaemon := context.WithCancel(context.Background())
	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		_ = daemon.Run(ctx, st, daemon.WithFleetPollInterval(50*time.Millisecond))
	}()
	t.Cleanup(func() { cancelDaemon(); <-daemonDone })

	c, conn := dialReady(t, st)
	defer conn.Close()

	// Start one app on the host.
	const appName = "fleet-app"
	rpc(t, "Start", func(ctx context.Context) (*pb.ProcList, error) {
		return c.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{
			{Name: appName, Cmd: "sh", Args: []string{"-c", "sleep 60"}, Instances: 1},
		}})
	})
	waitState(t, c, appName, "online", 1)

	// It shows in the host view (List)...
	list := rpc(t, "List", func(ctx context.Context) (*pb.ProcList, error) {
		return c.List(ctx, &pb.Empty{})
	})
	found := false
	for _, p := range list.GetProcs() {
		if p.GetName() == appName {
			found = true
		}
	}
	if !found {
		t.Fatalf("List did not include %q; got %d procs", appName, len(list.GetProcs()))
	}

	// ...but the server has NOT seen this agent yet (not enrolled).
	if fs.sawProc(appName) || fs.openCount() != 0 {
		t.Fatalf("server saw agent before enroll: openStreams=%d sawProc=%v", fs.openCount(), fs.sawProc(appName))
	}

	// Enroll: write the server config to the store. The supervisor picks it up
	// and the real fleet client connects.
	const agentName = "host-1"
	if err := st.SaveServer(&config.ServerConfig{
		Address:     fs.addr,
		Name:        agentName,
		Token:       "enroll-token",
		Fingerprint: fs.fingerprint,
	}); err != nil {
		t.Fatalf("SaveServer: %v", err)
	}

	// The server must receive a Hello from this agent and a snapshot carrying
	// the app.
	waitForCond(t, "fake server received Hello from "+agentName, func() bool {
		return fs.sawHello(agentName)
	})
	waitForCond(t, "fake server received snapshot containing "+appName, func() bool {
		return fs.sawProc(appName)
	})
	if !fs.enrollSeen {
		t.Fatalf("server never saw the marshal-enroll credential")
	}

	// Unenroll: clear the server config. The supervisor must drop the stream.
	if err := st.ClearServer(); err != nil {
		t.Fatalf("ClearServer: %v", err)
	}
	waitForCond(t, "fleet stream dropped after unenroll", func() bool {
		return fs.openCount() == 0
	})
}
