package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"marshal/internal/logstore"
)

type fakeLogs struct{ afters []int64 }

func (f *fakeLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error) {
	f.afters = append(f.afters, afterRowID)
	return []logstore.StoredLine{
		{RowID: 7, TsMs: 1000, Label: "web#0", Stderr: false, Text: "hello"},
		{RowID: 8, TsMs: 1001, Label: "web#1", Stderr: true, Text: "oops"},
	}, 8, nil
}

func (f *fakeLogs) ErrorCounts(string, int64) (map[string]int64, error) { return nil, nil }

func TestLogsRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/logs?agent=dev-1&selector=web")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie logs = %d; want 401", resp.StatusCode)
	}
}

func TestLogsBackfill(t *testing.T) {
	fl := &fakeLogs{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs?agent=dev-1&selector=web", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logs = %d; want 200", resp.StatusCode)
	}
	var got logsView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cursor != 8 || len(got.Lines) != 2 {
		t.Fatalf("logs = %+v, want cursor 8 + 2 lines", got)
	}
	if got.Lines[0].Name != "web" || got.Lines[0].Instance != 0 || got.Lines[0].Text != "hello" || got.Lines[0].Stderr {
		t.Fatalf("line0 = %+v", got.Lines[0])
	}
	if got.Lines[1].Instance != 1 || !got.Lines[1].Stderr {
		t.Fatalf("line1 = %+v", got.Lines[1])
	}
	if len(fl.afters) != 1 || fl.afters[0] != 0 {
		t.Fatalf("backfill afters = %v, want [0]", fl.afters)
	}
}

func TestLogsFollowForwardsAfter(t *testing.T) {
	fl := &fakeLogs{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs?agent=dev-1&selector=web&after=8&stream=stderr", nil)
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("follow logs = %d; want 200", resp.StatusCode)
	}
	if len(fl.afters) != 1 || fl.afters[0] != 8 {
		t.Fatalf("follow afters = %v, want [8]", fl.afters)
	}
}

func TestLogsMissingParams(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	req, _ := http.NewRequest("GET", srv.URL+"/api/logs?agent=dev-1", nil) // no selector
	req.AddCookie(cookie)
	resp, _ := c.Do(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing selector = %d; want 400", resp.StatusCode)
	}
}

type recordingLogs struct{ gotText string }

func (r *recordingLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter, text string) ([]logstore.StoredLine, int64, error) {
	r.gotText = text
	return []logstore.StoredLine{{RowID: 1, TsMs: 1, Label: "web#0", Stderr: false, Text: "x"}}, 1, nil
}

func (r *recordingLogs) ErrorCounts(string, int64) (map[string]int64, error) { return nil, nil }

func TestLogsThreadsQueryFilter(t *testing.T) {
	rl := &recordingLogs{}
	h := newHandler(fakeLister{}, &fakeMetrics{}, rl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", nil)
	req := httptest.NewRequest("GET", "/api/logs?agent=dev-1&selector=web&q=boom", nil)
	rec := httptest.NewRecorder()
	h.logs(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	if rl.gotText != "boom" {
		t.Fatalf("Since received text %q; want %q", rl.gotText, "boom")
	}
}

type statLogs struct{ counts map[string]int64 }

func (s statLogs) Since(string, string, int64, int, logstore.StreamFilter, string) ([]logstore.StoredLine, int64, error) {
	return nil, 0, nil
}
func (s statLogs) ErrorCounts(string, int64) (map[string]int64, error) { return s.counts, nil }

func TestLogStatsEndpoint(t *testing.T) {
	sl := statLogs{counts: map[string]int64{"web#0": 4, "api#0": 1}}
	h := newHandler(fakeLister{}, &fakeMetrics{}, sl, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", "", nil)
	req := httptest.NewRequest("GET", "/api/logstats?agent=dev-1", nil)
	rec := httptest.NewRecorder()
	h.logstats(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d; want 200", rec.Code)
	}
	var body struct {
		Counts map[string]int64 `json:"counts"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Counts["web#0"] != 4 || body.Counts["api#0"] != 1 {
		t.Fatalf("counts = %v; want web#0:4 api#0:1", body.Counts)
	}
}
