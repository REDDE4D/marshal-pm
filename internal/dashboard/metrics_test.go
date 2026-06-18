package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/metricstore"
	"marshal/internal/pb"
)

type fakeMetrics struct{ calls []string }

func (f *fakeMetrics) History(agent, selector string, sinceMs, bucketMs int64) ([]metricstore.Bucket, error) {
	f.calls = append(f.calls, agent+"/"+selector)
	return []metricstore.Bucket{{TsMs: 1000, CpuAvg: 1, CpuMax: 2, MemAvg: 10, MemMax: 20}}, nil
}

func loginCookie(t *testing.T, c *http.Client, base string) *http.Cookie {
	t.Helper()
	resp, _ := c.Post(base+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	cookie := sessionCookieFrom(resp)
	if cookie == nil {
		t.Fatal("login set no session cookie")
	}
	return cookie
}

func TestMetricsRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/metrics")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie metrics = %d; want 401", resp.StatusCode)
	}
}

func TestMetricsBatched(t *testing.T) {
	lister := fakeLister{agents: []*pb.AgentState{{
		AgentName: "dev-1",
		Procs:     []*pb.ProcInfo{{Name: "ticker"}, {Name: "web"}},
	}}}
	fm := &fakeMetrics{}
	srv := httptest.NewServer(NewHandler(lister, fm, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/metrics", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics = %d; want 200", resp.StatusCode)
	}
	var got []agentMetricsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Agent != "dev-1" || len(got[0].Procs) != 2 {
		t.Fatalf("batched metrics = %+v", got)
	}
	if got[0].Procs[0].Name != "ticker" || len(got[0].Procs[0].Buckets) != 1 {
		t.Fatalf("proc metrics = %+v", got[0].Procs[0])
	}
	if got[0].Procs[0].Buckets[0].Ts != 1000 || got[0].Procs[0].Buckets[0].CpuMax != 2 {
		t.Fatalf("bucket = %+v", got[0].Procs[0].Buckets[0])
	}
}

func TestMetricsSingleSeries(t *testing.T) {
	lister := fakeLister{agents: []*pb.AgentState{{AgentName: "dev-1", Procs: []*pb.ProcInfo{{Name: "ticker"}}}}}
	fm := &fakeMetrics{}
	srv := httptest.NewServer(NewHandler(lister, fm, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/metrics?agent=dev-1&selector=ticker&since=60000&bucket=1000", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("single metrics = %d; want 200", resp.StatusCode)
	}
	var got []agentMetricsView
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if len(got) != 1 || len(got[0].Procs) != 1 || got[0].Procs[0].Name != "ticker" {
		t.Fatalf("single-series metrics = %+v", got)
	}
	if len(fm.calls) != 1 || fm.calls[0] != "dev-1/ticker" {
		t.Fatalf("History calls = %v; want one dev-1/ticker", fm.calls)
	}
}
