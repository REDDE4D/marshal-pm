package deploy

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"marshal/internal/config"
	"marshal/internal/pb"
)

const (
	phaseCloning    = "cloning"
	phaseBuilding   = "building"
	phaseFailed     = "failed"
	phaseCommitting = "committing"
)

// Runner executes a command in dir, streaming combined output to stdout/stderr.
// env, when non-nil, is appended to the inherited environment.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error
}

// Host is the agent surface the deployer drives after a successful build.
type Host interface {
	Exists(name string) bool                     // an app of this name is already managed
	Source(name string) (config.GitSource, bool) // persisted git source for redeploy
	Launch(app config.App) error                 // mgr.Add + persist (the start chain)
	Restart(name string) error                   // restart in place (picks up new binary)
	Writers(label string) (stdout, stderr io.Writer)
}

// Credential is a git credential pushed per-deploy (M22/M25). For HTTPS, Token
// is set and SSH is false. For SSH, SSH is true and PrivateKey/KnownHosts are
// set. An empty Token with SSH false means "no managed credential".
type Credential struct {
	Username   string
	Token      string // HTTPS personal-access token
	PrivateKey string // SSH OpenSSH-format private key
	KnownHosts string // SSH server-pinned host key line(s)
	SSH        bool   // true → SSH key auth
}

func (c Credential) httpsActive() bool { return !c.SSH && c.Token != "" }

// String redacts secrets so a stray %v/%+v cannot leak the token or key.
func (c Credential) String() string {
	kind := "https"
	if c.SSH {
		kind = "ssh"
	}
	return fmt.Sprintf("Credential{user:%q kind:%s}", c.Username, kind)
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
		if st.phase == phaseCommitting {
			continue
		}
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

// Root returns the clone directory of a known deployment and true. A deployment
// is "known" when its dir exists under deployRoot. Names containing a path
// separator are rejected outright. Returns ("", false) otherwise.
func (d *Deployer) Root(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", false
	}
	dir := d.dir(name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", false
	}
	return dir, true
}

// Start validates a git deploy and launches it in the background. The returned
// error rejects the request synchronously (duplicate name, empty repo); a nil
// return means "accepted" — progress is reported via Snapshots.
func (d *Deployer) Start(app config.App, cred Credential) error {
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
		d.runDeploy(app, cred, false)
	}()
	return nil
}

// Redeploy fetches the latest commit for an existing git app, rebuilds, and
// restarts it in place. If the rebuild fails the running app is left untouched.
func (d *Deployer) Redeploy(name string, cred Credential) error {
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
		d.runDeploy(app, cred, true)
	}()
	return nil
}

// Commit applies one file mutation in the app's clone and pushes it to origin.
// It refuses to run while a deploy/redeploy is in flight (and marks a transient
// committing state so a concurrent deploy is refused too).
func (d *Deployer) Commit(name string, kind pb.CommitKind, rel, newRel string, content []byte, message string, cred Credential) (*pb.CommitResult, error) {
	src, ok := d.host.Source(name)
	if !ok || src.Repo == "" {
		return nil, fmt.Errorf("app %q is not git-sourced", name)
	}
	dir, ok := d.Root(name)
	if !ok {
		return nil, fmt.Errorf("not a git deployment")
	}
	d.mu.Lock()
	if _, busy := d.states[name]; busy {
		d.mu.Unlock()
		return nil, fmt.Errorf("app %q is deploying", name)
	}
	d.states[name] = state{phase: phaseCommitting}
	d.mu.Unlock()
	defer d.clearState(name)

	return d.mutateAndPush(dir, src, cred, kind, rel, newRel, content, message)
}

