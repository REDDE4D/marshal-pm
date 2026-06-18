package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"marshal/internal/fleetauth"
	"marshal/internal/pb"
	"marshal/internal/server"
)

func TestFleetMetricsCmdShape(t *testing.T) {
	cmd := fleetCmd()
	var metrics bool
	for _, c := range cmd.Commands() {
		if c.Name() == "metrics" {
			metrics = true
			if c.Flags().Lookup("since") == nil || c.Flags().Lookup("server") == nil {
				t.Fatal("fleet metrics missing --since/--server flags")
			}
		}
	}
	if !metrics {
		t.Fatal("fleet has no metrics subcommand")
	}
}

func TestResolveServer(t *testing.T) {
	if got := resolveServer("explicit:1"); got != "explicit:1" {
		t.Fatalf("flag should win, got %q", got)
	}
	t.Setenv("MARSHAL_SERVER", "fromenv:2")
	if got := resolveServer(""); got != "fromenv:2" {
		t.Fatalf("env should win when no flag, got %q", got)
	}
	t.Setenv("MARSHAL_SERVER", "")
	if got := resolveServer(""); got != "localhost:9000" {
		t.Fatalf("default should be localhost:9000, got %q", got)
	}
}

func TestPrintFleet(t *testing.T) {
	resp := &pb.ListFleetResponse{Agents: []*pb.AgentState{
		{AgentName: "web-1", Connected: true, Procs: []*pb.ProcInfo{
			{Id: 1, Name: "api", InstanceId: 0, State: "online", Pid: 10, UptimeMs: 5000},
		}},
		{AgentName: "web-2", Connected: false, LastSeenUnix: time.Now().Add(-30 * time.Second).Unix()},
	}}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printFleet(cmd, resp)
	out := buf.String()
	for _, want := range []string{"web-1", "online", "api", "web-2", "offline", "CPU", "MEM"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintFleetCPUMem(t *testing.T) {
	// 12.5 CPU, 3276800 bytes = 3.1MB
	resp := &pb.ListFleetResponse{Agents: []*pb.AgentState{
		{AgentName: "srv-1", Connected: true, Procs: []*pb.ProcInfo{
			{Id: 2, Name: "worker", InstanceId: 0, State: "online", Pid: 42, UptimeMs: 10000, Cpu: 12.5, Mem: 3276800},
		}},
	}}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printFleet(cmd, resp)
	out := buf.String()
	for _, want := range []string{"12.5%", "3.1MB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintFleetOfflineCPUMem(t *testing.T) {
	// offline proc should show "-" for cpu and mem
	resp := &pb.ListFleetResponse{Agents: []*pb.AgentState{
		{AgentName: "srv-2", Connected: true, Procs: []*pb.ProcInfo{
			{Id: 3, Name: "cron", InstanceId: 0, State: "stopped", Pid: 0, Cpu: 5.0, Mem: 1048576},
		}},
	}}
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	printFleet(cmd, resp)
	out := buf.String()
	// Should not contain the actual cpu/mem values since state != "online"
	if strings.Contains(out, "5.0%") || strings.Contains(out, "1.0MB") {
		t.Fatalf("offline proc should not render cpu/mem values:\n%s", out)
	}
}

// newTLSControlStub starts a TLS gRPC server backed by the given Fleet
// implementation. It returns the listening address and the pinned fingerprint.
func newTLSControlStub(t *testing.T, impl pb.FleetServer) (addr, fingerprint string) {
	t.Helper()
	cert, fp, err := server.LoadOrCreateCert(t.TempDir(), "", "")
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
	pb.RegisterFleetServer(gs, impl)
	t.Cleanup(func() { gs.GracefulStop() })
	go func() { _ = gs.Serve(lis) }()
	return lis.Addr().String(), fp
}

func TestResolveServerAuth(t *testing.T) {
	// Flag wins.
	addr, fp, tok := resolveServerAuth("myhost:1234", "abc123", "mytoken")
	if addr != "myhost:1234" || fp != "abc123" || tok != "mytoken" {
		t.Fatalf("got addr=%q fp=%q tok=%q, want myhost:1234 abc123 mytoken", addr, fp, tok)
	}
	// Env fallback for fingerprint and token.
	t.Setenv("MARSHAL_FINGERPRINT", "envfp")
	t.Setenv("MARSHAL_TOKEN", "envtok")
	addr2, fp2, tok2 := resolveServerAuth("", "", "")
	if fp2 != "envfp" {
		t.Fatalf("fp from env = %q, want envfp", fp2)
	}
	if tok2 != "envtok" {
		t.Fatalf("token from env = %q, want envtok", tok2)
	}
	_ = addr2
	t.Setenv("MARSHAL_FINGERPRINT", "")
	t.Setenv("MARSHAL_TOKEN", "")
}

func TestFleetRestartSendsControlOp(t *testing.T) {
	captured := make(chan *pb.FleetControlRequest, 1)
	addr, fp := newTLSControlStub(t, &controlStub{captured: captured})

	// Verify dialFleet works with the pinned fingerprint.
	cfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()

	cmd := fleetCmd()
	cmd.SetArgs([]string{"restart", "web-1", "api",
		"--server", addr,
		"--fingerprint", fp,
	})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	select {
	case req := <-captured:
		if req.GetAgentName() != "web-1" || req.GetOp().GetRestart().GetTarget() != "api" {
			t.Fatalf("captured = %v, want web-1 restart api", req)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FleetControl was never called")
	}
}

type controlStub struct {
	pb.UnimplementedFleetServer
	captured chan *pb.FleetControlRequest
}

func (s *controlStub) FleetControl(_ context.Context, req *pb.FleetControlRequest) (*pb.FleetControlResponse, error) {
	s.captured <- req
	return &pb.FleetControlResponse{Result: &pb.ControlResult{Ok: true}}, nil
}
