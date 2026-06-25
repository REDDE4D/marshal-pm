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

// requireNode skips a test when node isn't on PATH (the .js eval path needs it).
func requireNode(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A package.json with "type":"module" makes node treat a .js file as ESM, so a
// CommonJS module.exports is ignored and node returns an empty object. The error
// should point the user at the .cjs fix rather than the opaque "no apps found".
func TestLoadESMTypedJSGivesCjsHint(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"type":"module"}`)
	path := writeFile(t, dir, "ecosystem.config.js",
		`module.exports = { apps: [ { name: "svc", script: "src/index.js" } ] };`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an ESM-typed .js ecosystem file")
	}
	if !strings.Contains(err.Error(), ".cjs") || !strings.Contains(err.Error(), "type") {
		t.Errorf("error should explain the type:module / .cjs fix, got: %v", err)
	}
}

// An `export default {...}` ecosystem lands under a "default" key (not "apps").
// PM2 files must be CommonJS; the error should say so.
func TestLoadESMExportDefaultGivesHint(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"type":"module"}`)
	path := writeFile(t, dir, "ecosystem.config.js",
		`export default { apps: [ { name: "svc", script: "s.js" } ] };`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an export-default ecosystem file")
	}
	if !strings.Contains(err.Error(), "module.exports") {
		t.Errorf("error should require CommonJS module.exports, got: %v", err)
	}
}

// When node itself throws while evaluating the config, its stderr must surface
// in the error instead of collapsing to a bare "exit status 1".
func TestLoadNodeErrorIncludesStderr(t *testing.T) {
	requireNode(t)
	dir := t.TempDir()
	path := writeFile(t, dir, "ecosystem.config.js", `throw new Error("kaboom-from-config");`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error when the config throws")
	}
	if !strings.Contains(err.Error(), "kaboom-from-config") {
		t.Errorf("error should include node's stderr, got: %v", err)
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
