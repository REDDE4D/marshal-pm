// Package daemon implements the marshald gRPC Daemon service over a Unix socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"marshal/internal/manager"
	"marshal/internal/pb"
	"marshal/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.DaemonServer backed by a dynamic manager.
type Server struct {
	pb.UnimplementedDaemonServer
	mgr   *manager.Manager
	store *store.Store
	kill  func() // triggers daemon shutdown (set by Run)
}

// Start admits and launches one or more apps.
func (s *Server) Start(_ context.Context, req *pb.StartRequest) (*pb.ProcList, error) {
	var out []manager.InstanceSnapshot
	for _, spec := range req.GetApps() {
		app, err := appSpecToConfig(spec)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%v", err)
		}
		snaps, err := s.mgr.Add(app)
		if err != nil {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		out = append(out, snaps...)
	}
	return toProcList(out), nil
}

func (s *Server) Stop(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Stop, sel)
}

func (s *Server) Restart(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Restart, sel)
}

func (s *Server) Delete(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Delete, sel)
}

func (s *Server) Describe(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	return s.mutate(s.mgr.Describe, sel)
}

// mutate runs a selector-based manager op, mapping not-found to NotFound.
func (s *Server) mutate(op func(string) ([]manager.InstanceSnapshot, error), sel *pb.Selector) (*pb.ProcList, error) {
	snaps, err := op(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	return toProcList(snaps), nil
}

func (s *Server) List(_ context.Context, _ *pb.Empty) (*pb.ProcList, error) {
	return toProcList(s.mgr.List()), nil
}

func (s *Server) Save(_ context.Context, _ *pb.Empty) (*pb.Ack, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "no store configured")
	}
	if err := s.store.Save(s.mgr.Specs()); err != nil {
		return nil, status.Errorf(codes.Internal, "save: %v", err)
	}
	return &pb.Ack{Ok: true, Message: "saved"}, nil
}

func (s *Server) Resurrect(_ context.Context, _ *pb.Empty) (*pb.ProcList, error) {
	if s.store == nil {
		return nil, status.Error(codes.Unavailable, "no store configured")
	}
	apps, err := s.store.Load()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load dump: %v", err)
	}
	var out []manager.InstanceSnapshot
	for _, app := range apps {
		snaps, err := s.mgr.Add(app) // skip already-running apps
		if err != nil {
			continue
		}
		out = append(out, snaps...)
	}
	return toProcList(out), nil
}

func (s *Server) Kill(_ context.Context, _ *pb.Empty) (*pb.Ack, error) {
	if s.kill != nil {
		go s.kill() // async: GracefulStop waits for in-flight RPCs, so calling it inline here would deadlock on this very RPC
	}
	return &pb.Ack{Ok: true, Message: "stopping"}, nil
}

// Run starts the daemon: resolves the socket, auto-resurrects, serves until ctx
// is canceled or Kill is called, then gracefully stops everything.
func Run(ctx context.Context, st *store.Store) error {
	if err := st.EnsureDir(); err != nil {
		return err
	}
	mgr := manager.New(ctx)
	if apps, err := st.Load(); err == nil {
		for _, app := range apps {
			_, _ = mgr.Add(app)
		}
	}

	sock := st.SocketPath()
	removeStaleSocket(sock)
	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}

	gs := grpc.NewServer()
	srv := &Server{mgr: mgr, store: st}
	var once sync.Once
	stopped := make(chan struct{})
	srv.kill = func() { once.Do(func() { close(stopped) }) }
	pb.RegisterDaemonServer(gs, srv)

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-serveCtx.Done():
		case <-stopped:
		}
		gs.GracefulStop()
	}()

	serveErr := gs.Serve(lis)
	cancel() // unblock the watcher if Serve returned on its own
	mgr.StopAll()
	_ = os.Remove(sock)
	if serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
		return serveErr
	}
	return nil
}

// removeStaleSocket deletes a leftover socket file if nothing is listening.
func removeStaleSocket(sock string) {
	if _, err := os.Stat(sock); err != nil {
		return
	}
	if c, err := net.Dial("unix", sock); err == nil {
		_ = c.Close()
		return // a live daemon already owns it
	}
	_ = os.Remove(sock)
}
