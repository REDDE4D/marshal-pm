package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfine(t *testing.T) {
	root := t.TempDir()
	// Build a small tree: root/sub/file.txt
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "file.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret outside root, and a symlink inside root pointing at it.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}

	ok := []struct{ rel, wantSuffix string }{
		{"", ""},                         // root itself
		{".", ""},                        // root itself
		{"sub", "sub"},                   // a dir
		{"sub/file.txt", "sub/file.txt"}, // a file
	}
	for _, c := range ok {
		got, err := confine(root, c.rel)
		if err != nil {
			t.Errorf("confine(%q) unexpected error: %v", c.rel, err)
			continue
		}
		if c.wantSuffix != "" && !strings.HasSuffix(got, c.wantSuffix) {
			t.Errorf("confine(%q) = %q, want suffix %q", c.rel, got, c.wantSuffix)
		}
	}

	bad := []string{
		"..",
		"../../etc/passwd",
		"sub/../../escapeout",
		"/etc/passwd",       // absolute
		"escape",            // symlink pointing outside root
		"escape/anything",   // path through the escaping symlink
	}
	for _, rel := range bad {
		if got, err := confine(root, rel); err == nil {
			t.Errorf("confine(%q) = %q, want error (escape must be rejected)", rel, got)
		}
	}
}
