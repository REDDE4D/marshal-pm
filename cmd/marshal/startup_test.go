package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// startup --system never touches the real init system: it stages the unit file
// under the state dir and prints the sudo block. Safe to run in CI on the host OS
// (both darwin and linux stage+print for --system).
func TestStartupSystemStagesAndPrints(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)

	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"startup", "--system"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "sudo") {
		t.Fatalf("expected a sudo block, got:\n%s", out.String())
	}
	staged, _ := filepath.Glob(filepath.Join(data, "marshal", "*"))
	if len(staged) == 0 {
		t.Fatalf("no staged unit file under %s/marshal", data)
	}
}
