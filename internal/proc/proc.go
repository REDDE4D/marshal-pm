// Package proc spawns and signals a single supervised OS process.
package proc

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
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
//
// Each process is placed in its own process group (Setpgid) so that Signal and
// Kill reach the whole tree — a supervised command that itself spawns children
// (e.g. a shell wrapper) is stopped completely rather than leaking grandchildren.
func Start(spec Spec) (*Process, error) {
	cmd := exec.Command(spec.Cmd, spec.Args...)
	cmd.Dir = spec.Cwd
	cmd.Env = buildEnv(spec)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

// Signal sends a signal to the process's entire process group, so children of
// the supervised command receive it too. Falls back to signaling just the
// process if the signal is not a syscall.Signal.
func (p *Process) Signal(sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return p.cmd.Process.Signal(sig)
	}
	// Negative pid targets the process group (== pid, since Start sets Setpgid).
	return syscall.Kill(-p.cmd.Process.Pid, s)
}

// Kill force-kills the process and its whole process group.
func (p *Process) Kill() error {
	return syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
}
