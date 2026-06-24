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
	"strings"

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

// LogsDir is the directory holding per-instance rotated log files.
func (s *Store) LogsDir() string { return filepath.Join(s.base, "logs") }

// MetricsDBPath is the SQLite file holding metric history.
func (s *Store) MetricsDBPath() string { return filepath.Join(s.base, "metrics.db") }

// RestartsDBPath is the SQLite file holding restart-event history.
func (s *Store) RestartsDBPath() string { return filepath.Join(s.base, "restarts.db") }

// EnsureLogsDir creates the logs directory (0700) if it does not exist.
func (s *Store) EnsureLogsDir() error { return os.MkdirAll(s.LogsDir(), 0o700) }

// DeploysDir is where git checkouts live (one subdir per app).
func (s *Store) DeploysDir() string { return filepath.Join(s.base, "deploys") }

func (s *Store) dumpPath() string { return filepath.Join(s.base, "dump.json") }

func (s *Store) serverPath() string { return filepath.Join(s.base, "fleet.json") }

// EnsureDir creates the state directory if it does not exist.
func (s *Store) EnsureDir() error { return os.MkdirAll(s.base, 0o700) }

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
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write dump: %w", err)
	}
	if err := os.Rename(tmp, s.dumpPath()); err != nil {
		return fmt.Errorf("rename dump: %w", err)
	}
	return nil
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

// SaveServer writes the central-server config to fleet.json atomically.
func (s *Store) SaveServer(sc *config.ServerConfig) error {
	if err := s.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode fleet config: %w", err)
	}
	tmp := s.serverPath() + ".tmp"
	defer os.Remove(tmp)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write fleet config: %w", err)
	}
	if err := os.Rename(tmp, s.serverPath()); err != nil {
		return fmt.Errorf("rename fleet config: %w", err)
	}
	return nil
}

// LoadServer reads fleet.json. A missing file yields (nil, nil).
func (s *Store) LoadServer() (*config.ServerConfig, error) {
	data, err := os.ReadFile(s.serverPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read fleet config: %w", err)
	}
	var sc config.ServerConfig
	if err := json.Unmarshal(data, &sc); err != nil {
		return nil, fmt.Errorf("decode fleet config: %w", err)
	}
	return &sc, nil
}

// FleetTokenPath is the file holding the minted per-agent fleet token.
func (s *Store) FleetTokenPath() string { return filepath.Join(s.base, "fleet-token") }

// LoadFleetToken reads the minted per-agent token. A missing file yields ("", nil).
func (s *Store) LoadFleetToken() (string, error) {
	b, err := os.ReadFile(s.FleetTokenPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// SaveFleetToken writes the minted per-agent token (0600).
func (s *Store) SaveFleetToken(token string) error {
	return os.WriteFile(s.FleetTokenPath(), []byte(token), 0o600)
}
