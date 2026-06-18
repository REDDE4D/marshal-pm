package main

import (
	"bytes"
	"strings"
	"testing"

	"marshal/internal/server"
)

func TestServerFingerprintCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir) // so defaultServerDataDir resolves under temp
	cmd := serverCmd()
	cmd.SetArgs([]string{"fingerprint"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(out.String())) < 32 {
		t.Fatalf("expected a fingerprint, got %q", out.String())
	}
}

func TestServerTokenRotateCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	for _, which := range []string{"enroll", "admin"} {
		t.Run(which, func(t *testing.T) {
			cmd := serverCmd()
			cmd.SetArgs([]string{"token", "--rotate", which})
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if !strings.Contains(got, which) {
				t.Fatalf("expected %q in output, got %q", which, got)
			}
			// token should be non-trivially long
			parts := strings.Fields(got)
			tok := parts[len(parts)-1]
			if len(tok) < 16 {
				t.Fatalf("token too short: %q", tok)
			}
		})
	}
}

func TestServerTokenRotateMissingFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	cmd := serverCmd()
	cmd.SetArgs([]string{"token"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error when --rotate not provided")
	}
}

func TestServerAgentLsCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	dataDir := defaultServerDataDir()

	// Enroll a couple of agents directly via the server package.
	a, _, err := server.LoadOrInitAuth(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnrollAgent("agent-alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnrollAgent("agent-beta"); err != nil {
		t.Fatal(err)
	}

	cmd := serverCmd()
	cmd.SetArgs([]string{"agent", "ls"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "agent-alpha") || !strings.Contains(got, "agent-beta") {
		t.Fatalf("expected both agents in ls output, got:\n%s", got)
	}
}

func TestServerAgentRmCmd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	dataDir := defaultServerDataDir()

	a, _, err := server.LoadOrInitAuth(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.EnrollAgent("agent-delta"); err != nil {
		t.Fatal(err)
	}

	// rm the agent
	cmd := serverCmd()
	cmd.SetArgs([]string{"agent", "rm", "agent-delta"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed") {
		t.Fatalf("expected 'removed' in output, got %q", out.String())
	}

	// rm a nonexistent agent should error
	cmd2 := serverCmd()
	cmd2.SetArgs([]string{"agent", "rm", "no-such-agent"})
	cmd2.SilenceErrors = true
	cmd2.SilenceUsage = true
	if err := cmd2.Execute(); err == nil {
		t.Fatal("expected error for missing agent")
	}
}
