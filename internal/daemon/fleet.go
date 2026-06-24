package daemon

import (
	"time"

	"marshal/internal/eventstore"
	"marshal/internal/fleet"
	"marshal/internal/logs"
	"marshal/internal/metrics"
	"marshal/internal/metricstore"
	"marshal/internal/pb"
)

// fleetSnapshot returns a SnapshotFunc over the manager's current instances,
// merging the sampler's latest cpu/mem/threads/fds (cpu/mem/threads zero and
// fds -1 until the first sample tick) and each instance's last exit code/reason.
// It also appends synthetic deployer entries (in-flight / failed deploys).
func (s *Server) fleetSnapshot() fleet.SnapshotFunc {
	return func() []*pb.ProcInfo {
		snaps := s.mgr.List()
		out := make([]*pb.ProcInfo, 0, len(snaps))
		var rollups map[string]eventstore.Rollup
		if s.estore != nil {
			rollups, _ = s.estore.Rollups(time.Now().UnixMilli() - 24*60*60*1000)
		}
		for _, sn := range snaps {
			sm := metrics.Sample{Fds: -1} // default: unavailable until first sample
			if s.metrics != nil {
				if v, ok := s.metrics.Get(sn.Label); ok {
					sm = v
				}
			}
			out = append(out, snapshotToProc(sn, sm, rollups[sn.Label]))
		}
		if s.deployer != nil {
			deploySnaps := s.deployer.Snapshots()
			for i := range deploySnaps {
				out = append(out, &deploySnaps[i])
			}
		}
		return out
	}
}

// logsSince adapts the log registry's ring to the fleet client's LogsFunc:
// ring lines across all sinks strictly newer than sinceTsMs, as wire lines.
func logsSince(reg *logs.Registry) fleet.LogsFunc {
	return func(sinceTsMs int64) []*pb.LogShipLine {
		if reg == nil {
			return nil
		}
		lines := reg.RingSince(sinceTsMs)
		out := make([]*pb.LogShipLine, 0, len(lines))
		for _, ln := range lines {
			out = append(out, &pb.LogShipLine{
				TsMs: ln.Ts.UnixMilli(), Label: ln.Label, Stderr: ln.Stderr, Text: ln.Text,
			})
		}
		return out
	}
}

// metricsSince adapts a local metric store to the fleet client's MetricsFunc:
// raw rows strictly newer than sinceTsMs, as wire samples.
func metricsSince(mdb *metricstore.Store) fleet.MetricsFunc {
	return func(sinceTsMs int64) []*pb.MetricSample {
		if mdb == nil {
			return nil
		}
		rows, err := mdb.SamplesSince(sinceTsMs)
		if err != nil {
			return nil
		}
		out := make([]*pb.MetricSample, 0, len(rows))
		for _, r := range rows {
			out = append(out, &pb.MetricSample{
				TsMs: r.TsMs, Label: r.Label, Cpu: r.Cpu, Mem: int64(r.Mem),
			})
		}
		return out
	}
}
