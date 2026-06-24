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
	Threads    int32   `json:"threads"`
	OpenFds    int32   `json:"open_fds"` // -1 = unavailable on this platform
	ExitCode   int32   `json:"exit_code"`
	ExitReason string  `json:"exit_reason,omitempty"` // "" = never exited
}

type hostView struct {
	CPUPercent float64 `json:"cpu_percent"`
	Load1      float64 `json:"load1"`
	Load5      float64 `json:"load5"`
	Load15     float64 `json:"load15"`
	MemTotal   uint64  `json:"mem_total"`
	MemUsed    uint64  `json:"mem_used"`
	MemUsedPct float64 `json:"mem_used_pct"`
	NetRxBps   float64 `json:"net_rx_bps"`
	NetTxBps   float64 `json:"net_tx_bps"`
}

type agentView struct {
	Name           string     `json:"name"`
	Connected      bool       `json:"connected"`
	LastSeen       int64      `json:"last_seen_unix"`
	Procs          []procView `json:"procs"`
	Hostname       string     `json:"hostname,omitempty"`
	IP             string     `json:"ip,omitempty"`
	OS             string     `json:"os,omitempty"`
	Arch           string     `json:"arch,omitempty"`
	MarshalVersion string     `json:"marshal_version,omitempty"`
	HostBootUnix   int64      `json:"host_boot_unix,omitempty"`
	Host           *hostView  `json:"host,omitempty"`
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
				Threads:    p.GetThreads(),
				OpenFds:    p.GetOpenFds(),
				ExitCode:   p.GetExitCode(),
				ExitReason: p.GetExitReason(),
			})
		}
		var host *hostView
		if h := a.GetHost(); h != nil {
			host = &hostView{
				CPUPercent: h.GetCpuPercent(),
				Load1:      h.GetLoad1(),
				Load5:      h.GetLoad5(),
				Load15:     h.GetLoad15(),
				MemTotal:   h.GetMemTotal(),
				MemUsed:    h.GetMemUsed(),
				MemUsedPct: h.GetMemUsedPct(),
				NetRxBps:   h.GetNetRxBps(),
				NetTxBps:   h.GetNetTxBps(),
			}
		}
		out = append(out, agentView{
			Name:           a.GetAgentName(),
			Connected:      a.GetConnected(),
			LastSeen:       a.GetLastSeenUnix(),
			Procs:          procs,
			Hostname:       a.GetHostname(),
			IP:             a.GetIp(),
			OS:             a.GetOs(),
			Arch:           a.GetArch(),
			MarshalVersion: a.GetMarshalVersion(),
			HostBootUnix:   a.GetHostBootUnix(),
			Host:           host,
		})
	}
	return out
}
