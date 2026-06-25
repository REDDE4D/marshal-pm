package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://github.com/x/y/releases/tag/"+tag)
		w.WriteHeader(http.StatusFound)
	}))
}

func TestCheckerRefreshUpdatesSnapshot(t *testing.T) {
	srv := newTestServer(t, "v0.7.2")
	defer srv.Close()

	c := New("v0.4.1", WithReleasesURL(srv.URL), WithHTTPClient(srv.Client()))
	// Before any refresh, latest is unknown and nothing is flagged.
	if s := c.Snapshot(); s.Latest != "" || s.Outdated {
		t.Fatalf("initial snapshot = %+v, want empty/!outdated", s)
	}
	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	s := c.Snapshot()
	if s.Current != "v0.4.1" || s.Latest != "v0.7.2" || !s.Outdated {
		t.Fatalf("snapshot = %+v, want current=v0.4.1 latest=v0.7.2 outdated=true", s)
	}
	if s.CheckedAt.IsZero() {
		t.Fatal("CheckedAt not set")
	}
}

func TestCheckerDisabledOptsOut(t *testing.T) {
	srv := newTestServer(t, "v9.9.9")
	defer srv.Close()

	c := New("v0.4.1", WithReleasesURL(srv.URL), WithHTTPClient(srv.Client()), WithEnabled(false))
	if c.Enabled() {
		t.Fatal("Enabled() = true, want false")
	}
	// refresh on a disabled checker is a no-op (no network, snapshot stays empty).
	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if s := c.Snapshot(); s.Latest != "" || s.Outdated {
		t.Fatalf("disabled snapshot = %+v, want empty", s)
	}
}
