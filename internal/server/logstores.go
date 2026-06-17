package server

import (
	"os"
	"path/filepath"
	"sync"

	"marshal/internal/logstore"
)

// logStores manages lazily-opened per-agent log stores under a data dir.
type logStores struct {
	dir string
	mu  sync.Mutex
	m   map[string]*logstore.Store
}

func newLogStores(dir string) *logStores {
	return &logStores{dir: dir, m: map[string]*logstore.Store{}}
}

func (s *logStores) agentDir(agent string) string {
	return filepath.Join(s.dir, "agents", sanitizeAgent(agent))
}

// get returns the agent's store, opening (and creating its directory) on first use.
func (s *logStores) get(agent string) (*logstore.Store, error) {
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
	st, err := logstore.Open(filepath.Join(dir, "logs.db"))
	if err != nil {
		return nil, err
	}
	s.m[key] = st
	return st, nil
}

// has reports whether the agent's store directory exists on disk.
func (s *logStores) has(agent string) bool {
	if _, err := os.Stat(s.agentDir(agent)); err == nil {
		return true
	}
	return false
}

func (s *logStores) closeAll() error {
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

// pruneAll deletes lines older than beforeMs from every open store.
func (s *logStores) pruneAll(beforeMs int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.m {
		_, _ = st.Prune(beforeMs)
	}
}
