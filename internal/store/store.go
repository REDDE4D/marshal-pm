// Package store owns the marshal state directory (~/.marshal or
// $XDG_DATA_HOME/marshal) and the dump.json app snapshot used by save/resurrect.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"marshal/internal/config"
)

// Store resolves paths within the state directory.
type Store struct{ base string }

// New resolves the state directory from $XDG_DATA_HOME (preferred) or $HOME.
func New() (*Store, error) {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return &Store{base: filepath.Join(xdg, "marshal")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return &Store{base: filepath.Join(home, ".marshal")}, nil
}

// NewAt builds a store rooted at an explicit base (used in tests).
func NewAt(base string) *Store { return &Store{base: base} }

// Dir is the state directory.
func (s *Store) Dir() string { return s.base }

// SocketPath is the gRPC Unix socket path.
func (s *Store) SocketPath() string { return filepath.Join(s.base, "marshald.sock") }

// LogPath is where an auto-spawned daemon writes stdout/stderr.
func (s *Store) LogPath() string { return filepath.Join(s.base, "marshald.log") }

func (s *Store) dumpPath() string { return filepath.Join(s.base, "dump.json") }

// EnsureDir creates the state directory if it does not exist.
func (s *Store) EnsureDir() error { return os.MkdirAll(s.base, 0o755) }

// Save writes app definitions to dump.json atomically.
func (s *Store) Save(apps []config.App) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(apps, "", "  ")
	if err != nil {
		return fmt.Errorf("encode dump: %w", err)
	}
	tmp := s.dumpPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write dump: %w", err)
	}
	return os.Rename(tmp, s.dumpPath())
}

// Load reads dump.json. A missing file yields an empty slice and no error.
func (s *Store) Load() ([]config.App, error) {
	data, err := os.ReadFile(s.dumpPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read dump: %w", err)
	}
	var apps []config.App
	if err := json.Unmarshal(data, &apps); err != nil {
		return nil, fmt.Errorf("decode dump: %w", err)
	}
	return apps, nil
}
