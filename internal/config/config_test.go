package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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

func TestLoadLogRetention(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marshal.yaml")
	yaml := "apps:\n" +
		"  - name: api\n" +
		"    cmd: ./api\n" +
		"    logs:\n" +
		"      max_size_mb: 50\n" +
		"      max_age_days: 0\n" +
		"      compress: false\n" +
		"  - name: web\n" +
		"    cmd: ./web\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	api := cfg.Apps[0]
	if api.Logs == nil || api.Logs.MaxSizeMB == nil || *api.Logs.MaxSizeMB != 50 {
		t.Fatalf("api max_size_mb not parsed: %+v", api.Logs)
	}
	if api.Logs.MaxAgeDays == nil || *api.Logs.MaxAgeDays != 0 {
		t.Fatalf("explicit max_age_days:0 must be preserved, not nil")
	}
	if api.Logs.Compress == nil || *api.Logs.Compress != false {
		t.Fatalf("compress:false must be preserved")
	}
	if api.Logs.MaxBackups != nil {
		t.Fatalf("omitted max_backups must stay nil for default fallback")
	}
	if cfg.Apps[1].Logs != nil {
		t.Fatalf("web has no logs block; want nil")
	}
}
