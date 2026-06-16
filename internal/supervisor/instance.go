package supervisor

import (
	"context"
	"sync"
	"syscall"
	"time"

	"marshal/internal/config"
	"marshal/internal/proc"
)

// Policy is the resolved restart policy for one instance.
type Policy struct {
	Mode        config.RestartMode
	MinUptime   time.Duration
	MaxRestarts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	KillTimeout time.Duration
}

// Snapshot is a point-in-time view of an instance.
type Snapshot struct {
	State     State
	Pid       int
	Restarts  int
	StartedAt time.Time
}

// Instance supervises a single OS process, restarting it per Policy.
type Instance struct {
	spec   proc.Spec
	policy Policy

	mu        sync.Mutex
	state     State
	pid       int
	restarts  int // total restarts
	unstable  int // consecutive sub-MinUptime restarts
	startedAt time.Time
}

// NewInstance builds an instance supervisor. Call Run to start it.
func NewInstance(spec proc.Spec, policy Policy) *Instance {
	return &Instance{spec: spec, policy: policy, state: StateStarting}
}

// Snapshot returns the current observable state.
func (i *Instance) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	return Snapshot{State: i.state, Pid: i.pid, Restarts: i.restarts, StartedAt: i.startedAt}
}

func (i *Instance) set(state State, pid int, startedAt time.Time) {
	i.mu.Lock()
	i.state = state
	if pid >= 0 {
		i.pid = pid
	}
	if !startedAt.IsZero() {
		i.startedAt = startedAt
	}
	i.mu.Unlock()
}

// Run supervises the process until ctx is canceled, then gracefully stops it.
func (i *Instance) Run(ctx context.Context) {
	for {
		p, err := proc.Start(i.spec)
		if err != nil {
			// Treat spawn failure like a crash for restart accounting.
			if !i.handleExit(ctx, time.Now(), true) {
				return
			}
			continue
		}
		started := time.Now()
		i.set(StateOnline, p.Pid(), started)

		exited := make(chan error, 1)
		go func() { exited <- p.Wait() }()

		select {
		case <-ctx.Done():
			i.stop(p, exited)
			i.set(StateStopped, -1, time.Time{})
			return
		case waitErr := <-exited:
			failed := waitErr != nil
			if !i.handleExit(ctx, started, failed) {
				return
			}
		}
	}
}

// handleExit decides whether to restart. Returns false to terminate Run.
func (i *Instance) handleExit(ctx context.Context, started time.Time, failed bool) bool {
	if ctx.Err() != nil {
		i.set(StateStopped, -1, time.Time{})
		return false
	}

	// Should we restart at all?
	restart := false
	switch i.policy.Mode {
	case config.RestartAlways:
		restart = true
	case config.RestartOnFailure:
		restart = failed
	case config.RestartNo:
		restart = false
	}
	if !restart {
		if failed {
			i.set(StateErrored, -1, time.Time{})
		} else {
			i.set(StateStopped, -1, time.Time{})
		}
		return false
	}

	// Stability accounting.
	uptime := time.Since(started)
	i.mu.Lock()
	i.restarts++
	if uptime < i.policy.MinUptime {
		i.unstable++
	} else {
		i.unstable = 0
	}
	unstable := i.unstable
	i.mu.Unlock()

	if unstable > i.policy.MaxRestarts {
		i.set(StateErrored, -1, time.Time{})
		return false
	}

	i.set(StateRestarting, -1, time.Time{})
	delay := Backoff(unstable, i.policy.BaseBackoff, i.policy.MaxBackoff)
	select {
	case <-ctx.Done():
		i.set(StateStopped, -1, time.Time{})
		return false
	case <-time.After(delay):
		return true
	}
}

// stop sends SIGTERM, then SIGKILL after KillTimeout. It waits on the existing
// exited channel (fed by Run's single p.Wait goroutine) rather than calling
// p.Wait again — os/exec.Wait is not safe to call twice.
func (i *Instance) stop(p *proc.Process, exited <-chan error) {
	i.set(StateStopping, -1, time.Time{})
	_ = p.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(i.policy.KillTimeout):
		_ = p.Kill()
		<-exited
	}
}
