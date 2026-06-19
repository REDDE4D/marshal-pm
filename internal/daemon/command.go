package daemon

import (
	"fmt"

	"marshal/internal/manager"
	"marshal/internal/pb"
)

// handleFleetCommand executes a control command received from the fleet server
// and returns its result. It is wired into the fleet client via fleet.WithCommands.
func (s *Server) handleFleetCommand(cmd *pb.Command) *pb.ControlResult {
	op := cmd.GetOp()
	if op == nil {
		return &pb.ControlResult{Ok: false, Error: "command has no op"}
	}

	var (
		snaps []manager.InstanceSnapshot
		err   error
	)

	switch v := op.GetOp().(type) {
	case *pb.ControlOp_Start:
		snaps, err = s.doStart(v.Start.GetApps())
		if err == nil && s.store != nil {
			_ = s.store.Save(s.mgr.Specs())
		}

	case *pb.ControlOp_Stop:
		snaps, err = s.mgr.Stop(v.Stop.GetTarget())

	case *pb.ControlOp_Restart:
		snaps, err = s.mgr.Restart(v.Restart.GetTarget())

	case *pb.ControlOp_Delete:
		snaps, err = s.mgr.Delete(v.Delete.GetTarget())
		forgot := false
		if s.deployer != nil {
			forgot = s.deployer.Forget(v.Delete.GetTarget())
		}
		if err != nil && forgot {
			err = nil // the target was a failed/in-flight deploy, now cleared
		}
		if err == nil && s.store != nil {
			_ = s.store.Save(s.mgr.Specs())
		}

	case *pb.ControlOp_Deploy:
		app, cerr := appSpecToConfig(v.Deploy.GetApp())
		if cerr != nil {
			return &pb.ControlResult{Ok: false, Error: cerr.Error()}
		}
		if derr := s.deployer.Start(app); derr != nil {
			return &pb.ControlResult{Ok: false, Error: derr.Error()}
		}
		return &pb.ControlResult{Ok: true}

	case *pb.ControlOp_Redeploy:
		if derr := s.deployer.Redeploy(v.Redeploy.GetTarget()); derr != nil {
			return &pb.ControlResult{Ok: false, Error: derr.Error()}
		}
		return &pb.ControlResult{Ok: true}

	default:
		return &pb.ControlResult{Ok: false, Error: fmt.Sprintf("unknown op type %T", op.GetOp())}
	}

	if err != nil {
		return &pb.ControlResult{Ok: false, Error: err.Error()}
	}
	return &pb.ControlResult{Ok: true, Procs: s.procList(snaps).GetProcs()}
}
