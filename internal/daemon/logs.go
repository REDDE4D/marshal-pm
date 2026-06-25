package daemon

import (
	"sort"
	"strconv"
	"strings"

	"github.com/REDDE4D/marshal-pm/internal/logs"
	"github.com/REDDE4D/marshal-pm/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fanLine struct {
	label string
	line  logs.Line
}

// streamMatch reports whether a line on the given stream passes the filter.
func streamMatch(f pb.LogStream, stderr bool) bool {
	switch f {
	case pb.LogStream_LOG_STREAM_STDOUT:
		return !stderr
	case pb.LogStream_LOG_STREAM_STDERR:
		return stderr
	default:
		return true
	}
}

// backfillLines chooses the backfill source per the M6 routing rule:
//   - per-stream filter -> read that stream from files (deep, restart-durable);
//   - merged -> ring when it still holds all history; else files (best-effort merge).
func backfillLines(labeled []logs.Labeled, n int, st pb.LogStream) []fanLine {
	if st == pb.LogStream_LOG_STREAM_STDOUT || st == pb.LogStream_LOG_STREAM_STDERR {
		stderr := st == pb.LogStream_LOG_STREAM_STDERR
		var all []fanLine
		for _, ls := range labeled {
			lines, _ := ls.Sink.FileBackfill(stderr, n)
			for _, ln := range lines {
				all = append(all, fanLine{label: ls.Label, line: ln})
			}
		}
		return trimTail(all, n)
	}

	// Merged: the ring gives an exact, timestamp-ordered view of the most
	// recent lines — use it when it already satisfies the request. Otherwise
	// the on-disk files may hold deeper history (large n, or a cold ring after
	// a restart), so consult them; fall back to the ring only when the files
	// have nothing beyond it, preserving exact ordering for small outputs.
	// Cross-stream/cross-instance order in the file path is best-effort
	// (disk lines carry no timestamp) — the documented Approach-A limitation.
	ring := mergeBackfill(labeled, n)
	if n > 0 && len(ring) >= n {
		return ring
	}
	var all []fanLine
	for _, ls := range labeled {
		for _, stderr := range []bool{false, true} {
			lines, _ := ls.Sink.FileBackfill(stderr, n)
			for _, ln := range lines {
				all = append(all, fanLine{label: ls.Label, line: ln})
			}
		}
	}
	if len(all) <= len(ring) {
		return ring
	}
	return trimTail(all, n)
}

func trimTail(all []fanLine, n int) []fanLine {
	if n > 0 && len(all) > n {
		return all[len(all)-n:]
	}
	return all
}

// Logs streams an app's captured output: a backfill of the last N lines, then
// (if follow) live lines until the client disconnects.
func (s *Server) Logs(req *pb.LogRequest, stream pb.Daemon_LogsServer) error {
	if s.logs == nil {
		return status.Error(codes.Unavailable, "logs not configured")
	}
	snaps, err := s.mgr.Describe(req.GetTarget())
	if err != nil {
		return status.Errorf(codes.NotFound, "%v", err)
	}
	labels := make([]string, 0, len(snaps))
	for _, sn := range snaps {
		labels = append(labels, sn.Label)
	}
	labeled := s.logs.ResolveLabeled(labels)
	st := req.GetStream()
	n := int(req.GetLines())

	if !req.GetFollow() {
		for _, fl := range backfillLines(labeled, n, st) {
			if err := stream.Send(lineToProto(fl)); err != nil {
				return err
			}
		}
		return nil
	}

	// Follow: atomically snapshot the ring and subscribe per sink (closes the
	// backfill->subscribe race); deeper file history is not replayed for -f.
	agg := make(chan fanLine, 256)
	var cancels []func()
	var bf []fanLine
	for _, ls := range labeled {
		ring, ch, cancel := ls.Sink.SubscribeWithRing(n)
		cancels = append(cancels, cancel)
		for _, ln := range ring {
			if streamMatch(st, ln.Stderr) {
				bf = append(bf, fanLine{label: ls.Label, line: ln})
			}
		}
		go func(label string, ch <-chan logs.Line) {
			for ln := range ch {
				select {
				case agg <- fanLine{label: label, line: ln}:
				case <-stream.Context().Done():
					return
				}
			}
		}(ls.Label, ch)
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	sortByTs(bf)
	for _, fl := range trimTail(bf, n) {
		if err := stream.Send(lineToProto(fl)); err != nil {
			return err
		}
	}

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case fl := <-agg:
			if streamMatch(st, fl.line.Stderr) {
				if err := stream.Send(lineToProto(fl)); err != nil {
					return err
				}
			}
		}
	}
}

// mergeBackfill collects each sink's last-n lines, orders them by timestamp,
// and trims to the n most recent overall.
func mergeBackfill(labeled []logs.Labeled, n int) []fanLine {
	var all []fanLine
	for _, ls := range labeled {
		for _, ln := range ls.Sink.Backfill(n) {
			all = append(all, fanLine{label: ls.Label, line: ln})
		}
	}
	sortByTs(all)
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

func sortByTs(all []fanLine) {
	sort.SliceStable(all, func(i, j int) bool { return all[i].line.Ts.Before(all[j].line.Ts) })
}

func lineToProto(fl fanLine) *pb.LogLine {
	name, idx := splitLabel(fl.label)
	return &pb.LogLine{
		Name:       name,
		InstanceId: idx,
		Stderr:     fl.line.Stderr,
		Line:       fl.line.Text,
	}
}

// splitLabel parses "name#idx" into its parts.
func splitLabel(label string) (string, int32) {
	i := strings.LastIndexByte(label, '#')
	if i < 0 {
		return label, 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return label[:i], int32(n)
}
