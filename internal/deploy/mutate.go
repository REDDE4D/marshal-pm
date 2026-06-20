package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// confineNew is confine's sibling for paths that may not exist yet (create /
// rename destination). It does the same lexical containment check, then resolves
// the deepest *existing* ancestor via EvalSymlinks and verifies that stays inside
// the real root, defeating a symlinked parent directory. Returns generic errors.
func confineNew(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	full := filepath.Clean(filepath.Join(root, rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root")
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("deploy root unavailable")
	}
	// Walk up to the deepest existing ancestor and resolve it.
	anc := full
	var tail []string
	for {
		if anc == root {
			break
		}
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		tail = append([]string{filepath.Base(anc)}, tail...)
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	realAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", fmt.Errorf("path escapes deploy root")
	}
	if realAnc != realRoot && !strings.HasPrefix(realAnc, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root via symlink")
	}
	return filepath.Join(append([]string{realAnc}, tail...)...), nil
}

// isGitInternal reports whether rel addresses the clone's own .git directory.
func isGitInternal(rel string) bool {
	clean := filepath.Clean(filepath.FromSlash(rel))
	parts := strings.Split(clean, string(filepath.Separator))
	return len(parts) > 0 && parts[0] == ".git"
}
