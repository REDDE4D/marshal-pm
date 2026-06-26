// Package config loads and validates marshal.yaml app definitions.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// validAppName matches safe app names: must start with an alphanumeric character
// and contain only alphanumerics, dots, underscores, and hyphens.
var validAppName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

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

// ByteSize is a byte count that unmarshals from "300M"/"1G"/"512K" or a plain
// integer. Suffixes are 1024-based; KB/MB/GB are accepted as aliases of K/M/G.
type ByteSize struct{ Bytes uint64 }

func parseByteSize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	upper := strings.ToUpper(s)
	mult := uint64(1)
	switch {
	case strings.HasSuffix(upper, "GB"):
		mult, upper = 1<<30, strings.TrimSuffix(upper, "GB")
	case strings.HasSuffix(upper, "G"):
		mult, upper = 1<<30, strings.TrimSuffix(upper, "G")
	case strings.HasSuffix(upper, "MB"):
		mult, upper = 1<<20, strings.TrimSuffix(upper, "MB")
	case strings.HasSuffix(upper, "M"):
		mult, upper = 1<<20, strings.TrimSuffix(upper, "M")
	case strings.HasSuffix(upper, "KB"):
		mult, upper = 1<<10, strings.TrimSuffix(upper, "KB")
	case strings.HasSuffix(upper, "K"):
		mult, upper = 1<<10, strings.TrimSuffix(upper, "K")
	case strings.HasSuffix(upper, "B"):
		upper = strings.TrimSuffix(upper, "B")
	}
	n, err := strconv.ParseUint(strings.TrimSpace(upper), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", s, err)
	}
	return n * mult, nil
}

// UnmarshalYAML parses a size string ("300M") or a bare integer (bytes).
func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		var n uint64
		if err2 := value.Decode(&n); err2 == nil {
			b.Bytes = n
			return nil
		}
		return err
	}
	n, err := parseByteSize(s)
	if err != nil {
		return err
	}
	b.Bytes = n
	return nil
}

// MarshalJSON renders the size as a plain byte count (for dump.json round-trips).
func (b ByteSize) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatUint(b.Bytes, 10)), nil
}

// UnmarshalJSON parses either a number (bytes) or a quoted size string.
func (b *ByteSize) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	n, err := parseByteSize(s)
	if err != nil {
		return err
	}
	b.Bytes = n
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
	Name      string            `yaml:"name" json:"name"`
	Cmd       string            `yaml:"cmd" json:"cmd"`
	Args      []string          `yaml:"args" json:"args"`
	Cwd       string            `yaml:"cwd" json:"cwd"`
	Instances int               `yaml:"instances" json:"instances"`
	Env       map[string]string `yaml:"env" json:"env"`
	// EnvFile names a dotenv file (KEY=VALUE lines) loaded at config-load time and
	// merged into Env, with inline Env taking precedence. Resolved relative to the
	// marshal.yaml directory (absolute paths are used as-is). It is a load-time
	// directive only — after loading, Env holds the merged result — so it is not
	// persisted to JSON/dump.json.
	EnvFile          string        `yaml:"env_file" json:"-"`
	Restart          RestartMode   `yaml:"restart" json:"restart"`
	MaxRestarts      int           `yaml:"max_restarts" json:"max_restarts"`
	KillTimeout      Duration      `yaml:"kill_timeout" json:"kill_timeout"`
	MaxMemoryRestart ByteSize      `yaml:"max_memory_restart" json:"max_memory_restart,omitempty"`
	Logs             *LogRetention `yaml:"logs" json:"logs,omitempty"`
	Source           *GitSource    `yaml:"source" json:"source,omitempty"` // M21 git deploy
}

// GitSource describes deploying an app from a git repository (M21).
type GitSource struct {
	Repo       string `yaml:"repo" json:"repo"`
	Ref        string `yaml:"ref" json:"ref,omitempty"`
	Build      string `yaml:"build" json:"build,omitempty"`
	Subdir     string `yaml:"subdir" json:"subdir,omitempty"`
	Credential string `yaml:"credential" json:"credential,omitempty"` // M22 credstore name
}

// Config is the top-level marshal.yaml document.
type Config struct {
	Server *ServerConfig `yaml:"server" json:"server,omitempty"`
	Apps   []App         `yaml:"apps"`
}

// Load reads and parses a marshal.yaml file from disk. Per-app env_file
// directives are resolved relative to the file's directory and merged into Env
// before defaults/validation.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.loadEnvFiles(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := cfg.Prepare(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// loadEnvFiles reads each app's env_file (relative to baseDir, or absolute) and
// merges its KEY=VALUE pairs into the app's Env. Inline Env wins on collision.
func (c *Config) loadEnvFiles(baseDir string) error {
	for i := range c.Apps {
		a := &c.Apps[i]
		if a.EnvFile == "" {
			continue
		}
		path := a.EnvFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("app %q: read env_file %q: %w", a.Name, a.EnvFile, err)
		}
		fileEnv := parseDotEnv(data)
		if a.Env == nil {
			a.Env = map[string]string{}
		}
		for k, v := range fileEnv {
			if _, inline := a.Env[k]; !inline { // inline Env takes precedence
				a.Env[k] = v
			}
		}
	}
	return nil
}

// parseDotEnv parses dotenv content: KEY=VALUE per line, ignoring blanks and
// '#' comments. A leading "export " is stripped, key and value are trimmed, and
// a matched pair of surrounding single or double quotes is removed from the
// value. Only the first '=' splits the line, so values may contain '='.
func parseDotEnv(data []byte) map[string]string {
	out := map[string]string{}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		i := strings.IndexByte(line, '=')
		if i <= 0 { // no '=' or empty key
			continue
		}
		key := strings.TrimSpace(line[:i])
		if key == "" {
			continue
		}
		val := strings.TrimSpace(line[i+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		out[key] = val
	}
	return out
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
	if len(c.Apps) == 0 && c.Server == nil {
		return fmt.Errorf("config has no apps")
	}
	seen := map[string]bool{}
	for _, a := range c.Apps {
		if a.Name == "" {
			return fmt.Errorf("app with cmd %q has no name", a.Cmd)
		}
		if a.Name == "." || a.Name == ".." || !validAppName.MatchString(a.Name) {
			return fmt.Errorf("invalid app name %q: must match [A-Za-z0-9._-] and start alphanumeric", a.Name)
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
		if a.Source != nil {
			if err := a.Source.Validate(); err != nil {
				return fmt.Errorf("app %q: %w", a.Name, err)
			}
		}
	}
	return nil
}

// Validate rejects git-source fields that could be abused: repo/ref values that
// git would interpret as command-line options (leading "-"), and subdir values
// that escape the clone directory (absolute paths or ".." traversal). These
// fields flow into git argv and into the build/run working directory, so they
// must be confined before use. It is called both at config-parse time and again
// at the deploy sink, so resurrected state (dump.json) is re-checked.
func (s *GitSource) Validate() error {
	if s.Repo == "" {
		return fmt.Errorf("source.repo is required")
	}
	if strings.HasPrefix(s.Repo, "-") {
		return fmt.Errorf("source.repo %q must not start with '-'", s.Repo)
	}
	if strings.HasPrefix(s.Ref, "-") {
		return fmt.Errorf("source.ref %q must not start with '-'", s.Ref)
	}
	if s.Subdir != "" {
		if filepath.IsAbs(s.Subdir) {
			return fmt.Errorf("source.subdir %q must be relative", s.Subdir)
		}
		clean := filepath.Clean(s.Subdir)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("source.subdir %q escapes the repository", s.Subdir)
		}
	}
	return nil
}
