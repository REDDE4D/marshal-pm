package manager

import (
	"context"
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
