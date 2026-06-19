package deploy

import (
	"context"
	"fmt"
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
	wg     sync.WaitGroup // tracks in-flight deploy goroutines (test sync)
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

// Start validates a git deploy and launches it in the background. The returned
// error rejects the request synchronously (duplicate name, empty repo); a nil
// return means "accepted" — progress is reported via Snapshots.
func (d *Deployer) Start(app config.App) error {
	if app.Source == nil || app.Source.Repo == "" {
		return fmt.Errorf("git source requires a repo")
	}
	if d.host.Exists(app.Name) {
		return fmt.Errorf("app %q already exists", app.Name)
	}
	d.mu.Lock()
	if _, busy := d.states[app.Name]; busy {
		d.mu.Unlock()
		return fmt.Errorf("app %q already exists", app.Name)
	}
	d.states[app.Name] = state{phase: phaseCloning}
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runDeploy(app, false)
	}()
	return nil
}

// Redeploy fetches the latest commit for an existing git app, rebuilds, and
// restarts it in place. If the rebuild fails the running app is left untouched.
func (d *Deployer) Redeploy(name string) error {
	src, ok := d.host.Source(name)
	if !ok || src.Repo == "" {
		return fmt.Errorf("app %q is not git-sourced", name)
	}
	d.mu.Lock()
	if _, busy := d.states[name]; busy {
		d.mu.Unlock()
		return fmt.Errorf("app %q is already deploying", name)
	}
	d.states[name] = state{phase: phaseBuilding}
	d.mu.Unlock()

	app := config.App{Name: name, Source: &src}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runDeploy(app, true)
	}()
	return nil
}

// runDeploy performs clone (or fetch when redeploy)+build, then launches or
// restarts. On any failure it leaves a failed state for the dashboard.
func (d *Deployer) runDeploy(app config.App, redeploy bool) {
	ctx := context.Background()
	dir := d.dir(app.Name)
	stdout, stderr := d.host.Writers(app.Name + "#0")
	src := *app.Source

	d.setState(app.Name, phaseCloning, "")
	if err := d.fetch(ctx, dir, src, redeploy, stdout, stderr); err != nil {
		d.setState(app.Name, phaseFailed, summarize("clone", err))
		return
	}

	d.setState(app.Name, phaseBuilding, "")
	buildDir := dir
	if src.Subdir != "" {
		buildDir = filepath.Join(dir, src.Subdir)
	}
	build := src.Build
	if build == "" {
		build = DetectBuild(buildDir)
	}
	if build != "" {
		if err := d.runner.Run(ctx, buildDir, stdout, stderr, "sh", "-c", build); err != nil {
			d.setState(app.Name, phaseFailed, summarize("build", err))
			return
		}
	}

	if redeploy {
		if err := d.host.Restart(app.Name); err != nil {
			d.setState(app.Name, phaseFailed, summarize("restart", err))
			return
		}
		d.clearState(app.Name)
		return
	}

	launch := app
	launch.Cwd = buildDir
	if err := d.host.Launch(launch); err != nil {
		d.setState(app.Name, phaseFailed, summarize("launch", err))
		return
	}
	d.clearState(app.Name)
}

// fetch clones into dir for a fresh deploy, or fetches+resets for a redeploy.
func (d *Deployer) fetch(ctx context.Context, dir string, src config.GitSource, redeploy bool, stdout, stderr io.Writer) error {
	if !redeploy {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		if err := d.runner.Run(ctx, "", stdout, stderr, "git", "clone", src.Repo, dir); err != nil {
			return err
		}
		if src.Ref != "" {
			return d.runner.Run(ctx, dir, stdout, stderr, "git", "checkout", src.Ref)
		}
		return nil
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if err := d.runner.Run(ctx, dir, stdout, stderr, "git", "fetch", "origin", ref); err != nil {
		return err
	}
	return d.runner.Run(ctx, dir, stdout, stderr, "git", "reset", "--hard", "FETCH_HEAD")
}

func summarize(stage string, err error) string { return stage + " failed: " + err.Error() }

// wait blocks until all in-flight deploy goroutines finish. Tests only.
func (d *Deployer) wait() { d.wg.Wait() }
