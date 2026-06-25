package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeMinter struct {
	tok   string
	fp    string
	addr  string
	err   error
	calls int
}

func (f *fakeMinter) RotateEnrollToken() (string, error) { f.calls++; return f.tok, f.err }
func (f *fakeMinter) Fingerprint() string                { return f.fp }
func (f *fakeMinter) FleetAddress() string               { return f.addr }

func newConnectHandler(t *testing.T, m EnrollMinter) *handler {
	t.Helper()
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", nil, nil)
	h.enroll = m
	return h
}

func TestConnectTokenRequiresSession(t *testing.T) {
	h := newConnectHandler(t, &fakeMinter{tok: "T", fp: "FP", addr: ":9000"})
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/fleet/connect-token", strings.NewReader(`{}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-session = %d; want 401", resp.StatusCode)
	}
}

func TestConnectTokenReturnsMintedFields(t *testing.T) {
	m := &fakeMinter{tok: "enroll-XYZ", fp: "abc123", addr: ":9000"}
	h := newConnectHandler(t, m)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	req, _ := http.NewRequest("POST", srv.URL+"/api/fleet/connect-token", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("= %d; want 200", resp.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["token"] != "enroll-XYZ" {
		t.Fatalf("token=%q", got["token"])
	}
	if got["fingerprint"] != "abc123" {
		t.Fatalf("fingerprint=%q", got["fingerprint"])
	}
	if !strings.HasSuffix(got["default_address"], ":9000") {
		t.Fatalf("default_address=%q; want host:9000", got["default_address"])
	}
	if m.calls != 1 {
		t.Fatalf("RotateEnrollToken calls=%d; want 1", m.calls)
	}
}

func TestConnectTokenUnavailableWhenNilMinter(t *testing.T) {
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", nil, nil)
	// h.enroll left nil
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	req, _ := http.NewRequest("POST", srv.URL+"/api/fleet/connect-token", strings.NewReader(`{}`))
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil minter = %d; want 503", resp.StatusCode)
	}
}

func TestDefaultConnectAddress(t *testing.T) {
	cases := []struct{ reqHost, fleet, want string }{
		{"127.0.0.1:9001", ":9000", "127.0.0.1:9000"},
		{"example.com:9001", "0.0.0.0:9000", "example.com:9000"},
		{"host-no-port", ":9000", "host-no-port:9000"},
	}
	for _, c := range cases {
		if got := defaultConnectAddress(c.reqHost, c.fleet); got != c.want {
			t.Errorf("defaultConnectAddress(%q,%q)=%q; want %q", c.reqHost, c.fleet, got, c.want)
		}
	}
}
