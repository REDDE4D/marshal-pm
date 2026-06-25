package dashboard

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"marshal/internal/audit"
)

func postLogin(t *testing.T, c *http.Client, base, jsonBody string) {
	t.Helper()
	resp, err := c.Post(base+"/api/login", "application/json", strings.NewReader(jsonBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestLoginRecordsSuccessAndInvalid(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "login-audit.log")
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", audit.New(auditPath, audit.DefaultMaxBytes), nil)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()

	postLogin(t, c, srv.URL, `{"User":"admin","Pass":"pw"}`)   // success
	postLogin(t, c, srv.URL, `{"User":"admin","Pass":"nope"}`) // invalid

	evs, err := audit.Read(auditPath, audit.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("got %d events; want 2", len(evs))
	}
	if evs[0].Outcome != audit.OutcomeSuccess || evs[0].User != "admin" {
		t.Errorf("e0 = %+v; want success/admin", evs[0])
	}
	if evs[1].Outcome != audit.OutcomeInvalid {
		t.Errorf("e1 = %+v; want invalid_credentials", evs[1])
	}
	if evs[0].IP == "" {
		t.Errorf("event missing IP")
	}
}

func TestLoginRecordsRateLimited(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "login-audit.log")
	h := newHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", audit.New(auditPath, audit.DefaultMaxBytes), nil)
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()

	// 5 wrong attempts engage the lock; the 6th is rejected while locked.
	for i := 0; i < 6; i++ {
		postLogin(t, c, srv.URL, `{"User":"admin","Pass":"nope"}`)
	}
	evs, err := audit.Read(auditPath, audit.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatal("no events recorded")
	}
	last := evs[len(evs)-1]
	if last.Outcome != audit.OutcomeRateLimited {
		t.Fatalf("last outcome = %q; want rate_limited", last.Outcome)
	}
}

func TestLoginNoAuditWhenDisabled(t *testing.T) {
	// NewHandler passes no audit path → h.audit is nil → Record is a no-op.
	srv := httptest.NewServer(NewHandler(fakeLister{}, &fakeMetrics{}, &fakeLogs{}, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour))
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"User":"admin","Pass":"pw"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d; want 200 (nil audit must not break login)", resp.StatusCode)
	}
}
