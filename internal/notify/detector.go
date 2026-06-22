package notify

import (
	"context"
	"fmt"
	"time"

	"marshal/internal/pb"
)

// Lister is the subset of *server.Registry the detector reads.
type Lister interface{ List() []*pb.AgentState }

// Emitter receives detected events (the Dispatcher implements it).
type Emitter interface{ Emit(Event) }

// diff compares two fleet snapshots and returns the events implied by the
// transitions. A nil/absent prev (or new agent/process) seeds silently.
func diff(prev, next []*pb.AgentState, now time.Time) []Event {
	prevAgents := map[string]*pb.AgentState{}
	for _, a := range prev {
		prevAgents[a.GetAgentName()] = a
	}
	var out []Event
	for _, a := range next {
		pa, known := prevAgents[a.GetAgentName()]
		if !known {
			continue // new agent: seed without events
		}
		if pa.GetConnected() && !a.GetConnected() {
			out = append(out, Event{Type: EventAgentDown, Agent: a.GetAgentName(), Detail: "agent stopped reporting", Time: now})
		} else if !pa.GetConnected() && a.GetConnected() {
			out = append(out, Event{Type: EventAgentUp, Agent: a.GetAgentName(), Detail: "agent reconnected", Time: now})
		}
		prevProcs := map[string]*pb.ProcInfo{}
		for _, p := range pa.GetProcs() {
			prevProcs[p.GetName()] = p
		}
		for _, p := range a.GetProcs() {
			pp, seen := prevProcs[p.GetName()]
			if !seen {
				continue // new process: seed without events
			}
			if e, ok := procEvent(a.GetAgentName(), pp.GetState(), p, now); ok {
				out = append(out, e)
			}
		}
	}
	return out
}

// procEvent maps a single process state transition to an event, if any.
func procEvent(agentName, prevState string, p *pb.ProcInfo, now time.Time) (Event, bool) {
	cur := p.GetState()
	if cur == prevState {
		return Event{}, false
	}
	base := Event{Agent: agentName, Process: p.GetName(), Time: now}
	switch cur {
	case "restarting":
		base.Type = EventCrash
		base.Detail = fmt.Sprintf("crashed (restart #%d)", p.GetRestarts())
		return base, true
	case "errored":
		base.Type = EventRestartLoop
		base.Detail = fmt.Sprintf("gave up after %d restarts", p.GetRestarts())
		return base, true
	case "failed":
		base.Type = EventDeployFail
		base.Detail = p.GetDetail()
		if base.Detail == "" {
			base.Detail = "deploy failed"
		}
		return base, true
	}
	return Event{}, false
}

func procKey(agent, process string) string { return agent + "\x00" + process }

// recoveryDetail describes what a process recovered from.
func recoveryDetail(from EventType) string {
	switch from {
	case EventCrash:
		return "recovered after crash"
	case EventRestartLoop:
		return "recovered after restart loop"
	case EventDeployFail:
		return "deploy recovered"
	default:
		return "recovered"
	}
}

// recoveries records this tick's alerts, then emits a recovery for any alerting
// process that has returned to "online". A clean "stopped" clears the flag
// silently; processes that vanish from the snapshot are pruned.
func (d *Detector) recoveries(alerts []Event, next []*pb.AgentState, now time.Time) []Event {
	for _, e := range alerts {
		if e.Process != "" {
			d.alerting[procKey(e.Agent, e.Process)] = e.Type
		}
	}
	present := map[string]bool{}
	var out []Event
	for _, a := range next {
		for _, p := range a.GetProcs() {
			key := procKey(a.GetAgentName(), p.GetName())
			present[key] = true
			from, ok := d.alerting[key]
			if !ok {
				continue
			}
			switch p.GetState() {
			case "online":
				out = append(out, Event{Type: EventRecovered, Agent: a.GetAgentName(), Process: p.GetName(), Detail: recoveryDetail(from), Time: now})
				delete(d.alerting, key)
			case "stopped":
				delete(d.alerting, key) // clean stop: alarm moot
			}
		}
	}
	for key := range d.alerting {
		if !present[key] {
			delete(d.alerting, key)
		}
	}
	return out
}

// Detector polls fleet snapshots and emits events on transitions.
type Detector struct {
	lister   Lister
	emit     Emitter
	interval time.Duration
	now      func() time.Time
	prev     []*pb.AgentState
	alerting map[string]EventType // key: agent\x00process -> last alert type
}

// NewDetector builds a detector polling l every interval.
func NewDetector(l Lister, e Emitter, interval time.Duration) *Detector {
	return &Detector{lister: l, emit: e, interval: interval, now: time.Now, alerting: map[string]EventType{}}
}

// Run polls until ctx is cancelled. The first poll seeds the baseline.
func (d *Detector) Run(ctx context.Context) {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			next := d.lister.List()
			now := d.now()
			alerts := diff(d.prev, next, now)
			for _, e := range alerts {
				d.emit.Emit(e)
			}
			for _, e := range d.recoveries(alerts, next, now) {
				d.emit.Emit(e)
			}
			d.prev = next
		}
	}
}
