package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/logstore"
	"github.com/REDDE4D/marshal-pm/internal/pb"
)

// errLogs is a fakeLogs whose StderrSince returns canned lines per agent.
type errLogs struct {
	fakeLogs
	byAgent map[string][]logstore.StoredLine
}

func (e *errLogs) StderrSince(agent string, sinceMs int64) ([]logstore.StoredLine, error) {
	return e.byAgent[agent], nil
}

type twoAgentLister struct{}

func (twoAgentLister) List() []*pb.AgentState {
	return []*pb.AgentState{{AgentName: "edge-1"}, {AgentName: "edge-2"}}
}

func TestErrorsEndpointAggregatesFleet(t *testing.T) {
	now := time.Now().UnixMilli()
	el := &errLogs{byAgent: map[string][]logstore.StoredLine{
		"edge-1": {
			{TsMs: now - 1000, Label: "api#0", Stderr: true, Text: "connection to 10.0.0.1:1 failed"},
			{TsMs: now - 900, Label: "api#1", Stderr: true, Text: "connection to 10.0.0.2:2 failed"},
		},
		"edge-2": {
			{TsMs: now - 500, Label: "web#0", Stderr: true, Text: "level=info up"}, // excluded
		},
	}}
	srv := httptest.NewServer(NewHandler(twoAgentLister{}, &fakeMetrics{}, el, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/errors?range=24h", nil)
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Cluster struct {
			Errors        int `json:"errors"`
			Signatures    int `json:"signatures"`
			AffectedProcs int `json:"affected_procs"`
		} `json:"cluster"`
		Signatures []struct {
			Count    int      `json:"count"`
			Affected []string `json:"affected"`
			Buckets  []int    `json:"buckets"`
		} `json:"signatures"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Cluster.Errors != 2 || out.Cluster.Signatures != 1 || out.Cluster.AffectedProcs != 2 {
		t.Fatalf("cluster = %+v, want errors=2 sigs=1 procs=2", out.Cluster)
	}
	if len(out.Signatures) != 1 || out.Signatures[0].Count != 2 || len(out.Signatures[0].Buckets) != 24 {
		t.Fatalf("signatures = %+v", out.Signatures)
	}
}

func TestErrorsRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(twoAgentLister{}, &fakeMetrics{}, &errLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/errors?range=24h")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie errors = %d; want 401", resp.StatusCode)
	}
}

func TestErrorsAgentFilter(t *testing.T) {
	now := time.Now().UnixMilli()
	el := &errLogs{byAgent: map[string][]logstore.StoredLine{
		"edge-1": {
			{TsMs: now - 1000, Label: "api#0", Stderr: true, Text: "disk full"},
		},
		"edge-2": {
			{TsMs: now - 500, Label: "web#0", Stderr: true, Text: "connection refused"},
		},
	}}
	srv := httptest.NewServer(NewHandler(twoAgentLister{}, &fakeMetrics{}, el, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/errors?range=24h&agent=edge-1", nil)
	req.AddCookie(cookie)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out struct {
		Cluster struct {
			Errors int `json:"errors"`
		} `json:"cluster"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	// Only edge-1's error, not edge-2's
	if out.Cluster.Errors != 1 {
		t.Fatalf("cluster.errors = %d; want 1 (agent-filtered)", out.Cluster.Errors)
	}
}
