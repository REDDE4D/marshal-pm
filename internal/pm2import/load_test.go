package pm2import

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJSONFlexibleFields(t *testing.T) {
	eco, err := parseJSON([]byte(`{"apps":[
		{"name":"a","script":"a.js","args":"--port 80","instances":"max","env":{"PORT":8080,"DEBUG":true,"HOST":"x"}},
		{"name":"b","script":"b.js","args":["--flag","v"],"instances":4,"node_args":"--inspect","kill_timeout":3000,"max_restarts":7,"watch":["src"]}
	]}`))
	if err != nil {
		t.Fatalf("parseJSON: %v", err)
	}
	a, b := eco.Apps[0], eco.Apps[1]
	if strings.Join(a.Args, " ") != "--port 80" {
		t.Errorf("a.Args = %v, want [--port 80]", a.Args)
	}
	if !a.InstancesMax {
		t.Errorf("a should request instances max")
	}
	if a.Env["PORT"] != "8080" || a.Env["DEBUG"] != "true" || a.Env["HOST"] != "x" {
		t.Errorf("a.Env stringify wrong: %v", a.Env)
	}
	if b.Instances != 4 || b.MaxRestarts != 7 || b.KillTimeoutMs != 3000 {
		t.Errorf("b numeric fields wrong: %+v", b)
	}
	if strings.Join(b.NodeArgs, " ") != "--inspect" || !b.Watch {
		t.Errorf("b node_args/watch wrong: %+v", b)
	}
}

func TestLoadJSConfigViaNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	dir := t.TempDir()
	js := `const base = { FOO: "bar" };
module.exports = { apps: [
  { name: "svc", script: "src/index.js", env: { ...base, PORT: 3000 } },
] };`
	path := filepath.Join(dir, "ecosystem.config.js")
	if err := os.WriteFile(path, []byte(js), 0o600); err != nil {
		t.Fatal(err)
	}
	eco, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(eco.Apps) != 1 || eco.Apps[0].Name != "svc" {
		t.Fatalf("apps = %+v", eco.Apps)
	}
	if eco.Apps[0].Env["FOO"] != "bar" || eco.Apps[0].Env["PORT"] != "3000" {
		t.Errorf("env (resolved via node spread) wrong: %v", eco.Apps[0].Env)
	}
}
