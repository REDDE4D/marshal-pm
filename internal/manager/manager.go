// Package manager owns a set of supervised apps, each fanned into N instances,
// and supports runtime mutation (add/stop/restart/delete) for the daemon.
package manager

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/proc"
	"github.com/REDDE4D/marshal-pm/internal/supervisor"
)

// InstanceSnapshot is a labeled view of one supervised instance.
type InstanceSnapshot struct {
	ID         int    // app id (stable, monotonic)
	Name       string // app name
	InstanceID int    // 0..instances-1
	Label      string // "name#idx"
	Source     string // "command" | "git" (M21), derived from the app spec
	Credential string // M22 credstore name, from the app spec's GitSource
	supervisor.Snapshot
}

// LogProvider supplies per-instance output writers and disposes of them.
type LogProvider interface {
	WriterPair(label string) (stdout, stderr io.Writer)
	Remove(label string)
}

// Option configures a Manager.
type Option func(*Manager)

// WithLogs wires per-instance stdout/stderr capture.
func WithLogs(lp LogProvider) Option {
	return func(m *Manager) { m.logs = lp }
}

// RestartSink records a restart event for an instance label. M-E.
type RestartSink interface {
	Record(label string, tsMs int64) error
}

// WithRestartSink wires per-instance restart events to sink.
func WithRestartSink(s RestartSink) Option {
	return func(m *Manager) { m.restartSink = s }
}

// managedInstance is one running (or stopped) instance and its lifecycle handles.
type managedInstance struct {
	instanceID int
	label      string
	inst       *supervisor.Instance
	cancel     context.CancelFunc
	done       chan struct{}
}

// managedApp groups an app's instances with its definition.
type managedApp struct {
	id    int
	name  string
	spec  config.App
	insts []*managedInstance
}

// Manager supervises apps and their instances under a base context.
type Manager struct {
	ctx context.Context

	// opMu serializes mutating operations (Add/Stop/Restart/Delete/StopAll) so a
	// blocking stop cannot interleave with another mutator and orphan goroutines.
	opMu sync.Mutex

	mu          sync.Mutex
	apps        []*managedApp
	nextID      int
	logs        LogProvider
	restartSink RestartSink

	// onReloadStep, when set, fires during Reload after an instance is stopped
	// and before its replacement starts. Test seam only; nil in production.
	onReloadStep func()
}

// New builds an empty manager rooted at ctx. Instances spawned by Add run until
// ctx is canceled, the manager is StopAll'd, or they are individually stopped.
func New(ctx context.Context, opts ...Option) *Manager {
	m := &Manager{ctx: ctx}
	for _, o := range opts {
		o(m)
	}
	return m
}

func policyFor(app config.App) supervisor.Policy {
	return supervisor.Policy{
		Mode:        app.Restart,
		MinUptime:   time.Second,
		MaxRestarts: app.MaxRestarts,
		BaseBackoff: 100 * time.Millisecond,
		MaxBackoff:  15 * time.Second,
		KillTimeout: app.KillTimeout.Duration,
	}
}

// startInstance launches one instance goroutine. Caller holds m.mu.
func (m *Manager) startInstance(app config.App, idx int) *managedInstance {
	label := fmt.Sprintf("%s#%d", app.Name, idx)
	spec := proc.Spec{Cmd: app.Cmd, Args: app.Args, Cwd: app.Cwd, Env: app.Env, InstanceID: idx}
	if m.logs != nil {
		spec.Stdout, spec.Stderr = m.logs.WriterPair(label)
	}
	var sopts []supervisor.Option
	if m.restartSink != nil {
		l := label // capture this instance's "name#idx"
		sopts = append(sopts, supervisor.WithOnRestart(func() {
			_ = m.restartSink.Record(l, time.Now().UnixMilli())
		}))
	}
	inst := supervisor.NewInstance(spec, policyFor(app), sopts...)
	ictx, cancel := context.WithCancel(m.ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		inst.Run(ictx)
	}()
	return &managedInstance{
		instanceID: idx,
		label:      label,
		inst:       inst,
		cancel:     cancel,
		done:       done,
	}
}

// Add registers a new app (already defaulted/validated) and starts its instances.
func (m *Manager) Add(app config.App) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.apps {
		if a.name == app.Name {
			return nil, fmt.Errorf("app %q already exists", app.Name)
		}
	}
	m.nextID++
	ma := &managedApp{id: m.nextID, name: app.Name, spec: app}
	for idx := 0; idx < app.Instances; idx++ {
		ma.insts = append(ma.insts, m.startInstance(app, idx))
	}
	m.apps = append(m.apps, ma)
	return snapshotApp(ma), nil
}

// Stop gracefully stops the selected apps' instances; the apps remain listed.
func (m *Manager) Stop(sel string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	m.mu.Unlock()

	stopInstances(insts)
	return m.Describe(sel)
}

// Restart stops then recreates the selected apps' instances.
func (m *Manager) Restart(sel string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	m.mu.Unlock()

	stopInstances(insts)

	m.mu.Lock()
	for _, a := range apps {
		fresh := make([]*managedInstance, 0, a.spec.Instances)
		for idx := 0; idx < a.spec.Instances; idx++ {
			fresh = append(fresh, m.startInstance(a.spec, idx))
		}
		a.insts = fresh
	}
	m.mu.Unlock()
	return m.Describe(sel)
}

// reloadOnlineTimeout bounds the wait for a freshly started instance to come
// online before a rolling reload proceeds to the next instance.
const reloadOnlineTimeout = 10 * time.Second

