// Package manager runs a whole marshal.yaml: each app fanned into N instances.
package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"marshal/internal/config"
	"marshal/internal/proc"
	"marshal/internal/supervisor"
)

// entry pairs an instance with its display label.
type entry struct {
	label    string
	instance *supervisor.Instance
}

// InstanceSnapshot is a labeled view of one supervised instance.
type InstanceSnapshot struct {
	Label string
	supervisor.Snapshot
}

// Manager supervises every instance of every app in a config.
type Manager struct {
	entries []entry
}

// New builds a Manager from a validated config.
func New(cfg *config.Config) *Manager {
	var entries []entry
	for _, app := range cfg.Apps {
		policy := supervisor.Policy{
			Mode:        app.Restart,
			MinUptime:   time.Second,
			MaxRestarts: app.MaxRestarts,
			BaseBackoff: 100 * time.Millisecond,
			MaxBackoff:  15 * time.Second,
			KillTimeout: app.KillTimeout.Duration,
		}
		for idx := 0; idx < app.Instances; idx++ {
			spec := proc.Spec{
				Cmd:        app.Cmd,
				Args:       app.Args,
				Cwd:        app.Cwd,
				Env:        app.Env,
				InstanceID: idx,
			}
			entries = append(entries, entry{
				label:    fmt.Sprintf("%s#%d", app.Name, idx),
				instance: supervisor.NewInstance(spec, policy),
			})
		}
	}
	return &Manager{entries: entries}
}

// Run starts all instances and blocks until ctx is canceled and all stop.
func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, e := range m.entries {
		wg.Add(1)
		go func(in *supervisor.Instance) {
			defer wg.Done()
			in.Run(ctx)
		}(e.instance)
	}
	wg.Wait()
}

// Snapshot returns a labeled snapshot of every instance.
func (m *Manager) Snapshot() []InstanceSnapshot {
	out := make([]InstanceSnapshot, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, InstanceSnapshot{Label: e.label, Snapshot: e.instance.Snapshot()})
	}
	return out
}
