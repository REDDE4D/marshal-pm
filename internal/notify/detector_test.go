package notify

import (
	"testing"
	"time"

	"marshal/internal/pb"
)

func agent(name string, connected bool, procs ...*pb.ProcInfo) *pb.AgentState {
	return &pb.AgentState{AgentName: name, Connected: connected, Procs: procs}
}
func proc(name, state string, restarts int32) *pb.ProcInfo {
	return &pb.ProcInfo{Name: name, State: state, Restarts: restarts}
}

func types(evs []Event) []EventType {
	out := make([]EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

func TestDiffSeedsSilently(t *testing.T) {
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	if evs := diff(nil, next, time.Now()); len(evs) != 0 {
		t.Fatalf("seed should emit nothing, got %v", types(evs))
	}
}

func TestDiffCrash(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 1))}
	evs := diff(prev, next, time.Now())
	if len(evs) != 1 || evs[0].Type != EventCrash || evs[0].Agent != "dev-1" || evs[0].Process != "api" {
		t.Fatalf("want one crash for dev-1/api, got %+v", evs)
	}
}

func TestDiffRestartLoopAndDeployFail(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 5), proc("web", "building", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 6), proc("web", "failed", 0))}
	got := map[EventType]bool{}
	for _, e := range diff(prev, next, time.Now()) {
		got[e.Type] = true
	}
	if !got[EventRestartLoop] || !got[EventDeployFail] {
		t.Fatalf("want restart_loop + deploy_fail, got %v", got)
	}
}

func TestDiffAgentDownUp(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true)}
	next := []*pb.AgentState{agent("dev-1", false)}
	if evs := diff(prev, next, time.Now()); len(evs) != 1 || evs[0].Type != EventAgentDown {
		t.Fatalf("want agent_down, got %+v", evs)
	}
	if evs := diff(next, prev, time.Now()); len(evs) != 1 || evs[0].Type != EventAgentUp {
		t.Fatalf("want agent_up, got %+v", evs)
	}
}

func TestDiffNoEventOnSteadyState(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 5))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 5))}
	if evs := diff(prev, next, time.Now()); len(evs) != 0 {
		t.Fatalf("steady errored should not re-emit, got %v", types(evs))
	}
}

func TestDiffCleanStopNoEvent(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("api", "stopped", 0))}
	if evs := diff(prev, next, time.Now()); len(evs) != 0 {
		t.Fatalf("clean stop should not alert, got %v", types(evs))
	}
}
