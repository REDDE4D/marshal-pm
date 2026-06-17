package daemon

import (
	"context"
	"testing"

	"marshal/internal/manager"
	"marshal/internal/pb"
	"marshal/internal/store"
)

// newCommandTestServer builds a minimal Server suitable for handleFleetCommand tests.
// It uses a real store (no metrics/logs — procList tolerates nil samplers).
func newCommandTestServer(t *testing.T) *Server {
	t.Helper()
	st := store.NewAt(t.TempDir())
	if err := st.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	return &Server{mgr: manager.New(context.Background()), store: st}
}

func sleepLongSpec(name string) *pb.AppSpec {
	return &pb.AppSpec{Name: name, Cmd: "sleep", Args: []string{"30"}, Instances: 1, Restart: "no"}
}

func TestHandleFleetCommandStart(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	cmd := &pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app1")}},
		}},
	}
	res := s.handleFleetCommand(cmd)
	if !res.GetOk() {
		t.Fatalf("expected Ok=true, got error: %s", res.GetError())
	}
	if len(res.GetProcs()) == 0 {
		t.Fatal("expected procs in result, got none")
	}

	// verify auto-save: store should be loadable with 1 app
	apps, err := s.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(apps) != 1 || apps[0].Name != "app1" {
		t.Fatalf("store after start = %+v, want [{Name:app1}]", apps)
	}
}

func TestHandleFleetCommandStop(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	// Start an app first via handleFleetCommand
	startRes := s.handleFleetCommand(&pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app2")}},
		}},
	})
	if !startRes.GetOk() {
		t.Fatalf("start failed: %s", startRes.GetError())
	}

	stopRes := s.handleFleetCommand(&pb.Command{
		RequestId: 2,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: "app2"}}},
	})
	if !stopRes.GetOk() {
		t.Fatalf("stop failed: %s", stopRes.GetError())
	}
}

func TestHandleFleetCommandRestart(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	_ = s.handleFleetCommand(&pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app3")}},
		}},
	})

	res := s.handleFleetCommand(&pb.Command{
		RequestId: 2,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: "app3"}}},
	})
	if !res.GetOk() {
		t.Fatalf("restart failed: %s", res.GetError())
	}
}

func TestHandleFleetCommandDelete(t *testing.T) {
	s := newCommandTestServer(t)
	defer s.mgr.StopAll()

	_ = s.handleFleetCommand(&pb.Command{
		RequestId: 1,
		Op: &pb.ControlOp{Op: &pb.ControlOp_Start{
			Start: &pb.StartRequest{Apps: []*pb.AppSpec{sleepLongSpec("app4")}},
		}},
	})

	res := s.handleFleetCommand(&pb.Command{
		RequestId: 2,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Delete{Delete: &pb.Selector{Target: "app4"}}},
	})
	if !res.GetOk() {
		t.Fatalf("delete failed: %s", res.GetError())
	}

	// verify auto-save after delete: store should have 0 apps
	apps, err := s.store.Load()
	if err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if len(apps) != 0 {
		t.Fatalf("store after delete = %+v, want empty", apps)
	}
}

func TestHandleFleetCommandUnknownSelector(t *testing.T) {
	s := newCommandTestServer(t)

	res := s.handleFleetCommand(&pb.Command{
		RequestId: 3,
		Op:        &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: "ghost"}}},
	})
	if res.GetOk() {
		t.Fatal("expected Ok=false for unknown selector")
	}
	if res.GetError() == "" {
		t.Fatal("expected non-empty error string")
	}
}

func TestHandleFleetCommandNilOp(t *testing.T) {
	s := newCommandTestServer(t)

	res := s.handleFleetCommand(&pb.Command{RequestId: 99, Op: nil})
	if res.GetOk() {
		t.Fatal("expected Ok=false for nil op")
	}
}
