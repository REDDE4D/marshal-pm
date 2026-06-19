// Package config loads and validates marshal.yaml app definitions.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// RestartMode controls when an exited process is restarted.
type RestartMode string

const (
	RestartAlways    RestartMode = "always"
	RestartOnFailure RestartMode = "on-failure"
	RestartNo        RestartMode = "no"
)

// LogRetention overrides per-app log rotation/retention. Nil fields fall back
// to the daemon default; a non-nil pointer is honoured verbatim.
type LogRetention struct {
	MaxSizeMB  *int  `yaml:"max_size_mb" json:"max_size_mb,omitempty"`
	MaxBackups *int  `yaml:"max_backups" json:"max_backups,omitempty"`
	MaxAgeDays *int  `yaml:"max_age_days" json:"max_age_days,omitempty"`
	Compress   *bool `yaml:"compress" json:"compress,omitempty"`
}

// Duration is a time.Duration that unmarshals from a string like "5s".
type Duration struct{ time.Duration }

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// MarshalJSON renders the duration as a Go duration string (e.g. "5s").
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(d.Duration.String())), nil
}

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = parsed
	return nil
}

// ServerConfig points the agent at a central server. Presence enables fleet mode.
type ServerConfig struct {
	Address     string `yaml:"address" json:"address"`
	Name        string `yaml:"name" json:"name,omitempty"`
	Token       string `yaml:"token" json:"token,omitempty"`             // enrollment token (used until enrolled)
	Fingerprint string `yaml:"fingerprint" json:"fingerprint,omitempty"` // pinned server cert SHA-256
	CA          string `yaml:"ca" json:"ca,omitempty"`                   // CA file path (alternative to fingerprint)
}

// App is one supervised application definition.
type App struct {
	Name        string            `yaml:"name" json:"name"`
	Cmd         string            `yaml:"cmd" json:"cmd"`
	Args        []string          `yaml:"args" json:"args"`
	Cwd         string            `yaml:"cwd" json:"cwd"`
	Instances   int               `yaml:"instances" json:"instances"`
	Env         map[string]string `yaml:"env" json:"env"`
	Restart     RestartMode       `yaml:"restart" json:"restart"`
	MaxRestarts int               `yaml:"max_restarts" json:"max_restarts"`
	KillTimeout Duration          `yaml:"kill_timeout" json:"kill_timeout"`
	Logs        *LogRetention     `yaml:"logs" json:"logs,omitempty"`
	Source      *GitSource        `yaml:"source" json:"source,omitempty"` // M21 git deploy
}

// GitSource describes deploying an app from a git repository (M21).
type GitSource struct {
	Repo   string `yaml:"repo" json:"repo"`
	Ref    string `yaml:"ref" json:"ref,omitempty"`
	Build  string `yaml:"build" json:"build,omitempty"`
	Subdir string `yaml:"subdir" json:"subdir,omitempty"`
}

// Config is the top-level marshal.yaml document.
type Config struct {
	Server *ServerConfig `yaml:"server" json:"server,omitempty"`
	Apps   []App         `yaml:"apps"`
}

// Load reads and parses a marshal.yaml file from disk.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes YAML bytes, applies defaults, and validates.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Prepare(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Prepare applies per-app defaults and validates the config in place.
func (c *Config) Prepare() error {
	c.applyDefaults()
	return c.validate()
}

func (c *Config) applyDefaults() {
	for i := range c.Apps {
		a := &c.Apps[i]
		if a.Instances == 0 {
			a.Instances = 1
		}
		if a.Restart == "" {
			a.Restart = RestartAlways
		}
		if a.MaxRestarts == 0 {
			a.MaxRestarts = 16
		}
		if a.KillTimeout.Duration == 0 {
			a.KillTimeout.Duration = 5 * time.Second
		}
	}
}

func (c *Config) validate() error {
	if c.Server != nil {
		if c.Server.Address == "" {
			return fmt.Errorf("server.address is required when a server block is present")
		}
		if c.Server.Fingerprint != "" && c.Server.CA != "" {
			return fmt.Errorf("server.fingerprint and server.ca are mutually exclusive")
		}
		if c.Server.Fingerprint == "" && c.Server.CA == "" {
			return fmt.Errorf("server needs a trust source: set server.fingerprint or server.ca")
		}
	}
	if len(c.Apps) == 0 {
		return fmt.Errorf("config has no apps")
	}
	seen := map[string]bool{}
	for _, a := range c.Apps {
		if a.Name == "" {
			return fmt.Errorf("app with cmd %q has no name", a.Cmd)
		}
		if seen[a.Name] {
			return fmt.Errorf("duplicate app name %q", a.Name)
		}
		seen[a.Name] = true
		if a.Cmd == "" {
			return fmt.Errorf("app %q has no cmd", a.Name)
		}
		switch a.Restart {
		case RestartAlways, RestartOnFailure, RestartNo:
		default:
			return fmt.Errorf("app %q has invalid restart mode %q", a.Name, a.Restart)
		}
		// applyDefaults has already promoted Instances == 0 to 1; only negatives remain invalid.
		if a.Instances < 0 {
			return fmt.Errorf("app %q has invalid instances %d", a.Name, a.Instances)
		}
	}
	return nil
}
