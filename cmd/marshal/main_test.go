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

func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{1536, "1.5KB"},
		{5 * 1024 * 1024, "5.0MB"},
		{1 << 60, "1048576.0TB"}, // 1 EB: exp clamped to TB, no panic
	}
	for _, c := range cases {
		if got := humanizeBytes(c.in); got != c.want {
			t.Errorf("humanizeBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
