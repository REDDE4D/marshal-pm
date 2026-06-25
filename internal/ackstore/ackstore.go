// Package ackstore persists which error signatures have been acknowledged in the
// dashboard, keyed by the stable errsig signature id. An acknowledgement records
// the time it was made; a signature counts as acknowledged only until it recurs
// after that time (the dashboard re-surfaces it on a fresh occurrence).
package ackstore

import (
	"encoding/json"
	"os"
	"sync"
)

// Store maps signature id → acknowledged-at (unix ms). Safe for concurrent use.
type Store struct {
	path  string
	mu    sync.Mutex
	acked map[string]int64
}

// Open loads the ack store from path (a missing file yields an empty store).
func Open(path string) (*Store, error) {
	s := &Store{path: path, acked: map[string]int64{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.acked); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Ack records that id was acknowledged at atMs and persists.
func (s *Store) Ack(id string, atMs int64) error {
	s.mu.Lock()
	s.acked[id] = atMs
	s.mu.Unlock()
	return s.save()
}

// Unack clears any acknowledgement for id and persists.
func (s *Store) Unack(id string) error {
	s.mu.Lock()
	delete(s.acked, id)
	s.mu.Unlock()
	return s.save()
}

// AckedAt returns the acknowledgement time for id, if any.
func (s *Store) AckedAt(id string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.acked[id]
	return at, ok
}

// Snapshot returns a copy of all acknowledgements.
func (s *Store) Snapshot() map[string]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int64, len(s.acked))
	for k, v := range s.acked {
		out[k] = v
	}
	return out
}

// Prune drops acknowledgements older than beforeMs (their signatures have aged
// out of retention). Persists if anything changed.
func (s *Store) Prune(beforeMs int64) {
	s.mu.Lock()
	changed := false
	for id, at := range s.acked {
		if at < beforeMs {
			delete(s.acked, id)
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		_ = s.save()
	}
}

func (s *Store) save() error {
	s.mu.Lock()
	data, err := json.Marshal(s.acked)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
