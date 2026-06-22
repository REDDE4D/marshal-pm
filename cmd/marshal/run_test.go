package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe buffer so the test can poll captured output
// while os/exec's output copier writes to it concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestRunSupervisesAndStops builds the marshal binary, runs a config with a
// short-lived app, waits for the app to actually start, sends SIGINT, and
// asserts a clean shutdown.
func TestRunSupervisesAndStops(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "marshal")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}

	cfgPath := filepath.Join(dir, "marshal.yaml")
	// The supervised command is a shell that spawns a child (sleep). Because the
	// supervisor signals the whole process group, SIGTERM reaches the sleep too,
	// so shutdown is prompt and no grandchild is orphaned holding the stdout pipe.
	cfg := `
apps:
  - name: hello
    cmd: sh
    args: ["-c", "echo started; sleep 30"]
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	out := &syncBuffer{}
	cmd := exec.Command(bin, "run", cfgPath)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait until the app has actually started before interrupting, so the test
	// does not race process startup (which can lag under load). The deadlines
	// here and below are deliberately generous: the test returns as soon as its
	// condition is met, so a high ceiling costs nothing on a fast machine but
	// keeps the test from flaking on loaded CI runners (notably under -race).
	waitFor(t, 15*time.Second, func() bool { return strings.Contains(out.String(), "started") },
		func() { _ = cmd.Process.Kill() }, "app did not print 'started' in time", out)

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("marshal run did not exit after SIGINT; output:\n%s", out.String())
	}

	if !strings.Contains(out.String(), "all processes stopped") {
		t.Fatalf("expected clean-shutdown message, got:\n%s", out.String())
	}
}

// waitFor polls cond until true or the timeout elapses, failing the test on timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, onFail func(), msg string, out *syncBuffer) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			onFail()
			t.Fatalf("%s; output:\n%s", msg, out.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
