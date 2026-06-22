package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marshal/internal/notify"
)

type fakeNotifs struct {
	channels []notify.Channel
	rules    []notify.Rule
	settings notify.Settings
	secrets  map[string]map[string]string
}

func (f *fakeNotifs) Channels() []notify.Channel { return f.channels }
func (f *fakeNotifs) HasSecret(name string) bool { _, ok := f.secrets[name]; return ok }
func (f *fakeNotifs) PutChannel(c notify.Channel, s map[string]string) error {
	f.channels = append(f.channels, c)
	if f.secrets == nil {
		f.secrets = map[string]map[string]string{}
	}
	if len(s) > 0 {
		f.secrets[c.Name] = s
	}
	return nil
}
func (f *fakeNotifs) DeleteChannel(name string) bool { return true }
func (f *fakeNotifs) ChannelSecrets(name string) (map[string]string, bool, error) {
	return f.secrets[name], true, nil
}
func (f *fakeNotifs) Rules() []notify.Rule                { return f.rules }
func (f *fakeNotifs) PutRule(r notify.Rule) error         { f.rules = append(f.rules, r); return nil }
func (f *fakeNotifs) DeleteRule(name string) bool         { return true }
func (f *fakeNotifs) Settings() notify.Settings           { return f.settings }
func (f *fakeNotifs) SetSettings(s notify.Settings) error { f.settings = s; return nil }

// testHandlerWithNotifs builds a *handler wired with a fake notifs store and an
// authenticated session, returning the handler and a session cookie.
func testHandlerWithNotifs(t *testing.T, n Notifications) *handler {
	t.Helper()
	h := newHandler(nil, nil, nil, nil, nil, 0, "", "", nil)
	h.notifs = n
	h.notifBuild = func(c notify.Channel, _ map[string]string) (notify.Sender, error) {
		return senderFunc(func() error { return nil }), nil
	}
	return h
}

type senderFunc func() error

func (s senderFunc) Send(_ context.Context, _ notify.Message) error { return s() }

func TestGetNotificationsRedactsSecrets(t *testing.T) {
	n := &fakeNotifs{channels: []notify.Channel{{Name: "tg", Type: "telegram", Enabled: true}}, secrets: map[string]map[string]string{"tg": {"bot_token": "SECRET"}}}
	h := testHandlerWithNotifs(t, n)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	h.getNotifications(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "SECRET") {
		t.Fatal("secret leaked in GET response")
	}
	var out struct {
		Channels []struct {
			Name      string `json:"name"`
			HasSecret bool   `json:"has_secret"`
		} `json:"channels"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Channels) != 1 || !out.Channels[0].HasSecret {
		t.Fatalf("has_secret not surfaced: %+v", out.Channels)
	}
}

func TestPutChannelCreates(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	body, _ := json.Marshal(map[string]any{
		"name": "wh", "type": "webhook", "enabled": true,
		"config": map[string]string{"url": "https://x.test"}, "secrets": map[string]string{"hmac": "k"},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels", bytes.NewReader(body))
	h.putChannel(rec, req)
	if rec.Code != 201 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if len(n.channels) != 1 || n.channels[0].Name != "wh" || n.secrets["wh"]["hmac"] != "k" {
		t.Fatalf("channel not stored: %+v", n.channels)
	}
}
