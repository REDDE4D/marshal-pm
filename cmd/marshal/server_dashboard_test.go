package main

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/server"
)

func TestServerPasswdSetsUser(t *testing.T) {
	dir := t.TempDir()

	// Feed the password via a pipe standing in for stdin (non-TTY path).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "hunter2\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	cmd := serverPasswdCmd()
	cmd.SetArgs([]string{"--data-dir", dir, "--user", "admin"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("passwd command: %v", err)
	}

	ok, err := server.HasDashboardUserDir(dir)
	if err != nil || !ok {
		t.Fatalf("HasDashboardUserDir = %v, %v", ok, err)
	}
	a, _, _ := server.LoadOrInitAuth(dir)
	if !a.VerifyDashboardUser("admin", "hunter2") {
		t.Fatal("password set by command not verifiable")
	}
}
