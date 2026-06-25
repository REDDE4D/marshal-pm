package server

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/REDDE4D/marshal-pm/internal/audit"
)

type ctxKey int

const (
	keyAgentName ctxKey = iota
	keyEnrolling
)

func metaToken(ctx context.Context, key string) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// recordAuthFailure appends a failed gRPC auth attempt to the audit log (no-op
// when no log is attached). source identifies the credential class (admin /
// agent / enroll) in the User field; the password is never present here.
func (a *AuthStore) recordAuthFailure(ctx context.Context, source string) {
	a.recordOutcome(ctx, source, audit.OutcomeInvalid)
}

func (a *AuthStore) recordOutcome(ctx context.Context, source, outcome string) {
	a.audit.Record(audit.Event{
		Time:    time.Now().UTC(),
		User:    "fleet:" + source,
		IP:      peerIP(ctx),
		Outcome: outcome,
	})
}

// throttleBlocked reports whether the source IP is currently locked out for too
// many recent auth failures. Helpers are nil-safe so an AuthStore without a
// throttle simply never blocks.
func (a *AuthStore) throttleBlocked(ip string) bool {
	if a.throttle == nil || ip == "" {
		return false
	}
	blocked, _ := a.throttle.RetryAfter(ip)
	return blocked
}

func (a *AuthStore) throttleFail(ip string) {
	if a.throttle != nil && ip != "" {
		a.throttle.Fail(ip)
	}
}

func (a *AuthStore) throttleReset(ip string) {
	if a.throttle != nil && ip != "" {
		a.throttle.Reset(ip)
	}
}

// unaryAuth requires a valid admin token on every unary operator RPC.
func (a *AuthStore) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	ip := peerIP(ctx)
	if a.throttleBlocked(ip) {
		a.recordOutcome(ctx, "admin", audit.OutcomeRateLimited)
		return nil, status.Error(codes.ResourceExhausted, "too many failed auth attempts")
	}
	tok := metaToken(ctx, "marshal-token")
	if tok == "" {
		a.throttleFail(ip)
		a.recordAuthFailure(ctx, "admin")
		return nil, status.Error(codes.Unauthenticated, "missing admin token")
	}
	if !a.verifyAdmin(tok) {
		a.throttleFail(ip)
		a.recordAuthFailure(ctx, "admin")
		return nil, status.Error(codes.PermissionDenied, "invalid admin token")
	}
	a.throttleReset(ip)
	return handler(ctx, req)
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// streamAuth authenticates the Connect stream: a valid per-agent token resolves
// to its bound identity; otherwise a valid enroll token permits enrollment.
func (a *AuthStore) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := ss.Context()
	ip := peerIP(ctx)
	if a.throttleBlocked(ip) {
		a.recordOutcome(ctx, "agent", audit.OutcomeRateLimited)
		return status.Error(codes.ResourceExhausted, "too many failed auth attempts")
	}
	if tok := metaToken(ctx, "marshal-token"); tok != "" {
		if name, ok := a.authAgent(tok); ok {
			a.throttleReset(ip)
			ctx = context.WithValue(ctx, keyAgentName, name)
			return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}
		a.throttleFail(ip)
		a.recordAuthFailure(ctx, "agent")
		return status.Error(codes.PermissionDenied, "invalid agent token")
	}
	if enroll := metaToken(ctx, "marshal-enroll"); enroll != "" {
		if a.verifyEnroll(enroll) {
			a.throttleReset(ip)
			ctx = context.WithValue(ctx, keyEnrolling, true)
			return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}
		a.throttleFail(ip)
		a.recordAuthFailure(ctx, "enroll")
		return status.Error(codes.PermissionDenied, "invalid enrollment token")
	}
	a.throttleFail(ip)
	a.recordAuthFailure(ctx, "agent")
	return status.Error(codes.Unauthenticated, "missing agent or enrollment token")
}

func authedAgentName(ctx context.Context) (string, bool) {
	name, ok := ctx.Value(keyAgentName).(string)
	return name, ok
}

func isEnrolling(ctx context.Context) bool {
	v, _ := ctx.Value(keyEnrolling).(bool)
	return v
}
