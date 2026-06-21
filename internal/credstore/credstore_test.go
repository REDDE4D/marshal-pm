package credstore

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
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

func TestCredentialsFileMode(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Put("gh-ci", "octocat", "ghp_x")
	fi, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("credentials.json mode = %v, want 0600", fi.Mode().Perm())
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

func TestGenerateAndGetKey(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.Generate("deploykey")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Fatalf("public key = %q, want ssh-ed25519 prefix", pub)
	}

	// Meta exposes the public key + type, never the private key.
	metas := s.List()
	if len(metas) != 1 || metas[0].Type != "ssh-key" || metas[0].PublicKey != pub {
		t.Fatalf("meta = %+v", metas)
	}

	// The private key round-trips via GetKey...
	priv, kh, ok, err := s.GetKey("deploykey")
	if err != nil || !ok {
		t.Fatalf("GetKey ok=%v err=%v", ok, err)
	}
	if !strings.Contains(priv, "PRIVATE KEY") {
		t.Fatalf("private key not returned: %q", priv)
	}
	if kh != "" {
		t.Fatalf("known_hosts should start empty, got %q", kh)
	}

	// ...but is NOT present in plaintext on disk.
	raw, _ := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if bytes.Contains(raw, []byte(priv)) {
		t.Fatal("private key leaked to credentials.json in plaintext")
	}

	// SetKnownHosts persists the pin.
	if err := s.SetKnownHosts("deploykey", "github.com ssh-ed25519 AAAA"); err != nil {
		t.Fatal(err)
	}
	_, kh2, _, _ := s.GetKey("deploykey")
	if kh2 != "github.com ssh-ed25519 AAAA" {
		t.Fatalf("pin not persisted: %q", kh2)
	}
}

func TestGetRejectsSSHKeyEntry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Generate("dk"); err != nil {
		t.Fatal(err)
	}
	u, tk, ok, err := s.Get("dk")
	if err != nil {
		t.Fatalf("Get returned unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("Get returned ok=true for an ssh-key entry; want ok=false")
	}
	if tk != "" {
		t.Fatalf("Get returned non-empty token %q for ssh-key entry; private key must not leak via Get", tk)
	}
	_ = u // username is also empty, but the critical check is ok==false and tk==""
}

func TestHTTPSEntriesStillWork(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put("tok", "octocat", "ghp_xxx"); err != nil {
		t.Fatal(err)
	}
	u, tk, ok, err := s.Get("tok")
	if err != nil || !ok || u != "octocat" || tk != "ghp_xxx" {
		t.Fatalf("https get broke: u=%q tk=%q ok=%v err=%v", u, tk, ok, err)
	}
	if m := s.List(); m[0].Type != "https-token" || m[0].PublicKey != "" {
		t.Fatalf("https meta wrong: %+v", m[0])
	}
}
