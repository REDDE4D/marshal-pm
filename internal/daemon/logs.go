package daemon

import (
	"sort"
	"strconv"
	"strings"

	"marshal/internal/logs"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fanLine struct {
	label string
	line  logs.Line
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

	n := int(req.GetLines())
	for _, fl := range mergeBackfill(labeled, n) {
		if err := stream.Send(lineToProto(fl)); err != nil {
			return err
		}
	}
	if !req.GetFollow() {
		return nil
	}

	agg := make(chan fanLine, 256)
	var cancels []func()
	for _, ls := range labeled {
		ch, cancel := ls.Sink.Subscribe()
		cancels = append(cancels, cancel)
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

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case fl := <-agg:
			if err := stream.Send(lineToProto(fl)); err != nil {
				return err
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
	sort.SliceStable(all, func(i, j int) bool { return all[i].line.Ts.Before(all[j].line.Ts) })
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
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
