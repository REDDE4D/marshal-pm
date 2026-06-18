package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateCertGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fp == "" || len(cert.Certificate) == 0 {
		t.Fatal("empty cert or fingerprint")
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestLoadOrCreateCertIsStableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	_, fp1, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, fp2, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if fp1 != fp2 {
		t.Fatalf("fingerprint changed across calls: %s vs %s", fp1, fp2)
	}
}
