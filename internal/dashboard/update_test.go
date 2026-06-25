package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/updatecheck"
)

type fakeUpdate struct {
	enabled bool
	res     updatecheck.Result
}

func (f fakeUpdate) Enabled() bool                { return f.enabled }
func (f fakeUpdate) Snapshot() updatecheck.Result { return f.res }

func doUpdateReq(t *testing.T, h *handler) updateView {
	t.Helper()
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/update", nil)
	req.AddCookie(loginCookie(t, c, srv.URL))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var v updateView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestUpdateEndpointReportsOutdatedServerAndAgents(t *testing.T) {
	lister := fakeLister{agents: []*pb.AgentState{
		{AgentName: "old-1", MarshalVersion: "v0.4.0"},
		{AgentName: "current-1", MarshalVersion: "v0.7.2"},
	}}
	h := newHandler(lister, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", nil, nil)
	h.updater = fakeUpdate{enabled: true, res: updatecheck.Result{Current: "v0.4.0", Latest: "v0.7.2", Outdated: true}}

	v := doUpdateReq(t, h)
	if !v.Enabled || !v.Outdated || v.Latest != "v0.7.2" {
		t.Fatalf("view = %+v, want enabled+outdated, latest v0.7.2", v)
	}
	if len(v.OutdatedAgents) != 1 || v.OutdatedAgents[0] != "old-1" {
		t.Fatalf("outdated agents = %v, want [old-1]", v.OutdatedAgents)
	}
}

func TestUpdateEndpointDisabled(t *testing.T) {
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", nil, nil)
	// h.update left nil → disabled.
	v := doUpdateReq(t, h)
	if v.Enabled {
		t.Fatalf("view = %+v, want enabled=false when no checker", v)
	}
}
