package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marshal/internal/notify"
)

type fakeNotifs struct {
	channels   []notify.Channel
	rules      []notify.Rule
	settings   notify.Settings
	secrets    map[string]map[string]string
	secretsErr error
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
func (f *fakeNotifs) DeleteChannel(name string) bool {
	for i, c := range f.channels {
		if c.Name == name {
			f.channels = append(f.channels[:i], f.channels[i+1:]...)
			return true
		}
	}
	return false
}
func (f *fakeNotifs) ChannelSecrets(name string) (map[string]string, bool, error) {
	if f.secretsErr != nil {
		return nil, false, f.secretsErr
	}
	return f.secrets[name], true, nil
}
func (f *fakeNotifs) Rules() []notify.Rule        { return f.rules }
func (f *fakeNotifs) PutRule(r notify.Rule) error { f.rules = append(f.rules, r); return nil }
func (f *fakeNotifs) DeleteRule(name string) bool {
	for i, r := range f.rules {
		if r.Name == name {
			f.rules = append(f.rules[:i], f.rules[i+1:]...)
			return true
		}
	}
	return false
}
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

func TestPutChannelRejectsMissingFields(t *testing.T) {
	h := testHandlerWithNotifs(t, &fakeNotifs{})
	body, _ := json.Marshal(map[string]any{"name": "wh"}) // no type
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels", bytes.NewReader(body))
	h.putChannel(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestDeleteChannel(t *testing.T) {
	n := &fakeNotifs{channels: []notify.Channel{{Name: "tg", Type: "telegram"}}}
	h := testHandlerWithNotifs(t, n)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/notifications/channels/tg", nil)
	req.SetPathValue("name", "tg")
	h.deleteChannelHandler(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if len(n.channels) != 0 {
		t.Fatalf("channel not removed: %+v", n.channels)
	}

	// Deleting again is a 404.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/notifications/channels/tg", nil)
	req.SetPathValue("name", "tg")
	h.deleteChannelHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestTestChannelSends(t *testing.T) {
	n := &fakeNotifs{channels: []notify.Channel{{Name: "tg", Type: "telegram"}}}
	h := testHandlerWithNotifs(t, n)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels/tg/test", nil)
	req.SetPathValue("name", "tg")
	h.testChannel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("want ok:true, got %s", rec.Body)
	}
}

func TestTestChannelUnknownIs404(t *testing.T) {
	h := testHandlerWithNotifs(t, &fakeNotifs{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels/nope/test", nil)
	req.SetPathValue("name", "nope")
	h.testChannel(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestTestChannelReportsSendError(t *testing.T) {
	n := &fakeNotifs{channels: []notify.Channel{{Name: "tg", Type: "telegram"}}}
	h := testHandlerWithNotifs(t, n)
	h.notifBuild = func(c notify.Channel, _ map[string]string) (notify.Sender, error) {
		return senderFunc(func() error { return errors.New("boom") }), nil
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels/tg/test", nil)
	req.SetPathValue("name", "tg")
	h.testChannel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("want ok:false with error, got %s", rec.Body)
	}
}

func TestTestChannelReportsSecretsError(t *testing.T) {
	n := &fakeNotifs{channels: []notify.Channel{{Name: "tg", Type: "telegram"}}, secretsErr: errors.New("decrypt failed")}
	h := testHandlerWithNotifs(t, n)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/channels/tg/test", nil)
	req.SetPathValue("name", "tg")
	h.testChannel(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"ok":false`) || !strings.Contains(rec.Body.String(), "decrypt failed") {
		t.Fatalf("want ok:false with error, got %s", rec.Body)
	}
}

func TestPutRuleCreates(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	body, _ := json.Marshal(notify.Rule{Name: "crashes", Enabled: true, Events: []notify.EventType{notify.EventCrash}, Channels: []string{"tg"}})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/rules", bytes.NewReader(body))
	h.putRule(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body %s", rec.Code, rec.Body)
	}
	if len(n.rules) != 1 || n.rules[0].Name != "crashes" {
		t.Fatalf("rule not stored: %+v", n.rules)
	}
}

func TestPutRuleRejectsMissingName(t *testing.T) {
	h := testHandlerWithNotifs(t, &fakeNotifs{})
	body, _ := json.Marshal(notify.Rule{Enabled: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/rules", bytes.NewReader(body))
	h.putRule(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestDeleteRule(t *testing.T) {
	n := &fakeNotifs{rules: []notify.Rule{{Name: "crashes"}}}
	h := testHandlerWithNotifs(t, n)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/notifications/rules/crashes", nil)
	req.SetPathValue("name", "crashes")
	h.deleteRuleHandler(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	if len(n.rules) != 0 {
		t.Fatalf("rule not removed: %+v", n.rules)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/notifications/rules/crashes", nil)
	req.SetPathValue("name", "crashes")
	h.deleteRuleHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestPutSettings(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	body, _ := json.Marshal(notify.Settings{CooldownSeconds: 90})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/settings", bytes.NewReader(body))
	h.putSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body)
	}
	if n.settings.CooldownSeconds != 90 {
		t.Fatalf("settings not stored: %+v", n.settings)
	}
}

func TestPutSettingsRoundTripsSuppressRecovery(t *testing.T) {
	n := &fakeNotifs{}
	h := testHandlerWithNotifs(t, n)
	body, _ := json.Marshal(notify.Settings{CooldownSeconds: 60, SuppressRecovery: true})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/settings", bytes.NewReader(body))
	h.putSettings(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body %s", rec.Code, rec.Body)
	}
	if !n.settings.SuppressRecovery {
		t.Fatalf("suppress_recovery not stored: %+v", n.settings)
	}
}

func TestPutSettingsRejectsBadJSON(t *testing.T) {
	h := testHandlerWithNotifs(t, &fakeNotifs{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/notifications/settings", strings.NewReader("{not json"))
	h.putSettings(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestNotifsUnavailableIs503(t *testing.T) {
	h := newHandler(nil, nil, nil, nil, nil, 0, "", "", nil) // h.notifs == nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/notifications", nil)
	h.getNotifications(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
}
