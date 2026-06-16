// Package proc spawns and signals a single supervised OS process.
package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// Spec describes one process to launch.
type Spec struct {
	Cmd        string
	Args       []string
	Cwd        string
	Env        map[string]string
	InstanceID int
}

// Process is a running OS process.
type Process struct {
	cmd *exec.Cmd
}

// Start launches the process described by spec. Stdout/stderr inherit the
// parent's (file capture arrives in milestone M3).
func Start(spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Cmd, spec.Args...)
	cmd.Dir = spec.Cwd
	cmd.Env = buildEnv(spec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", spec.Cmd, err)
	}
	return &Process{cmd: cmd}, nil
}

func buildEnv(spec Spec) []string {
	env := os.Environ()
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	// Appended last so it takes precedence on exec (last matching entry wins).
	env = append(env, "MARSHAL_INSTANCE_ID="+strconv.Itoa(spec.InstanceID))
	return env
}

// Pid returns the OS process id.
func (p *Process) Pid() int { return p.cmd.Process.Pid }

// Wait blocks until the process exits, returning a non-nil error on non-zero exit.
func (p *Process) Wait() error { return p.cmd.Wait() }

// Signal sends a signal to the process.
func (p *Process) Signal(sig os.Signal) error { return p.cmd.Process.Signal(sig) }

// Kill force-kills the process.
func (p *Process) Kill() error { return p.cmd.Process.Kill() }
