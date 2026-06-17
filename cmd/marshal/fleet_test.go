package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/pb"
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
