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
		"/etc/passwd",     // absolute
		"escape",          // symlink pointing outside root
		"escape/anything", // path through the escaping symlink
	}
	for _, rel := range bad {
		if got, err := confine(root, rel); err == nil {
			t.Errorf("confine(%q) = %q, want error (escape must be rejected)", rel, got)
		}
	}
}

func TestListDirOrdersDirsFirst(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "zdir"))
	mustMkdir(t, filepath.Join(root, "adir"))
	mustWrite(t, filepath.Join(root, "b.txt"), "x")
	mustWrite(t, filepath.Join(root, "a.txt"), "x")

	l, err := ListDir(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range l.GetEntries() {
		names = append(names, e.GetName())
	}
	want := []string{"adir", "zdir", "a.txt", "b.txt"} // dirs first (alpha), then files (alpha)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", names, want)
	}
	if !l.GetEntries()[0].GetIsDir() {
		t.Errorf("first entry should be a dir")
	}
}

func TestReadFileText(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "hello")
	fc, err := ReadFile(root, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(fc.GetContent()) != "hello" || fc.GetBinary() || fc.GetTruncated() {
		t.Errorf("got %+v, want content=hello binary=false truncated=false", fc)
	}
	if fc.GetSize() != 5 {
		t.Errorf("size = %d, want 5", fc.GetSize())
	}
}

func TestReadFileBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "b.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	fc, err := ReadFile(root, "b.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !fc.GetBinary() || len(fc.GetContent()) != 0 {
		t.Errorf("got binary=%v len(content)=%d, want binary=true content empty", fc.GetBinary(), len(fc.GetContent()))
	}
}

func TestReadFileTruncates(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("a", maxFileBytes+100)
	mustWrite(t, filepath.Join(root, "big.txt"), big)
	fc, err := ReadFile(root, "big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !fc.GetTruncated() || len(fc.GetContent()) != maxFileBytes {
		t.Errorf("got truncated=%v len=%d, want truncated=true len=%d", fc.GetTruncated(), len(fc.GetContent()), maxFileBytes)
	}
	if fc.GetSize() != int64(maxFileBytes+100) {
		t.Errorf("size = %d, want %d", fc.GetSize(), maxFileBytes+100)
	}
}

func TestReadFileRejectsDir(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "d"))
	if _, err := ReadFile(root, "d"); err == nil {
		t.Errorf("ReadFile on a directory should error")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
