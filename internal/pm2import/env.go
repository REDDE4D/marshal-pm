package pm2import

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var unsafeFilenameChar = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// envFileName derives a safe `<name>.env` filename from an app name.
func envFileName(name string) string {
	s := unsafeFilenameChar.ReplaceAllString(name, "_")
	s = strings.Trim(s, "._-")
	if s == "" {
		s = "app"
	}
	return s + ".env"
}

// renderDotEnv renders an env map as sorted KEY=VALUE lines.
func renderDotEnv(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return []byte(b.String())
}

// SplitEnvFiles writes each app's inline env to a 0600 `<name>.env` file in dir,
// replaces the app's inline Env with an env_file reference (the basename, which
// Marshal resolves relative to the marshal.yaml), and clears the inline Env.
// This keeps resolved secrets out of the generated marshal.yaml. Apps with no
// env are left untouched. Returns the list of files written.
func (c *Config) SplitEnvFiles(dir string) ([]string, error) {
	var written []string
	for i := range c.Apps {
		a := &c.Apps[i]
		if len(a.Env) == 0 {
			continue
		}
		fname := envFileName(a.Name)
		if err := os.WriteFile(filepath.Join(dir, fname), renderDotEnv(a.Env), 0o600); err != nil {
			return written, fmt.Errorf("write env file for %q: %w", a.Name, err)
		}
		a.EnvFile = fname
		a.Env = nil
		written = append(written, fname)
	}
	return written, nil
}
