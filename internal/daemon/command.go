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
		if err == nil && s.store != nil {
			_ = s.store.Save(s.mgr.Specs())
		}

	default:
		return &pb.ControlResult{Ok: false, Error: fmt.Sprintf("unknown op type %T", op.GetOp())}
	}

	if err != nil {
		return &pb.ControlResult{Ok: false, Error: err.Error()}
	}
	return &pb.ControlResult{Ok: true, Procs: s.procList(snaps).GetProcs()}
}
