package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/dashboard"
	"marshal/internal/pb"
)

// emptyLister is a minimal dashboard.FleetLister stub for tests that don't
// need real agent data.
type emptyLister struct{}

func (emptyLister) List() []*pb.AgentState { return nil }

func TestServeDirStartsDashboard(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "pw"); err != nil {
		t.Fatal(err)
	}

	gl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Reserve a port for the dashboard, then release it for ServeDir to bind.
	hl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpAddr := hl.Addr().String()
	_ = hl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeDir(ctx, gl, dir, "", "", httpAddr) }()

	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	base := "https://" + httpAddr

	// Wait for the dashboard to come up.
	var resp *http.Response
	for i := 0; i < 100; i++ {
		resp, err = client.Get(base + "/api/fleet")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dashboard never came up: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("fleet without cookie = %d; want 401", resp.StatusCode)
	}

	resp, err = client.Post(base+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200", resp.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "marshal_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}

	req, _ := http.NewRequest("GET", base+"/api/fleet", nil)
	req.AddCookie(cookie)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated fleet = %d; want 200", resp.StatusCode)
	}
}

func TestDashboardSessionDiesAfterPasswordChange(t *testing.T) {
	dir := t.TempDir()
	if err := SetDashboardPassword(dir, "admin", "old"); err != nil {
		t.Fatal(err)
	}
	a, _, err := LoadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := dashboard.NewHandler(emptyLister{}, nil, nil, nil, a, time.Hour)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := srv.Client()

	resp, err := c.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"old"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200", resp.StatusCode)
	}
	var ck *http.Cookie
	for _, x := range resp.Cookies() {
		if x.Name == "marshal_session" {
			ck = x
		}
	}
	if ck == nil {
		t.Fatal("no session cookie")
	}

	status := func() int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/fleet", nil)
		req.AddCookie(ck)
		r, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r.StatusCode
	}

	if got := status(); got != http.StatusOK {
		t.Fatalf("fleet before change = %d; want 200", got)
	}

	// Change the password on disk (as `server passwd` would) and hot-reload.
	if err := SetDashboardPassword(dir, "admin", "new"); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != http.StatusUnauthorized {
		t.Fatalf("fleet after password change = %d; want 401", got)
	}
}
