package server

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryAuthRejectsMissingToken(t *testing.T) {
	dir := t.TempDir()
	a, secrets, _ := loadOrInitAuth(dir)
	info := &grpc.UnaryServerInfo{FullMethod: "/marshal.v1.Fleet/ListFleet"}
	called := false
	h := func(ctx context.Context, req any) (any, error) { called = true; return "ok", nil }

	_, err := a.unaryAuth(context.Background(), nil, info, h)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: code = %v, want Unauthenticated", status.Code(err))
	}
	if called {
		t.Fatal("handler ran without auth")
	}

	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", "nope"))
	if _, err := a.unaryAuth(bad, nil, info, h); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("bad token: code = %v, want PermissionDenied", status.Code(err))
	}

	good := metadata.NewIncomingContext(context.Background(), metadata.Pairs("marshal-token", secrets.AdminToken))
	if _, err := a.unaryAuth(good, nil, info, h); err != nil {
		t.Fatalf("valid admin token rejected: %v", err)
	}
	if !called {
		t.Fatal("handler did not run with valid token")
	}
}
