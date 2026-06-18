package dashboard

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestSessionCreateValidate(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now }, "")
	tok, err := s.create("admin")
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	user, ok := s.validate(tok)
	if !ok || user != "admin" {
		t.Fatalf("validate = %q, %v; want admin, true", user, ok)
	}
}

func TestSessionExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now }, "")
	tok, _ := s.create("admin")
	now = now.Add(2 * time.Hour)
	if _, ok := s.validate(tok); ok {
		t.Fatal("expired session still valid")
	}
}

func TestSessionDelete(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now }, "")
	tok, _ := s.create("a")
	s.delete(tok)
	if _, ok := s.validate(tok); ok {
		t.Fatal("deleted session still valid")
	}
}

func TestSessionSweep(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now }, "")
	tok, _ := s.create("b")
	now = now.Add(2 * time.Hour)
	s.sweep()
	if _, present := s.m[hashSessionToken(tok)]; present {
		t.Fatal("sweep did not remove expired session")
	}
}

func TestSweepLoop(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now }, "")
	tok, err := s.create("c")
	if err != nil {
		t.Fatal(err)
	}

	// Advance clock past expiry so the next sweep removes the session.
	now = now.Add(2 * time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sweepLoop(ctx, 10*time.Millisecond)
	}()

	// Wait up to 500ms for the expired entry to be removed.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, present := s.m[hashSessionToken(tok)]
		s.mu.Unlock()
		if !present {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.mu.Lock()
	_, present := s.m[hashSessionToken(tok)]
	s.mu.Unlock()
	if present {
		t.Fatal("sweepLoop did not remove expired session")
	}

	// Cancel and confirm the goroutine exits.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sweepLoop goroutine did not return after context cancel")
	}
}

func TestSessionPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	now := time.Unix(1000, 0)

	s1 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	tok, err := s1.create("admin")
	if err != nil {
		t.Fatal(err)
	}

	// A brand-new store at the same path (simulating a restart) sees the session.
	s2 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	user, ok := s2.validate(tok)
	if !ok || user != "admin" {
		t.Fatalf("after reload validate = %q, %v; want admin, true", user, ok)
	}
}

func TestSessionLoadDropsExpired(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	now := time.Unix(1000, 0)

	s1 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	tok, _ := s1.create("admin")

	// Reload after the TTL has elapsed: the expired entry must be dropped.
	later := now.Add(2 * time.Hour)
	s2 := newSessionStore(time.Hour, func() time.Time { return later }, path)
	if _, ok := s2.validate(tok); ok {
		t.Fatal("expired session survived reload")
	}
}

func TestSessionDeletePersists(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	now := time.Unix(1000, 0)

	s1 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	tok, _ := s1.create("admin")
	s1.delete(tok)

	s2 := newSessionStore(time.Hour, func() time.Time { return now }, path)
	if _, ok := s2.validate(tok); ok {
		t.Fatal("deleted session reappeared after reload")
	}
}

func TestSessionEmptyPathNoFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	// Store is constructed with "" (not path) — any I/O to path is a bug.
	s := newSessionStore(time.Hour, nil, "")
	if _, err := s.create("admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("in-memory store wrote a file at %s", path)
	}
}

func TestSessionCorruptFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sessions.json"
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSessionStore(time.Hour, nil, path) // must not panic
	if _, ok := s.validate("anything"); ok {
		t.Fatal("validated against a corrupt-loaded store")
	}
}
