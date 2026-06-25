package daemon

import (
	"fmt"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/eventstore"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/metrics"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/supervisor"
)

// appSpecToConfig converts a wire AppSpec into a defaulted, validated config.App.
func appSpecToConfig(s *pb.AppSpec) (config.App, error) {
	app := config.App{
		Name:        s.GetName(),
		Cmd:         s.GetCmd(),
		Args:        s.GetArgs(),
		Cwd:         s.GetCwd(),
		Instances:   int(s.GetInstances()),
		Env:         s.GetEnv(),
		Restart:     config.RestartMode(s.GetRestart()),
		MaxRestarts: int(s.GetMaxRestarts()),
	}
	if kt := s.GetKillTimeout(); kt != "" {
		d, err := time.ParseDuration(kt)
		if err != nil {
			return config.App{}, fmt.Errorf("invalid kill_timeout %q: %w", kt, err)
		}
		app.KillTimeout = config.Duration{Duration: d}
	}
	if lr := s.GetLogs(); lr != nil {
		app.Logs = &config.LogRetention{}
		if lr.MaxSizeMb != nil {
			v := int(lr.GetMaxSizeMb())
			app.Logs.MaxSizeMB = &v
		}
		if lr.MaxBackups != nil {
			v := int(lr.GetMaxBackups())
			app.Logs.MaxBackups = &v
		}
		if lr.MaxAgeDays != nil {
			v := int(lr.GetMaxAgeDays())
			app.Logs.MaxAgeDays = &v
		}
		if lr.Compress != nil {
			v := lr.GetCompress()
			app.Logs.Compress = &v
		}
	}
	if gs := s.GetSource(); gs != nil {
		app.Source = &config.GitSource{
			Repo:       gs.GetRepo(),
			Ref:        gs.GetRef(),
			Build:      gs.GetBuild(),
			Subdir:     gs.GetSubdir(),
			Credential: gs.GetCredential(),
		}
	}
	cfg := config.Config{Apps: []config.App{app}}
	if err := cfg.Prepare(); err != nil {
		return config.App{}, err
	}
	return cfg.Apps[0], nil
}

// snapshotToProc converts a manager snapshot + metrics + restart rollup into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot, sm metrics.Sample, rs eventstore.Rollup) *pb.ProcInfo {
	var uptimeMs int64
	if s.State == supervisor.StateOnline && !s.StartedAt.IsZero() {
		uptimeMs = time.Since(s.StartedAt).Milliseconds()
	}
	var lastRestartUnix int64
	if rs.LastMs > 0 {
		lastRestartUnix = rs.LastMs / 1000
	}
	return &pb.ProcInfo{
		Id:              int32(s.ID),
		Name:            s.Name,
		InstanceId:      int32(s.InstanceID),
		State:           string(s.State),
		Pid:             int32(s.Pid),
		UptimeMs:        uptimeMs,
		Restarts:        int32(s.Restarts),
		Cpu:             sm.Cpu,
		Mem:             int64(sm.Mem),
		Source:          s.Source,
		Credential:      s.Credential,
		Threads:         sm.Threads,
		OpenFds:         sm.Fds,
		ExitCode:        s.ExitCode,
		ExitReason:      s.ExitReason,
		Restarts24H:     rs.Count24h,
		LastRestartUnix: lastRestartUnix,
	}
}

// procList renders snapshots as a ProcList, merging in the latest metrics.
func (srv *Server) procList(snaps []manager.InstanceSnapshot) *pb.ProcList {
	procs := make([]*pb.ProcInfo, 0, len(snaps))
	var rollups map[string]eventstore.Rollup
	if srv.estore != nil {
		rollups, _ = srv.estore.Rollups(time.Now().UnixMilli() - 24*60*60*1000)
	}
	for _, s := range snaps {
		sm := metrics.Sample{Fds: -1} // default: unavailable until first sample
		if srv.metrics != nil {
			if v, ok := srv.metrics.Get(s.Label); ok {
				sm = v
			}
		}
		procs = append(procs, snapshotToProc(s, sm, rollups[s.Label]))
	}
	return &pb.ProcList{Procs: procs}
}
