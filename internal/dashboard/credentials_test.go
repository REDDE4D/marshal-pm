package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"marshal/internal/credstore"
)

func TestCredentialsCRUD(t *testing.T) {
	cs, err := credstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := newTestHandlerWithCreds(t, cs) // helper defined below

	// Create.
	rec := httptest.NewRecorder()
	h.createCredential(rec, httptest.NewRequest("POST", "/api/credentials",
		strings.NewReader(`{"name":"gh-ci","username":"octocat","token":"ghp_x"}`)))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create: %d", rec.Code)
	}

	// List has no token.
	rec = httptest.NewRecorder()
	h.listCredentials(rec, httptest.NewRequest("GET", "/api/credentials", nil))
	if rec.Code != 200 {
		t.Fatalf("list: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "ghp_x") {
		t.Fatalf("token leaked into list response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gh-ci") {
		t.Fatalf("created credential missing from list: %s", rec.Body.String())
	}
}

func TestCredentialsDisabledWhenNil(t *testing.T) {
	h := newTestHandlerWithCreds(t, nil)
	rec := httptest.NewRecorder()
	h.listCredentials(rec, httptest.NewRequest("GET", "/api/credentials", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when disabled, got %d", rec.Code)
	}
}

func newTestHandlerWithCreds(t *testing.T, creds Credentials) *handler {
	t.Helper()
	return newHandler(nil, nil, nil, nil, nil, time.Hour, "", "", creds)
}
