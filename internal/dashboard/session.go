package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

type session struct {
	User   string    `json:"user"`
	Stamp  string    `json:"stamp"`
	Expiry time.Time `json:"expiry"`
}

// sessionStore maps a session-token *hash* to a session. When path is non-empty
// the map is persisted to that file (atomic write, 0600) and reloaded on
// construction, so sessions survive a server restart. An empty path keeps the
// store purely in-memory.
type sessionStore struct {
	ttl  time.Duration
	now  func() time.Time
	path string
	mu   sync.Mutex
	m    map[string]session
}

// hashSessionToken returns the hex SHA-256 of a session token. The plaintext
// token lives only in the user's cookie; memory and disk hold only the hash.
func hashSessionToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// newSessionStore returns a store with the given session lifetime. If now is
// nil, time.Now is used. If path is non-empty, persisted sessions are loaded
// from it (expired entries dropped) and every mutation is written back.
func newSessionStore(ttl time.Duration, now func() time.Time, path string) *sessionStore {
	if now == nil {
		now = time.Now
	}
	s := &sessionStore{ttl: ttl, now: now, path: path, m: map[string]session{}}
	if path != "" {
		s.load()
	}
	return s
}

// create mints a random 256-bit session token for user, recording the
// credential stamp under which it was minted, and returns the plaintext token;
// the store keeps only its hash.
func (s *sessionStore) create(user, stamp string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.m[hashSessionToken(tok)] = session{User: user, Stamp: stamp, Expiry: s.now().Add(s.ttl)}
	s.persistLocked()
	s.mu.Unlock()
	return tok, nil
}

// validate returns the user and credential stamp for a live token, or ok=false
// if the token is unknown or expired (expired tokens are removed). The caller
// compares the stamp against the user's current credential.
func (s *sessionStore) validate(tok string) (string, string, bool) {
	h := hashSessionToken(tok)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[h]
	if !ok {
		return "", "", false
	}
	if !s.now().Before(sess.Expiry) {
		delete(s.m, h)
		s.persistLocked()
		return "", "", false
	}
	return sess.User, sess.Stamp, true
}

// delete removes a token (logout).
func (s *sessionStore) delete(tok string) {
	s.mu.Lock()
	delete(s.m, hashSessionToken(tok))
	s.persistLocked()
	s.mu.Unlock()
}

// sweep removes all expired sessions.
func (s *sessionStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	changed := false
	for h, sess := range s.m {
		if !now.Before(sess.Expiry) {
			delete(s.m, h)
			changed = true
		}
	}
	if changed {
		s.persistLocked()
	}
}

// sweepLoop periodically removes expired sessions until ctx is canceled.
func (s *sessionStore) sweepLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep()
		}
	}
}

// persistLocked atomically writes the session map to disk. Caller holds s.mu.
// No-op for an in-memory store (path == "").
func (s *sessionStore) persistLocked() {
	if s.path == "" {
		return
	}
	b, err := json.Marshal(s.m)
	if err != nil {
		log.Printf("dashboard: marshal sessions: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("dashboard: write sessions: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("dashboard: rename sessions: %v", err)
	}
}

// load reads persisted sessions, dropping expired entries. A missing file is
// fine; a corrupt file logs and leaves the store empty.
func (s *sessionStore) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("dashboard: read sessions: %v", err)
		}
		return
	}
	var m map[string]session
	if err := json.Unmarshal(b, &m); err != nil {
		log.Printf("dashboard: parse sessions, starting empty: %v", err)
		return
	}
	now := s.now()
	for h, sess := range m {
		if now.Before(sess.Expiry) {
			s.m[h] = sess
		}
	}
}