// Reload performs a rolling graceful restart of the selected apps: each app's
// instances are restarted one at a time (stop, wait for exit, start, wait for
// online), so a multi-instance app keeps at most one instance down at any moment.
// A single-instance app degrades to an ordinary graceful restart.
//
// Known behaviors:
//  1. A replacement instance that does not reach online within reloadOnlineTimeout
//     is skipped past without failing the reload — Reload is a rolling restart, not
//     a health gate (mirrors Restart's behavior).
//  2. Reload of a stopped app also starts its instances: it iterates spec.Instances
//     and restarts each slot regardless of prior state.
//  3. If the manager context is canceled mid-reload (e.g. daemon shutdown), Reload
//     aborts after the current instance wait and returns context.Canceled rather than
//     spinning up doomed instances and reporting success.
func (m *Manager) Reload(sel string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	apps = append([]*managedApp(nil), apps...) // own the slice; we mutate per index below
	m.mu.Unlock()

	for _, a := range apps {
		for idx := 0; idx < a.spec.Instances; idx++ {
			m.mu.Lock()
			var old *managedInstance
			if idx < len(a.insts) {
				old = a.insts[idx]
			}
			m.mu.Unlock()

			if old != nil {
				old.cancel()
				<-old.done
			}

			if m.onReloadStep != nil {
				m.onReloadStep()
			}

			m.mu.Lock()
			fresh := m.startInstance(a.spec, idx)
			if idx < len(a.insts) {
				a.insts[idx] = fresh
			} else {
				a.insts = append(a.insts, fresh)
			}
			m.mu.Unlock()

			waitInstanceOnline(m.ctx, fresh, reloadOnlineTimeout)
			if m.ctx.Err() != nil {
				return nil, m.ctx.Err()
			}
		}
	}
	return m.Describe(sel)
}

// waitInstanceOnline waits until the instance reports Online, the timeout elapses,
// or the manager context is canceled (daemon shutdown). Uses a ticker to avoid
// busy-polling. Best-effort: a never-online instance simply ends the wait so
// reload can proceed (or ctx.Err() is checked by the caller after return).
func waitInstanceOnline(ctx context.Context, in *managedInstance, timeout time.Duration) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
			if in.inst.Snapshot().State == supervisor.StateOnline {
				return
			}
		}
	}
}

// Delete stops the selected apps and removes them from management.
func (m *Manager) Delete(sel string) ([]InstanceSnapshot, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	apps, err := m.resolve(sel)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	insts := collectInstances(apps)
	del := make(map[int]bool, len(apps))
	var removed []InstanceSnapshot
	for _, a := range apps {
		del[a.id] = true
		removed = append(removed, snapshotApp(a)...)
	}
	remaining := m.apps[:0:0]
	for _, a := range m.apps {
		if !del[a.id] {
			remaining = append(remaining, a)
		}
	}
	m.apps = remaining
	m.mu.Unlock()

	stopInstances(insts)
	if m.logs != nil {
		for _, s := range removed {
			m.logs.Remove(s.Label)
		}
	}
	return removed, nil
}

// List snapshots every instance of every app.
func (m *Manager) List() []InstanceSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []InstanceSnapshot
	for _, a := range m.apps {
		out = append(out, snapshotApp(a)...)
	}
	return out
}

// Describe snapshots the instances of the selected apps.
func (m *Manager) Describe(sel string) ([]InstanceSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	apps, err := m.resolve(sel)
	if err != nil {
		return nil, err
	}
	var out []InstanceSnapshot
	for _, a := range apps {
		out = append(out, snapshotApp(a)...)
	}
	return out, nil
}

// Specs returns the current app definitions (for save).
func (m *Manager) Specs() []config.App {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]config.App, 0, len(m.apps))
	for _, a := range m.apps {
		out = append(out, a.spec)
	}
	return out
}

// StopAll gracefully stops every instance (used on daemon shutdown).
func (m *Manager) StopAll() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	insts := collectInstances(m.apps)
	m.mu.Unlock()
	stopInstances(insts)
}

// resolve maps a selector to apps. Caller holds m.mu. "all" -> every app;
// an integer -> id match; otherwise -> name match.
func (m *Manager) resolve(sel string) ([]*managedApp, error) {
	if sel == "all" {
		return append([]*managedApp(nil), m.apps...), nil
	}
	if id, err := strconv.Atoi(sel); err == nil {
		for _, a := range m.apps {
			if a.id == id {
				return []*managedApp{a}, nil
			}
		}
	}
	for _, a := range m.apps {
		if a.name == sel {
			return []*managedApp{a}, nil
		}
	}
	return nil, fmt.Errorf("no app matching %q", sel)
}

func collectInstances(apps []*managedApp) []*managedInstance {
	var insts []*managedInstance
	for _, a := range apps {
		insts = append(insts, a.insts...)
	}
	return insts
}

// stopInstances cancels each instance's context, then waits for all to exit.
func stopInstances(insts []*managedInstance) {
	for _, in := range insts {
		in.cancel()
	}
	for _, in := range insts {
		<-in.done
	}
}

func snapshotApp(a *managedApp) []InstanceSnapshot {
	src := "command"
	cred := ""
	if a.spec.Source != nil {
		src = "git"
		cred = a.spec.Source.Credential
	}
	out := make([]InstanceSnapshot, 0, len(a.insts))
	for _, in := range a.insts {
		out = append(out, InstanceSnapshot{
			ID:         a.id,
			Name:       a.name,
			InstanceID: in.instanceID,
			Label:      in.label,
			Source:     src,
			Credential: cred,
			Snapshot:   in.inst.Snapshot(),
		})
	}
	return out
}
