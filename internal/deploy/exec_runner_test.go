package deploy

import (
	"bytes"
	"context"
	"testing"
)

func TestExecRunnerStreamsOutputAndError(t *testing.T) {
	var out bytes.Buffer
	r := ExecRunner{}
	if err := r.Run(context.Background(), "", &out, &out, "sh", "-c", "echo hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "hello\n" {
		t.Fatalf("output %q", got)
	}
	if err := r.Run(context.Background(), "", &out, &out, "sh", "-c", "exit 3"); err == nil {
		t.Fatal("expected non-zero exit to error")
	}
}
