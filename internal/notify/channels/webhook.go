package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

type webhook struct {
	url  string
	hmac string
}

func newWebhook(c notify.Channel, secrets map[string]string) (*webhook, error) {
	url := c.Config["url"]
	if url == "" {
		return nil, fmt.Errorf("webhook: url required")
	}
	return &webhook{url: url, hmac: secrets["hmac"]}, nil
}

func (w *webhook) Send(ctx context.Context, m notify.Message) error {
	body, err := json.Marshal(map[string]any{
		"type":    string(m.Event.Type),
		"agent":   m.Event.Agent,
		"process": m.Event.Process,
		"detail":  m.Event.Detail,
		"time":    m.Event.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.hmac != "" {
		mac := hmac.New(sha256.New, []byte(w.hmac))
		mac.Write(body)
		req.Header.Set("X-Marshal-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return doExpectOK(req)
}

// doExpectOK runs the request via the seam and treats non-2xx as an error.
func doExpectOK(req *http.Request) error {
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned %d", req.URL.Host, resp.StatusCode)
	}
	return nil
}
