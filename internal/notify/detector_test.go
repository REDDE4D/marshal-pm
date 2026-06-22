package notify

import (
	"context"
	"sync"
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

func TestDiffDeployFailCarriesDetail(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("web", "building", 0))}
	failed := proc("web", "failed", 0)
	failed.Detail = "exit status 1: build error"
	next := []*pb.AgentState{agent("dev-1", true, failed)}
	evs := diff(prev, next, time.Now())
	if len(evs) != 1 || evs[0].Type != EventDeployFail {
		t.Fatalf("want one deploy_fail, got %+v", evs)
	}
	if evs[0].Detail != "exit status 1: build error" {
		t.Fatalf("detail not carried through: %q", evs[0].Detail)
	}
}

func TestDiffDeployFailFallsBackWhenNoDetail(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("web", "building", 0))}
	next := []*pb.AgentState{agent("dev-1", true, proc("web", "failed", 0))}
	evs := diff(prev, next, time.Now())
	if len(evs) != 1 || evs[0].Type != EventDeployFail {
		t.Fatalf("want one deploy_fail, got %+v", evs)
	}
	if evs[0].Detail != "deploy failed" {
		t.Fatalf("want fallback detail, got %q", evs[0].Detail)
	}
}

// A process appearing for the first time in the same tick as another process
// transitions: the new one seeds silently, only the transition emits.
func TestDiffNewProcessSeedsAlongsideTransition(t *testing.T) {
	prev := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	next := []*pb.AgentState{agent("dev-1", true,
		proc("api", "restarting", 1), // transition -> crash
		proc("worker", "online", 0),  // brand new -> seed silently
	)}
	evs := diff(prev, next, time.Now())
	if len(evs) != 1 {
		t.Fatalf("want exactly one event, got %+v", evs)
	}
	if evs[0].Type != EventCrash || evs[0].Process != "api" {
		t.Fatalf("want crash for api, got %+v", evs[0])
	}
}

type fakeLister struct {
	mu    sync.Mutex
	snaps [][]*pb.AgentState
	i     int
}

func (f *fakeLister) List() []*pb.AgentState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.i >= len(f.snaps) {
		return f.snaps[len(f.snaps)-1]
	}
	s := f.snaps[f.i]
	f.i++
	return s
}

type recEmitter struct {
	mu  sync.Mutex
	evs []Event
}

func (r *recEmitter) Emit(e Event) { r.mu.Lock(); r.evs = append(r.evs, e); r.mu.Unlock() }
func (r *recEmitter) count() int   { r.mu.Lock(); defer r.mu.Unlock(); return len(r.evs) }

func TestDetectorRunEmitsOnTransition(t *testing.T) {
	lst := &fakeLister{snaps: [][]*pb.AgentState{
		{agent("dev-1", true, proc("api", "online", 0))},     // seed
		{agent("dev-1", true, proc("api", "restarting", 1))}, // crash
	}}
	em := &recEmitter{}
	d := NewDetector(lst, em, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	deadline := time.After(2 * time.Second)
	for em.count() < 1 {
		select {
		case <-deadline:
			t.Fatal("no event within deadline")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	em.mu.Lock()
	defer em.mu.Unlock()
	if em.evs[0].Type != EventCrash {
		t.Fatalf("want crash, got %v", em.evs[0].Type)
	}
}
