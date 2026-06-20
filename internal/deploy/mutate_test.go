package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfineNew(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlinked parent escaping the root.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}

	ok := []string{"new.txt", "sub/new.txt", "deep/dir/new.txt"} // leaf/dirs may not exist
	for _, rel := range ok {
		got, err := confineNew(root, rel)
		if err != nil {
			t.Errorf("confineNew(%q) unexpected error: %v", rel, err)
			continue
		}
		if !strings.HasSuffix(got, filepath.FromSlash(rel)) {
			t.Errorf("confineNew(%q) = %q, want suffix %q", rel, got, rel)
		}
	}

	bad := []string{"..", "../escape.txt", "sub/../../escape.txt", "/etc/x", "out/escape.txt"}
	for _, rel := range bad {
		if got, err := confineNew(root, rel); err == nil {
			t.Errorf("confineNew(%q) = %q, want error", rel, got)
		}
	}
}

func TestIsGitInternal(t *testing.T) {
	for _, rel := range []string{".git", ".git/config", ".git/refs/heads/main"} {
		if !isGitInternal(rel) {
			t.Errorf("isGitInternal(%q) = false, want true", rel)
		}
	}
	for _, rel := range []string{"src/.git.go", "agitator", "sub/file.txt"} {
		if isGitInternal(rel) {
			t.Errorf("isGitInternal(%q) = true, want false", rel)
		}
	}
}
