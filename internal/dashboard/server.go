package dashboard

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/audit"
	"github.com/REDDE4D/marshal-pm/internal/notify"
)

// Serve runs the dashboard HTTP server over TLS on addr until ctx is canceled.
// cert is the server's TLS certificate (shared with the gRPC service).
// sessionsPath persists sessions to disk; "" keeps them in-memory. auditLog
// enables the login audit log; nil disables it (shared with the gRPC interceptors).
// creds may be nil, which disables credential endpoints (they return 503).
// notifs and notifBuild may be nil, which disables notification endpoints.
// enroll may be nil, which disables the agent connect-token endpoint (returns 503);
// when non-nil it mints and rotates the single shared enroll token used by
// the Connect Agent modal to generate agent connect commands.
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath string, auditLog *audit.Log, creds Credentials, notifs Notifications, notifBuild notify.BuildFunc, enroll EnrollMinter, updater UpdateStatus, acks Acks) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour, sessionsPath, auditLog, creds)
	h.notifs = notifs
	h.notifBuild = notifBuild
	h.enroll = enroll
	h.updater = updater
	h.acks = acks
	go h.sessions.sweepLoop(ctx, time.Hour)
	srv := &http.Server{
		Addr:      addr,
		Handler:   h.mux,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ServeTLS(lis, "", ""); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
