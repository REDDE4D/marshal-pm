package dashboard

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/pb"
)

type fakeAuth struct{ user, pass string }

func (f fakeAuth) VerifyDashboardUser(u, p string) bool { return u == f.user && p == f.pass }
func (f fakeAuth) DashboardCredentialStamp(u string) (string, bool) {
	if u != f.user {
		return "", false
	}
	return "stamp-" + f.pass, true
}

type countingAuth struct {
	user, pass string
	calls      int
}

func (c *countingAuth) VerifyDashboardUser(u, p string) bool {
	c.calls++
	return u == c.user && p == c.pass
}

func (c *countingAuth) DashboardCredentialStamp(u string) (string, bool) {
	if u != c.user {
		return "", false
	}
	return "stamp", true
}

func sessionCookieFrom(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	return nil
}

func TestLoginFleetLogout(t *testing.T) {
	auth := fakeAuth{user: "admin", pass: "pw"}
	lister := fakeLister{agents: []*pb.AgentState{{AgentName: "dev-1", Connected: true}}}
	srv := httptest.NewServer(NewHandler(lister, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()

	// fleet without a cookie → 401
	resp, err := c.Get(srv.URL + "/api/fleet")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie fleet = %d; want 401", resp.StatusCode)
	}

	// bad login → 401
	resp, _ = c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login = %d; want 401", resp.StatusCode)
	}

	// good login → 200 + cookie
	resp, _ = c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good login = %d; want 200", resp.StatusCode)
	}
	cookie := sessionCookieFrom(resp)
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	// fleet with cookie → 200 + JSON
	req, _ := http.NewRequest("GET", srv.URL+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth fleet = %d; want 200", resp.StatusCode)
	}
	var got []agentView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "dev-1" {
		t.Fatalf("fleet json = %+v", got)
	}

	// logout → subsequent fleet → 401
	req, _ = http.NewRequest("POST", srv.URL+"/api/logout", nil)
	req.AddCookie(cookie)
	if _, err := c.Do(req); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, _ = c.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-logout fleet = %d; want 401", resp.StatusCode)
	}
}

func TestSPAFallback(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{}, time.Hour))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/some/client/route")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("spa fallback = %d; want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(strings.ToLower(string(b)), "<html") {
		t.Fatalf("expected index.html, got %q", string(b))
	}
}

func TestUnknownAPIRouteNotFound(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api route = %d; want 404", resp.StatusCode)
	}
}

func TestSessionSurvivesHandlerRestart(t *testing.T) {
	path := t.TempDir() + "/sessions.json"
	auth := fakeAuth{user: "admin", pass: "pw"}

	// First handler: log in, capture the cookie.
	h1 := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour, path, "")
	srv1 := httptest.NewServer(h1.mux)
	c1 := srv1.Client()
	resp, _ := c1.Post(srv1.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	cookie := sessionCookieFrom(resp)
	srv1.Close()
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	// Second handler at the same path (simulating a restart): the cookie still validates.
	h2 := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour, path, "")
	srv2 := httptest.NewServer(h2.mux)
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, _ = srv2.Client().Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post-restart fleet = %d; want 200 (session not persisted)", resp.StatusCode)
	}
}

func TestLoginLockoutReturns429(t *testing.T) {
	auth := &countingAuth{user: "admin", pass: "pw"}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()

	// Five bad logins from the same client → lockout.
	for i := 0; i < lockoutThreshold; i++ {
		resp, _ := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d; want 401", i, resp.StatusCode)
		}
	}
	callsBefore := auth.calls

	// The next attempt is locked: 429 + Retry-After, and no verify call.
	resp, _ := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("locked login = %d; want 429", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("locked login missing Retry-After header")
	}
	if auth.calls != callsBefore {
		t.Fatalf("VerifyDashboardUser called while locked (%d -> %d)", callsBefore, auth.calls)
	}
}

func TestLoginSuccessResetsLimiter(t *testing.T) {
	auth := &countingAuth{user: "admin", pass: "pw"}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, auth, time.Hour))
	defer srv.Close()
	c := srv.Client()

	// Four bad logins (one below the threshold), then a good one.
	for i := 0; i < lockoutThreshold-1; i++ {
		c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	}
	resp, _ := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("good login = %d; want 200", resp.StatusCode)
	}
	// The counter is reset, so a fresh bad login returns 401 (not 429).
	resp, _ = c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"nope"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-success bad login = %d; want 401", resp.StatusCode)
	}
}
