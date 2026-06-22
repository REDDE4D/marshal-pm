package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"marshal/internal/notify"
)

// Notifications is the subset of *notify.Store the dashboard needs.
type Notifications interface {
	Channels() []notify.Channel
	HasSecret(name string) bool
	PutChannel(c notify.Channel, secrets map[string]string) error
	DeleteChannel(name string) bool
	ChannelSecrets(name string) (map[string]string, bool, error)
	Rules() []notify.Rule
	PutRule(r notify.Rule) error
	DeleteRule(name string) bool
	Settings() notify.Settings
	SetSettings(s notify.Settings) error
}

type channelView struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Enabled   bool              `json:"enabled"`
	Config    map[string]string `json:"config"`
	HasSecret bool              `json:"has_secret"`
}

func (h *handler) notifsReady(w http.ResponseWriter) bool {
	if h.notifs == nil {
		http.Error(w, "notifications unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *handler) getNotifications(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	views := make([]channelView, 0)
	for _, c := range h.notifs.Channels() {
		views = append(views, channelView{Name: c.Name, Type: c.Type, Enabled: c.Enabled, Config: c.Config, HasSecret: h.notifs.HasSecret(c.Name)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": views,
		"rules":    h.notifs.Rules(),
		"settings": h.notifs.Settings(),
	})
}

type channelReq struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

func (h *handler) putChannel(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	var body channelReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.Type == "" {
		http.Error(w, "name and type required", http.StatusBadRequest)
		return
	}
	ch := notify.Channel{Name: body.Name, Type: body.Type, Enabled: body.Enabled, Config: body.Config}
	if err := h.notifs.PutChannel(ch, body.Secrets); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (h *handler) deleteChannelHandler(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	if !h.notifs.DeleteChannel(r.PathValue("name")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) testChannel(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	name := r.PathValue("name")
	var target *notify.Channel
	for _, c := range h.notifs.Channels() {
		if c.Name == name {
			cc := c
			target = &cc
			break
		}
	}
	if target == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	secrets, _, err := h.notifs.ChannelSecrets(name)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sender, err := h.notifBuild(*target, secrets)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	msg := notify.Message{
		Title: "Marshal test notification",
		Body:  "This is a test message from Marshal.",
		Event: notify.Event{Type: "test", Agent: "marshal", Detail: "test", Time: time.Now()},
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := sender.Send(ctx, msg); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *handler) putRule(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	var rule notify.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if rule.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if err := h.notifs.PutRule(rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (h *handler) deleteRuleHandler(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	if !h.notifs.DeleteRule(r.PathValue("name")) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	if !h.notifsReady(w) {
		return
	}
	var s notify.Settings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.notifs.SetSettings(s); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
