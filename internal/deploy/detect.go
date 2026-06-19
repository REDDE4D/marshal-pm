// Package deploy clones, builds, and launches apps from git sources (M21).
package deploy

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DetectBuild infers a shell build command for a checkout when the user did
// not supply one. Returns "" when no build step is needed (run as-is). The
// table is intentionally small (Go + Node); an explicit build always wins
// upstream, so this is only a convenience default.
func DetectBuild(dir string) string {
	if exists(filepath.Join(dir, "go.mod")) {
		return "go build ./..."
	}
	if pkg := filepath.Join(dir, "package.json"); exists(pkg) {
		if hasNpmBuildScript(pkg) {
			return "npm ci && npm run build"
		}
		return "npm ci"
	}
	return ""
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func hasNpmBuildScript(pkgPath string) bool {
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false
	}
	_, ok := pkg.Scripts["build"]
	return ok
}
