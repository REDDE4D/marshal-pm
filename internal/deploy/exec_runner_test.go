package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExecRunnerStreamsOutputAndError(t *testing.T) {
	var out bytes.Buffer
	r := ExecRunner{}
	if err := r.Run(context.Background(), "", nil, &out, &out, "sh", "-c", "echo hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); got != "hello\n" {
		t.Fatalf("output %q", got)
	}
	if err := r.Run(context.Background(), "", nil, &out, &out, "sh", "-c", "exit 3"); err == nil {
		t.Fatal("expected non-zero exit to error")
	}
}

func TestExecRunnerEnv(t *testing.T) {
	var buf bytes.Buffer
	err := ExecRunner{}.Run(context.Background(), "", []string{"MARSHAL_TEST_VAR=hello"}, &buf, &buf, "sh", "-c", "echo $MARSHAL_TEST_VAR")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "hello" {
		t.Fatalf("env not applied: %q", buf.String())
	}
}
