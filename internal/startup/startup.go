// Package startup generates and installs a boot service (systemd on Linux,
// launchd on macOS) that runs `marshal daemon` at startup. Rendering a Plan is
// pure; the only side effects (writing files, running enable commands) live in
// Apply/Remove/StageAndPrint and are driven by an injectable Runner.
package startup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config is the resolved environment the boot service must reproduce so the
// booted daemon finds the same state dir and auto-resurrects dump.json.
type Config struct {
	Binary   string // absolute path to the marshal binary
	User     string // current username (used only for --system units)
	Home     string // $HOME to pin
	XDGData  string // $XDG_DATA_HOME to pin, or "" (omitted from the unit)
	System   bool   // --system: root-level unit vs per-user
	StageDir string // dir to stage the --system file in (the state dir)
	UID      int    // numeric uid (launchd user domain: gui/<uid>)
}

// Cmd is a single command to run (user install) or print (--system).
type Cmd struct {
	Name string
	Args []string
}

func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// shellQuote returns s safe to paste into a POSIX shell, single-quoting it only
// when it contains shell-significant characters.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`&;|<>(){}[]*?#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Display renders the command shell-quoted, for printing to a human (the
// --system flow). Use String() for the exec/recorder path, which never goes
// through a shell.
func (c Cmd) Display() string {
	parts := make([]string, 0, len(c.Args)+1)
	parts = append(parts, shellQuote(c.Name))
	for _, a := range c.Args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// Plan is the fully-resolved set of side effects. Building it is pure.
type Plan struct {
	UnitPath    string // where the unit/plist file belongs
	StagePath   string // where we stage it for --system (under StageDir)
	Content     string // rendered unit/plist text
	PostInstall []Cmd  // run (user) or print (--system) after writing
	PostRemove  []Cmd  // for unstartup
	NeedsRoot   bool   // == Config.System
	Label       string // "marshal.service" / "com.marshal.daemon"
}

// Platform renders Plans for one init system.
type Platform interface {
	InstallPlan(Config) Plan
	RemovePlan(Config) Plan
}

// Runner executes a Cmd. Tests inject a recorder; production uses ExecRunner.
type Runner interface {
	Run(Cmd) error
}

// ExecRunner runs commands for real, streaming their output.
type ExecRunner struct {
	Out io.Writer
	Err io.Writer
}

func (r ExecRunner) Run(c Cmd) error {
	ec := exec.Command(c.Name, c.Args...)
	ec.Stdout = r.Out
	ec.Stderr = r.Err
	return ec.Run()
}

// Detect picks the init system for the current OS.
func Detect(goos string) (Platform, error) {
	return detect(goos, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	})
}

func detect(goos string, hasSystemd func(string) bool) (Platform, error) {
	switch goos {
	case "darwin":
		return launchd{}, nil
	case "linux":
		if hasSystemd("/run/systemd/system") {
			return systemd{}, nil
		}
		return nil, fmt.Errorf("systemd not detected (only systemd is supported on Linux)")
	default:
		return nil, fmt.Errorf("boot startup is not supported on %s", goos)
	}
}

// Apply installs a user-level plan: write the unit file, then run PostInstall.
func Apply(p Plan, r Runner) error {
	if err := os.MkdirAll(filepath.Dir(p.UnitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p.UnitPath, []byte(p.Content), 0o644); err != nil {
		return err
	}
	for _, c := range p.PostInstall {
		if err := r.Run(c); err != nil {
			return fmt.Errorf("%s: %w", c, err)
		}
	}
	return nil
}

// Remove uninstalls a user-level plan: run PostRemove (best effort), then delete
// the unit file. Removing a non-existent file is success (idempotent).
func Remove(p Plan, r Runner) error {
	for _, c := range p.PostRemove {
		_ = r.Run(c)
	}
	if err := os.Remove(p.UnitPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// StageAndPrint handles a --system plan: stage the file under StageDir and print
// the sudo command block for the user to run. It never executes anything.
func StageAndPrint(p Plan, out io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(p.StagePath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p.StagePath, []byte(p.Content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "Staged unit at %s\n\n", p.StagePath)
	fmt.Fprintln(out, "Run these commands to install the system service:")
	fmt.Fprintln(out)
	for _, c := range p.PostInstall {
		fmt.Fprintf(out, "  %s\n", c.Display())
	}
	fmt.Fprintln(out)
	return nil
}
