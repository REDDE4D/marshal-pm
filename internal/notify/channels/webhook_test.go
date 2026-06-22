package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"marshal/internal/notify"
)

type fakeDoer struct {
	req  *http.Request
	body string
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.req, f.body = r, string(b)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func withDoer(t *testing.T, d httpDoer) {
	t.Helper()
	old := httpClient
	httpClient = d
	t.Cleanup(func() { httpClient = old })
}

func TestWebhookSendsSignedJSON(t *testing.T) {
	fd := &fakeDoer{}
	withDoer(t, fd)
	s, err := New(notify.Channel{Name: "wh", Type: "webhook", Config: map[string]string{"url": "https://example.test/hook"}},
		map[string]string{"hmac": "topsecret"})
	if err != nil {
		t.Fatal(err)
	}
	ev := notify.Event{Type: notify.EventCrash, Agent: "dev-1", Process: "api", Detail: "crashed"}
	if err := s.Send(context.Background(), notify.Message{Title: "t", Body: "b", Event: ev}); err != nil {
		t.Fatal(err)
	}
	if fd.req.URL.String() != "https://example.test/hook" {
		t.Fatalf("url: %s", fd.req.URL)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(fd.body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["type"] != "crash" || payload["agent"] != "dev-1" || payload["process"] != "api" {
		t.Fatalf("payload: %v", payload)
	}
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write([]byte(fd.body))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := fd.req.Header.Get("X-Marshal-Signature"); got != want {
		t.Fatalf("signature: got %s want %s", got, want)
	}
}

func TestNewUnknownType(t *testing.T) {
	if _, err := New(notify.Channel{Name: "x", Type: "carrier-pigeon"}, nil); err == nil {
		t.Fatal("expected error for unknown type")
	}
}
