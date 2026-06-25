package notify

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/pb"
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

// tick simulates one detector poll: compute alerts via diff, then recoveries.
func (d *Detector) tick(prev, next []*pb.AgentState, now time.Time) []Event {
	alerts := diff(prev, next, now)
	return d.recoveries(alerts, next, now)
}

func newDetectorForTest() *Detector {
	return &Detector{alerting: map[string]EventType{}}
}

func TestRecoveryDetail(t *testing.T) {
	cases := map[EventType]string{
		EventCrash:         "recovered after crash",
		EventRestartLoop:   "recovered after restart loop",
		EventDeployFail:    "deploy recovered",
		EventType("weird"): "recovered",
	}
	for from, want := range cases {
		if got := recoveryDetail(from); got != want {
			t.Errorf("recoveryDetail(%q) = %q, want %q", from, got, want)
		}
	}
}

func TestRecoveryAfterCrash(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	online := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	restarting := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 1))}
	// tick 1: crash -> no recovery yet
	if evs := d.tick(online, restarting, now); len(evs) != 0 {
		t.Fatalf("no recovery on crash tick, got %+v", evs)
	}
	// tick 2: back online -> recovered
	evs := d.tick(restarting, online, now)
	if len(evs) != 1 || evs[0].Type != EventRecovered || evs[0].Process != "api" {
		t.Fatalf("want recovered for api, got %+v", evs)
	}
	if evs[0].Detail != "recovered after crash" {
		t.Fatalf("want crash detail, got %q", evs[0].Detail)
	}
}

func TestRecoveryDeployPathThroughBuilding(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	building := []*pb.AgentState{agent("dev-1", true, proc("web", "building", 0))}
	failed := []*pb.AgentState{agent("dev-1", true, proc("web", "failed", 0))}
	online := []*pb.AgentState{agent("dev-1", true, proc("web", "online", 0))}
	if evs := d.tick(building, failed, now); len(evs) != 0 { // deploy_fail tick
		t.Fatalf("no recovery on fail tick, got %+v", evs)
	}
	if evs := d.tick(failed, building, now); len(evs) != 0 { // rebuild, still alerting
		t.Fatalf("no recovery while building, got %+v", evs)
	}
	evs := d.tick(building, online, now) // online -> recovered
	if len(evs) != 1 || evs[0].Type != EventRecovered || evs[0].Detail != "deploy recovered" {
		t.Fatalf("want deploy recovered, got %+v", evs)
	}
}

func TestCleanStopWhileAlertingClearsSilently(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	online := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	errored := []*pb.AgentState{agent("dev-1", true, proc("api", "errored", 5))}
	stopped := []*pb.AgentState{agent("dev-1", true, proc("api", "stopped", 5))}
	d.tick(online, errored, now) // restart_loop -> alerting
	if evs := d.tick(errored, stopped, now); len(evs) != 0 {
		t.Fatalf("clean stop must not emit recovery, got %+v", evs)
	}
	// coming back online after a clean stop is a normal start, not a recovery
	if evs := d.tick(stopped, online, now); len(evs) != 0 {
		t.Fatalf("start after clean stop must not recover, got %+v", evs)
	}
}

func TestRecoveryPrunesVanishedProcess(t *testing.T) {
	d := newDetectorForTest()
	now := time.Now()
	online := []*pb.AgentState{agent("dev-1", true, proc("api", "online", 0))}
	restarting := []*pb.AgentState{agent("dev-1", true, proc("api", "restarting", 1))}
	gone := []*pb.AgentState{agent("dev-1", true)}           // api removed
	d.tick(online, restarting, now)                          // alerting api
	if evs := d.tick(restarting, gone, now); len(evs) != 0 { // api vanished -> pruned, no recovery
		t.Fatalf("vanished process must not emit recovery, got %+v", evs)
	}
	if len(d.alerting) != 0 {
		t.Fatalf("alerting map should be pruned, got %v", d.alerting)
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
