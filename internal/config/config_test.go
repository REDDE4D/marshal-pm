package config

import (
	"testing"
	"time"
)

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./server
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	app := cfg.Apps[0]
	if app.Instances != 1 {
		t.Errorf("instances default = %d, want 1", app.Instances)
	}
	if app.Restart != RestartAlways {
		t.Errorf("restart default = %q, want always", app.Restart)
	}
	if app.MaxRestarts != 16 {
		t.Errorf("max_restarts default = %d, want 16", app.MaxRestarts)
	}
	if app.KillTimeout.Duration != 5*time.Second {
		t.Errorf("kill_timeout default = %v, want 5s", app.KillTimeout.Duration)
	}
}

func TestParseDuration(t *testing.T) {
	cfg, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./server
    kill_timeout: 12s
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Apps[0].KillTimeout.Duration; got != 12*time.Second {
		t.Errorf("kill_timeout = %v, want 12s", got)
	}
}

func TestValidateRejectsMissingName(t *testing.T) {
	_, err := Parse([]byte(`
apps:
  - cmd: ./server
`))
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestValidateRejectsDuplicateName(t *testing.T) {
	_, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./a
  - name: api
    cmd: ./b
`))
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestValidateRejectsBadRestartMode(t *testing.T) {
	_, err := Parse([]byte(`
apps:
  - name: api
    cmd: ./server
    restart: sometimes
`))
	if err == nil {
		t.Fatal("expected error for bad restart mode")
	}
}
