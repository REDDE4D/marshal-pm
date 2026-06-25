package daemon

import (
	"fmt"

	"github.com/REDDE4D/marshal-pm/internal/deploy"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/pb"
)

// credFromProto maps a wire credential to the deployer's credential, branching
// on kind. A nil credential maps to the zero value (no managed credential).
func credFromProto(c *pb.GitCredential) deploy.Credential {
	if c == nil {
		return deploy.Credential{}
	}
	if c.GetKind() == pb.CredentialKind_CRED_SSH {
		return deploy.Credential{
			Username:   c.GetUsername(),
			PrivateKey: c.GetPrivateKey(),
			KnownHosts: c.GetKnownHosts(),
			SSH:        true,
		}
	}
	return deploy.Credential{Username: c.GetUsername(), Token: c.GetToken()}
}

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

	case *pb.ControlOp_Reload:
		snaps, err = s.mgr.Reload(v.Reload.GetTarget())

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
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		app, cerr := appSpecToConfig(v.Deploy.GetApp())
		if cerr != nil {
			return &pb.ControlResult{Ok: false, Error: cerr.Error()}
		}
		c := v.Deploy.GetCredential()
		cred := credFromProto(c)
		if derr := s.deployer.Start(app, cred); derr != nil {
			return &pb.ControlResult{Ok: false, Error: derr.Error()}
		}
		return &pb.ControlResult{Ok: true}

	case *pb.ControlOp_Redeploy:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		rc := v.Redeploy.GetCredential()
		cred := credFromProto(rc)
		if derr := s.deployer.Redeploy(v.Redeploy.GetTarget(), cred); derr != nil {
			return &pb.ControlResult{Ok: false, Error: derr.Error()}
		}
		return &pb.ControlResult{Ok: true}

	case *pb.ControlOp_ListDir:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		root, ok := s.deployer.Root(v.ListDir.GetApp())
		if !ok {
			return &pb.ControlResult{Ok: false, Error: "not a git deployment"}
		}
		listing, lerr := deploy.ListDir(root, v.ListDir.GetPath())
		if lerr != nil {
			return &pb.ControlResult{Ok: false, Error: lerr.Error()}
		}
		return &pb.ControlResult{Ok: true, Dir: listing}

	case *pb.ControlOp_ReadFile:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		root, ok := s.deployer.Root(v.ReadFile.GetApp())
		if !ok {
			return &pb.ControlResult{Ok: false, Error: "not a git deployment"}
		}
		fc, ferr := deploy.ReadFile(root, v.ReadFile.GetPath())
		if ferr != nil {
			return &pb.ControlResult{Ok: false, Error: ferr.Error()}
		}
		return &pb.ControlResult{Ok: true, File: fc}

	case *pb.ControlOp_Commit:
		if s.deployer == nil {
			return &pb.ControlResult{Ok: false, Error: "deploy not supported"}
		}
		c := v.Commit
		cc := c.GetCredential()
		cred := credFromProto(cc)
		res, cerr := s.deployer.Commit(c.GetApp(), c.GetKind(), c.GetPath(), c.GetNewPath(), c.GetContent(), c.GetMessage(), cred)
		if cerr != nil {
			return &pb.ControlResult{Ok: false, Error: cerr.Error()}
		}
		return &pb.ControlResult{Ok: true, Commit: res}

	default:
		return &pb.ControlResult{Ok: false, Error: fmt.Sprintf("unknown op type %T", op.GetOp())}
	}

	if err != nil {
		return &pb.ControlResult{Ok: false, Error: err.Error()}
	}
	return &pb.ControlResult{Ok: true, Procs: s.procList(snaps).GetProcs()}
}
