package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/pb"
)

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
	for _, want := range []string{"web-1", "online", "api", "web-2", "offline"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
