package dashboard

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"marshal/internal/audit"
)

const sessionCookie = "marshal_session"

// Authenticator verifies dashboard credentials. *server.AuthStore satisfies it.
type Authenticator interface {
	VerifyDashboardUser(user, password string) bool
	DashboardCredentialStamp(user string) (string, bool)
}

type ctxKey string

const userKey ctxKey = "user"

type handler struct {
	lister      FleetLister
	metricsHist MetricsHistory
	logsHist    LogsHistory
	controller  FleetController
	auth        Authenticator
	sessions    *sessionStore
	limiter     *loginLimiter
	audit       *audit.Log
	files       fs.FS
	static      http.Handler
	mux         http.Handler
	creds       Credentials
}

// newHandler builds a *handler (with its mux) for the given session lifetime.
// sessionsPath persists sessions to disk; "" keeps them in-memory.
// auditPath enables the login audit log; "" disables it.
// creds may be nil, which disables credential endpoints (they return 503).
func newHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration, sessionsPath, auditPath string, creds Credentials) *handler {
	files := staticFS()
	var al *audit.Log
	if auditPath != "" {
		al = audit.New(auditPath, audit.DefaultMaxBytes)
	}
	h := &handler{
		lister:      lister,
		metricsHist: metrics,
		logsHist:    logs,
		controller:  controller,
		auth:        auth,
		sessions:    newSessionStore(ttl, nil, sessionsPath),
		limiter:     newLoginLimiter(nil),
		audit:       al,
		files:       files,
		static:      http.FileServer(http.FS(files)),
		creds:       creds,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.logout)
	mux.HandleFunc("GET /api/session", h.requireSession(h.session))
	mux.HandleFunc("GET /api/fleet", h.requireSession(h.fleet))
	mux.HandleFunc("GET /api/metrics", h.requireSession(h.metrics))
	mux.HandleFunc("GET /api/logs", h.requireSession(h.logs))
	mux.HandleFunc("GET /api/logstats", h.requireSession(h.logstats))
	mux.HandleFunc("POST /api/control", h.requireSession(h.control))
	mux.HandleFunc("POST /api/apps", h.requireSession(h.apps))
	mux.HandleFunc("POST /api/apps/redeploy", h.requireSession(h.redeploy))
	mux.HandleFunc("GET /api/credentials", h.requireSession(h.listCredentials))
	mux.HandleFunc("POST /api/credentials", h.requireSession(h.createCredential))
	mux.HandleFunc("DELETE /api/credentials/{name}", h.requireSession(h.deleteCredential))
	mux.HandleFunc("/", h.spa)
	h.mux = mux
	return h
}

// NewHandler builds the dashboard HTTP handler with the given session lifetime.
// The returned http.Handler is safe to use with httptest servers in unit tests.
// Credentials are disabled (nil) — use newHandler directly if you need them.
func NewHandler(lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, ttl time.Duration) http.Handler {
	return newHandler(lister, metrics, logs, controller, auth, ttl, "", "", nil).mux
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
	ip := clientIP(r)
	key := body.User + "|" + ip
	if locked, wait := h.limiter.retryAfter(key); locked {
		h.audit.Record(audit.Event{Time: time.Now().UTC(), User: body.User, IP: ip, Outcome: audit.OutcomeRateLimited})
		secs := int(wait.Seconds())
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		http.Error(w, "too many attempts", http.StatusTooManyRequests)
		return
	}
	if !h.auth.VerifyDashboardUser(body.User, body.Pass) {
		h.limiter.fail(key)
		h.audit.Record(audit.Event{Time: time.Now().UTC(), User: body.User, IP: ip, Outcome: audit.OutcomeInvalid})
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	h.limiter.reset(key)
	stamp, _ := h.auth.DashboardCredentialStamp(body.User)
	tok, err := h.sessions.create(body.User, stamp)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit.Record(audit.Event{Time: time.Now().UTC(), User: body.User, IP: ip, Outcome: audit.OutcomeSuccess})
	h.setSessionCookie(w, tok, 0)
	writeJSON(w, http.StatusOK, map[string]string{"user": body.User})
}

// clientIP returns the source IP for r, stripping the port. It falls back to the
// raw RemoteAddr if there is no port (Marshal serves direct TLS, so RemoteAddr
// is the real client — no X-Forwarded-For to consult).
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
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
		user, stamp, ok := h.sessions.validate(c.Value)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Invalidate if the credential changed (stamp mismatch) or the user is
		// gone. The session dies on its next request; no push needed.
		cur, exists := h.auth.DashboardCredentialStamp(user)
		if !exists || cur != stamp {
			h.sessions.delete(c.Value)
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
