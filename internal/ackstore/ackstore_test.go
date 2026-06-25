package ackstore

import (
	"path/filepath"
	"testing"
)

func TestAckUnackPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acks.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.AckedAt("sig1"); ok {
		t.Fatal("sig1 should not be acked yet")
	}
	if err := s.Ack("sig1", 1000); err != nil {
		t.Fatal(err)
	}
	if at, ok := s.AckedAt("sig1"); !ok || at != 1000 {
		t.Fatalf("AckedAt = (%d,%v), want (1000,true)", at, ok)
	}

	// Reopen → ack persisted to disk.
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if at, ok := s2.AckedAt("sig1"); !ok || at != 1000 {
		t.Fatalf("after reopen AckedAt = (%d,%v), want (1000,true)", at, ok)
	}

	if err := s2.Unack("sig1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.AckedAt("sig1"); ok {
		t.Fatal("sig1 should be unacked after Unack")
	}
}

func TestPruneDropsStaleAcks(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "acks.json"))
	s.Ack("old", 100)
	s.Ack("new", 5000)
	s.Prune(1000) // drop acks older than 1000ms
	if _, ok := s.AckedAt("old"); ok {
		t.Fatal("old ack should be pruned")
	}
	if _, ok := s.AckedAt("new"); !ok {
		t.Fatal("new ack should survive")
	}
}
