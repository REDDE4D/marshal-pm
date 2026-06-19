package daemon

import (
	"fmt"
	"time"

	"marshal/internal/config"
	"marshal/internal/manager"
	"marshal/internal/pb"
	"marshal/internal/supervisor"
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
			Repo:   gs.GetRepo(),
			Ref:    gs.GetRef(),
			Build:  gs.GetBuild(),
			Subdir: gs.GetSubdir(),
		}
	}
	cfg := config.Config{Apps: []config.App{app}}
	if err := cfg.Prepare(); err != nil {
		return config.App{}, err
	}
	return cfg.Apps[0], nil
}

// snapshotToProc converts a manager snapshot + metrics into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot, cpu float64, mem uint64) *pb.ProcInfo {
	var uptimeMs int64
	if s.State == supervisor.StateOnline && !s.StartedAt.IsZero() {
		uptimeMs = time.Since(s.StartedAt).Milliseconds()
	}
	return &pb.ProcInfo{
		Id:         int32(s.ID),
		Name:       s.Name,
		InstanceId: int32(s.InstanceID),
		State:      string(s.State),
		Pid:        int32(s.Pid),
		UptimeMs:   uptimeMs,
		Restarts:   int32(s.Restarts),
		Cpu:        cpu,
		Mem:        int64(mem),
		Source:     s.Source,
	}
}

// procList renders snapshots as a ProcList, merging in the latest metrics.
func (srv *Server) procList(snaps []manager.InstanceSnapshot) *pb.ProcList {
	procs := make([]*pb.ProcInfo, 0, len(snaps))
	for _, s := range snaps {
		var cpu float64
		var mem uint64
		if srv.metrics != nil {
			if sm, ok := srv.metrics.Get(s.Label); ok {
				cpu, mem = sm.Cpu, sm.Mem
			}
		}
		procs = append(procs, snapshotToProc(s, cpu, mem))
	}
	return &pb.ProcList{Procs: procs}
}
