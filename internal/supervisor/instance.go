package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/proc"
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
	State      State
	Pid        int
	Restarts   int
	StartedAt  time.Time
	ExitCode   int32  // last exit code; -1 if signaled or spawn failure
	ExitReason string // e.g. "exit status 1" / "signal: killed"; "" = never exited
}

// Instance supervises a single OS process, restarting it per Policy.
type Instance struct {
	spec   proc.Spec
	policy Policy

	mu         sync.Mutex
	state      State
	pid        int
	restarts   int // total restarts
	unstable   int // consecutive sub-MinUptime restarts
	startedAt  time.Time
	exitCode   int32  // last observed exit code
	exitReason string // last observed exit reason ("" until first exit)
	onRestart  func() // M-E: fired once per genuine restart (nil if unset)
}

// Option configures an Instance.
type Option func(*Instance)

// WithOnRestart registers a hook fired once per genuine restart (not on a clean
// stop, operator stop, or no-restart path).
func WithOnRestart(fn func()) Option { return func(i *Instance) { i.onRestart = fn } }

// NewInstance builds an instance supervisor. Call Run to start it.
func NewInstance(spec proc.Spec, policy Policy, opts ...Option) *Instance {
	i := &Instance{spec: spec, policy: policy, state: StateStarting}
	for _, o := range opts {
		o(i)
	}
	return i
}

// Snapshot returns the current observable state.
func (i *Instance) Snapshot() Snapshot {
	i.mu.Lock()
	defer i.mu.Unlock()
	return Snapshot{
		State: i.state, Pid: i.pid, Restarts: i.restarts, StartedAt: i.startedAt,
		ExitCode: i.exitCode, ExitReason: i.exitReason,
	}
}

// ResetCounters zeroes the lifetime and crash-loop restart counters. It does not
// change process state or restart anything; for a running instance it restores
// the crash-loop headroom before MaxRestarts is reached again.
func (i *Instance) ResetCounters() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.restarts = 0
	i.unstable = 0
}

// set updates observable state under the lock. A pid < 0 leaves the stored pid
// unchanged; pass the real pid when going Online, or 0 to clear it when the
// process is no longer running. A zero startedAt likewise leaves it unchanged.
func (i *Instance) set(state State, pid int, startedAt time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.state = state
	if pid >= 0 {
		i.pid = pid
	}
	if !startedAt.IsZero() {
		i.startedAt = startedAt
	}
}

// Run supervises the process until ctx is canceled, then gracefully stops it.
func (i *Instance) Run(ctx context.Context) {
	for {
		started := time.Now()
		p, err := proc.Start(i.spec)
		if err != nil {
			// Spawn failure: treat like an immediate crash for restart accounting.
			if !i.handleExit(ctx, started, err) {
				return
			}
			continue
		}
		i.set(StateOnline, p.Pid(), started)

		exited := make(chan error, 1)
		go func() { exited <- p.Wait() }()

		select {
		case <-ctx.Done():
			i.stop(p, exited)
			i.set(StateStopped, 0, time.Time{})
			return
		case waitErr := <-exited:
			if !i.handleExit(ctx, started, waitErr) {
				return
			}
		}
	}
}

// deriveExit maps a Wait/Start error to a numeric code and human reason.
// nil -> clean exit 0; *exec.ExitError -> its code (-1 if signaled) and string;
// any other error (e.g. spawn failure) -> -1 and the error text.
func deriveExit(waitErr error) (int32, string) {
	if waitErr == nil {
		return 0, "exit status 0"
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) {
		return int32(ee.ExitCode()), ee.String()
	}
	return -1, waitErr.Error()
}

// recordExit stores the most recent exit under the lock. Persists across
// restarts; overwritten only by the next exit.
func (i *Instance) recordExit(waitErr error) {
	code, reason := deriveExit(waitErr)
	i.mu.Lock()
	i.exitCode = code
	i.exitReason = reason
	i.mu.Unlock()
}

// handleExit runs after a process terminates (or fails to spawn) and decides whether to restart. It returns false to terminate Run.
func (i *Instance) handleExit(ctx context.Context, started time.Time, waitErr error) bool {
	i.recordExit(waitErr)
	failed := waitErr != nil
	if ctx.Err() != nil {
		i.set(StateStopped, 0, time.Time{})
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
			i.set(StateErrored, 0, time.Time{})
		} else {
			i.set(StateStopped, 0, time.Time{})
		}
		return false
	}

	// Stability accounting.
	uptime := time.Since(started)
	i.mu.Lock()
	// restarts is a monotonic total; unstable counts only consecutive sub-MinUptime cycles.
	i.restarts++
	if uptime < i.policy.MinUptime {
		i.unstable++
	} else {
		i.unstable = 0
	}
	unstable := i.unstable
	i.mu.Unlock()

	if i.onRestart != nil {
		i.onRestart() // M-E: count this restart
	}

	if unstable > i.policy.MaxRestarts {
		i.set(StateErrored, 0, time.Time{})
		return false
	}

	i.set(StateRestarting, 0, time.Time{})
	delay := Backoff(unstable, i.policy.BaseBackoff, i.policy.MaxBackoff)
	select {
	case <-ctx.Done():
		i.set(StateStopped, 0, time.Time{})
		return false
	case <-time.After(delay):
		return true
	}
}

// stop sends SIGTERM, then SIGKILL after KillTimeout. It waits on the existing
// exited channel (fed by Run's single p.Wait goroutine) rather than calling
// p.Wait again — os/exec.Wait is not safe to call twice.
func (i *Instance) stop(p *proc.Process, exited <-chan error) {
	i.set(StateStopping, 0, time.Time{})
	_ = p.Signal(syscall.SIGTERM)
	select {
	case waitErr := <-exited:
		i.recordExit(waitErr)
	case <-time.After(i.policy.KillTimeout):
		_ = p.Kill()
		i.recordExit(<-exited)
	}
}
