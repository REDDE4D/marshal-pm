package dashboard

import (
	"testing"
	"time"
)

func TestSessionCreateValidate(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
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
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, _ := s.create("admin")
	now = now.Add(2 * time.Hour)
	if _, ok := s.validate(tok); ok {
		t.Fatal("expired session still valid")
	}
}

func TestSessionDelete(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, _ := s.create("a")
	s.delete(tok)
	if _, ok := s.validate(tok); ok {
		t.Fatal("deleted session still valid")
	}
}

func TestSessionSweep(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newSessionStore(time.Hour, func() time.Time { return now })
	tok, _ := s.create("b")
	now = now.Add(2 * time.Hour)
	s.sweep()
	if _, present := s.m[tok]; present {
		t.Fatal("sweep did not remove expired session")
	}
}
