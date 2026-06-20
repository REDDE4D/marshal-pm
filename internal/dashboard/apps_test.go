package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/credstore"
	"marshal/internal/pb"
)

// fakeCreds is a test-only implementation of the Credentials interface.
// It is used to test SSH credential resolution without real disk I/O.
type fakeCreds struct {
	metas []credstore.Meta
	priv  string // private key returned by GetKey
	kh    string // known_hosts returned by GetKey

	setKH string // captured value passed to SetKnownHosts

	// HTTPS fields (for Get)
	httpsUser string
	httpsTok  string
	httpsOk   bool
}

func (f *fakeCreds) List() []credstore.Meta { return f.metas }

func (f *fakeCreds) Get(name string) (string, string, bool, error) {
	if f.httpsOk {
		return f.httpsUser, f.httpsTok, true, nil
	}
	return "", "", false, nil
}

func (f *fakeCreds) Put(name, user, tok string) error { return nil }

func (f *fakeCreds) Generate(name string) (string, error) { return "ssh-ed25519 FAKE", nil }

func (f *fakeCreds) GetKey(name string) (string, string, bool, error) {
	return f.priv, f.kh, f.priv != "", nil
}

func (f *fakeCreds) SetKnownHosts(name, line string) error {
	f.setKH = line
	f.kh = line
	return nil
}

func (f *fakeCreds) Delete(name string) bool { return true }

func TestSSHHostPort(t *testing.T) {
	cases := []struct{ repo, host, port string }{
		{"git@github.com:o/r.git", "github.com", ""},
		{"ssh://git@ssh.github.com:443/o/r.git", "ssh.github.com", "443"},
		{"ssh://git@example.com/o/r.git", "example.com", ""},
	}
	for _, c := range cases {
		h, p := sshHostPort(c.repo)
		if h != c.host || p != c.port {
			t.Fatalf("%s -> (%q,%q), want (%q,%q)", c.repo, h, p, c.host, c.port)
		}
	}
}

func TestResolveSSHScansAndPins(t *testing.T) {
	fc := &fakeCreds{
		metas: []credstore.Meta{{Name: "dk", Type: "ssh-key", PublicKey: "ssh-ed25519 AAAA"}},
		priv:  "PRIVKEY",
		kh:    "", // no pin yet → must scan
	}
	h := newTestHandlerWithCreds(t, fc)
	var scanCalled bool
	h.scanHost = func(hp string) (string, error) {
		if hp != "github.com" {
			t.Fatalf("scanned %q, want github.com", hp)
		}
		scanCalled = true
		return "github.com ssh-ed25519 SCANNED", nil
	}

	cred, err := h.resolveCredential("dk", "git@github.com:o/r.git")
	if err != nil {
		t.Fatal(err)
	}
	if cred.GetKind() != pb.CredentialKind_CRED_SSH || cred.GetPrivateKey() != "PRIVKEY" {
		t.Fatalf("cred = %+v", cred)
	}
	if cred.GetKnownHosts() != "github.com ssh-ed25519 SCANNED" {
		t.Fatalf("known_hosts = %q", cred.GetKnownHosts())
	}
	if !scanCalled {
		t.Fatal("scanHost was not called")
	}
	if fc.setKH != "github.com ssh-ed25519 SCANNED" {
		t.Fatal("pin was not persisted via SetKnownHosts")
	}
}

func TestResolveSSHAlreadyPinnedSkipsScan(t *testing.T) {
	fc := &fakeCreds{
		metas: []credstore.Meta{{Name: "dk", Type: "ssh-key"}},
		priv:  "PRIVKEY",
		kh:    "github.com ssh-ed25519 PINNED",
	}
	h := newTestHandlerWithCreds(t, fc)
	h.scanHost = func(string) (string, error) {
		t.Fatal("must not scan when already pinned")
		return "", nil
	}
	cred, err := h.resolveCredential("dk", "git@github.com:o/r.git")
	if err != nil || cred.GetKnownHosts() != "github.com ssh-ed25519 PINNED" {
		t.Fatalf("cred=%+v err=%v", cred, err)
	}
}

// authedRequest builds a request with a user already injected into the context,
// so tests can call handler methods directly without going through requireSession.
func authedRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req.WithContext(context.WithValue(req.Context(), userKey, "admin"))
}

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

func TestAddAppUnsupportedSourceType(t *testing.T) {
	fc := &fakeController{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	// "svn" is not a known source type — hits the default branch of the type switch.
	resp := postApps(t, c, srv.URL, cookie, `{"agent":"dev-1","source":{"type":"svn","name":"web","cmd":"./svn-run"}}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported source type = %d; want 400", resp.StatusCode)
	}
	body := resp.Body
	defer body.Close()
	var raw []byte
	raw, _ = io.ReadAll(body)
	if !strings.Contains(string(raw), "unsupported source type") {
		t.Fatalf("expected 'unsupported source type' in body, got: %q", string(raw))
	}
	// Controller should NOT have been called for an unsupported type.
	if fc.gotAgent != "" {
		t.Fatalf("controller should not have been called for unsupported type, got agent=%q", fc.gotAgent)
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

func TestDeployAttachesResolvedCredential(t *testing.T) {
	cs, err := credstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.Put("gh-ci", "octocat", "ghp_SECRET"); err != nil {
		t.Fatal(err)
	}
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", cs)

	body := `{"agent":"a1","source":{"type":"git","name":"priv","repo":"https://x/y.git","credential":"gh-ci"}}`
	rec := httptest.NewRecorder()
	req := authedRequest(t, "POST", "/api/apps", body)
	h.apps(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d; want 200: %s", rec.Code, rec.Body.String())
	}
	dep := fc.gotOp.GetDeploy()
	if dep == nil || dep.GetCredential().GetToken() != "ghp_SECRET" {
		t.Fatalf("token not resolved+attached: %+v", fc.gotOp)
	}
	if dep.GetApp().GetSource().GetCredential() != "gh-ci" {
		t.Fatalf("credential name not set on GitSource: %+v", dep.GetApp().GetSource())
	}
}

func TestDeployUnknownCredential(t *testing.T) {
	cs, err := credstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", cs)
	body := `{"agent":"a1","source":{"type":"git","name":"priv","repo":"https://x/y.git","credential":"nope"}}`
	rec := httptest.NewRecorder()
	h.apps(rec, authedRequest(t, "POST", "/api/apps", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for unknown credential, got %d", rec.Code)
	}
}
