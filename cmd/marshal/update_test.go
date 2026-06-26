package main

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/pb"
)

func TestUpdateBanner(t *testing.T) {
	// Outdated → formatted line containing both versions and the releases URL.
	got := updateBanner(&pb.UpdateInfo{Current: "v0.11.0", Latest: "v0.12.0", Outdated: true})
	for _, want := range []string{"v0.12.0", "v0.11.0", "update available", "releases"} {
		if !contains(got, want) {
			t.Fatalf("banner %q missing %q", got, want)
		}
	}
	// Not outdated → empty.
	if s := updateBanner(&pb.UpdateInfo{Current: "v0.12.0", Latest: "v0.12.0", Outdated: false}); s != "" {
		t.Fatalf("up-to-date should yield empty banner, got %q", s)
	}
	// Outdated but empty latest → empty (no data yet).
	if s := updateBanner(&pb.UpdateInfo{Outdated: true, Latest: ""}); s != "" {
		t.Fatalf("empty latest should yield empty banner, got %q", s)
	}
	// nil → empty.
	if s := updateBanner(nil); s != "" {
		t.Fatalf("nil should yield empty banner, got %q", s)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
