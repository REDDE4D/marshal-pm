package pm2import

import (
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

func TestConvertScriptAndInterpreter(t *testing.T) {
	cases := []struct {
		name    string
		in      PM2App
		wantCmd string
		wantArg []string
	}{
		{"js infers node", PM2App{Name: "a", Script: "src/index.js"}, "node", []string{"src/index.js"}},
		{"js with args", PM2App{Name: "a", Script: "app.js", Args: []string{"--port", "80"}}, "node", []string{"app.js", "--port", "80"}},
		{"py infers python3", PM2App{Name: "a", Script: "main.py"}, "python3", []string{"main.py"}},
		{"explicit interpreter", PM2App{Name: "a", Script: "app.js", Interpreter: "bun"}, "bun", []string{"app.js"}},
		{"interpreter none runs script directly", PM2App{Name: "a", Script: "./server", Interpreter: "none"}, "./server", nil},
		{"no extension runs directly", PM2App{Name: "a", Script: "./server", Args: []string{"-p", "8080"}}, "./server", []string{"-p", "8080"}},
		{"node_args precede script", PM2App{Name: "a", Script: "app.js", NodeArgs: []string{"--max-old-space-size=512"}}, "node", []string{"--max-old-space-size=512", "app.js"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, _ := Convert(Ecosystem{Apps: []PM2App{c.in}})
			got := cfg.Apps[0]
			if got.Cmd != c.wantCmd {
				t.Errorf("cmd = %q, want %q", got.Cmd, c.wantCmd)
			}
			if strings.Join(got.Args, "\x00") != strings.Join(c.wantArg, "\x00") {
				t.Errorf("args = %v, want %v", got.Args, c.wantArg)
			}
		})
	}
}

func TestConvertFieldsAndDefaults(t *testing.T) {
	cfg, _ := Convert(Ecosystem{Apps: []PM2App{{
		Name:          "api",
		Script:        "src/index.js",
		Cwd:           "./svc",
		Env:           map[string]string{"PORT": "8080"},
		EnvFile:       ".env.api",
		Instances:     3,
		Autorestart:   boolp(false),
		MaxRestarts:   10,
		KillTimeoutMs: 5000,
	}}})
	a := cfg.Apps[0]
	if a.Name != "api" || a.Cwd != "./svc" || a.EnvFile != ".env.api" || a.Instances != 3 || a.MaxRestarts != 10 {
		t.Fatalf("field mismatch: %+v", a)
	}
	if a.Env["PORT"] != "8080" {
		t.Errorf("env PORT = %q", a.Env["PORT"])
	}
	if a.Restart != "no" {
		t.Errorf("restart = %q, want no (autorestart:false)", a.Restart)
	}
	if a.KillTimeout != "5s" {
		t.Errorf("kill_timeout = %q, want 5s", a.KillTimeout)
	}
}

func TestConvertWarnsOnUnsupported(t *testing.T) {
	_, warns := Convert(Ecosystem{Apps: []PM2App{{
		Name: "x", Script: "a.js", ExecMode: "cluster", InstancesMax: true, CronRestart: "0 0 * * *", Watch: true,
	}}})
	joined := strings.ToLower(strings.Join(warns, "\n"))
	for _, want := range []string{"cluster", "instances", "cron", "watch"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning about %q in: %v", want, warns)
		}
	}
}

func TestConvertYAMLRoundTripsThroughLoad(t *testing.T) {
	cfg, _ := Convert(Ecosystem{Apps: []PM2App{{Name: "api", Script: "src/index.js", Env: map[string]string{"K": "v"}}}})
	out, err := cfg.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "name: api") || !strings.Contains(s, "cmd: node") {
		t.Fatalf("yaml missing expected fields:\n%s", s)
	}
	// Empty/default fields must be omitted for a clean file.
	if strings.Contains(s, "max_restarts:") || strings.Contains(s, "source:") {
		t.Fatalf("yaml should omit empty fields:\n%s", s)
	}
}
