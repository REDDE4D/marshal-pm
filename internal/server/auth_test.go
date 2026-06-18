package server

import (
	"path/filepath"
	"testing"
)

func TestLoadOrInitAuthGeneratesSecretsOnce(t *testing.T) {
	dir := t.TempDir()
	a, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if secrets == nil || secrets.EnrollToken == "" || secrets.AdminToken == "" {
		t.Fatal("expected fresh secrets on first init")
	}
	if !a.verifyAdmin(secrets.AdminToken) || !a.verifyEnroll(secrets.EnrollToken) {
		t.Fatal("generated secrets do not verify")
	}
	if a.verifyAdmin(secrets.EnrollToken) {
		t.Fatal("enroll token must not pass as admin")
	}
	// Reload: existing file, no new secrets, same verification.
	a2, secrets2, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if secrets2 != nil {
		t.Fatal("expected nil secrets on reload")
	}
	if !a2.verifyAdmin(secrets.AdminToken) {
		t.Fatal("admin token broke across reload")
	}
	if _, err := filepath.Abs(a.path); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollAndAuthAgent(t *testing.T) {
	dir := t.TempDir()
	a, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := a.enrollAgent("dev-1")
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := a.authAgent(tok); !ok || name != "dev-1" {
		t.Fatalf("authAgent = %q,%v", name, ok)
	}
	if _, err := a.enrollAgent("dev-1"); err == nil {
		t.Fatal("re-enrolling an existing name must error")
	}
	// Survives reload.
	a2, _, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := a2.authAgent(tok); !ok || name != "dev-1" {
		t.Fatalf("agent token broke across reload: %q,%v", name, ok)
	}
	if !a2.removeAgent("dev-1") {
		t.Fatal("removeAgent should report true")
	}
	if _, ok := a2.authAgent(tok); ok {
		t.Fatal("revoked agent still authenticates")
	}
}
