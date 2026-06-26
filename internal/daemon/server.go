// Package daemon implements the marshald gRPC Daemon service over a Unix socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/deploy"
	"github.com/REDDE4D/marshal-pm/internal/eventstore"
	"github.com/REDDE4D/marshal-pm/internal/fleet"
	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/hostmetrics"
	"github.com/REDDE4D/marshal-pm/internal/logs"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/memguard"
	"github.com/REDDE4D/marshal-pm/internal/metrics"
	"github.com/REDDE4D/marshal-pm/internal/metricstore"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
	"github.com/REDDE4D/marshal-pm/internal/supervisor"
	"github.com/REDDE4D/marshal-pm/internal/version"

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
	estore           *eventstore.Store  // restart-event history (M-E)
	kill             func()             // triggers daemon shutdown (set by Run)
	logPolicyDefault logs.Policy        // effective default log policy (from WithLogRetention)
	deployer         *deploy.Deployer
	guard            *memguard.Guard // memory-limit restart guard (M-?)
}

// launchApp admits one already-converted app into the manager and sets its log
// policy. Shared by doStart and the deployer's Launch.
func (s *Server) launchApp(app config.App) ([]manager.InstanceSnapshot, error) {
	if s.logs != nil {
		s.logs.SetPolicy(app.Name, logPolicy(app, s.logPolicyDefault))
	}
	if s.guard != nil {
		s.guard.SetLimit(app.Name, app.MaxMemoryRestart.Bytes)
	}
	return s.mgr.Add(app)
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
		snaps, err := s.launchApp(app)
		if err != nil {
			return nil, err
		}
		out = append(out, snaps...)
	}
	return out, nil
}

// --- deploy.Host ---

func (s *Server) Exists(name string) bool {
	for _, sp := range s.mgr.Specs() {
		if sp.Name == name {
			return true
		}
	}
	return false
}

func (s *Server) Source(name string) (config.GitSource, bool) {
	for _, sp := range s.mgr.Specs() {
		if sp.Name == name && sp.Source != nil {
			return *sp.Source, true
		}
	}
	return config.GitSource{}, false
}

func (s *Server) Launch(app config.App) error {
	if _, err := s.launchApp(app); err != nil {
		return err
	}
	if s.store != nil {
		_ = s.store.Save(s.mgr.Specs())
	}
	return nil
}

func (s *Server) Writers(label string) (io.Writer, io.Writer) {
	if s.logs == nil {
		return io.Discard, io.Discard
	}
	return s.logs.WriterPair(label)
}

// deployHost is a thin adapter that exposes *Server as a deploy.Host.
// It is needed because *Server already has a Restart method with a different
// signature (the gRPC DaemonServer.Restart), so we cannot implement
// deploy.Host.Restart directly on *Server.
type deployHost struct{ s *Server }

