package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/pb"
)

type fakeController struct {
	gotAgent string
	gotOp    *pb.ControlOp
	res      *pb.ControlResult
	err      error
}

func (f *fakeController) Control(_ context.Context, agent string, op *pb.ControlOp) (*pb.ControlResult, error) {
	f.gotAgent = agent
	f.gotOp = op
	return f.res, f.err
}

func postControl(t *testing.T, c *http.Client, base string, cookie *http.Cookie, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", base+"/api/control", strings.NewReader(body))
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

func TestControlRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp := postControl(t, srv.Client(), srv.URL, nil, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie control = %d; want 401", resp.StatusCode)
	}
}

func TestControlRestartHappyPath(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"restart"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restart = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != true {
		t.Fatalf("restart body = %+v; want ok:true", got)
	}
	if fc.gotAgent != "dev-1" || fc.gotOp.GetRestart().GetTarget() != "web" {
		t.Fatalf("forwarded agent=%q op=%+v; want dev-1/restart web", fc.gotAgent, fc.gotOp)
	}
}

func TestControlStopForwardsStopOp(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d; want 200", resp.StatusCode)
	}
	if fc.gotOp.GetStop().GetTarget() != "web" {
		t.Fatalf("forwarded op=%+v; want stop web", fc.gotOp)
	}
}

func TestControlBadAction(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"unknown"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad action = %d; want 400", resp.StatusCode)
	}
}

func TestControlDeleteForwardsDeleteOp(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"delete"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d; want 200", resp.StatusCode)
	}
	if fc.gotOp.GetDelete().GetTarget() != "web" {
		t.Fatalf("forwarded op=%+v; want delete web", fc.gotOp)
	}
}

func TestControlMissingFields(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, &fakeController{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","action":"stop"}`) // no selector
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing selector = %d; want 400", resp.StatusCode)
	}
}

func TestControlTransportErrorIs502(t *testing.T) {
	fc := &fakeController{err: errors.New("agent \"dev-1\" not connected")}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("transport error = %d; want 502", resp.StatusCode)
	}
}

func TestControlAgentErrorPassthrough(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: false, Error: "no app matching \"web\""}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)
	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"stop"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent-error = %d; want 200", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["ok"] != false || got["error"] != "no app matching \"web\"" {
		t.Fatalf("agent-error body = %+v", got)
	}
}

func TestControlOpReload(t *testing.T) {
	op := controlOp("reload", "web")
	if op == nil {
		t.Fatal("controlOp(reload) = nil, want a ControlOp")
	}
	r, ok := op.GetOp().(*pb.ControlOp_Reload)
	if !ok {
		t.Fatalf("controlOp(reload) op type = %T, want *pb.ControlOp_Reload", op.GetOp())
	}
	if r.Reload.GetTarget() != "web" {
		t.Fatalf("reload target = %q, want web", r.Reload.GetTarget())
	}
}

func TestControlReloadHappyPath(t *testing.T) {
	fc := &fakeController{res: &pb.ControlResult{Ok: true}}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fc, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	resp := postControl(t, c, srv.URL, cookie, `{"agent":"dev-1","selector":"web","action":"reload"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reload control = %d, want 200", resp.StatusCode)
	}
	if _, ok := fc.gotOp.GetOp().(*pb.ControlOp_Reload); !ok {
		t.Fatalf("forwarded op type = %T, want *pb.ControlOp_Reload", fc.gotOp.GetOp())
	}
}
