package dashboard

import "marshal/internal/pb"

// FleetLister is the read side of the live registry the dashboard renders.
// *server.Registry satisfies it.
type FleetLister interface {
	List() []*pb.AgentState
}

type procView struct {
	Name       string  `json:"name"`
	State      string  `json:"state"`
	PID        int32   `json:"pid"`
	UptimeMs   int64   `json:"uptime_ms"`
	Restarts   int32   `json:"restarts"`
	CPU        float64 `json:"cpu"`
	Mem        int64   `json:"mem"`
	Source     string  `json:"source"`               // "command" | "git" — drives the redeploy button (M21)
	Detail     string  `json:"detail"`               // status summary for in-flight/failed deploys (M21)
	Credential string  `json:"credential,omitempty"` // M22 credential name (drives redeploy)
}

type agentView struct {
	Name      string     `json:"name"`
	Connected bool       `json:"connected"`
	LastSeen  int64      `json:"last_seen_unix"`
	Procs     []procView `json:"procs"`
}

// fleetView maps the live registry state into JSON-friendly view structs.
func fleetView(l FleetLister) []agentView {
	agents := l.List()
	out := make([]agentView, 0, len(agents))
	for _, a := range agents {
		procs := make([]procView, 0, len(a.GetProcs()))
		for _, p := range a.GetProcs() {
			procs = append(procs, procView{
				Name:       p.GetName(),
				State:      p.GetState(),
				PID:        p.GetPid(),
				UptimeMs:   p.GetUptimeMs(),
				Restarts:   p.GetRestarts(),
				CPU:        p.GetCpu(),
				Mem:        p.GetMem(),
				Source:     p.GetSource(),
				Detail:     p.GetDetail(),
				Credential: p.GetCredential(),
			})
		}
		out = append(out, agentView{
			Name:      a.GetAgentName(),
			Connected: a.GetConnected(),
			LastSeen:  a.GetLastSeenUnix(),
			Procs:     procs,
		})
	}
	return out
}
