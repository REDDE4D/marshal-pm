package dashboard

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/REDDE4D/marshal-pm/internal/credstore"
)

// Credentials is the subset of credstore.Store the dashboard needs.
type Credentials interface {
	List() []credstore.Meta
	Get(name string) (username, token string, ok bool, err error)
	Put(name, username, token string) error
	Generate(name string) (publicKey string, err error)
	GetKey(name string) (privateKey, knownHosts string, ok bool, err error)
	SetKnownHosts(name, line string) error
	Delete(name string) bool
}

type credentialReq struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // "" or "https-token" → HTTPS; "ssh-key" → generate a key
	Username string `json:"username"`
	Token    string `json:"token"`
}

func (h *handler) listCredentials(w http.ResponseWriter, r *http.Request) {
	if h.creds == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, h.creds.List())
}

func (h *handler) createCredential(w http.ResponseWriter, r *http.Request) {
	if h.creds == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	var body credentialReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	user, _ := r.Context().Value(userKey).(string)

	if body.Type == "ssh-key" {
		pub, err := h.creds.Generate(body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("dashboard: credential.generate %s (ssh-key) by %s", body.Name, user) // never log the key
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "public_key": pub})
		return
	}

	if body.Token == "" {
		http.Error(w, "name and token required", http.StatusBadRequest)
		return
	}
	if err := h.creds.Put(body.Name, body.Username, body.Token); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("dashboard: credential.put %s (user=%s) by %s", body.Name, body.Username, user) // never log the token
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (h *handler) deleteCredential(w http.ResponseWriter, r *http.Request) {
	if h.creds == nil {
		http.Error(w, "credentials unavailable", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	if !h.creds.Delete(name) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: credential.delete %s by %s", name, user)
	w.WriteHeader(http.StatusNoContent)
}
