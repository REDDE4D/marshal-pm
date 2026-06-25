package pm2import

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// rawApp mirrors the on-disk PM2 app shape, where several fields accept multiple
// JSON types (string-or-array, number-or-"max", etc.).
type rawApp struct {
	Name            string                 `json:"name"`
	Script          string                 `json:"script"`
	Args            json.RawMessage        `json:"args"`
	Interpreter     string                 `json:"interpreter"`
	InterpreterArgs json.RawMessage        `json:"interpreter_args"`
	NodeArgs        json.RawMessage        `json:"node_args"`
	Cwd             string                 `json:"cwd"`
	Env             map[string]interface{} `json:"env"`
	EnvFile         string                 `json:"env_file"`
	Instances       json.RawMessage        `json:"instances"`
	ExecMode        string                 `json:"exec_mode"`
	Autorestart     *bool                  `json:"autorestart"`
	MaxRestarts     *int                   `json:"max_restarts"`
	KillTimeout     *int                   `json:"kill_timeout"`
	CronRestart     string                 `json:"cron_restart"`
	Watch           json.RawMessage        `json:"watch"`
}

type rawEco struct {
	Apps []rawApp `json:"apps"`
}

// Load reads and parses a PM2 ecosystem file. .json is read directly, .yaml/.yml
// are converted, and .js/.cjs/.mjs are evaluated with node (which must be on PATH
// — it runs the file, exactly as PM2 would, to resolve any dynamic config).
func Load(path string) (Ecosystem, error) {
	var jsonBytes []byte
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".cjs", ".mjs":
		b, err := nodeEval(path)
		if err != nil {
			return Ecosystem{}, err
		}
		if err := checkEvalResult(path, b); err != nil {
			return Ecosystem{}, err
		}
		jsonBytes = b
	case ".json":
		b, err := os.ReadFile(path)
		if err != nil {
			return Ecosystem{}, fmt.Errorf("read ecosystem: %w", err)
		}
		jsonBytes = b
	case ".yaml", ".yml":
		b, err := os.ReadFile(path)
		if err != nil {
			return Ecosystem{}, fmt.Errorf("read ecosystem: %w", err)
		}
		var doc interface{}
		if err := yaml.Unmarshal(b, &doc); err != nil {
			return Ecosystem{}, fmt.Errorf("parse ecosystem yaml: %w", err)
		}
		jsonBytes, err = json.Marshal(doc)
		if err != nil {
			return Ecosystem{}, err
		}
	default:
		return Ecosystem{}, fmt.Errorf("unsupported ecosystem file %q (expected .js/.cjs/.json/.yaml)", path)
	}
	eco, err := parseJSON(jsonBytes)
	if err != nil {
		return Ecosystem{}, err
	}
	// Record the file's directory so Convert can resolve each app's cwd against
	// it (PM2 semantics), making the generated config independent of the daemon's
	// own working directory.
	if abs, aerr := filepath.Abs(path); aerr == nil {
		eco.BaseDir = filepath.Dir(abs)
	}
	return eco, nil
}

// nodeEval runs the ecosystem file through node and returns its exported config
// as JSON. Requires node on PATH.
func nodeEval(path string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("node"); err != nil {
		return nil, fmt.Errorf("node is required to evaluate a .js ecosystem file (not found on PATH); convert it to JSON, or install Node")
	}
	const script = `process.stdout.write(JSON.stringify(require(process.argv[1])))`
	out, err := exec.Command("node", "-e", script, abs).Output()
	if err != nil {
		// Output() captures node's stderr into ExitError.Stderr; surface it so a
		// throwing config reports its real cause instead of a bare exit status.
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("evaluate ecosystem with node: %w\n%s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("evaluate ecosystem with node: %w", err)
	}
	return out, nil
}

// checkEvalResult inspects the JSON node produced from a .js/.cjs/.mjs ecosystem
// file and, when it has no top-level `apps`, returns a diagnostic for the usual
// cause: the file was evaluated as an ES module instead of CommonJS. An ESM
// `export default {...}` lands under a `default` key; a CommonJS `module.exports`
// in a file that node treats as ESM (because a nearby package.json sets
// "type":"module") is dropped entirely, leaving `{}`. Returns nil when `apps` is
// present or there's no ESM evidence — the generic empty case is reported by the
// caller as the usual "no apps found".
func checkEvalResult(path string, b []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil // not an object; let parseJSON surface any error
	}
	if _, ok := obj["apps"]; ok {
		return nil
	}
	if _, ok := obj["default"]; ok {
		return fmt.Errorf("ecosystem file %q exported an ES module (a `default` export but no top-level `apps`); PM2 ecosystem files must be CommonJS — use `module.exports = { apps: [...] }` and name the file .cjs", path)
	}
	if strings.EqualFold(filepath.Ext(path), ".js") && nearestPackageIsModule(path) {
		return fmt.Errorf("ecosystem file %q evaluated to an empty export: a nearby package.json sets \"type\":\"module\", so node treated this .js file as an ES module and ignored `module.exports`. Rename it to .cjs (or remove \"type\":\"module\")", path)
	}
	return nil
}

// nearestPackageIsModule walks up from path's directory to the first package.json
// (node's resolution order) and reports whether it declares "type":"module".
func nearestPackageIsModule(path string) bool {
	dir, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	dir = filepath.Dir(dir)
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			var pkg struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &pkg) != nil {
				return false // unparseable package.json — assume CommonJS
			}
			return strings.EqualFold(pkg.Type, "module")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func parseJSON(b []byte) (Ecosystem, error) {
	var raw rawEco
	if err := json.Unmarshal(b, &raw); err != nil {
		return Ecosystem{}, fmt.Errorf("parse ecosystem: %w", err)
	}
	eco := Ecosystem{}
	for _, r := range raw.Apps {
		eco.Apps = append(eco.Apps, normalize(r))
	}
	return eco, nil
}

func normalize(r rawApp) PM2App {
	a := PM2App{
		Name:        r.Name,
		Script:      r.Script,
		Args:        stringOrList(r.Args),
		Interpreter: r.Interpreter,
		Cwd:         r.Cwd,
		EnvFile:     r.EnvFile,
		ExecMode:    r.ExecMode,
		Autorestart: r.Autorestart,
		CronRestart: r.CronRestart,
	}
	// node_args / interpreter_args are aliases; prefer node_args.
	a.NodeArgs = stringOrList(r.NodeArgs)
	if len(a.NodeArgs) == 0 {
		a.NodeArgs = stringOrList(r.InterpreterArgs)
	}
	if len(r.Env) > 0 {
		a.Env = map[string]string{}
		for k, v := range r.Env {
			a.Env[k] = stringify(v)
		}
	}
	if r.MaxRestarts != nil {
		a.MaxRestarts = *r.MaxRestarts
	}
	if r.KillTimeout != nil {
		a.KillTimeoutMs = *r.KillTimeout
	}
	a.Instances, a.InstancesMax = parseInstances(r.Instances)
	a.Watch = truthyWatch(r.Watch)
	return a
}

// stringOrList accepts a JSON string (split on whitespace) or array of strings.
func stringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.Fields(s)
	}
	return nil
}

func parseInstances(raw json.RawMessage) (n int, max bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		if f < 0 {
			return 0, true // -1 means "all cores"
		}
		return int(f), false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.EqualFold(s, "max") || s == "-1" {
			return 0, true
		}
		if v, err := strconv.Atoi(s); err == nil {
			return v, false
		}
	}
	return 0, false
}

func truthyWatch(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	// A non-empty array of paths also enables watch.
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return len(list) > 0
	}
	return false
}

func stringify(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}
