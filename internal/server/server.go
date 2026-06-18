package server

import (
	"context"
	"crypto/tls"
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

	"marshal/internal/dashboard"
	"marshal/internal/logstore"
	"marshal/internal/metricstore"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
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
	auth   *AuthStore
}

// NewServer wires a Fleet server to a registry and (optional) metric/log stores.
// auth may be nil (no auth enforcement, for unit tests that call methods directly).
func NewServer(reg *Registry, ss *stores, ls *logStores, auth *AuthStore) *Server {
	return &Server{reg: reg, stores: ss, logs: ls, broker: newBroker(), auth: auth}
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
			ctx := stream.Context()
			ack := &pb.HelloAck{}
			// s.auth == nil only occurs in unit tests that call NewServer directly and
			// drive Connect without the interceptor. Serve rejects a nil AuthStore, so
			// this branch is unreachable when the server is run for real.
			if s.auth == nil {
				// No auth configured (direct unit-test calls that bypass the
				// interceptor): trust the self-asserted name as before.
				name = m.Hello.GetAgentName()
				if name == "" {
					return status.Error(codes.InvalidArgument, "agent_name must not be empty")
				}
			} else if isEnrolling(ctx) {
				requested := m.Hello.GetAgentName()
				if requested == "" {
					return status.Error(codes.InvalidArgument, "agent_name must not be empty")
				}
				tok, err := s.auth.enrollAgent(requested)
				if err != nil {
					return status.Errorf(codes.AlreadyExists, "enroll %q: %v", requested, err)
				}
				name = requested
				ack.AgentToken = tok
			} else if authed, ok := authedAgentName(ctx); ok {
				name = authed
			} else {
				return status.Error(codes.Unauthenticated, "unauthenticated connect")
			}
			s.reg.Open(name)
			sess = s.broker.register(name, stream.Send)
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					ack.LastMetricTsMs, _ = st.MaxTs()
				}
			}
			if s.logs != nil {
				if st, err := s.logs.get(name); err == nil {
					ack.LastLogTsMs, _ = st.MaxTs()
				}
			}
			_ = sess.sendMsg(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{HelloAck: ack}})
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
	sinceMs := req.GetSinceMs()
	if sinceMs <= 0 {
		sinceMs = defaultHistoryMs
	}
	buckets, err := s.stores.History(req.GetAgentName(), req.GetSelector(), sinceMs, req.GetBucketMs())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "history: %v", err)
	}
	resp := &pb.MetricsHistoryResponse{}
	for _, b := range buckets {
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

// Serve registers the Fleet service on lis (TLS) and serves until ctx is canceled.
// ss/ls may be nil (no storage); when set they are closed on shutdown.
// auth must not be nil: unary and stream interceptors enforce admin/enroll tokens.
func Serve(ctx context.Context, lis net.Listener, reg *Registry, ss *stores, ls *logStores, cert tls.Certificate, auth *AuthStore) error {
	if auth == nil {
		return errors.New("server: Serve requires a non-nil AuthStore")
	}
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	gs := grpc.NewServer(
		grpc.Creds(creds),
		grpc.UnaryInterceptor(auth.unaryAuth),
		grpc.StreamInterceptor(auth.streamAuth),
	)
	pb.RegisterFleetServer(gs, NewServer(reg, ss, ls, auth))
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
// serves over TLS until ctx is canceled. certPath and keyPath may be empty
// strings, in which case they default to dataDir/cert.pem and dataDir/key.pem
// (and are generated on first run).
func ServeDir(ctx context.Context, lis net.Listener, dataDir, certPath, keyPath, httpAddr string, opts ...RegOption) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	cert, fp, err := LoadOrCreateCert(dataDir, certPath, keyPath)
	if err != nil {
		return err
	}
	log.Printf("fleet: server cert fingerprint %s", fp)
	auth, _, err := loadOrInitAuth(dataDir)
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
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
	reg := NewRegistry(opts...)
	if httpAddr != "" {
		if !auth.HasDashboardUser() {
			log.Printf("dashboard: no user set — run 'marshal server passwd'")
		}
		go func() {
			if err := dashboard.Serve(ctx, httpAddr, reg, ss, ls, auth, cert); err != nil {
				log.Printf("dashboard: %v", err)
			}
		}()
		log.Printf("dashboard: serving on %s", httpAddr)
	}
	return Serve(ctx, lis, reg, ss, ls, cert, auth)
}
