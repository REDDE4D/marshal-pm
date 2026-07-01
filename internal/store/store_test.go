package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/config"
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

func TestSaveDumpIsPrivate(t *testing.T) {
	// dump.json carries app Env, which may hold secrets — it must not be
	// world/group readable.
	s := NewAt(filepath.Join(t.TempDir(), "state"))
	if err := s.Save([]config.App{{Name: "api", Cmd: "./server", Env: map[string]string{"DB_PASSWORD": "s3cret"}}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Join(s.Dir(), "dump.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("dump.json perm = %o, want 600", perm)
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

func TestEnsureDirIsPrivate(t *testing.T) {
	s := NewAt(filepath.Join(t.TempDir(), "state"))
	if err := s.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(s.Dir())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir perm = %o, want 700", perm)
	}
}

func TestLogsDirCreatedPrivate(t *testing.T) {
	base := t.TempDir()
	s := NewAt(base)
	want := filepath.Join(base, "logs")
	if s.LogsDir() != want {
		t.Fatalf("LogsDir = %q, want %q", s.LogsDir(), want)
	}
	if err := s.EnsureLogsDir(); err != nil {
		t.Fatalf("EnsureLogsDir: %v", err)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir perm = %o, want 700", info.Mode().Perm())
	}
}

func TestSaveLoadServer(t *testing.T) {
	st := NewAt(t.TempDir())
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Name: "web-1"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Address != "srv:9000" || got.Name != "web-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadServerMissing(t *testing.T) {
	got, err := NewAt(t.TempDir()).LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestFleetTokenRoundTrip(t *testing.T) {
	s := NewAt(t.TempDir())
	if err := s.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadFleetToken()
	if err != nil || got != "" {
		t.Fatalf("missing token: %q, %v", got, err)
	}
	if err := s.SaveFleetToken("tok-123"); err != nil {
		t.Fatal(err)
	}
	got, err = s.LoadFleetToken()
	if err != nil || got != "tok-123" {
		t.Fatalf("LoadFleetToken = %q, %v", got, err)
	}
	info, err := os.Stat(s.FleetTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestClearServerRemovesConfigAndToken(t *testing.T) {
	st := NewAt(t.TempDir())
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Token: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveFleetToken("agent-tok"); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearServer(); err != nil {
		t.Fatalf("ClearServer: %v", err)
	}
	if sc, _ := st.LoadServer(); sc != nil {
		t.Errorf("server config still present: %+v", sc)
	}
	if tok, _ := st.LoadFleetToken(); tok != "" {
		t.Errorf("fleet token still present: %q", tok)
	}
	// Idempotent: clearing again on an empty store is fine.
	if err := st.ClearServer(); err != nil {
		t.Errorf("second ClearServer: %v", err)
	}
}

func TestSaveLoadRoundTripsAppID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	s, err := New()
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	in := []config.App{
		{Name: "a", Cmd: "true", ID: 1},
		{Name: "b", Cmd: "true", ID: 2},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(out) != 2 || out[0].ID != 1 || out[1].ID != 2 {
		t.Fatalf("IDs not round-tripped: %+v", out)
	}
}
