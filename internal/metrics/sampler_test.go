package metrics

import (
	"os/exec"
	"testing"
	"time"
)

// startGroup launches a shell that backgrounds a child and waits, so the
// process group has a parent + at least one child.
func startGroup(t *testing.T) (pid int, stop func()) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 5 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Give the shell a moment to fork the child.
	time.Sleep(200 * time.Millisecond)
	return cmd.Process.Pid, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }
}

func TestGroupPidsIncludesChildren(t *testing.T) {
	pid, stop := startGroup(t)
	defer stop()
	pids := groupPids(int32(pid))
	if len(pids) < 2 {
		t.Fatalf("groupPids(%d) = %v, want >= 2 (parent + child)", pid, pids)
	}
}

func TestSamplerRecordsMemAndPrunesDeadHandles(t *testing.T) {
	pid, stop := startGroup(t)
	s := NewSampler(time.Hour) // manual sampling
	s.sample([]Instance{{Label: "a#0", Pid: pid, Online: true}})

	got, ok := s.Get("a#0")
	if !ok || got.Mem == 0 {
		t.Fatalf("Get(a#0) = %+v ok=%v, want non-zero Mem", got, ok)
	}

	stop()
	s.sample(nil) // no live instances → handles pruned
	s.mu.Lock()
	n := len(s.procs)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("procs handles = %d after pruning, want 0", n)
	}
}

func TestSamplerSkipsOfflineInstances(t *testing.T) {
	s := NewSampler(time.Hour)
	s.sample([]Instance{{Label: "a#0", Pid: 99999999, Online: false}})
	if _, ok := s.Get("a#0"); ok {
		t.Fatal("offline instance should not be sampled")
	}
}

func TestSetOnTickFiresWithLabeledSamples(t *testing.T) {
	s := NewSampler(time.Hour)
	var got map[string]Sample
	s.SetOnTick(func(m map[string]Sample) { got = m })
	s.sample([]Instance{{Label: "a#0", Pid: 99999999, Online: true}})
	if got == nil {
		t.Fatal("onTick was not invoked")
	}
	if _, ok := got["a#0"]; !ok {
		t.Fatalf("onTick map = %+v, want an entry for a#0", got)
	}
}

func TestSamplerRecordsThreadsAndFds(t *testing.T) {
	pid, stop := startGroup(t)
	defer stop()
	s := NewSampler(time.Hour)
	s.sample([]Instance{{Label: "a#0", Pid: pid, Online: true}})

	got, ok := s.Get("a#0")
	if !ok {
		t.Fatal("no sample for a#0")
	}
	if got.Threads < 1 {
		t.Fatalf("Threads = %d, want >= 1", got.Threads)
	}
	// Fds is -1 (unavailable, e.g. darwin) or a real positive count — never a
	// misleading 0 for a live process group.
	if got.Fds == 0 {
		t.Fatalf("Fds = 0, want -1 (unavailable) or > 0")
	}
}
