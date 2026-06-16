package daemon

import (
	"context"
	"testing"

	"marshal/internal/manager"
	"marshal/internal/pb"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{mgr: manager.New(ctx)}
	return srv, func() { srv.mgr.StopAll(); cancel() }
}

func sleepSpec(name string, n int32) *pb.AppSpec {
	return &pb.AppSpec{Name: name, Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: n}
}

func TestStartThenList(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	ctx := context.Background()

	if _, err := srv.Start(ctx, &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 2)}}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	list, err := srv.List(ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Procs) != 2 {
		t.Fatalf("got %d procs, want 2", len(list.Procs))
	}
}

func TestStartDuplicateIsAlreadyExists(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	ctx := context.Background()
	req := &pb.StartRequest{Apps: []*pb.AppSpec{sleepSpec("a", 1)}}
	if _, err := srv.Start(ctx, req); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	_, err := srv.Start(ctx, req)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("got %v, want AlreadyExists", err)
	}
}

func TestStopUnknownIsNotFound(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	_, err := srv.Stop(context.Background(), &pb.Selector{Target: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got %v, want NotFound", err)
	}
}

func TestStartInvalidSpecIsInvalidArgument(t *testing.T) {
	srv, done := newTestServer(t)
	defer done()
	// Missing cmd fails config validation.
	_, err := srv.Start(context.Background(),
		&pb.StartRequest{Apps: []*pb.AppSpec{{Name: "x"}}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument", err)
	}
}
