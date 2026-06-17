package logs

import (
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry owns one Sink per instance label, keyed by "name#idx".
type Registry struct {
	dir string
	now func() time.Time

	mu       sync.Mutex
	sinks    map[string]*Sink
	def      Policy
	policies map[string]Policy // by app name (label without #idx)
}

// Labeled pairs a sink with its instance label.
type Labeled struct {
	Label string
	Sink  *Sink
}

// NewRegistry builds a registry writing rotated files under dir.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir, now: time.Now, sinks: map[string]*Sink{}, def: DefaultPolicy, policies: map[string]Policy{}}
}

// SetDefaultPolicy sets the fallback policy for apps without an override.
func (r *Registry) SetDefaultPolicy(p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.def = p
}

// SetPolicy registers a per-app retention policy, keyed by app name.
func (r *Registry) SetPolicy(app string, p Policy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies[app] = p
}

func (r *Registry) policyFor(label string) Policy {
	name := label
	if i := strings.LastIndexByte(label, '#'); i >= 0 {
		name = label[:i]
	}
	if p, ok := r.policies[name]; ok {
		return p
	}
	return r.def
}

// For returns the sink for label, creating it on first use (and reusing it
// across instance restarts so files and history persist).
func (r *Registry) For(label string) *Sink {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sinks[label]
	if !ok {
		s = newSinkP(r.dir, label, r.policyFor(label), r.now)
		r.sinks[label] = s
	}
	return s
}

// WriterPair returns the stdout/stderr writers for label. Satisfies the
// manager.LogProvider interface.
func (r *Registry) WriterPair(label string) (io.Writer, io.Writer) {
	s := r.For(label)
	return s.Writer(false), s.Writer(true)
}

// Remove closes and drops the sink for label (on delete).
func (r *Registry) Remove(label string) {
	r.mu.Lock()
	s, ok := r.sinks[label]
	delete(r.sinks, label)
	r.mu.Unlock()
	if ok {
		_ = s.Close()
	}
}

// ResolveLabeled returns the existing sinks for labels, skipping unknown ones,
// preserving the input order.
func (r *Registry) ResolveLabeled(labels []string) []Labeled {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Labeled, 0, len(labels))
	for _, l := range labels {
		if s, ok := r.sinks[l]; ok {
			out = append(out, Labeled{Label: l, Sink: s})
		}
	}
	return out
}

// LabeledLine is one ring line tagged with its instance label.
type LabeledLine struct {
	Label  string
	Ts     time.Time
	Stderr bool
	Text   string
}

// RingSince returns every current sink's in-memory ring lines with a timestamp
// strictly newer than sinceMs, merged ascending by timestamp. New sinks created
// after a prior call are naturally included (the sink map is read fresh).
func (r *Registry) RingSince(sinceMs int64) []LabeledLine {
	r.mu.Lock()
	type entry struct {
		label string
		sink  *Sink
	}
	snap := make([]entry, 0, len(r.sinks))
	for l, s := range r.sinks {
		snap = append(snap, entry{l, s})
	}
	r.mu.Unlock()

	var out []LabeledLine
	for _, e := range snap {
		for _, ln := range e.sink.Backfill(0) { // whole ring
			if ln.Ts.UnixMilli() > sinceMs {
				out = append(out, LabeledLine{Label: e.label, Ts: ln.Ts, Stderr: ln.Stderr, Text: ln.Text})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out
}
