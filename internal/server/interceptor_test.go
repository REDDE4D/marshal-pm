package server

import (
	"context"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"marshal/internal/audit"
)

func TestInterceptorAuditsAuthFailures(t *testing.T) {
	dir := t.TempDir()
	a, secrets, _ := loadOrInitAuth(dir)
	auditPath := filepath.Join(dir, "audit.log")
	a.SetAuditLog(audit.New(auditPath, audit.DefaultMaxBytes))

	noop := func(ctx context.Context, req any) (any, error) { return "ok", nil }
	uinfo := &grpc.UnaryServerInfo{FullMethod: "/marshal.v1.Fleet/ListFleet"}
	sinfo := &grpc.StreamServerInfo{FullMethod: "/marshal.v1.Fleet/Connect"}
	snoop := func(srv any, stream grpc.ServerStream) error { return nil }

	// 1) bad admin token, 2) missing admin token, 3) bad agent token, 4) bad enroll token.
	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", "nope"))
	_, _ = a.unaryAuth(bad, nil, uinfo, noop)
	_, _ = a.unaryAuth(context.Background(), nil, uinfo, noop)
	_ = a.streamAuth(nil, &mockServerStream{ctx: metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("marshal-token", "bad-agent"))}, sinfo, snoop)
	_ = a.streamAuth(nil, &mockServerStream{ctx: metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("marshal-enroll", "bad-enroll"))}, sinfo, snoop)

	// A valid admin call must NOT be recorded as a failure.
	good := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", secrets.AdminToken))
	_, _ = a.unaryAuth(good, nil, uinfo, noop)

	events, err := audit.Read(auditPath, audit.ReadOptions{})
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("recorded %d events, want 4 failures: %+v", len(events), events)
	}
	for _, ev := range events {
		if ev.Outcome != audit.OutcomeInvalid {
			t.Errorf("outcome = %q, want %q", ev.Outcome, audit.OutcomeInvalid)
		}
	}
}

// mockServerStream is a minimal grpc.ServerStream for interceptor tests.
// Only Context() is meaningful; all other methods are no-ops.
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context { return m.ctx }

func TestUnaryAuth(t *testing.T) {
	dir := t.TempDir()
	a, secrets, _ := loadOrInitAuth(dir)
	info := &grpc.UnaryServerInfo{FullMethod: "/marshal.v1.Fleet/ListFleet"}

	t.Run("missing token → Unauthenticated, handler not called", func(t *testing.T) {
		called := false
		h := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }

		_, err := a.unaryAuth(context.Background(), nil, info, h)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
		}
		if called {
			t.Fatal("handler ran without auth")
		}
	})

	t.Run("invalid token → PermissionDenied, handler not called", func(t *testing.T) {
		called := false
		h := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }

		bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", "nope"))
		if _, err := a.unaryAuth(bad, nil, info, h); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("bad token: code = %v, want PermissionDenied", status.Code(err))
		}
		if called {
			t.Fatal("handler ran with invalid token")
		}
	})

	t.Run("valid admin token → handler called", func(t *testing.T) {
		called := false
		h := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }

		good := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", secrets.AdminToken))
		if _, err := a.unaryAuth(good, nil, info, h); err != nil {
			t.Fatalf("valid admin token rejected: %v", err)
		}
		if !called {
			t.Fatal("handler did not run with valid token")
		}
	})
}

func TestStreamAuth(t *testing.T) {
	dir := t.TempDir()
	a, secrets, _ := loadOrInitAuth(dir)
	info := &grpc.StreamServerInfo{FullMethod: "/marshal.v1.Fleet/Connect"}

	t.Run("no metadata → Unauthenticated, handler not called", func(t *testing.T) {
		called := false
		h := func(srv any, stream grpc.ServerStream) error { called = true; return nil }

		ss := &mockServerStream{ctx: context.Background()}
		err := a.streamAuth(nil, ss, info, h)
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
		}
		if called {
			t.Fatal("handler ran without any metadata")
		}
	})

	t.Run("invalid marshal-token → PermissionDenied, no fallthrough to enroll", func(t *testing.T) {
		called := false
		h := func(srv any, stream grpc.ServerStream) error { called = true; return nil }

		// Supply both an invalid agent token AND a valid enroll token to verify
		// that the invalid token path does not fall through to the enroll check.
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(
			"marshal-token", "bad-agent-token",
			"marshal-enroll", secrets.EnrollToken,
		))
		ss := &mockServerStream{ctx: ctx}
		err := a.streamAuth(nil, ss, info, h)
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
		}
		if called {
			t.Fatal("handler ran despite invalid agent token")
		}
	})

	t.Run("valid per-agent token → handler called with agent name in context", func(t *testing.T) {
		agentToken, err := a.enrollAgent("web-1")
		if err != nil {
			t.Fatalf("enrollAgent: %v", err)
		}

		var capturedCtx context.Context
		h := func(srv any, stream grpc.ServerStream) error {
			capturedCtx = stream.Context()
			return nil
		}

		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", agentToken))
		ss := &mockServerStream{ctx: ctx}
		if err := a.streamAuth(nil, ss, info, h); err != nil {
			t.Fatalf("streamAuth with valid agent token: %v", err)
		}
		if capturedCtx == nil {
			t.Fatal("handler was not called")
		}
		name, ok := authedAgentName(capturedCtx)
		if !ok || name != "web-1" {
			t.Fatalf("authedAgentName = (%q, %v), want (\"web-1\", true)", name, ok)
		}
	})

	t.Run("valid enroll token → handler called with enrolling flag in context", func(t *testing.T) {
		var capturedCtx context.Context
		h := func(srv any, stream grpc.ServerStream) error {
			capturedCtx = stream.Context()
			return nil
		}

		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-enroll", secrets.EnrollToken))
		ss := &mockServerStream{ctx: ctx}
		if err := a.streamAuth(nil, ss, info, h); err != nil {
			t.Fatalf("streamAuth with valid enroll token: %v", err)
		}
		if capturedCtx == nil {
			t.Fatal("handler was not called")
		}
		if !isEnrolling(capturedCtx) {
			t.Fatal("isEnrolling = false, want true")
		}
	})
}
