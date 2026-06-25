package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOutdated(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"v0.4.0", "v0.4.1", true},
		{"v0.4.1", "v0.4.1", false},
		{"v0.4.1", "v0.4.0", false},        // never "downgrade"
		{"v0.4.0", "v0.5.0", true},         // minor
		{"v0.9.0", "v1.0.0", true},         // major
		{"v1.2.3", "v1.2.10", true},        // numeric, not lexical, patch compare
		{"0.4.0", "v0.4.1", true},          // tolerate missing leading v
		{"", "v0.4.1", false},              // unknown current → don't nag
		{"v0.4.0", "", false},              // unknown latest → don't nag
		{"0.0.0-dev", "v0.4.1", false},     // local dev build → don't nag
		{"v0.4.0-3-gabc", "v0.4.1", true},  // git-describe build compares on the base
		{"v0.4.1-3-gabc", "v0.4.1", false}, // ahead of the release → not outdated
	}
	for _, c := range cases {
		if got := Outdated(c.current, c.latest); got != c.want {
			t.Errorf("Outdated(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestFetchLatestParsesRedirect(t *testing.T) {
	// GitHub's /releases/latest 302-redirects to /releases/tag/vX.Y.Z.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/REDDE4D/marshal-pm/releases/latest" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Location", "https://github.com/REDDE4D/marshal-pm/releases/tag/v0.7.2")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	got, err := fetchLatest(context.Background(), srv.Client(), srv.URL+"/REDDE4D/marshal-pm/releases/latest")
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	if got != "v0.7.2" {
		t.Fatalf("latest = %q, want v0.7.2", got)
	}
}

func TestFetchLatestDoesNotFollowRedirect(t *testing.T) {
	// If the client followed the redirect it would hit the tag page (200) and
	// fail to parse; the function must read the Location of the first response.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits > 1 {
			t.Errorf("redirect was followed (%d hits)", hits)
		}
		w.Header().Set("Location", "/releases/tag/v1.0.0")
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	got, err := fetchLatest(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetchLatest: %v", err)
	}
	if got != "v1.0.0" {
		t.Fatalf("latest = %q, want v1.0.0", got)
	}
}
