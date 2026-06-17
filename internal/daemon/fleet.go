package daemon

import (
	"marshal/internal/fleet"
	"marshal/internal/manager"
	"marshal/internal/pb"
)

// procInfos adapts manager snapshots to wire ProcInfo. cpu/mem are zero in M7
// (metric streaming is M8); it reuses snapshotToProc for the field mapping.
func procInfos(snaps []manager.InstanceSnapshot) []*pb.ProcInfo {
	out := make([]*pb.ProcInfo, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, snapshotToProc(s, 0, 0))
	}
	return out
}

// fleetSnapshot returns a SnapshotFunc over the manager's current instances.
func fleetSnapshot(m *manager.Manager) fleet.SnapshotFunc {
	return func() []*pb.ProcInfo { return procInfos(m.List()) }
}
