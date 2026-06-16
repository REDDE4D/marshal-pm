package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"marshal/internal/config"
	"marshal/internal/supervisor"
)

func TestManagerRunsAllInstances(t *testing.T) {
	cfg := &config.Config{Apps: []config.App{
		{Name: "a", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 2,
			Restart: config.RestartAlways, MaxRestarts: 3,
			KillTimeout: config.Duration{Duration: time.Second}},
		{Name: "b", Cmd: "sh", Args: []string{"-c", "sleep 30"}, Instances: 1,
			Restart: config.RestartAlways, MaxRestarts: 3,
			KillTimeout: config.Duration{Duration: time.Second}},
	}}

	m := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); m.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)

	snaps := m.Snapshot()
	if len(snaps) != 3 {
		t.Fatalf("got %d instances, want 3", len(snaps))
	}
	online := 0
	for _, s := range snaps {
		if s.State == supervisor.StateOnline {
			online++
		}
	}
	if online != 3 {
		t.Fatalf("online = %d, want 3", online)
	}
	cancel()
	wg.Wait()
}
