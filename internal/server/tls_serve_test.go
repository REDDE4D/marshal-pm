package server

import (
	"context"
	"net"
	"testing"
	"time"

	"marshal/internal/fleetauth"
	"marshal/internal/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestServeOverTLS(t *testing.T) {
	dir := t.TempDir()
	cert, fp, err := LoadOrCreateCert(dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Serve(ctx, lis, NewRegistry(), nil, nil, cert)

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
	if _, err := pb.NewFleetClient(conn).ListFleet(dctx, &pb.ListFleetRequest{}); err != nil {
		t.Fatalf("ListFleet over TLS failed: %v", err)
	}
}
