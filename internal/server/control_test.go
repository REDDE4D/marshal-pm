package server

import (
	"context"
	"testing"

	"marshal/internal/pb"
)

func TestControlUnknownAgentErrors(t *testing.T) {
	srv := NewServer(NewRegistry(), nil, nil, nil)
	op := &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: "web"}}}
	if _, err := srv.Control(context.Background(), "ghost", op); err == nil {
		t.Fatal("Control on an unconnected agent = nil err; want error")
	}
}
