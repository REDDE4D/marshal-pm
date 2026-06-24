package manager

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"marshal/internal/config"
	"marshal/internal/supervisor"
)

func sleepApp(name string, instances int) config.App {
	return config.App{
		Name: name, Cmd: "sh", Args: []string{"-c", "sleep 30"},
		Instances: instances, Restart: config.RestartAlways, MaxRestarts: 3,
		KillTimeout: config.Duration{Duration: time.Second},
	}
}

// waitOnline polls until want instances report Online or the deadline passes.
func waitOnline(m *Manager, want int) int {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		online := 0
		for _, s := range m.List() {
			if s.State == supervisor.StateOnline {
				online++
			}
		}
		if online >= want {
			return online
		}
		time.Sleep(20 * time.Millisecond)
	}
	online := 0
	for _, s := range m.List() {
		if s.State == supervisor.StateOnline {
			online++
		}
	}
	return online
}

func TestAddFansIntoInstancesAndAssignsID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)

	snaps, err := m.Add(sleepApp("a", 2))
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d instances, want 2", len(snaps))
	}
	if snaps[0].ID != 1 || snaps[0].Name != "a" {
		t.Fatalf("unexpected id/name: %+v", snaps[0])
	}
	if got := waitOnline(m, 2); got != 2 {
		t.Fatalf("online = %d, want 2", got)
	}
	m.StopAll()
}

func TestAddDuplicateNameRejected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := m.Add(sleepApp("a", 1)); err == nil {
		t.Fatal("second Add: want duplicate-name error")
	}
	m.StopAll()
}

func TestStopThenRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 1)

	if _, err := m.Stop("a"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, s := range m.List() {
		if s.State != supervisor.StateStopped {
			t.Fatalf("after Stop state = %s, want stopped", s.State)
		}
	}

	if _, err := m.Restart("a"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := waitOnline(m, 1); got != 1 {
		t.Fatalf("after Restart online = %d, want 1", got)
	}
	m.StopAll()
}

func TestDeleteRemovesApp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 1)
	if _, err := m.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(m.List()) != 0 {
		t.Fatalf("after Delete List has %d, want 0", len(m.List()))
	}
}

func TestSelectorByIDAndAll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := m.Add(sleepApp("b", 1)); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	waitOnline(m, 2)

	byID, err := m.Describe("2")
	if err != nil {
		t.Fatalf("Describe by id: %v", err)
	}
	if len(byID) != 1 || byID[0].Name != "b" {
		t.Fatalf("id=2 resolved to %+v", byID)
	}

	all, err := m.Describe("all")
	if err != nil {
		t.Fatalf("Describe all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all resolved %d, want 2", len(all))
	}

	if _, err := m.Describe("nope"); err == nil {
		t.Fatal("Describe unknown: want error")
	}
	m.StopAll()
}

func TestDeleteOneOfManyLeavesOthers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := m.Add(sleepApp("b", 1)); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	waitOnline(m, 2)
	if _, err := m.Delete("a"); err != nil {
		t.Fatalf("Delete a: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Name != "b" {
		t.Fatalf("after deleting a, list = %+v, want only b", list)
	}
	if list[0].State != supervisor.StateOnline {
		t.Fatalf("b should still be online, got %s", list[0].State)
	}
	m.StopAll()
}

func TestStopAllSelector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 1)); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := m.Add(sleepApp("b", 2)); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	waitOnline(m, 3)
	if _, err := m.Stop("all"); err != nil {
		t.Fatalf("Stop all: %v", err)
	}
	for _, s := range m.List() {
		if s.State != supervisor.StateStopped {
			t.Fatalf("after Stop all, %s state = %s, want stopped", s.Label, s.State)
		}
	}
}

func TestSpecsReflectsAddedApps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 2)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	specs := m.Specs()
	if len(specs) != 1 || specs[0].Name != "a" || specs[0].Instances != 2 {
		t.Fatalf("Specs = %+v", specs)
	}
	m.StopAll()
}

type fakeLogs struct {
	mu      sync.Mutex
	writers map[string]*safeBuf
	removed []string
}

func newFakeLogs() *fakeLogs { return &fakeLogs{writers: map[string]*safeBuf{}} }

func (f *fakeLogs) WriterPair(label string) (io.Writer, io.Writer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b := &safeBuf{}
	f.writers[label] = b
	return b, b
}

