package channels

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/notify"
)

func TestTelegramSend(t *testing.T) {
	fd := &fakeDoer{}
	withDoer(t, fd)
	s, err := New(notify.Channel{Name: "tg", Type: "telegram", Config: map[string]string{"chat_id": "999"}},
		map[string]string{"bot_token": "BOT:TOK"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fd.req.URL.String(), "/botBOT:TOK/sendMessage") {
		t.Fatalf("url: %s", fd.req.URL)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(fd.body), &p)
	if p["chat_id"] != "999" || p["text"] != "hello" {
		t.Fatalf("body: %v", p)
	}
}

func TestSlackSend(t *testing.T) {
	fd := &fakeDoer{}
	withDoer(t, fd)
	s, err := New(notify.Channel{Name: "sl", Type: "slack"}, map[string]string{"webhook_url": "https://hooks.slack.test/xyz"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), notify.Message{Body: "boom"}); err != nil {
		t.Fatal(err)
	}
	if fd.req.URL.String() != "https://hooks.slack.test/xyz" {
		t.Fatalf("url: %s", fd.req.URL)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(fd.body), &p)
	if p["text"] != "boom" {
		t.Fatalf("body: %v", p)
	}
}
