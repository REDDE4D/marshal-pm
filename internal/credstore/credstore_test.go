package credstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPutGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Put("gh-ci", "octocat", "ghp_secret123"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	user, tok, ok, err := s.Get("gh-ci")
	if err != nil || !ok {
		t.Fatalf("Get ok=%v err=%v", ok, err)
	}
	if user != "octocat" || tok != "ghp_secret123" {
		t.Fatalf("got %q/%q", user, tok)
	}
}

func TestListHasNoSecret(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put("gh-ci", "octocat", "ghp_secret123")
	metas := s.List()
	if len(metas) != 1 || metas[0].Name != "gh-ci" || metas[0].Username != "octocat" {
		t.Fatalf("meta: %+v", metas)
	}
	// The on-disk file must not contain the plaintext token.
	b, _ := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if string(b) == "" || containsPlaintext(b, "ghp_secret123") {
		t.Fatalf("plaintext token leaked to disk: %s", b)
	}
}

func TestPutRotatesAndDelete(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put("gh-ci", "octocat", "old")
	_ = s.Put("gh-ci", "octocat", "new") // rotate
	_, tok, _, _ := s.Get("gh-ci")
	if tok != "new" {
		t.Fatalf("rotate failed, got %q", tok)
	}
	if !s.Delete("gh-ci") {
		t.Fatalf("Delete returned false")
	}
	if _, _, ok, _ := s.Get("gh-ci"); ok {
		t.Fatalf("still present after delete")
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, _ := Open(dir)
	_ = s1.Put("gh-ci", "octocat", "ghp_secret123")
	s2, err := Open(dir) // reuses master.key on disk
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_, tok, ok, _ := s2.Get("gh-ci")
	if !ok || tok != "ghp_secret123" {
		t.Fatalf("reopen lost data: ok=%v tok=%q", ok, tok)
	}
}

func TestMasterKeyFileMode(t *testing.T) {
	dir := t.TempDir()
	_, _ = Open(dir)
	fi, err := os.Stat(filepath.Join(dir, "master.key"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("master.key mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestInvalidEnvKey(t *testing.T) {
	t.Setenv("MARSHAL_MASTER_KEY", "not-base64-or-wrong-len")
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatalf("expected error on invalid MARSHAL_MASTER_KEY")
	}
}

func TestBadName(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Put("../escape", "u", "t"); err == nil {
		t.Fatalf("expected name validation error")
	}
	if err := s.Put("ok", "u", ""); err == nil {
		t.Fatalf("expected empty-token error")
	}
}

// containsPlaintext reports whether b contains needle as a raw substring.
func containsPlaintext(b []byte, needle string) bool {
	return len(needle) > 0 && bytesContains(b, []byte(needle))
}
func bytesContains(b, sub []byte) bool {
	for i := 0; i+len(sub) <= len(b); i++ {
		if string(b[i:i+len(sub)]) == string(sub) {
			return true
		}
	}
	return false
}
