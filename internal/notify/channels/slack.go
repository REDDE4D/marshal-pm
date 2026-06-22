package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"marshal/internal/notify"
)

type slack struct{ webhookURL string }

func newSlack(_ notify.Channel, secrets map[string]string) (notify.Sender, error) {
	url := secrets["webhook_url"]
	if url == "" {
		return nil, fmt.Errorf("slack: webhook_url required")
	}
	return &slack{webhookURL: url}, nil
}

func (s *slack) Send(ctx context.Context, m notify.Message) error {
	body, _ := json.Marshal(map[string]string{"text": m.Body})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doExpectOK(req)
}
