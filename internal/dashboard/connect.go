package dashboard

import (
	"log"
	"net"
	"net/http"
)

// EnrollMinter mints a fresh enroll token and exposes the data the dashboard
// needs to build an agent connect command: the server cert fingerprint and the
// fleet (agent-facing) listen address. *server.enrollMinter satisfies it.
type EnrollMinter interface {
	RotateEnrollToken() (string, error)
	Fingerprint() string
	FleetAddress() string // e.g. ":9000" or "0.0.0.0:9000"
}

// connectToken serves POST /api/fleet/connect-token: mints a fresh enroll token
// (rotating the single shared one), returning it ONCE with the cert fingerprint
// and a default address (request host + fleet port) for assembling an agent
// connect command. Session-gated. The token is never written to logs.
func (h *handler) connectToken(w http.ResponseWriter, r *http.Request) {
	if h.enroll == nil {
		http.Error(w, "fleet enrollment unavailable", http.StatusServiceUnavailable)
		return
	}

	tok, err := h.enroll.RotateEnrollToken()
	if err != nil {
		http.Error(w, "could not mint token", http.StatusInternalServerError)
		return
	}
	user, _ := r.Context().Value(userKey).(string)
	log.Printf("dashboard: minted agent enroll token by %s", user) // never log the token
	writeJSON(w, http.StatusOK, map[string]string{
		"token":           tok,
		"fingerprint":     h.enroll.Fingerprint(),
		"default_address": defaultConnectAddress(r.Host, h.enroll.FleetAddress()),
	})
}

// defaultConnectAddress combines the request host (sans port) with the fleet
// listen port, e.g. ("127.0.0.1:9001", ":9000") -> "127.0.0.1:9000". If the
// request host has no port it is used as-is; if the fleet port is unknown the
// bare host is returned.
func defaultConnectAddress(reqHost, fleetAddr string) string {
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}
	_, port, err := net.SplitHostPort(fleetAddr)
	if err != nil || port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
