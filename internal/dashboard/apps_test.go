package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/pb"
)

func postApps(t *testing.T, c *http.Client, base string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/apps", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAddAppRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp := postApps(t, srv.Client(), srv.URL, nil, `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/bin/true"}}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie add = %d; want 401", resp.StatusCode)
	}
}

func TestAddAppHappyPath(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	body := `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/usr/bin/node","args":["server.js"],"cwd":"/srv","instances":2,"env":{"PORT":"3000"},"restart":"on-failure","max_restarts":5,"kill_timeout":"7s"}}`
	resp := postApps(t, c, srv.URL, cookie, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != true {
		t.Fatalf("add body = %+v; want ok:true", got)
	}
	if fc.gotAgent != "dev-1" {
		t.Fatalf("agent = %q; want dev-1", fc.gotAgent)
	}
	apps := fc.gotOp.GetStart().GetApps()
	if len(apps) != 1 {
		t.Fatalf("apps = %d; want 1", len(apps))
	}
	a := apps[0]
	if a.GetName() != "web" || a.GetCmd() != "/usr/bin/node" || a.GetCwd() != "/srv" ||
		a.GetInstances() != 2 || a.GetRestart() != "on-failure" || a.GetMaxRestarts() != 5 ||
		a.GetKillTimeout() != "7s" || len(a.GetArgs()) != 1 || a.GetArgs()[0] != "server.js" ||
		a.GetEnv()["PORT"] != "3000" {
		t.Fatalf("spec = %+v", a)
	}
}

func TestAddAppUnsupportedSource(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"git","name":"web","cmd":"x"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("git source = %d; want 400", resp.StatusCode)
	}
}

func TestAddAppMissingFields(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	// missing cmd
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"command","name":"web"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing cmd = %d; want 400", resp.StatusCode)
	}
	// missing agent
	resp = postApps(t, c, srv.URL, cookie, `{"source":{"type":"command","name":"web","cmd":"x"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing agent = %d; want 400", resp.StatusCode)
	}
}

func TestAddAppValidationErrorPassthrough(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: false, Error: "app \"web\" already exists"}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/bin/true"}}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dup-name = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != false || got["error"] != "app \"web\" already exists" {
		t.Fatalf("dup-name body = %+v", got)
	}
}

func TestAddAppTransportErrorIs502(t *testing.T) {
	fc := &fakeController{err: errors.New("agent \"dev-1\" not connected")}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"command","name":"web","cmd":"/bin/true"}}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("transport error = %d; want 502", resp.StatusCode)
	}
}

func postRedeploy(t *testing.T, c *http.Client, base string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/apps/redeploy", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAppsGitSourceSendsDeployOp(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	body := `{"agent":"dev-1","source":{"type":"git","name":"web","cmd":"./server","repo":"https://example/r.git","ref":"main","build":"go build -o server ."}}`
	resp := postApps(t, c, srv.URL, cookie, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d; want 200", resp.StatusCode)
	}
	dep, ok := fc.gotOp.GetOp().(*pb.ControlOp_Deploy)
	if !ok {
		t.Fatalf("expected ControlOp_Deploy, got %T", fc.gotOp.GetOp())
	}
	if dep.Deploy.GetApp().GetSource().GetRepo() != "https://example/r.git" {
		t.Fatalf("repo not forwarded: %+v", dep.Deploy.GetApp().GetSource())
	}
}

func TestAppsGitSourceRequiresRepo(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	body := `{"agent":"dev-1","source":{"type":"git","name":"web","cmd":"./server"}}`
	resp := postApps(t, c, srv.URL, cookie, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRedeploySendsRedeployOp(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postRedeploy(t, c, srv.URL, cookie, `{"agent":"dev-1","name":"web"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d; want 200", resp.StatusCode)
	}
	rop, ok := fc.gotOp.GetOp().(*pb.ControlOp_Redeploy)
	if !ok || rop.Redeploy.GetTarget() != "web" {
		t.Fatalf("expected redeploy of web, got %+v", fc.gotOp.GetOp())
	}
}
