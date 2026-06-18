package server

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
