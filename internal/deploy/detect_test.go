package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectBuild(t *testing.T) {
	cases := []struct {
		name  string
		setup func(dir string)
		want  string
	}{
		{"go module", func(d string) { write(t, d, "go.mod", "module x\n") }, "go build ./..."},
		{"node with build script",
			func(d string) { write(t, d, "package.json", `{"scripts":{"build":"vite build"}}`) },
			"npm ci && npm run build"},
		{"node without build script",
			func(d string) { write(t, d, "package.json", `{"scripts":{"start":"node ."}}`) },
			"npm ci"},
		{"node with no scripts key",
			func(d string) { write(t, d, "package.json", `{"name":"x"}`) },
			"npm ci"},
		{"empty repo", func(d string) {}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(dir)
			if got := DetectBuild(dir); got != tc.want {
				t.Fatalf("DetectBuild=%q want %q", got, tc.want)
			}
		})
	}
}
