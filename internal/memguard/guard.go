// Package memguard restarts an app when its sampled RSS exceeds a configured
// limit for a sustained number of consecutive metric samples (debounced).
package memguard

import (
	"strings"
	"sync"

	"github.com/REDDE4D/marshal-pm/internal/metrics"
)

// defaultThreshold is the number of consecutive over-limit samples required
// before a restart fires (~10-15s at the daemon's default 5s tick).
const defaultThreshold = 3

// Guard tracks per-app memory limits and per-instance breach streaks.
type Guard struct {
	mu        sync.Mutex
	limits    map[string]uint64 // by app name; absent = no limit
	breach    map[string]int    // by instance label ("name#idx")
	threshold int
	restart   func(name string)
	logf      func(string, ...any)
}

// New builds a Guard. restart is called with an app name when it should be
// restarted; logf (may be nil) records the reason.
func New(restart func(name string), logf func(string, ...any)) *Guard {
	return &Guard{
		limits:    map[string]uint64{},
		breach:    map[string]int{},
		threshold: defaultThreshold,
		restart:   restart,
		logf:      logf,
	}
}

// SetLimit sets (or, when bytes==0, removes) an app's memory limit.
func (g *Guard) SetLimit(app string, bytes uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if bytes == 0 {
		delete(g.limits, app)
		return
	}
	g.limits[app] = bytes
}

// Remove drops an app's limit and any pending breach state (on delete).
func (g *Guard) Remove(app string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.limits, app)
	for label := range g.breach {
		if appName(label) == app {
			delete(g.breach, label)
		}
	}
}

// Check evaluates one tick's samples and fires restarts for apps that have
// exceeded their limit for `threshold` consecutive ticks. At most one restart
// per app per Check.
func (g *Guard) Check(samples map[string]metrics.Sample) {
	type fire struct {
		name, label string
		mem, limit  uint64
	}
	var fires []fire

	g.mu.Lock()
	fired := map[string]bool{}
	for label, sm := range samples {
		name := appName(label)
		limit, ok := g.limits[name]
		if !ok {
			continue
		}
		if sm.Mem <= limit {
			delete(g.breach, label)
			continue
		}
		g.breach[label]++
		if g.breach[label] < g.threshold || fired[name] {
			continue
		}
		fired[name] = true
		fires = append(fires, fire{name, label, sm.Mem, limit})
		for l := range g.breach {
			if appName(l) == name {
				delete(g.breach, l)
			}
		}
	}
	g.mu.Unlock()

	for _, f := range fires {
		if g.logf != nil {
			g.logf("memguard: %s rss %d exceeded limit %d for %d samples; restarting %s",
				f.label, f.mem, f.limit, g.threshold, f.name)
		}
		if g.restart != nil {
			g.restart(f.name)
		}
	}
}

func appName(label string) string {
	if i := strings.LastIndexByte(label, '#'); i >= 0 {
		return label[:i]
	}
	return label
}
