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
		Procs: []*pb.ProcInfo{{
			Name: "ticker", State: "running", Pid: 99, UptimeMs: 1000, Restarts: 2, Cpu: 1.5, Mem: 2048,
		}},
	}}}
	v := fleetView(f)
	if len(v) != 1 {
		t.Fatalf("len(v) = %d; want 1", len(v))
	}
	if v[0].Name != "dev-1" || !v[0].Connected || v[0].LastSeen != 42 {
		t.Fatalf("agent view = %+v", v[0])
	}
	if len(v[0].Procs) != 1 {
		t.Fatalf("len procs = %d; want 1", len(v[0].Procs))
	}
	p := v[0].Procs[0]
	if p.Name != "ticker" || p.State != "running" || p.PID != 99 || p.Restarts != 2 {
		t.Fatalf("proc view = %+v", p)
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
