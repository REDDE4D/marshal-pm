package errsig

import "testing"

func TestIsError(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"panic: nil pointer dereference", true},
		{"ERROR: connection refused", true},
		{"Traceback (most recent call last):", true},
		{"level=error msg=\"boom\"", true},
		{"plain stderr line with no level", true}, // stderr default
		{"level=info msg=started", false},
		{"[INFO] listening on :8080", false},
		{"level=warn retrying", false},
		{"DEBUG cache miss", false},
		{"INFO listening", false},
		{"connection pool info failed to allocate", true}, // "info" mid-message, no leading level
		{"retry warning backoff exceeded", true},          // "warning" mid-message
		{"disk space info: 0 bytes remaining", true},      // "info" mid-message
		{"ERROR: warn subsystem down", true},              // error marker wins despite "warn"
	}
	for _, c := range cases {
		if got := IsError(c.text); got != c.want {
			t.Errorf("IsError(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestNormalizeCollapsesVariants(t *testing.T) {
	a := Normalize("2026-06-24T10:00:00Z connection to 10.0.0.5:5432 failed after 1.5s")
	b := Normalize("2026-06-24T11:22:33Z connection to 10.0.0.9:6000 failed after 240ms")
	if a != b {
		t.Fatalf("variants did not collapse:\n a=%q\n b=%q", a, b)
	}
	if a == "" {
		t.Fatal("normalized to empty")
	}
}

func TestNormalizeKeepsDistinct(t *testing.T) {
	if Normalize("disk full") == Normalize("connection refused") {
		t.Fatal("distinct messages collapsed")
	}
}

func TestSignatureStableAndShort(t *testing.T) {
	s1 := Signature("error code 42 at 10.0.0.1:1")
	s2 := Signature("error code 99 at 10.0.0.2:2")
	if s1 != s2 {
		t.Fatalf("variant signatures differ: %s vs %s", s1, s2)
	}
	if len(s1) != 12 {
		t.Fatalf("signature length = %d, want 12", len(s1))
	}
}
