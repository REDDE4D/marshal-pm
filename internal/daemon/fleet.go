package daemon

import (
	"marshal/internal/fleet"
	"marshal/internal/logs"
	"marshal/internal/manager"
	"marshal/internal/metrics"
	"marshal/internal/metricstore"
	"marshal/internal/pb"
)

// fleetSnapshot returns a SnapshotFunc over the manager's current instances,
// merging the sampler's latest cpu/mem (zero until the first sample tick).
func fleetSnapshot(m *manager.Manager, smp *metrics.Sampler) fleet.SnapshotFunc {
	return func() []*pb.ProcInfo {
		snaps := m.List()
		out := make([]*pb.ProcInfo, 0, len(snaps))
		for _, s := range snaps {
			var cpu float64
			var mem uint64
			if smp != nil {
				if sm, ok := smp.Get(s.Label); ok {
					cpu, mem = sm.Cpu, sm.Mem
				}
			}
			out = append(out, snapshotToProc(s, cpu, mem))
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
