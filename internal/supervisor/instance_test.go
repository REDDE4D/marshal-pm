package supervisor

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"marshal/internal/config"
	"marshal/internal/proc"
)

func testPolicy(mode config.RestartMode) Policy {
	return Policy{
		Mode:        mode,
		MinUptime:   500 * time.Millisecond,
		MaxRestarts: 3,
		BaseBackoff: 10 * time.Millisecond,
		MaxBackoff:  50 * time.Millisecond,
		KillTimeout: time.Second,
	}
}

// runInstance runs i.Run in a goroutine and returns a cancel + wait closure.
func runInstance(i *Instance) (cancel func(), wait func()) {
	ctx, c := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); i.Run(ctx) }()
	return c, wg.Wait
}

func TestInstanceOnlineThenStop(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "sleep 30"}}, testPolicy(config.RestartAlways))
	cancel, wait := runInstance(i)
	time.Sleep(200 * time.Millisecond)
	if got := i.Snapshot().State; got != StateOnline {
		t.Fatalf("state = %q, want online", got)
	}
	cancel()
	wait()
	if got := i.Snapshot().State; got != StateStopped {
		t.Fatalf("state after cancel = %q, want stopped", got)
	}
	// Operator stop is recorded as an exit too: stop() sends SIGTERM and
	// recordExit captures it. Guard that path so a dropped recordExit in
	// stop() would fail the suite.
	if got := i.Snapshot().ExitReason; got == "" {
		t.Fatalf("ExitReason after operator stop = %q, want non-empty (e.g. \"signal: terminated\")", got)
	}
}

func TestInstanceOnFailureDoesNotRestartOnCleanExit(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 0"}}, testPolicy(config.RestartOnFailure))
	_, wait := runInstance(i)
	wait() // returns once the instance stops itself
	if got := i.Snapshot().Restarts; got != 0 {
		t.Fatalf("restarts = %d, want 0 (clean exit, on-failure)", got)
	}
	if got := i.Snapshot().State; got != StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
}

func TestInstanceErroredAfterMaxRestarts(t *testing.T) {
	// Fast-crashing process: exits immediately, always restarts, hits the cap.
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 1"}}, testPolicy(config.RestartAlways))
	_, wait := runInstance(i)
	wait()
	if got := i.Snapshot().State; got != StateErrored {
		t.Fatalf("state = %q, want errored", got)
	}
}

func TestInstanceRestartNoStopsOnFailure(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 1"}}, testPolicy(config.RestartNo))
	_, wait := runInstance(i)
	wait()
	if got := i.Snapshot().Restarts; got != 0 {
		t.Fatalf("restarts = %d, want 0 (restart: no)", got)
	}
	if got := i.Snapshot().State; got != StateErrored {
		t.Fatalf("state = %q, want errored (restart: no + failed exit)", got)
	}
}

func TestInstanceGracefulStopFallsBackToKill(t *testing.T) {
	// Ignores SIGTERM, so the KillTimeout path must SIGKILL it.
	i := NewInstance(
		proc.Spec{Cmd: "sh", Args: []string{"-c", "trap '' TERM; while true; do sleep 0.1; done"}},
		testPolicy(config.RestartAlways),
	)
	cancel, wait := runInstance(i)
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	cancel()
	wait()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("graceful stop took %v, expected kill near KillTimeout (1s)", elapsed)
	}
	if got := i.Snapshot().State; got != StateStopped {
		t.Fatalf("state = %q, want stopped", got)
	}
}

func TestDeriveExit(t *testing.T) {
	if c, r := deriveExit(nil); c != 0 || r != "exit status 0" {
		t.Fatalf("deriveExit(nil) = (%d, %q), want (0, \"exit status 0\")", c, r)
	}
	// Real non-zero exit yields *exec.ExitError.
	err := exec.Command("sh", "-c", "exit 3").Run()
	if c, r := deriveExit(err); c != 3 || r == "" {
		t.Fatalf("deriveExit(exit 3) = (%d, %q), want (3, non-empty)", c, r)
	}
	// Generic (non-ExitError) error, e.g. spawn failure.
	if c, r := deriveExit(errors.New("boom")); c != -1 || r != "boom" {
		t.Fatalf("deriveExit(boom) = (%d, %q), want (-1, \"boom\")", c, r)
	}
}

func TestInstanceRecordsExitCode(t *testing.T) {
	// Never exited yet -> blank reason.
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 7"}}, testPolicy(config.RestartNo))
	if got := i.Snapshot().ExitReason; got != "" {
		t.Fatalf("ExitReason before run = %q, want empty", got)
	}
	_, wait := runInstance(i)
	wait() // RestartNo + failure -> instance stops itself (errored)
	snap := i.Snapshot()
	if snap.ExitCode != 7 || snap.ExitReason != "exit status 7" {
		t.Fatalf("after exit 7: code=%d reason=%q, want 7 / \"exit status 7\"", snap.ExitCode, snap.ExitReason)
	}
}

func TestInstanceRecordsCleanExit(t *testing.T) {
	i := NewInstance(proc.Spec{Cmd: "sh", Args: []string{"-c", "exit 0"}}, testPolicy(config.RestartNo))
	_, wait := runInstance(i)
	wait()
	snap := i.Snapshot()
	if snap.ExitCode != 0 || snap.ExitReason != "exit status 0" {
		t.Fatalf("after clean exit: code=%d reason=%q, want 0 / \"exit status 0\"", snap.ExitCode, snap.ExitReason)
	}
}
