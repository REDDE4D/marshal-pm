package server

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"marshal/internal/metricstore"
)

// stores manages lazily-opened per-agent metric stores under a data dir.
type stores struct {
	dir string
	mu  sync.Mutex
	m   map[string]*metricstore.Store
}

func newStores(dir string) *stores {
	return &stores{dir: dir, m: map[string]*metricstore.Store{}}
}

func (s *stores) agentDir(agent string) string {
	return filepath.Join(s.dir, "agents", sanitizeAgent(agent))
}

// get returns the agent's store, opening (and creating its directory) on first use.
func (s *stores) get(agent string) (*metricstore.Store, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := sanitizeAgent(agent)
	if st, ok := s.m[key]; ok {
		return st, nil
	}
	dir := s.agentDir(agent)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	st, err := metricstore.Open(filepath.Join(dir, "metrics.db"))
	if err != nil {
		return nil, err
	}
	s.m[key] = st
	return st, nil
}

// has reports whether the agent's store directory exists on disk.
func (s *stores) has(agent string) bool {
	if _, err := os.Stat(s.agentDir(agent)); err == nil {
		return true
	}
	return false
}

func (s *stores) closeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, st := range s.m {
		if err := st.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// pruneAll deletes samples older than beforeMs from every open store.
func (s *stores) pruneAll(beforeMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		_, _ = st.Prune(beforeMs)
	}
}

// sanitizeAgent turns an agent name into a safe single path segment.
// Replaces '/', '\', and '.' with '_'. Empty input returns "_".
func sanitizeAgent(name string) string {
	if name == "" {
		return "_"
	}
	r := strings.NewReplacer("/", "_", "\\", "_", ".", "_")
	return r.Replace(name)
}
