package dashboard

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// Serve runs the dashboard HTTP server over TLS on addr until ctx is canceled.
// cert is the server's TLS certificate (shared with the gRPC service).
// sessionsPath persists sessions to disk; "" keeps them in-memory. auditPath
// enables the login audit log; "" disables it.
// creds may be nil, which disables credential endpoints (they return 503).
func Serve(ctx context.Context, addr string, lister FleetLister, metrics MetricsHistory, logs LogsHistory, controller FleetController, auth Authenticator, cert tls.Certificate, sessionsPath, auditPath string, creds Credentials) error {
	h := newHandler(lister, metrics, logs, controller, auth, 24*time.Hour, sessionsPath, auditPath, creds)
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
