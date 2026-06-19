package deploy

import (
	"fmt"
	"path/filepath"
	"strings"
)

// confine resolves a caller-supplied relative path against a trusted root and
// guarantees the result stays inside root. It rejects absolute paths and any
// path that escapes via "..", and resolves symlinks so a symlink inside the
// tree cannot point outside it. Returns the absolute, symlink-resolved path.
func confine(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths not allowed")
	}
	// Lexical containment check: join root+rel, clean, then verify we stay inside root.
	// filepath.Clean(filepath.Join(root, rel)) correctly propagates ".." escapes before
	// any symlink resolution, so ".."-based attacks are caught here.
	full := filepath.Clean(filepath.Join(root, rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root")
	}

	// Defeat symlink escape: resolve symlinks on both sides and re-check
	// containment against the *real* root.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	realFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err // includes "does not exist"
	}
	if realFull != realRoot && !strings.HasPrefix(realFull, realRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes deploy root via symlink")
	}
	return realFull, nil
}
