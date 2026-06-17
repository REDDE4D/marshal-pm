// Package daemon implements the marshald gRPC Daemon service over a Unix socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"marshal/internal/fleet"
	"marshal/internal/logs"
	"marshal/internal/manager"
	"marshal/internal/metrics"
	"marshal/internal/metricstore"
	"marshal/internal/pb"
	"marshal/internal/store"
	"marshal/internal/supervisor"
	"marshal/internal/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements pb.DaemonServer backed by a dynamic manager.
type Server struct {
	pb.UnimplementedDaemonServer
	mgr              *manager.Manager
	store            *store.Store
	logs             *logs.Registry
	metrics          *metrics.Sampler
	mdb              *metricstore.Store // metric history
	kill             func()             // triggers daemon shutdown (set by Run)
	logPolicyDefault logs.Policy        // effective default log policy (from WithLogRetention)
}

// doStart admits and launches one or more apps from wire specs.
// It is the shared core used by Start (gRPC) and handleFleetCommand (fleet).
func (s *Server) doStart(specs []*pb.AppSpec) ([]manager.InstanceSnapshot, error) {
	var out []manager.InstanceSnapshot
	for _, spec := range specs {
		app, err := appSpecToConfig(spec)
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		if s.logs != nil {
			s.logs.SetPolicy(app.Name, logPolicy(app, s.logPolicyDefault))
		}
		snaps, err := s.mgr.Add(app)
		if err != nil {
			return nil, err
		}
		out = append(out, snaps...)
	}
	return out, nil
}

// Start admits and launches one or more apps.
func (s *Server) Start(_ context.Context, req *pb.StartRequest) (*pb.ProcList, error) {
	snaps, err := s.doStart(req.GetApps())
	if err != nil {
		// doStart returns plain errors so the fleet command path gets clean
		// error strings; map them back to the gRPC codes the Daemon.Start RPC
		// has always returned: mgr.Add duplicates → AlreadyExists, config
		// validation errors → InvalidArgument.
		if isAlreadyExists(err) {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	return s.procList(snaps), nil
}

// isAlreadyExists reports whether err came from mgr.Add duplicate detection.
// The manager returns errors containing "already exists" for duplicate app names.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already")
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
	return s.procList(snaps), nil
}

func (s *Server) List(_ context.Context, _ *pb.Empty) (*pb.ProcList, error) {
	return s.procList(s.mgr.List()), nil
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
	return s.procList(out), nil
}

func (s *Server) Kill(_ context.Context, _ *pb.Empty) (*pb.Ack, error) {
	if s.kill != nil {
		go s.kill() // async: GracefulStop waits for in-flight RPCs, so calling it inline here would deadlock on this very RPC
	}
	return &pb.Ack{Ok: true, Message: "stopping"}, nil
}

type runOptions struct {
	sampleInterval time.Duration
	retention      time.Duration
	logRetention   logs.Policy
}

// Option configures Run.
type Option func(*runOptions)

// WithSampleInterval overrides the 5s metrics tick (used by tests).
func WithSampleInterval(d time.Duration) Option {
	return func(o *runOptions) { o.sampleInterval = d }
}

// WithRetention overrides the 7-day metric-history retention window (used by tests).
func WithRetention(d time.Duration) Option {
	return func(o *runOptions) { o.retention = d }
}

// WithLogRetention overrides the default log retention/compression policy.
func WithLogRetention(p logs.Policy) Option {
	return func(o *runOptions) { o.logRetention = p }
}

// metricsSnapshot adapts the manager's instance list to the sampler's view.
func metricsSnapshot(m *manager.Manager) func() []metrics.Instance {
	return func() []metrics.Instance {
		snaps := m.List()
		out := make([]metrics.Instance, 0, len(snaps))
		for _, s := range snaps {
			out = append(out, metrics.Instance{
				Label:  s.Label,
				Pid:    s.Pid,
				Online: s.State == supervisor.StateOnline,
			})
		}
		return out
	}
}

// Run starts the daemon: resolves the socket, auto-resurrects, serves until ctx
// is canceled or Kill is called, then gracefully stops everything.
func Run(ctx context.Context, st *store.Store, opts ...Option) error {
	cfg := runOptions{sampleInterval: 5 * time.Second, retention: 168 * time.Hour, logRetention: logs.DefaultPolicy}
	for _, o := range opts {
		o(&cfg)
	}
	if err := st.EnsureDir(); err != nil {
		return err
	}
	if err := st.EnsureLogsDir(); err != nil {
		return err
	}
	reg := logs.NewRegistry(st.LogsDir())
	reg.SetDefaultPolicy(cfg.logRetention)
	if apps, err := st.Load(); err == nil {
		for _, app := range apps {
			reg.SetPolicy(app.Name, logPolicy(app, cfg.logRetention))
		}
	}
	mgr := manager.New(ctx, manager.WithLogs(reg))
	sampler := metrics.NewSampler(cfg.sampleInterval)
	mdb, err := metricstore.Open(st.MetricsDBPath())
	if err != nil {
		return fmt.Errorf("open metrics db: %w", err)
	}
	sampler.SetOnTick(func(m map[string]metrics.Sample) {
		if len(m) == 0 {
			return
		}
		samples := make([]metricstore.Sample, 0, len(m))
		for label, sm := range m {
			samples = append(samples, metricstore.Sample{Label: label, Cpu: sm.Cpu, Mem: sm.Mem})
		}
		_ = mdb.Append(time.Now().UnixMilli(), samples)
	})
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
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = lis.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}

	gs := grpc.NewServer()
	srv := &Server{mgr: mgr, store: st, logs: reg, metrics: sampler, mdb: mdb, logPolicyDefault: cfg.logRetention}
	var once sync.Once
	stopped := make(chan struct{})
	srv.kill = func() { once.Do(func() { close(stopped) }) }
	pb.RegisterDaemonServer(gs, srv)

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if sc, err := st.LoadServer(); err == nil && sc != nil {
		name := sc.Name
		if name == "" {
			if h, hErr := os.Hostname(); hErr == nil {
				name = h
			}
		}
		if name == "" {
			name = "unknown"
		}
		fc := fleet.New(sc.Address, name, version.String(),
			fleetSnapshot(mgr, sampler),
			fleet.WithMetrics(metricsSince(mdb)),
			fleet.WithLogs(logsSince(reg)),
			fleet.WithCommands(srv.handleFleetCommand))
		go fc.Run(serveCtx)
	}
	go sampler.Run(serveCtx, metricsSnapshot(mgr))
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-serveCtx.Done():
				return
			case <-t.C:
				_, _ = mdb.Prune(time.Now().UnixMilli() - cfg.retention.Milliseconds())
			}
		}
	}()
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
	_ = mdb.Close()
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
