package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"marshal/internal/logstore"
	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.FleetServer backed by an in-memory Registry and, when
// configured, per-agent metric storage.
type Server struct {
	pb.UnimplementedFleetServer
	reg    *Registry
	stores *stores
	logs   *logStores
	broker *broker
}

// NewServer wires a Fleet server to a registry and (optional) metric/log stores.
func NewServer(reg *Registry, ss *stores, ls *logStores) *Server {
	return &Server{reg: reg, stores: ss, logs: ls, broker: newBroker()}
}

// Connect terminates one agent's upstream: reads Hello (acking the stored metric
// high-water-mark), StateSnapshot (live state), and MetricBatch (persisted).
func (s *Server) Connect(stream pb.Fleet_ConnectServer) error {
	var name string
	var sess *session
	defer func() {
		if sess != nil {
			s.broker.unregister(name, sess)
			sess.failAll()
		}
	}()
	for {
		msg, err := stream.Recv()
		if err != nil {
			if name != "" {
				s.reg.Close(name)
			}
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch m := msg.GetMsg().(type) {
		case *pb.AgentMessage_Hello:
			name = m.Hello.GetAgentName()
			if name == "" {
				return status.Error(codes.InvalidArgument, "agent_name must not be empty")
			}
			s.reg.Open(name)
			sess = s.broker.register(name, stream.Send)
			var watermark, logWM int64
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					watermark, _ = st.MaxTs()
				}
			}
			if s.logs != nil {
				if st, err := s.logs.get(name); err == nil {
					logWM, _ = st.MaxTs()
				}
			}
			_ = sess.sendMsg(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: watermark, LastLogTsMs: logWM},
			}})
		case *pb.AgentMessage_Snapshot:
			if name != "" {
				s.reg.Update(name, m.Snapshot.GetProcs())
			}
		case *pb.AgentMessage_Metrics:
			if name != "" && s.stores != nil {
				s.storeBatch(name, m.Metrics.GetSamples())
			}
		case *pb.AgentMessage_Logs:
			if name != "" && s.logs != nil {
				s.storeLogBatch(name, m.Logs.GetLines())
			}
		case *pb.AgentMessage_Result:
			if sess != nil {
				sess.deliver(m.Result)
			}
		}
	}
}

// storeBatch groups a flattened batch by ts and appends each group oldest-first,
// so the store's max(ts) always reflects a fully-committed prefix.
func (s *Server) storeBatch(agent string, samples []*pb.MetricSample) {
	st, err := s.stores.get(agent)
	if err != nil {
		log.Printf("fleet: open store for %s: %v", agent, err)
		return
	}
	byTs := map[int64][]metricstore.Sample{}
	var order []int64
	for _, sm := range samples {
		ts := sm.GetTsMs()
		if _, ok := byTs[ts]; !ok {
			order = append(order, ts)
		}
		byTs[ts] = append(byTs[ts], metricstore.Sample{
			Label: sm.GetLabel(), Cpu: sm.GetCpu(), Mem: uint64(sm.GetMem()),
		})
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	for _, ts := range order {
		if err := st.Append(ts, byTs[ts]); err != nil {
			log.Printf("fleet: append for %s: %v", agent, err)
			return
		}
	}
}

// storeLogBatch appends a flattened log batch oldest-first, so the store's
// max(ts) always reflects a fully-committed prefix.
func (s *Server) storeLogBatch(agent string, lines []*pb.LogShipLine) {
	st, err := s.logs.get(agent)
	if err != nil {
		log.Printf("fleet: open log store for %s: %v", agent, err)
		return
	}
	rows := make([]logstore.Line, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, logstore.Line{
			TsMs: l.GetTsMs(), Label: l.GetLabel(), Stderr: l.GetStderr(), Text: l.GetText(),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].TsMs < rows[j].TsMs })
	if err := st.Append(rows); err != nil {
		log.Printf("fleet: append logs for %s: %v", agent, err)
	}
}

const defaultHistoryMs = int64(60 * 60 * 1000) // 1h

// FleetMetricsHistory returns bucketed CPU/mem history for one agent's app/instance.
func (s *Server) FleetMetricsHistory(_ context.Context, req *pb.FleetMetricsHistoryRequest) (*pb.MetricsHistoryResponse, error) {
	if s.stores == nil || !s.stores.has(req.GetAgentName()) {
		return nil, status.Errorf(codes.NotFound, "no metric history for agent %q", req.GetAgentName())
	}
	st, err := s.stores.get(req.GetAgentName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "open store: %v", err)
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "labels: %v", err)
	}
	sel := req.GetSelector()
	var matched []string
	for _, l := range labels {
		if l == sel || strings.HasPrefix(l, sel+"#") {
			matched = append(matched, l)
		}
	}

	sinceMs := req.GetSinceMs()
	if sinceMs <= 0 {
		sinceMs = defaultHistoryMs
	}
	bucketMs := metricstore.AutoBucketMs(sinceMs, req.GetBucketMs())
	lowerMs := time.Now().UnixMilli() - sinceMs

	var series [][]metricstore.Bucket
	for _, l := range matched {
		bs, err := st.Query(metricstore.QueryReq{Label: l, SinceMs: lowerMs, BucketMs: bucketMs})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "query: %v", err)
		}
		series = append(series, bs)
	}

	resp := &pb.MetricsHistoryResponse{}
	for _, b := range metricstore.MergeBuckets(series) {
		resp.Buckets = append(resp.Buckets, &pb.MetricBucket{
			TsMs: b.TsMs, CpuAvg: b.CpuAvg, CpuMax: b.CpuMax, MemAvg: b.MemAvg, MemMax: b.MemMax,
		})
	}
	return resp, nil
}

