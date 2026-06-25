// Package pm2import converts PM2 ecosystem files into Marshal marshal.yaml
// configs. The conversion logic (Convert) is pure; loading handles the JSON/YAML
// forms directly and evaluates JS/CJS ecosystem files via node.
package pm2import

import (
	"fmt"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Ecosystem is a parsed PM2 ecosystem file (the normalized form).
type Ecosystem struct {
	Apps []PM2App
}

// PM2App is one PM2 app entry, normalized from the raw ecosystem JSON (see
// rawApp in load.go for the flexible on-disk shapes).
type PM2App struct {
	Name          string
	Script        string
	Args          []string
	Interpreter   string
	NodeArgs      []string
	Cwd           string
	Env           map[string]string
	EnvFile       string
	Instances     int  // 0 = unset
	InstancesMax  bool // "max" / -1 requested
	ExecMode      string
	Autorestart   *bool
	MaxRestarts   int // 0 = unset
	KillTimeoutMs int // 0 = unset
	CronRestart   string
	Watch         bool
}

// OutApp is a Marshal app rendered to YAML; omitempty keeps the output clean.
type OutApp struct {
	Name        string            `yaml:"name"`
	Cmd         string            `yaml:"cmd"`
	Args        []string          `yaml:"args,omitempty"`
	Cwd         string            `yaml:"cwd,omitempty"`
	Instances   int               `yaml:"instances,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	EnvFile     string            `yaml:"env_file,omitempty"`
	Restart     string            `yaml:"restart,omitempty"`
	MaxRestarts int               `yaml:"max_restarts,omitempty"`
	KillTimeout string            `yaml:"kill_timeout,omitempty"`
}

// Config is the converted marshal.yaml document.
type Config struct {
	Apps []OutApp `yaml:"apps"`
}

// YAML renders the converted config as marshal.yaml bytes.
func (c Config) YAML() ([]byte, error) { return yaml.Marshal(c) }

// interpreterByExt maps a script extension to its default interpreter, mirroring
// PM2's auto-detection. An empty result means "run the script directly".
func interpreterByExt(script string) string {
	switch strings.ToLower(path.Ext(script)) {
	case ".js", ".mjs", ".cjs":
		return "node"
	case ".ts":
		return "ts-node"
	case ".py":
		return "python3"
	case ".rb":
		return "ruby"
	case ".sh", ".bash":
		return "bash"
	default:
		return ""
	}
}

// Convert maps a PM2 ecosystem to a Marshal config plus human-readable warnings
// for fields that have no equivalent.
func Convert(eco Ecosystem) (Config, []string) {
	var cfg Config
	var warns []string
	warn := func(app, msg string) { warns = append(warns, fmt.Sprintf("%s: %s", app, msg)) }

	for _, p := range eco.Apps {
		name := p.Name
		if name == "" {
			name = strings.TrimSuffix(path.Base(p.Script), path.Ext(p.Script))
			warn(name, "app had no name; derived from script")
		}

		// Resolve interpreter → cmd/args.
		var cmd string
		var args []string
		interp := p.Interpreter
		switch {
		case interp == "none" || interp == "":
			if interp == "" {
				interp = interpreterByExt(p.Script)
			}
			if interp == "" || interp == "none" {
				cmd, args = p.Script, append([]string{}, p.Args...) // run the script directly
			} else {
				cmd = interp
				args = append(args, p.NodeArgs...)
				args = append(args, p.Script)
				args = append(args, p.Args...)
			}
		default:
			cmd = interp
			args = append(args, p.NodeArgs...)
			args = append(args, p.Script)
			args = append(args, p.Args...)
		}
		if len(args) == 0 {
			args = nil
		}

		out := OutApp{
			Name:        name,
			Cmd:         cmd,
			Args:        args,
			Cwd:         p.Cwd,
			Instances:   p.Instances,
			Env:         p.Env,
			EnvFile:     p.EnvFile,
			MaxRestarts: p.MaxRestarts,
		}
		if len(out.Env) == 0 {
			out.Env = nil
		}
		if p.Autorestart != nil && !*p.Autorestart {
			out.Restart = "no"
		}
		if p.KillTimeoutMs > 0 {
			out.KillTimeout = (time.Duration(p.KillTimeoutMs) * time.Millisecond).String()
		}

		// Warnings for unsupported PM2 features.
		if strings.EqualFold(p.ExecMode, "cluster") {
			warn(name, "exec_mode 'cluster' has no equivalent — Marshal runs fork-mode instances (no shared socket / load balancing)")
		}
		if p.InstancesMax {
			warn(name, "instances 'max'/-1 needs an explicit count in Marshal — set `instances:` to the desired number")
		}
		if p.CronRestart != "" {
			warn(name, "cron_restart is not supported")
		}
		if p.Watch {
			warn(name, "watch is not supported")
		}

		cfg.Apps = append(cfg.Apps, out)
	}
	return cfg, warns
}
