package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type session struct {
	user   string
	expiry time.Time
}

// sessionStore is an in-memory token→session map. Sessions are lost on process
// restart by design (v1).
type sessionStore struct {
	ttl time.Duration
	now func() time.Time
	mu  sync.Mutex
	m   map[string]session
}

// newSessionStore returns a store with the given session lifetime. If now is
// nil, time.Now is used.
func newSessionStore(ttl time.Duration, now func() time.Time) *sessionStore {
	if now == nil {
		now = time.Now
	}
	return &sessionStore{ttl: ttl, now: now, m: map[string]session{}}
}

// create mints a random 256-bit session token for user.
func (s *sessionStore) create(user string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	s.m[tok] = session{user: user, expiry: s.now().Add(s.ttl)}
	s.mu.Unlock()
	return tok, nil
}

// validate returns the user for a live token, or ok=false if the token is
// unknown or expired (expired tokens are removed).
func (s *sessionStore) validate(tok string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[tok]
	if !ok {
		return "", false
	}
	if !s.now().Before(sess.expiry) {
		delete(s.m, tok)
		return "", false
	}
	return sess.user, true
}

// delete removes a token (logout).
func (s *sessionStore) delete(tok string) {
	s.mu.Lock()
	delete(s.m, tok)
	s.mu.Unlock()
}

// sweep removes all expired sessions.
func (s *sessionStore) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for tok, sess := range s.m {
		if !now.Before(sess.expiry) {
			delete(s.m, tok)
		}
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
