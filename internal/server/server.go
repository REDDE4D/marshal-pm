package server

import (
	"context"
	"io"
	"net"

	"marshal/internal/pb"

	"google.golang.org/grpc"
)

// Server implements pb.FleetServer backed by an in-memory Registry.
type Server struct {
	pb.UnimplementedFleetServer
	reg *Registry
}

// NewServer wires a Fleet server to a registry.
func NewServer(reg *Registry) *Server { return &Server{reg: reg} }

// Connect terminates one agent's upstream. M7 reads Hello + StateSnapshot and
// acks Hello; the downstream direction is reserved for M9.
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
			s.reg.Open(name)
			_ = stream.Send(&pb.ServerMessage{Msg: &pb.ServerMessage_HelloAck{HelloAck: &pb.HelloAck{}}})
		case *pb.AgentMessage_Snapshot:
			if name != "" {
				s.reg.Update(name, m.Snapshot.GetProcs())
			}
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
	pb.RegisterFleetServer(gs, NewServer(reg))
	go func() {
		<-ctx.Done()
		gs.GracefulStop()
	}()
	return gs.Serve(lis)
}
