package logs

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGz(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	if _, err := zw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func TestFileBackfillAcrossSegments(t *testing.T) {
	dir := t.TempDir()
	// Oldest -> newest. Rotated names sort lexically by lumberjack's timestamp.
	writeGz(t, filepath.Join(dir, "app#0.out-2026-06-17T10-00-00.000.log.gz"), "a1\na2\n")
	writeFile(t, filepath.Join(dir, "app#0.out-2026-06-17T11-00-00.000.log"), "b1\nb2\n")
	writeFile(t, filepath.Join(dir, "app#0.out.log"), "c1\nc2\n") // active = newest

	got, err := fileBackfill(dir, "app#0", false, 5)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a2", "b1", "b2", "c1", "c2"}
	if g := texts(got); !equalStrings(g, want) {
		t.Fatalf("got %v want %v", g, want)
	}
}

func TestFileBackfillStreamSeparation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app#0.out.log"), "out1\nout2\n")
	writeFile(t, filepath.Join(dir, "app#0.err.log"), "err1\n")
	got, err := fileBackfill(dir, "app#0", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if g := texts(got); !equalStrings(g, []string{"err1"}) {
		t.Fatalf("stderr stream: got %v", g)
	}
}

func TestFileBackfillNoFiles(t *testing.T) {
	got, err := fileBackfill(t.TempDir(), "missing#0", false, 10)
	if err != nil {
		t.Fatalf("absent files must not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %v", texts(got))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
