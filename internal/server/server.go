package server

import (
	"context"
	"io"
	"log"
	"net"
	"sort"

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
}

// NewServer wires a Fleet server to a registry and (optional) metric stores.
func NewServer(reg *Registry, ss *stores) *Server { return &Server{reg: reg, stores: ss} }

// Connect terminates one agent's upstream: reads Hello (acking the stored metric
// high-water-mark), StateSnapshot (live state), and MetricBatch (persisted).
func (s *Server) Connect(stream pb.Fleet_ConnectServer) error {
	var name string
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
			var watermark int64
			if s.stores != nil {
				if st, err := s.stores.get(name); err == nil {
					watermark, _ = st.MaxTs()
				}
			}
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{
				HelloAck: &pb.HelloAck{LastMetricTsMs: watermark},
			}})
		case *pb.AgentMessage_Snapshot:
			if name != "" {
				s.reg.Update(name, m.Snapshot.GetProcs())
			}
		case *pb.AgentMessage_Metrics:
			if name != "" && s.stores != nil {
				s.storeBatch(name, m.Metrics.GetSamples())
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

// ListFleet returns the current aggregated fleet state.
func (s *Server) ListFleet(_ context.Context, _ *pb.ListFleetRequest) (*pb.ListFleetResponse, error) {
	return &pb.ListFleetResponse{Agents: s.reg.List()}, nil
}

// Serve registers the Fleet service on lis and serves until ctx is canceled.
func Serve(ctx context.Context, lis net.Listener, reg *Registry) error {
	gs := grpc.NewServer()
	pb.RegisterFleetServer(gs, NewServer(reg, nil))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	return gs.Serve(lis)
}
