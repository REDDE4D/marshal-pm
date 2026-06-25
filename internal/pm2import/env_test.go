package pm2import

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/config"
)

func TestSplitEnvFilesRoundTripsThroughLoad(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := Convert(Ecosystem{Apps: []PM2App{
		{Name: "SurvivorAdmin", Script: "src/index.js", Env: map[string]string{"TOKEN": "aegis-secret", "DB_HOST": "10.0.0.5"}},
		{Name: "NoEnv", Script: "x.js"},
	}})

	written, err := cfg.SplitEnvFiles(dir)
	if err != nil {
		t.Fatalf("SplitEnvFiles: %v", err)
	}
	if len(written) != 1 || written[0] != "SurvivorAdmin.env" {
		t.Fatalf("written = %v, want [SurvivorAdmin.env]", written)
	}

	a := cfg.Apps[0]
	if a.EnvFile != "SurvivorAdmin.env" || a.Env != nil {
		t.Fatalf("app not switched to env_file: %+v", a)
	}
	// Env file is 0600 (it holds secrets).
	info, err := os.Stat(filepath.Join(dir, "SurvivorAdmin.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("env file perm = %o, want 600", info.Mode().Perm())
	}

	// Write the generated marshal.yaml next to the env files and Load it: the
	// env_file must resolve and the secrets must come back.
	data, err := cfg.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marshal.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(filepath.Join(dir, "marshal.yaml"))
	if err != nil {
		t.Fatalf("Load generated config: %v", err)
	}
	got := loaded.Apps[0].Env
	if got["TOKEN"] != "aegis-secret" || got["DB_HOST"] != "10.0.0.5" {
		t.Fatalf("env not resolved from env_file: %v", got)
	}
}
