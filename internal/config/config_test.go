package config

import (
	"encoding/json"
	"strings"
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

func TestDurationJSONRoundTrip(t *testing.T) {
	type wrap struct {
		KT Duration `json:"kt"`
	}
	in := wrap{KT: Duration{Duration: 7 * time.Second}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `{"kt":"7s"}` {
		t.Fatalf("got %s, want {\"kt\":\"7s\"}", b)
	}
	var out wrap
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.KT.Duration != 7*time.Second {
		t.Fatalf("got %v, want 7s", out.KT.Duration)
	}
}

func TestAppJSONUsesSchemaKeys(t *testing.T) {
	b, err := json.Marshal(App{Name: "api", Cmd: "./server", MaxRestarts: 16,
		KillTimeout: Duration{Duration: 5 * time.Second}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{`"name"`, `"cmd"`, `"max_restarts"`, `"kill_timeout"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("App JSON missing %s: %s", key, s)
		}
	}
	if strings.Contains(s, `"MaxRestarts"`) {
		t.Fatalf("App JSON has Go-cased key: %s", s)
	}
}

func TestPrepareAppliesDefaultsAndValidates(t *testing.T) {
	cfg := &Config{Apps: []App{{Name: "api", Cmd: "./server"}}}
	if err := cfg.Prepare(); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	a := cfg.Apps[0]
	if a.Instances != 1 || a.Restart != RestartAlways || a.MaxRestarts != 16 ||
		a.KillTimeout.Duration != 5*time.Second {
		t.Fatalf("defaults not applied: %+v", a)
	}

	bad := &Config{Apps: []App{{Name: "x"}}} // no cmd
	if err := bad.Prepare(); err == nil {
		t.Fatal("Prepare: want error for missing cmd")
	}
}
