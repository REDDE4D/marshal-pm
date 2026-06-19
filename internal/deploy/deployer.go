package deploy

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/config"
	"marshal/internal/pb"
)

const (
	phaseCloning  = "cloning"
	phaseBuilding = "building"
	phaseFailed   = "failed"
)

// Runner executes a command in dir, streaming combined output to stdout/stderr.
type Runner interface {
	Run(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error
}

// Host is the agent surface the deployer drives after a successful build.
type Host interface {
	Exists(name string) bool                     // an app of this name is already managed
	Source(name string) (config.GitSource, bool) // persisted git source for redeploy
	Launch(app config.App) error                 // mgr.Add + persist (the start chain)
	Restart(name string) error                   // restart in place (picks up new binary)
	Writers(label string) (stdout, stderr io.Writer)
}

type state struct {
	phase  string
	detail string
}

// Deployer clones, builds, and launches git-sourced apps out of band, tracking
// per-app phase so the fleet heartbeat can surface progress.
type Deployer struct {
	host       Host
	runner     Runner
	deployRoot string

	mu     sync.Mutex
	states map[string]state
}

// New builds a Deployer. deployRoot is the directory under which each app's
// checkout lives (deployRoot/<name>).
func New(host Host, runner Runner, deployRoot string) *Deployer {
	return &Deployer{host: host, runner: runner, deployRoot: deployRoot, states: map[string]state{}}
}

func (d *Deployer) setState(name, phase, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[name] = state{phase: phase, detail: detail}
}

func (d *Deployer) clearState(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.states, name)
}

func (d *Deployer) dir(name string) string { return filepath.Join(d.deployRoot, name) }

// Snapshots returns one synthetic ProcInfo per tracked (in-flight or failed)
// deploy, so the heartbeat shows it even before any real instance exists.
func (d *Deployer) Snapshots() []pb.ProcInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]pb.ProcInfo, 0, len(d.states))
	for name, st := range d.states {
		out = append(out, pb.ProcInfo{
			Name:   name,
			State:  st.phase,
			Source: "git",
			Detail: st.detail,
		})
	}
	return out
}

// Forget clears any tracked state for name and removes its deploy dir. Returns
// true if a state entry existed.
func (d *Deployer) Forget(name string) bool {
	d.mu.Lock()
	_, had := d.states[name]
	delete(d.states, name)
	d.mu.Unlock()
	_ = os.RemoveAll(d.dir(name))
	return had
}
