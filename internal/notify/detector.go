package notify

import (
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
