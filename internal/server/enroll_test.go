package server

import "testing"

func TestEnrollMinterAdapter(t *testing.T) {
	a, _, err := loadOrInitAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := enrollMinter{auth: a, fp: "deadbeef", fleetAddr: "[::]:9000"}
	if m.Fingerprint() != "deadbeef" {
		t.Fatalf("Fingerprint=%q", m.Fingerprint())
	}
	if m.FleetAddress() != "[::]:9000" {
		t.Fatalf("FleetAddress=%q", m.FleetAddress())
	}
	t1, err := m.RotateEnrollToken()
	if err != nil || t1 == "" {
		t.Fatalf("rotate1 token=%q err=%v", t1, err)
	}
	t2, err := m.RotateEnrollToken()
	if err != nil {
		t.Fatal(err)
	}
	if t1 == t2 {
		t.Fatal("expected a fresh token on re-rotate")
	}
	if !a.verifyEnroll(t2) {
		t.Fatal("latest enroll token must verify")
	}
	if a.verifyEnroll(t1) {
		t.Fatal("rotated-out token must be rejected")
	}
}
