package dashboard

import (
	"testing"

	"marshal/internal/pb"
)

type fakeLister struct{ agents []*pb.AgentState }

func (f fakeLister) List() []*pb.AgentState { return f.agents }

func TestFleetView(t *testing.T) {
	f := fakeLister{agents: []*pb.AgentState{{
		AgentName:    "dev-1",
		Connected:    true,
		LastSeenUnix: 42,
		Host:         &pb.HostMetrics{CpuPercent: 7.5, Load1: 1.5, MemTotal: 4096, MemUsed: 1024, MemUsedPct: 25, NetRxBps: 100, NetTxBps: 200},
		Procs: []*pb.ProcInfo{{
			Name: "ticker", State: "running", Pid: 99, UptimeMs: 1000, Restarts: 2, Cpu: 1.5, Mem: 2048,
			Source: "command", Threads: 8, OpenFds: -1, ExitCode: 1, ExitReason: "exit status 1",
			Restarts24H: 5, LastRestartUnix: 1700000000,
		}, {
			Name: "gitapp", State: "failed", Source: "git", Detail: "build failed: exit status 1",
		}},
	}}}
	v := fleetView(f)
	if len(v) != 1 {
		t.Fatalf("len(v) = %d; want 1", len(v))
	}
	if v[0].Name != "dev-1" || !v[0].Connected || v[0].LastSeen != 42 {
		t.Fatalf("agent view = %+v", v[0])
	}
	if v[0].Host == nil {
		t.Fatal("host view is nil, want populated")
	}
	if v[0].Host.CPUPercent != 7.5 || v[0].Host.Load1 != 1.5 {
		t.Fatalf("host cpu/load = %v/%v, want 7.5/1.5", v[0].Host.CPUPercent, v[0].Host.Load1)
	}
	if v[0].Host.MemTotal != 4096 || v[0].Host.MemUsed != 1024 || v[0].Host.MemUsedPct != 25 {
		t.Fatalf("host mem = %+v", v[0].Host)
	}
	if v[0].Host.NetRxBps != 100 || v[0].Host.NetTxBps != 200 {
		t.Fatalf("host net = %v/%v, want 100/200", v[0].Host.NetRxBps, v[0].Host.NetTxBps)
	}
	if len(v[0].Procs) != 2 {
		t.Fatalf("len procs = %d; want 2", len(v[0].Procs))
	}
	p := v[0].Procs[0]
	if p.Name != "ticker" || p.State != "running" || p.PID != 99 || p.Restarts != 2 {
		t.Fatalf("proc view = %+v", p)
	}
	if p.Source != "command" {
		t.Fatalf("command proc Source = %q; want command", p.Source)
	}
	if p.Threads != 8 || p.OpenFds != -1 {
		t.Fatalf("threads/fds = %d/%d, want 8/-1", p.Threads, p.OpenFds)
	}
	if p.ExitCode != 1 || p.ExitReason != "exit status 1" {
		t.Fatalf("exit = (%d, %q), want (1, \"exit status 1\")", p.ExitCode, p.ExitReason)
	}
	if p.Restarts24h != 5 || p.LastRestartUnix != 1700000000 {
		t.Fatalf("restart rollup = %d/%d, want 5/1700000000", p.Restarts24h, p.LastRestartUnix)
	}
	// M21: git source + deploy detail must survive serialization (drives the
	// redeploy button and the failed-card reason in the dashboard).
	g := v[0].Procs[1]
	if g.Source != "git" || g.State != "failed" || g.Detail != "build failed: exit status 1" {
		t.Fatalf("git proc view = %+v; want source=git state=failed detail set", g)
	}
}

func TestFleetViewEmpty(t *testing.T) {
	v := fleetView(fakeLister{})
	if v == nil {
		t.Fatal("fleetView should return a non-nil empty slice for JSON []")
	}
	if len(v) != 0 {
		t.Fatalf("len(v) = %d; want 0", len(v))
	}
}

func TestProcViewCredential(t *testing.T) {
	// Build a fleet lister fake whose ProcInfo carries Credential, then assert
	// fleetView surfaces it.
	f := fakeLister{agents: []*pb.AgentState{{
		AgentName: "dev-1",
		Procs:     []*pb.ProcInfo{{Name: "priv", Source: "git", Credential: "gh-ci"}},
	}}}
	views := fleetView(f)
	if views[0].Procs[0].Credential != "gh-ci" {
		t.Fatalf("credential dropped by procView: %+v", views[0].Procs[0])
	}
}

func TestFleetViewIncludesMetadata(t *testing.T) {
	f := fakeLister{agents: []*pb.AgentState{{
		AgentName: "web-1", Connected: true, Hostname: "web-01", Ip: "203.0.113.7",
		Os: "linux", Arch: "amd64", MarshalVersion: "v0.1.0", HostBootUnix: 1700000000,
	}}}
	v := fleetView(f)[0]
	if v.Hostname != "web-01" || v.IP != "203.0.113.7" || v.OS != "linux" || v.Arch != "amd64" ||
		v.MarshalVersion != "v0.1.0" || v.HostBootUnix != 1700000000 {
		t.Fatalf("metadata missing from view: %+v", v)
	}
}
