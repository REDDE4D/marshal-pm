package logs

import (
	"io"
	"sync"
	"time"
)

// Registry owns one Sink per instance label, keyed by "name#idx".
type Registry struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	sinks map[string]*Sink
}

// Labeled pairs a sink with its instance label.
type Labeled struct {
	Label string
	Sink  *Sink
}

// NewRegistry builds a registry writing rotated files under dir.
func NewRegistry(dir string) *Registry {
	return &Registry{dir: dir, now: time.Now, sinks: map[string]*Sink{}}
}

// For returns the sink for label, creating it on first use (and reusing it
// across instance restarts so files and history persist).
func (r *Registry) For(label string) *Sink {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sinks[label]
	if !ok {
		s = newSink(r.dir, label, r.now)
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
