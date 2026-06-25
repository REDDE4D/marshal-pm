package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

func TestServeRejectsNilAuth(t *testing.T) {
	dir := t.TempDir()
	cert, _, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer(NewRegistry(), nil, nil, nil)
	serveErr := Serve(ctx, lis, srv, cert)
	if serveErr == nil {
		t.Fatal("Serve with nil auth: expected non-nil error, got nil")
	}
	if !strings.Contains(strings.ToLower(serveErr.Error()), "auth") {
		t.Fatalf("Serve with nil auth: error %q does not mention auth", serveErr.Error())
	}
}

func TestServeOverTLS(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	auth, secrets, err := loadOrInitAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer(NewRegistry(), nil, nil, auth)
	go Serve(ctx, lis, srv, cert)

	cfg, err := fleetauth.ClientTLS(fp, "")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dcancel()
	authedCtx := metadata.AppendToOutgoingContext(dctx, "marshal-token", secrets.AdminToken)
	if _, err := pb.NewFleetClient(conn).ListFleet(authedCtx, &pb.ListFleetRequest{}); err != nil {
		t.Fatalf("ListFleet over TLS failed: %v", err)
	}
}