const defaultLogLines = 15

// FleetLogsHistory returns the most recent stored log lines for one agent's
// app/instance selector, merged across instances and filtered by stream.
func (s *Server) FleetLogsHistory(_ context.Context, req *pb.FleetLogsHistoryRequest) (*pb.FleetLogsHistoryResponse, error) {
	if s.logs == nil || !s.logs.has(req.GetAgentName()) {
		return nil, status.Errorf(codes.NotFound, "no log history for agent %q", req.GetAgentName())
	}
	st, err := s.logs.get(req.GetAgentName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "open log store: %v", err)
	}
	labels, err := st.Labels()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "labels: %v", err)
	}
	sel := req.GetSelector()
	var matched []string
	for _, l := range labels {
		if l == sel || strings.HasPrefix(l, sel+"#") {
			matched = append(matched, l)
		}
	}
	limit := int(req.GetLines())
	if limit <= 0 {
		limit = defaultLogLines
	}
	filter := streamFilter(req.GetStream())

	var series [][]logstore.StoredLine
	for _, l := range matched {
		lines, err := st.Tail(l, limit, filter)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "tail: %v", err)
		}
		series = append(series, lines)
	}

	resp := &pb.FleetLogsHistoryResponse{}
	for _, ln := range logstore.MergeTail(series, limit) {
		name, idx := splitLabel(ln.Label)
		resp.Lines = append(resp.Lines, &pb.LogLine{
			Name: name, InstanceId: idx, Stderr: ln.Stderr, Line: ln.Text,
		})
	}
	return resp, nil
}

// streamFilter maps the wire enum to a logstore filter.
func streamFilter(st pb.LogStream) logstore.StreamFilter {
	switch st {
	case pb.LogStream_LOG_STREAM_STDOUT:
		return logstore.StreamStdout
	case pb.LogStream_LOG_STREAM_STDERR:
		return logstore.StreamStderr
	default:
		return logstore.StreamAny
	}
}

// splitLabel parses "name#idx" into its parts (idx 0 when absent/unparseable).
func splitLabel(label string) (string, int32) {
	i := strings.LastIndexByte(label, '#')
	if i < 0 {
		return label, 0
	}
	n, _ := strconv.Atoi(label[i+1:])
	return label[:i], int32(n)
}

// ListFleet returns the current aggregated fleet state.
func (s *Server) ListFleet(_ context.Context, _ *pb.ListFleetRequest) (*pb.ListFleetResponse, error) {
	return &pb.ListFleetResponse{Agents: s.reg.List()}, nil
}

// FleetControl routes a control command to one connected agent over its existing
// stream and returns the agent's result. The RPC context bounds the wait.
func (s *Server) FleetControl(ctx context.Context, req *pb.FleetControlRequest) (*pb.FleetControlResponse, error) {
	sess, ok := s.broker.get(req.GetAgentName())
	if !ok {
		return nil, status.Errorf(codes.Unavailable, "agent %q not connected", req.GetAgentName())
	}
	res, err := sess.dispatch(ctx, req.GetOp())
	if err != nil {
		if errors.Is(err, errDisconnected) {
			return nil, status.Errorf(codes.Unavailable, "agent %q disconnected", req.GetAgentName())
		}
		return nil, status.FromContextError(err).Err()
	}
	return &pb.FleetControlResponse{Result: res}, nil
}

// Serve registers the Fleet service on lis and serves until ctx is canceled.
// ss/ls may be nil (no storage); when set they are closed on shutdown.
func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores, ls *logStores) error {
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, NewServer(reg, ss, ls))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
		if ss != nil {
			_ = ss.closeAll()
		}
		if ls != nil {
			_ = ls.closeAll()
		}
	}()
	return gs.Serve(lis)
}

// ServeDir builds a registry + per-agent metric stores rooted at dataDir, then
// serves until ctx is canceled.
func ServeDir(ctx context.Context, lis net.Listener, dataDir string, opts ...RegOption) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	ss := newStores(dataDir)
	ls := newLogStores(dataDir)
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		const retentionMs = int64(7 * 24 * 60 * 60 * 1000)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cutoff := time.Now().UnixMilli() - retentionMs
				ss.pruneAll(cutoff)
				ls.pruneAll(cutoff)
			}
		}
	}()
	return Serve(ctx, lis, NewRegistry(opts...), ss, ls)
}
