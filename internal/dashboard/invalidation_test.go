package dashboard

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stampAuth is a mutable Authenticator: tests flip stamp/known to simulate a
// password change or a deleted user between requests.
type stampAuth struct {
	user, pass string
	stamp      string
	known      bool
}

func (s *stampAuth) VerifyDashboardUser(u, p string) bool { return u == s.user && p == s.pass }
func (s *stampAuth) DashboardCredentialStamp(u string) (string, bool) {
	if !s.known || u != s.user {
		return "", false
	}
	return s.stamp, true
}

func fleetStatus(t *testing.T, c *http.Client, base string, ck *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/fleet", nil)
	req.AddCookie(ck)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestSessionSurvivesUnchangedStamp(t *testing.T) {
	auth := &stampAuth{user: "admin", pass: "pw", stamp: "s1", known: true}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()
	ck := loginCookie(t, c, srv.URL)
	if got := fleetStatus(t, c, srv.URL, ck); got != http.StatusOK {
		t.Fatalf("fleet with unchanged stamp = %d; want 200", got)
	}
}

func TestSessionDiesOnStampChange(t *testing.T) {
	auth := &stampAuth{user: "admin", pass: "pw", stamp: "s1", known: true}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()
	ck := loginCookie(t, c, srv.URL)
	auth.stamp = "s2" // password changed under the session
	if got := fleetStatus(t, c, srv.URL, ck); got != http.StatusUnauthorized {
		t.Fatalf("fleet after stamp change = %d; want 401", got)
	}
}

func TestSessionDiesWhenUserGone(t *testing.T) {
	auth := &stampAuth{user: "admin", pass: "pw", stamp: "s1", known: true}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()
	ck := loginCookie(t, c, srv.URL)
	auth.known = false // user deleted under the session
	if got := fleetStatus(t, c, srv.URL, ck); got != http.StatusUnauthorized {
		t.Fatalf("fleet after user removed = %d; want 401", got)
	}
}
