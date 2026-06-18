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

func (f *fakeLogs) Since(agent, selector string, afterRowID int64, limit int, filter logstore.StreamFilter) ([]logstore.StoredLine, int64, error) {
	f.afters = append(f.afters, afterRowID)
	return []logstore.StoredLine{
		{RowID: 7, TsMs: 1000, Label: "web#0", Stderr: false, Text: "hello"},
		{RowID: 8, TsMs: 1001, Label: "web#1", Stderr: true, Text: "oops"},
	}, 8, nil
}

func TestLogsRequiresSession(t *testing.T) {
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, _ := srv.Client().Get(srv.URL + "/api/logs?agent=dev-1&selector=web")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie logs = %d; want 401", resp.StatusCode)
	}
}

func TestLogsBackfill(t *testing.T) {
	fl := &fakeLogs{}
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
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
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, fl, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
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
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
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
