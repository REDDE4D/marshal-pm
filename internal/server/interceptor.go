package server

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

// unaryAuth requires a valid admin token on every unary operator RPC.
func (a *authStore) unaryAuth(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	tok := metaToken(ctx, "marshal-token")
	if tok == "" {
		return nil, status.Error(codes.Unauthenticated, "missing admin token")
	}
	if !a.verifyAdmin(tok) {
		return nil, status.Error(codes.PermissionDenied, "invalid admin token")
	}
	return handler(ctx, req)
}

type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// streamAuth authenticates the Connect stream: a valid per-agent token resolves
// to its bound identity; otherwise a valid enroll token permits enrollment.
func (a *authStore) streamAuth(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := ss.Context()
	if tok := metaToken(ctx, "marshal-token"); tok != "" {
		if name, ok := a.authAgent(tok); ok {
			ctx = context.WithValue(ctx, keyAgentName, name)
			return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}
		return status.Error(codes.PermissionDenied, "invalid agent token")
	}
	if enroll := metaToken(ctx, "marshal-enroll"); enroll != "" {
		if a.verifyEnroll(enroll) {
			ctx = context.WithValue(ctx, keyEnrolling, true)
			return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}
		return status.Error(codes.PermissionDenied, "invalid enrollment token")
	}
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
