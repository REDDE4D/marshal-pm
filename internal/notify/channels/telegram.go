package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/REDDE4D/marshal-pm/internal/notify"
)

type telegram struct {
	token  string
	chatID string
}

func newTelegram(c notify.Channel, secrets map[string]string) (notify.Sender, error) {
	tok := secrets["bot_token"]
	chat := c.Config["chat_id"]
	if tok == "" || chat == "" {
		return nil, fmt.Errorf("telegram: bot_token and chat_id required")
	}
	return &telegram{token: tok, chatID: chat}, nil
}

func (t *telegram) Send(ctx context.Context, m notify.Message) error {
	body, _ := json.Marshal(map[string]string{"chat_id": t.chatID, "text": m.Body})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doExpectOK(req)
}
