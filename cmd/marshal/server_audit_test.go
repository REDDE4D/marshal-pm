package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/audit"
)

func runAudit(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := serverAuditCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("audit cmd: %v", err)
	}
	return out.String()
}

func TestServerAuditRendersAndFilters(t *testing.T) {
	dir := t.TempDir()
	l := audit.New(filepath.Join(dir, "login-audit.log"), audit.DefaultMaxBytes)
	base := time.Unix(0, 0).UTC()
	l.Record(audit.Event{Time: base, User: "admin", IP: "1.1.1.1", Outcome: audit.OutcomeSuccess})
	l.Record(audit.Event{Time: base.Add(time.Minute), User: "eve", IP: "2.2.2.2", Outcome: audit.OutcomeInvalid})

	all := runAudit(t, "--data-dir", dir)
	if !strings.Contains(all, "admin") || !strings.Contains(all, "eve") {
		t.Fatalf("output missing users:\n%s", all)
	}

	fails := runAudit(t, "--data-dir", dir, "--failures")
	if strings.Contains(fails, "admin") {
		t.Errorf("success leaked with --failures:\n%s", fails)
	}
	if !strings.Contains(fails, "eve") {
		t.Errorf("failure missing with --failures:\n%s", fails)
	}
}

func TestServerAuditEmpty(t *testing.T) {
	out := runAudit(t, "--data-dir", t.TempDir())
	if !strings.Contains(out, "no login attempts") {
		t.Fatalf("empty log should report no attempts; got:\n%s", out)
	}
}
