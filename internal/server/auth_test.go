package server

import (
	"path/filepath"
	"testing"
)

func TestRotateInvalidatesOldToken(t *testing.T) {
	dir := t.TempDir()
	a, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if secrets == nil {
		t.Fatal("expected fresh secrets on first init")
	}

	// Old admin token verifies before rotate.
	if !a.verifyAdmin(secrets.AdminToken) {
		t.Fatal("initial admin token should verify")
	}

	// Rotate the admin token.
	newTok, err := a.rotate("admin")
	if err != nil {
		t.Fatalf("rotate admin: %v", err)
	}

	// Old token must no longer verify.
	if a.verifyAdmin(secrets.AdminToken) {
		t.Fatal("old admin token must not verify after rotate")
	}

	// New token must verify.
	if !a.verifyAdmin(newTok) {
		t.Fatal("new admin token must verify after rotate")
	}

	// Persistence: reload and confirm new token still works.
	a2, secrets2, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if secrets2 != nil {
		t.Fatal("expected nil secrets on reload")
	}
	if !a2.verifyAdmin(newTok) {
		t.Fatal("new admin token must verify after reload")
	}
	if a2.verifyAdmin(secrets.AdminToken) {
		t.Fatal("old admin token must not verify after reload")
	}

	// Bogus token kind must error.
	if _, err := a.rotate("bogus"); err == nil {
		t.Fatal("rotate(bogus) must return an error")
	}
}

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
