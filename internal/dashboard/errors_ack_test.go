package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/ackstore"
	"github.com/REDDE4D/marshal-pm/internal/logstore"
)

func TestAcknowledgedDecision(t *testing.T) {
	// Acked and no newer occurrence → acknowledged.
	if !acknowledged(true, 5000, 4) { // last at 4s = 4000ms <= 5000
		t.Error("should be acknowledged when last occurrence predates the ack")
	}
	// Acked but recurred after the ack → not acknowledged (re-surfaces).
	if acknowledged(true, 5000, 6) { // last at 6000ms > 5000
		t.Error("should NOT be acknowledged when it recurred after the ack")
	}
	// Never acked.
	if acknowledged(false, 0, 4) {
		t.Error("unacked signature is not acknowledged")
	}
}

func TestErrorsAckEndpoint(t *testing.T) {
	now := time.Now().UnixMilli()
	el := &errLogs{byAgent: map[string][]logstore.StoredLine{
		"edge-1": {{TsMs: now - 1000, Label: "api#0", Stderr: true, Text: "connection to 10.0.0.1:1 failed"}},
	}}
	h := newHandler(twoAgentLister{}, &fakeMetrics{}, el, nil, fakeAuth{user: "admin", pass: "pw"}, time.Hour, "", nil, nil)
	acks, _ := ackstore.Open(filepath.Join(t.TempDir(), "acks.json"))
	h.acks = acks
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	c := srv.Client()
	cookie := loginCookie(t, c, srv.URL)

	getErrors := func() (id string, acked bool, unack int) {
		req, _ := http.NewRequest("GET", srv.URL+"/api/errors?range=24h", nil)
		req.AddCookie(cookie)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct {
			Cluster struct {
				Unacknowledged int `json:"unacknowledged"`
			} `json:"cluster"`
			Signatures []struct {
				ID           string `json:"id"`
				Acknowledged bool   `json:"acknowledged"`
			} `json:"signatures"`
		}
		json.NewDecoder(resp.Body).Decode(&out)
		if len(out.Signatures) != 1 {
			t.Fatalf("want 1 signature, got %d", len(out.Signatures))
		}
		return out.Signatures[0].ID, out.Signatures[0].Acknowledged, out.Cluster.Unacknowledged
	}

	id, acked, unack := getErrors()
	if acked || unack != 1 {
		t.Fatalf("before ack: acked=%v unack=%d, want false/1", acked, unack)
	}

	// Acknowledge it.
	req, _ := http.NewRequest("POST", srv.URL+"/api/errors/ack", strings.NewReader(`{"id":"`+id+`","ack":true}`))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ack POST status = %d", resp.StatusCode)
	}

	_, acked2, unack2 := getErrors()
	if !acked2 || unack2 != 0 {
		t.Fatalf("after ack: acked=%v unack=%d, want true/0", acked2, unack2)
	}
}