// runDeploy performs clone (or fetch when redeploy)+build, then launches or
// restarts. On any failure it leaves a failed state for the dashboard.
func (d *Deployer) runDeploy(app config.App, cred Credential, redeploy bool) {
	ctx := context.Background()
	dir := d.dir(app.Name)
	stdout, stderr := d.host.Writers(app.Name + "#0")
	src := *app.Source

	d.setState(app.Name, phaseCloning, "")
	if err := d.fetch(ctx, dir, src, cred, redeploy, stdout, stderr); err != nil {
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
		// Build step gets nil env — no credential during build.
		if err := d.runner.Run(ctx, buildDir, nil, stdout, stderr, "sh", "-c", build); err != nil {
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
func (d *Deployer) fetch(ctx context.Context, dir string, src config.GitSource, cred Credential, redeploy bool, stdout, stderr io.Writer) error {
	env, cleanup, err := d.gitCredEnv(cred)
	if err != nil {
		return err
	}
	defer cleanup()

	credActive := cred.httpsActive()

	if !redeploy {
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		cloneURL := src.Repo
		if credActive {
			cloneURL = withUsername(src.Repo, cred.Username)
		}
		if err := d.runner.Run(ctx, "", env, stdout, stderr, "git", gitArgs(credActive, "clone", cloneURL, dir)...); err != nil {
			return err
		}
		if src.Ref != "" {
			return d.runner.Run(ctx, dir, env, stdout, stderr, "git", gitArgs(credActive, "checkout", src.Ref)...)
		}
		return nil
	}
	ref := src.Ref
	if ref == "" {
		ref = "HEAD"
	}
	if err := d.runner.Run(ctx, dir, env, stdout, stderr, "git", gitArgs(credActive, "fetch", "origin", ref)...); err != nil {
		return err
	}
	return d.runner.Run(ctx, dir, env, stdout, stderr, "git", gitArgs(credActive, "reset", "--hard", "FETCH_HEAD")...)
}

// gitCredEnv returns the env vars that make git authenticate without putting
// secrets on the command line. For SSH credentials it writes a short-lived
// private key + known_hosts pair under a 0600 temp dir and sets
// GIT_SSH_COMMAND. For HTTPS it writes a throwaway GIT_ASKPASS helper. With
// no credential it returns (nil, noop, nil).
func (d *Deployer) gitCredEnv(cred Credential) (env []string, cleanup func(), err error) {
	if cred.SSH {
		tmp, err := os.MkdirTemp("", "marshal-ssh-")
		if err != nil {
			return nil, func() {}, err
		}
		fail := func(e error) ([]string, func(), error) { _ = os.RemoveAll(tmp); return nil, func() {}, e }
		keyPath := filepath.Join(tmp, "id")
		key := cred.PrivateKey
		if !strings.HasSuffix(key, "\n") {
			key += "\n" // OpenSSH refuses a key without a trailing newline
		}
		if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
			return fail(err)
		}
		khPath := filepath.Join(tmp, "known_hosts")
		if err := os.WriteFile(khPath, []byte(cred.KnownHosts), 0o600); err != nil {
			return fail(err)
		}
		sshCmd := fmt.Sprintf(
			"ssh -i '%s' -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o UserKnownHostsFile='%s'",
			keyPath, khPath)
		return []string{"GIT_SSH_COMMAND=" + sshCmd, "GIT_TERMINAL_PROMPT=0"}, func() { _ = os.RemoveAll(tmp) }, nil
	}
	if cred.Token == "" {
		return nil, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "marshal-askpass-")
	if err != nil {
		return nil, func() {}, err
	}
	script := filepath.Join(tmp, "askpass.sh")
	// $1 contains git's prompt text; "Username" → user, else → token.
	body := "#!/bin/sh\ncase \"$1\" in *Username*) printf '%s' \"$MARSHAL_GIT_USER\";; *) printf '%s' \"$MARSHAL_GIT_TOKEN\";; esac\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, func() {}, err
	}
	env = []string{
		"GIT_ASKPASS=" + script,
		"MARSHAL_GIT_USER=" + cred.Username,
		"MARSHAL_GIT_TOKEN=" + cred.Token,
		"GIT_TERMINAL_PROMPT=0",
	}
	return env, func() { _ = os.RemoveAll(tmp) }, nil
}

// withUsername injects a (non-secret) username into an https URL's authority.
func withUsername(raw, user string) string {
	if user == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.User = url.User(user)
	return u.String()
}

// gitArgs prepends an inline "disable credential helpers" config when a
// managed credential is active, so the GIT_ASKPASS token is authoritative and
// no inherited helper (osxkeychain/libsecret/store) caches or replays it.
func gitArgs(credActive bool, args ...string) []string {
	if credActive {
		return append([]string{"-c", "credential.helper="}, args...)
	}
	return args
}

func summarize(stage string, err error) string { return stage + " failed: " + err.Error() }

// wait blocks until all in-flight deploy goroutines finish. Tests only.
func (d *Deployer) wait() { d.wg.Wait() }
