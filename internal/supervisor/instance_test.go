package supervisor

import (
	"context"
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
