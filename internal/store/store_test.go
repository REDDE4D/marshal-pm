package store

import (
	"path/filepath"
	"testing"
	"time"

	"marshal/internal/config"
)

func TestPathsUnderBase(t *testing.T) {
	s := NewAt("/tmp/marshal-test")
	if s.SocketPath() != "/tmp/marshal-test/marshald.sock" {
		t.Fatalf("socket = %s", s.SocketPath())
	}
	if s.LogPath() != "/tmp/marshal-test/marshald.log" {
		t.Fatalf("log = %s", s.LogPath())
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewAt(filepath.Join(dir, "state"))

	apps := []config.App{{
		Name: "api", Cmd: "./server", Args: []string{"-p", "8080"},
		Instances: 2, Restart: config.RestartOnFailure, MaxRestarts: 16,
		KillTimeout: config.Duration{Duration: 5 * time.Second},
	}}
	if err := s.Save(apps); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0].Name != "api" || got[0].Instances != 2 ||
		got[0].KillTimeout.Duration != 5*time.Second {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestLoadMissingReturnsEmpty(t *testing.T) {
	s := NewAt(t.TempDir())
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d apps, want 0", len(got))
	}
}