func (h deployHost) Exists(name string) bool                     { return h.s.Exists(name) }
func (h deployHost) Source(name string) (config.GitSource, bool) { return h.s.Source(name) }
func (h deployHost) Launch(app config.App) error                 { return h.s.Launch(app) }
func (h deployHost) Writers(label string) (io.Writer, io.Writer) { return h.s.Writers(label) }
func (h deployHost) Restart(name string) error {
	_, err := h.s.mgr.Restart(name)
	return err
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
	snaps, err := s.mgr.Delete(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if s.guard != nil {
		seen := map[string]bool{}
		for _, sn := range snaps {
			if !seen[sn.Name] {
				seen[sn.Name] = true
				s.guard.Remove(sn.Name)
			}
		}
	}
	return s.procList(snaps), nil
}

// Reset zeroes the restart counters of the selected apps and prunes their
// recorded restart events.
func (s *Server) Reset(_ context.Context, sel *pb.Selector) (*pb.ProcList, error) {
	snaps, err := s.mgr.ResetCounters(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if s.estore != nil {
		labels := make([]string, 0, len(snaps))
		for _, sn := range snaps {
			labels = append(labels, sn.Label)
		}
		_, _ = s.estore.DeleteLabels(labels)
	}
	return s.procList(snaps), nil
}

// Flush clears captured logs for the selected apps.
func (s *Server) Flush(_ context.Context, sel *pb.Selector) (*pb.Ack, error) {
	snaps, err := s.mgr.Describe(sel.GetTarget())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "%v", err)
	}
	if s.logs != nil {
		labels := make([]string, 0, len(snaps))
		for _, sn := range snaps {
			labels = append(labels, sn.Label)
		}
		_ = s.logs.Truncate(labels)
	}
	return &pb.Ack{Ok: true, Message: fmt.Sprintf("flushed %d instance(s)", len(snaps))}, nil
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
	fleetPoll      time.Duration
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

// WithFleetPollInterval overrides how often the fleet supervisor re-reads the
// store's server config (default 2s; used by tests).
func WithFleetPollInterval(d time.Duration) Option {
	return func(o *runOptions) { o.fleetPoll = d }
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
	cfg := runOptions{sampleInterval: 5 * time.Second, retention: 168 * time.Hour, logRetention: logs.DefaultPolicy, fleetPoll: 2 * time.Second}
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
	estore, err := eventstore.Open(st.RestartsDBPath())
	if err != nil {
		return fmt.Errorf("open restarts db: %w", err)
	}
	mgr := manager.New(ctx, manager.WithLogs(reg), manager.WithRestartSink(estore))
	sampler := metrics.NewSampler(cfg.sampleInterval)
	hostSampler := hostmetrics.NewSampler()
	mdb, err := metricstore.Open(st.MetricsDBPath())
	if err != nil {
		return fmt.Errorf("open metrics db: %w", err)
	}
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
	srv := &Server{mgr: mgr, store: st, logs: reg, metrics: sampler, mdb: mdb, estore: estore, logPolicyDefault: cfg.logRetention}
	srv.deployer = deploy.New(deployHost{srv}, deploy.ExecRunner{}, st.DeploysDir())
	srv.guard = memguard.New(func(name string) { go func() { _, _ = mgr.Restart(name) }() }, log.Printf)
	if apps, err := st.Load(); err == nil {
		for _, app := range apps {
			srv.guard.SetLimit(app.Name, app.MaxMemoryRestart.Bytes)
		}
	}
	sampler.SetOnTick(func(m map[string]metrics.Sample) {
		srv.guard.Check(m)
		if len(m) == 0 {
			return
		}
		samples := make([]metricstore.Sample, 0, len(m))
		for label, sm := range m {
			samples = append(samples, metricstore.Sample{Label: label, Cpu: sm.Cpu, Mem: sm.Mem})
		}
		_ = mdb.Append(time.Now().UnixMilli(), samples)
	})
	var once sync.Once
	stopped := make(chan struct{})
	srv.kill = func() { once.Do(func() { close(stopped) }) }
	pb.RegisterDaemonServer(gs, srv)

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	run := func(cctx context.Context, tgt fleetTarget, fleetTok string) {
		tlsCfg, tErr := fleetauth.ClientTLS(tgt.fingerprint, tgt.ca)
		if tErr != nil {
			log.Printf("fleet: disabled, bad TLS config: %v", tErr)
			return
		}
		fc := fleet.New(tgt.address, tgt.name, version.String(),
			srv.fleetSnapshot(),
			fleet.WithTLS(tlsCfg),
			fleet.WithAuth(fleetTok, tgt.enrollToken, st.SaveFleetToken),
			fleet.WithMetrics(metricsSince(mdb)),
			fleet.WithLogs(logsSince(reg)),
			fleet.WithHost(func() *pb.HostMetrics { return hostSampler.Sample() }),
			fleet.WithCommands(srv.handleFleetCommand))
		fc.Run(cctx)
	}
	go superviseFleet(serveCtx, st, cfg.fleetPoll, run)
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
				_, _ = estore.Prune(time.Now().UnixMilli() - 7*24*60*60*1000) // M-E: 7-day retention
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
	_ = estore.Close()
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