func (f *fakeLogs) Remove(label string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, label)
}

func (f *fakeLogs) bufFor(label string) *safeBuf {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writers[label]
}

func (f *fakeLogs) removedLabels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func echoApp(name string) config.App {
	return config.App{
		Name: name, Cmd: "sh", Args: []string{"-c", "echo captured; sleep 30"},
		Instances: 1, Restart: config.RestartAlways, MaxRestarts: 3,
		KillTimeout: config.Duration{Duration: time.Second},
	}
}

func TestListReportsGitSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	_, err := m.Add(config.App{Name: "g", Cmd: "sleep", Args: []string{"60"}, Instances: 1,
		Source: &config.GitSource{Repo: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.StopAll()
	for _, s := range m.List() {
		if s.Name == "g" && s.Source != "git" {
			t.Fatalf("want source=git, got %q", s.Source)
		}
	}
}

// fakeSink records restart events for assertions.
type fakeSink struct {
	mu     sync.Mutex
	events []string // labels
}

func (f *fakeSink) Record(label string, tsMs int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, label)
	return nil
}
func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestManagerWiresRestartSink(t *testing.T) {
	sink := &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx, WithRestartSink(sink))
	// A crashing app under on-failure restarts a few times, then errors.
	app := config.App{
		Name: "crash", Cmd: "sh", Args: []string{"-c", "exit 1"},
		Instances: 1, Restart: config.RestartOnFailure, MaxRestarts: 2,
		KillTimeout: config.Duration{Duration: time.Second},
	}
	if _, err := m.Add(app); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Wait for the crash-restart cycle to produce at least one recorded restart.
	deadline := time.Now().Add(5 * time.Second)
	for sink.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.count() < 1 {
		t.Fatalf("sink recorded %d restarts, want >= 1", sink.count())
	}
	if sink.events[0] != "crash#0" {
		t.Fatalf("label = %q, want crash#0", sink.events[0])
	}
}

func TestReloadRestartsAllInstances(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 2)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := waitOnline(m, 2); got != 2 {
		t.Fatalf("setup online = %d, want 2", got)
	}

	before := map[string]int{}
	for _, s := range m.List() {
		before[s.Label] = s.Pid
	}

	if _, err := m.Reload("a"); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := waitOnline(m, 2); got != 2 {
		t.Fatalf("after Reload online = %d, want 2", got)
	}
	for _, s := range m.List() {
		if s.State != supervisor.StateOnline {
			t.Fatalf("%s state = %s, want online", s.Label, s.State)
		}
		if s.Pid == before[s.Label] || s.Pid == 0 {
			t.Fatalf("%s pid = %d (before %d); want a fresh non-zero pid", s.Label, s.Pid, before[s.Label])
		}
	}
	m.StopAll()
}

func TestReloadIsRolling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Add(sleepApp("a", 2)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 2)

	minOnline := 2
	m.onReloadStep = func() {
		online := 0
		for _, s := range m.List() {
			if s.State == supervisor.StateOnline {
				online++
			}
		}
		if online < minOnline {
			minOnline = online
		}
	}

	if _, err := m.Reload("a"); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// A rolling reload takes at most one instance of a 2-instance app down at a
	// time, so at every step (one instance stopped, replacement not yet started)
	// at least one instance is still online.
	if minOnline < 1 {
		t.Fatalf("minOnline during reload = %d, want >= 1 (rolling)", minOnline)
	}
	m.StopAll()
}

func TestReloadUnknownSelector(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := New(ctx)
	if _, err := m.Reload("nope"); err == nil {
		t.Fatal("Reload of unknown selector: want error, got nil")
	}
}

func TestWithLogsCapturesOutputAndRemovesOnDelete(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fl := newFakeLogs()
	m := New(ctx, WithLogs(fl))

	if _, err := m.Add(echoApp("a")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	waitOnline(m, 1)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b := fl.bufFor("a#0"); b != nil && strings.Contains(b.String(), "captured") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b := fl.bufFor("a#0"); b == nil || !strings.Contains(b.String(), "captured") {
		t.Fatalf("a#0 output not captured: %v", b)
	}

	if _, err := m.Delete("a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got := fl.removedLabels()
	if len(got) != 1 || got[0] != "a#0" {
		t.Fatalf("removed = %v, want [a#0]", got)
	}
}
