package deploy

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// ExecRunner runs commands with os/exec, streaming combined output to the
// provided writers. dir, when non-empty, is the working directory.
// env, when non-nil, is appended to the inherited environment.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
