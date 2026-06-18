package dashboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

const sessionCookie = "marshal_session"

// Authenticator verifies dashboard credentials. *server.AuthStore satisfies it.
type Authenticator interface {
	VerifyDashboardUser(user, password string) bool
}

type ctxKey string

const userKey ctxKey = "user"

type handler struct {
	lister   FleetLister
	auth     Authenticator
	sessions *sessionStore
	files    fs.FS
	static   http.Handler
	mux      http.Handler
}

// newHandler builds a *handler (with its mux) for the given session lifetime.
func newHandler(lister FleetLister, auth Authenticator, ttl time.Duration) *handler {
	files := staticFS()
	h := &handler{
		lister:   lister,
		auth:     auth,
		sessions: newSessionStore(ttl, nil),
		files:    files,
		static:   http.FileServer(http.FS(files)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.logout)
	mux.HandleFunc("GET /api/session", h.requireSession(h.session))
	mux.HandleFunc("GET /api/fleet", h.requireSession(h.fleet))
	mux.HandleFunc("/", h.spa)
	h.mux = mux
	return h
}

// NewHandler builds the dashboard HTTP handler with the given session lifetime.
// The returned http.Handler is safe to use with httptest servers in unit tests.
func NewHandler(lister FleetLister, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, auth, ttl).mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *handler) setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ User, Pass string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !h.auth.VerifyDashboardUser(body.User, body.Pass) {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	tok, err := h.sessions.create(body.User)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.setSessionCookie(w, tok, 0)
	writeJSON(w, http.StatusOK, map[string]string{"user": body.User})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		h.sessions.delete(c.Value)
	}
	h.setSessionCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user, ok := h.sessions.validate(c.Value)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	user, _ := r.Context().Value(userKey).(string)
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

func (h *handler) fleet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, fleetView(h.lister))
}

// spa serves embedded static assets, falling back to index.html for client-side
// routes. Unknown /api/ paths 404 (real API routes are registered explicitly).
func (h *handler) spa(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" {
		p = "index.html"
	}
	if f, err := h.files.Open(p); err == nil {
		_ = f.Close()
		h.static.ServeHTTP(w, r)
		return
	}
	// SPA fallback: serve index.html for unknown (client-routed) paths.
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/"
	h.static.ServeHTTP(w, r2)
}
