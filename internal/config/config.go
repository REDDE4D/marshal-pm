// Package config loads and validates marshal.yaml app definitions.
package config

import (
	"fmt"
	"os"
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

// App is one supervised application definition.
type App struct {
	Name        string            `yaml:"name"`
	Cmd         string            `yaml:"cmd"`
	Args        []string          `yaml:"args"`
	Cwd         string            `yaml:"cwd"`
	Instances   int               `yaml:"instances"`
	Env         map[string]string `yaml:"env"`
	Restart     RestartMode       `yaml:"restart"`
	MaxRestarts int               `yaml:"max_restarts"`
	KillTimeout Duration          `yaml:"kill_timeout"`
}

// Config is the top-level marshal.yaml document.
type Config struct {
	Apps []App `yaml:"apps"`
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
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
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
		if a.Instances < 1 {
			return fmt.Errorf("app %q has invalid instances %d", a.Name, a.Instances)
		}
	}
	return nil
}
