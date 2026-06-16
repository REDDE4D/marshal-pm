package main

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCLIErrorStripsGRPCPrefix(t *testing.T) {
	err := status.Error(codes.NotFound, `no app matching "x"`)
	if got := cliError(err); got != `no app matching "x"` {
		t.Fatalf("cliError = %q, want clean message", got)
	}
}

func TestCLIErrorPassesPlainErrors(t *testing.T) {
	if got := cliError(errors.New("boom")); got != "boom" {
		t.Fatalf("cliError = %q, want boom", got)
	}
}
