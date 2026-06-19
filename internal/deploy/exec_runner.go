package deploy

import (
	"context"
	"io"
	"os/exec"
)

// ExecRunner runs commands with os/exec, streaming combined output to the
// provided writers. dir, when non-empty, is the working directory.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
