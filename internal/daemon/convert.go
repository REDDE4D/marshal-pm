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
	cfg := config.Config{Apps: []config.App{app}}
	if err := cfg.Prepare(); err != nil {
		return config.App{}, err
	}
	return cfg.Apps[0], nil
}

// snapshotToProc converts a manager snapshot into a wire ProcInfo.
func snapshotToProc(s manager.InstanceSnapshot) *pb.ProcInfo {
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
	}
}

func toProcList(snaps []manager.InstanceSnapshot) *pb.ProcList {
	procs := make([]*pb.ProcInfo, 0, len(snaps))
	for _, s := range snaps {
		procs = append(procs, snapshotToProc(s))
	}
	return &pb.ProcList{Procs: procs}
}
