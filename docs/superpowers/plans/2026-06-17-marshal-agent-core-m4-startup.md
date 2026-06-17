# M4 Boot Startup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `marshal startup` / `marshal unstartup` that generate and install (or print) a systemd unit (Linux) or launchd plist (macOS) which runs `marshal daemon` on boot/login as the right user with the right environment.

**Architecture:** A new leaf package `internal/startup` builds a pure `Plan` (file path, content, command lists) from a resolved `Config`; rendering is pure string templating. The CLI performs the only side effects — user-level installs write the file and run the enable commands via an injectable `Runner`; `--system` stages the file under the state dir and prints the `sudo …` commands. Detection (`darwin`→launchd, `linux`→systemd) takes `goos` and a systemd probe as parameters so it is unit-testable.

**Tech Stack:** Go, cobra (CLI), `encoding/xml` (plist escaping), `os/exec` (runner). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-06-17-marshal-agent-core-m4-startup-design.md`

---

## File Structure

- Create: `internal/startup/startup.go` — `Config`, `Cmd`, `Plan`, `Platform`, `Runner`, `ExecRunner`, `Apply`, `Remove`, `StageAndPrint`, `Detect`/`detect`.
- Create: `internal/startup/systemd.go` — `systemd` platform + unit rendering.
- Create: `internal/startup/launchd.go` — `launchd` platform + plist rendering.
- Create: `internal/startup/detect_test.go`
- Create: `internal/startup/systemd_test.go`
- Create: `internal/startup/launchd_test.go`
- Create: `internal/startup/install_test.go`
- Create: `cmd/marshal/startup.go` — `startupCmd`, `unstartupCmd`, `resolveConfig`.
- Create: `cmd/marshal/startup_test.go`
- Modify: `cmd/marshal/main.go` — register the two commands.

All work is on a feature branch `m4-startup` cut from `main`.

---

## Task 0: Branch

- [ ] **Step 1: Cut the feature branch**

```bash
cd "/Users/sebastiankuprat/process manager"
git checkout -b m4-startup
git status
```
Expected: on branch `m4-startup`, working tree clean.

---

## Task 1: Core types, runner, and detection

**Files:**
- Create: `internal/startup/startup.go`
- Test: `internal/startup/detect_test.go`

- [ ] **Step 1: Write the failing detection test**

Create `internal/startup/detect_test.go`:

```go
package startup

import "testing"

func TestDetectDarwin(t *testing.T) {
	p, err := detect("darwin", func(string) bool { return false })
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if _, ok := p.(launchd); !ok {
		t.Fatalf("want launchd, got %T", p)
	}
}

func TestDetectLinuxWithSystemd(t *testing.T) {
	p, err := detect("linux", func(string) bool { return true })
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if _, ok := p.(systemd); !ok {
		t.Fatalf("want systemd, got %T", p)
	}
}

func TestDetectLinuxNoSystemd(t *testing.T) {
	if _, err := detect("linux", func(string) bool { return false }); err == nil {
		t.Fatal("want error when systemd absent")
	}
}

func TestDetectUnsupported(t *testing.T) {
	if _, err := detect("plan9", func(string) bool { return true }); err == nil {
		t.Fatal("want error for unsupported GOOS")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails to compile**

Run: `go test ./internal/startup/ -run TestDetect -v`
Expected: build failure — `undefined: detect`, `undefined: launchd`, `undefined: systemd`.

- [ ] **Step 3: Write the core package**

Create `internal/startup/startup.go`:

```go
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
		fmt.Fprintf(out, "  %s\n", c)
	}
	fmt.Fprintln(out)
	return nil
}
```

- [ ] **Step 4: Run the test — still fails (no systemd/launchd types yet)**

Run: `go test ./internal/startup/ -run TestDetect -v`
Expected: build failure — `undefined: launchd`, `undefined: systemd`. (Those land in Tasks 2–3; this is expected.) Do not commit yet.

---

## Task 2: systemd platform

**Files:**
- Create: `internal/startup/systemd.go`
- Test: `internal/startup/systemd_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/startup/systemd_test.go`:

```go
package startup

import (
	"strings"
	"testing"
)

func linuxConfig(system bool) Config {
	return Config{
		Binary:   "/usr/local/bin/marshal",
		User:     "alice",
		Home:     "/home/alice",
		System:   system,
		StageDir: "/home/alice/.marshal",
		UID:      1000,
	}
}

func TestSystemdUserUnit(t *testing.T) {
	p := systemd{}.InstallPlan(linuxConfig(false))
	if p.NeedsRoot {
		t.Fatal("user plan must not need root")
	}
	if p.UnitPath != "/home/alice/.config/systemd/user/marshal.service" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	for _, w := range []string{
		"ExecStart=/usr/local/bin/marshal daemon",
		"Restart=on-failure",
		"Environment=HOME=/home/alice",
		"WantedBy=default.target",
	} {
		if !strings.Contains(p.Content, w) {
			t.Errorf("content missing %q\n%s", w, p.Content)
		}
	}
	if strings.Contains(p.Content, "User=") {
		t.Error("user unit must not set User=")
	}
	if got := cmdStrings(p.PostInstall); !contains(got, "loginctl enable-linger alice") {
		t.Errorf("PostInstall missing enable-linger: %v", got)
	}
}

func TestSystemdSystemUnit(t *testing.T) {
	p := systemd{}.InstallPlan(linuxConfig(true))
	if !p.NeedsRoot {
		t.Fatal("system plan must need root")
	}
	if p.UnitPath != "/etc/systemd/system/marshal.service" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	if p.StagePath != "/home/alice/.marshal/marshal.service" {
		t.Fatalf("StagePath = %s", p.StagePath)
	}
	for _, w := range []string{"User=alice", "WantedBy=multi-user.target"} {
		if !strings.Contains(p.Content, w) {
			t.Errorf("content missing %q", w)
		}
	}
	if len(p.PostInstall) == 0 || p.PostInstall[0].String() != "sudo cp /home/alice/.marshal/marshal.service /etc/systemd/system/marshal.service" {
		t.Fatalf("first PostInstall = %v", p.PostInstall)
	}
}

func TestSystemdXDG(t *testing.T) {
	p := systemd{}.InstallPlan(linuxConfig(false))
	if strings.Contains(p.Content, "XDG_DATA_HOME") {
		t.Error("XDG_DATA_HOME must be omitted when empty")
	}
	c := linuxConfig(false)
	c.XDGData = "/data"
	if !strings.Contains(systemd{}.InstallPlan(c).Content, "Environment=XDG_DATA_HOME=/data") {
		t.Error("XDG_DATA_HOME must be present when set")
	}
}

func TestSystemdEnvQuoting(t *testing.T) {
	c := linuxConfig(false)
	c.Home = "/home/a b"
	if !strings.Contains(systemd{}.InstallPlan(c).Content, `Environment="HOME=/home/a b"`) {
		t.Error("env value with space must be quoted")
	}
}

// test helpers
func cmdStrings(cs []Cmd) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.String()
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/startup/ -run TestSystemd -v`
Expected: build failure — `undefined: systemd`.

- [ ] **Step 3: Implement the systemd platform**

Create `internal/startup/systemd.go`:

```go
package startup

import (
	"fmt"
	"path/filepath"
	"strings"
)

type systemd struct{}

func (systemd) InstallPlan(c Config) Plan {
	content := renderSystemdUnit(c)
	if c.System {
		unit := "/etc/systemd/system/marshal.service"
		stage := filepath.Join(c.StageDir, "marshal.service")
		return Plan{
			UnitPath:  unit,
			StagePath: stage,
			Content:   content,
			NeedsRoot: true,
			Label:     "marshal.service",
			PostInstall: []Cmd{
				{"sudo", []string{"cp", stage, unit}},
				{"sudo", []string{"systemctl", "daemon-reload"}},
				{"sudo", []string{"systemctl", "enable", "--now", "marshal.service"}},
			},
			PostRemove: []Cmd{
				{"sudo", []string{"systemctl", "disable", "--now", "marshal.service"}},
				{"sudo", []string{"rm", "-f", unit}},
			},
		}
	}
	unit := filepath.Join(c.Home, ".config", "systemd", "user", "marshal.service")
	return Plan{
		UnitPath:  unit,
		Content:   content,
		NeedsRoot: false,
		Label:     "marshal.service",
		PostInstall: []Cmd{
			{"systemctl", []string{"--user", "daemon-reload"}},
			{"systemctl", []string{"--user", "enable", "--now", "marshal.service"}},
			{"loginctl", []string{"enable-linger", c.User}},
		},
		PostRemove: []Cmd{
			{"systemctl", []string{"--user", "disable", "--now", "marshal.service"}},
		},
	}
}

func (s systemd) RemovePlan(c Config) Plan { return s.InstallPlan(c) }

func renderSystemdUnit(c Config) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=Marshal process manager\n")
	b.WriteString("After=network.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	fmt.Fprintf(&b, "ExecStart=%s daemon\n", c.Binary)
	b.WriteString("Restart=on-failure\n")
	if c.System {
		fmt.Fprintf(&b, "User=%s\n", c.User)
	}
	b.WriteString(systemdEnv("HOME", c.Home))
	if c.XDGData != "" {
		b.WriteString(systemdEnv("XDG_DATA_HOME", c.XDGData))
	}
	b.WriteString("\n[Install]\n")
	if c.System {
		b.WriteString("WantedBy=multi-user.target\n")
	} else {
		b.WriteString("WantedBy=default.target\n")
	}
	return b.String()
}

func systemdEnv(key, val string) string {
	if strings.ContainsAny(val, " \t") {
		return fmt.Sprintf("Environment=\"%s=%s\"\n", key, val)
	}
	return fmt.Sprintf("Environment=%s=%s\n", key, val)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/startup/ -run TestSystemd -v`
Expected: PASS (the `TestDetect*` `launchd` reference still fails to build — that is fixed in Task 3, so the package won't compile yet; run `go test ./internal/startup/ -run TestSystemd` only after Task 3 to see the whole package green). Do not commit yet.

---

## Task 3: launchd platform

**Files:**
- Create: `internal/startup/launchd.go`
- Test: `internal/startup/launchd_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/startup/launchd_test.go`:

```go
package startup

import (
	"strings"
	"testing"
)

func macConfig(system bool) Config {
	return Config{
		Binary:   "/usr/local/bin/marshal",
		User:     "alice",
		Home:     "/Users/alice",
		System:   system,
		StageDir: "/Users/alice/.marshal",
		UID:      501,
	}
}

func TestLaunchdUserPlist(t *testing.T) {
	p := launchd{}.InstallPlan(macConfig(false))
	if p.NeedsRoot {
		t.Fatal("user plan must not need root")
	}
	if p.UnitPath != "/Users/alice/Library/LaunchAgents/com.marshal.daemon.plist" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	for _, w := range []string{
		"<string>/usr/local/bin/marshal</string>",
		"<string>daemon</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>HOME</key>",
		"<string>/Users/alice</string>",
	} {
		if !strings.Contains(p.Content, w) {
			t.Errorf("content missing %q\n%s", w, p.Content)
		}
	}
	if strings.Contains(p.Content, "UserName") {
		t.Error("user agent must not set UserName")
	}
	if got := cmdStrings(p.PostInstall); !contains(got, "launchctl bootstrap gui/501 /Users/alice/Library/LaunchAgents/com.marshal.daemon.plist") {
		t.Errorf("PostInstall missing bootstrap: %v", got)
	}
}

func TestLaunchdSystemPlist(t *testing.T) {
	p := launchd{}.InstallPlan(macConfig(true))
	if !p.NeedsRoot {
		t.Fatal("system plan must need root")
	}
	if p.UnitPath != "/Library/LaunchDaemons/com.marshal.daemon.plist" {
		t.Fatalf("UnitPath = %s", p.UnitPath)
	}
	if p.StagePath != "/Users/alice/.marshal/com.marshal.daemon.plist" {
		t.Fatalf("StagePath = %s", p.StagePath)
	}
	if !strings.Contains(p.Content, "<key>UserName</key>") {
		t.Error("system daemon must set UserName")
	}
}

func TestLaunchdXDGAndEscape(t *testing.T) {
	p := launchd{}.InstallPlan(macConfig(false))
	if strings.Contains(p.Content, "XDG_DATA_HOME") {
		t.Error("XDG_DATA_HOME must be omitted when empty")
	}
	c := macConfig(false)
	c.XDGData = "/d&d"
	out := launchd{}.InstallPlan(c).Content
	if !strings.Contains(out, "/d&amp;d") {
		t.Error("ampersand must be XML-escaped")
	}
	if strings.Contains(out, "/d&d<") || strings.Contains(out, ">/d&d") {
		t.Error("raw unescaped ampersand present")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/startup/ -run TestLaunchd -v`
Expected: build failure — `undefined: launchd`.

- [ ] **Step 3: Implement the launchd platform**

Create `internal/startup/launchd.go`:

```go
package startup

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"
)

type launchd struct{}

const launchdLabel = "com.marshal.daemon"

func (launchd) InstallPlan(c Config) Plan {
	content := renderLaunchdPlist(c)
	file := launchdLabel + ".plist"
	if c.System {
		unit := "/Library/LaunchDaemons/" + file
		stage := filepath.Join(c.StageDir, file)
		return Plan{
			UnitPath:  unit,
			StagePath: stage,
			Content:   content,
			NeedsRoot: true,
			Label:     launchdLabel,
			PostInstall: []Cmd{
				{"sudo", []string{"cp", stage, unit}},
				{"sudo", []string{"launchctl", "bootstrap", "system", unit}},
			},
			PostRemove: []Cmd{
				{"sudo", []string{"launchctl", "bootout", "system", unit}},
				{"sudo", []string{"rm", "-f", unit}},
			},
		}
	}
	unit := filepath.Join(c.Home, "Library", "LaunchAgents", file)
	domain := fmt.Sprintf("gui/%d", c.UID)
	return Plan{
		UnitPath:  unit,
		Content:   content,
		NeedsRoot: false,
		Label:     launchdLabel,
		PostInstall: []Cmd{
			{"launchctl", []string{"bootstrap", domain, unit}},
		},
		PostRemove: []Cmd{
			{"launchctl", []string{"bootout", domain, unit}},
		},
	}
}

func (l launchd) RemovePlan(c Config) Plan { return l.InstallPlan(c) }

func renderLaunchdPlist(c Config) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n<dict>\n")
	stringElem(&b, "Label", launchdLabel)
	b.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	arrayString(&b, c.Binary)
	arrayString(&b, "daemon")
	b.WriteString("  </array>\n")
	boolElem(&b, "RunAtLoad", true)
	boolElem(&b, "KeepAlive", true)
	if c.System {
		stringElem(&b, "UserName", c.User)
	}
	b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
	stringElem(&b, "HOME", c.Home)
	if c.XDGData != "" {
		stringElem(&b, "XDG_DATA_HOME", c.XDGData)
	}
	b.WriteString("  </dict>\n")
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func stringElem(b *strings.Builder, key, val string) {
	fmt.Fprintf(b, "  <key>%s</key>\n  <string>%s</string>\n", xmlEsc(key), xmlEsc(val))
}

func arrayString(b *strings.Builder, val string) {
	fmt.Fprintf(b, "    <string>%s</string>\n", xmlEsc(val))
}

func boolElem(b *strings.Builder, key string, v bool) {
	tag := "false"
	if v {
		tag = "true"
	}
	fmt.Fprintf(b, "  <key>%s</key>\n  <%s/>\n", xmlEsc(key), tag)
}

func xmlEsc(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
```

- [ ] **Step 4: Run the whole package — all green**

Run: `go test ./internal/startup/ -v`
Expected: PASS for all `TestDetect*`, `TestSystemd*`, `TestLaunchd*`.

- [ ] **Step 5: Commit**

```bash
git add internal/startup/startup.go internal/startup/systemd.go internal/startup/launchd.go \
        internal/startup/detect_test.go internal/startup/systemd_test.go internal/startup/launchd_test.go
git commit -m "feat(startup): pure systemd/launchd plan generation + detection

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Apply / Remove / StageAndPrint executor

**Files:**
- Test: `internal/startup/install_test.go` (the implementations already exist in `startup.go` from Task 1; this task adds their tests)

- [ ] **Step 1: Write the failing test**

Create `internal/startup/install_test.go`:

```go
package startup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recRunner struct{ cmds []string }

func (r *recRunner) Run(c Cmd) error {
	r.cmds = append(r.cmds, c.String())
	return nil
}

func TestApplyWritesAndRuns(t *testing.T) {
	home := t.TempDir()
	p := Plan{
		UnitPath:    filepath.Join(home, ".config", "systemd", "user", "marshal.service"),
		Content:     "UNIT-BODY",
		PostInstall: []Cmd{{Name: "systemctl", Args: []string{"--user", "enable", "--now", "marshal.service"}}},
	}
	rr := &recRunner{}
	if err := Apply(p, rr); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	data, err := os.ReadFile(p.UnitPath)
	if err != nil {
		t.Fatalf("read unit: %v", err)
	}
	if string(data) != "UNIT-BODY" {
		t.Fatalf("content = %q", data)
	}
	info, _ := os.Stat(p.UnitPath)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if len(rr.cmds) != 1 || rr.cmds[0] != "systemctl --user enable --now marshal.service" {
		t.Fatalf("cmds = %v", rr.cmds)
	}
}

func TestRemoveDeletesAndRuns(t *testing.T) {
	home := t.TempDir()
	unit := filepath.Join(home, "marshal.service")
	if err := os.WriteFile(unit, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Plan{UnitPath: unit, PostRemove: []Cmd{{Name: "systemctl", Args: []string{"--user", "disable", "--now", "marshal.service"}}}}
	rr := &recRunner{}
	if err := Remove(p, rr); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Fatal("unit file should be gone")
	}
	if len(rr.cmds) != 1 {
		t.Fatalf("cmds = %v", rr.cmds)
	}
}

func TestRemoveMissingFileIsOK(t *testing.T) {
	p := Plan{UnitPath: filepath.Join(t.TempDir(), "nope.service")}
	if err := Remove(p, &recRunner{}); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
}

func TestStageAndPrint(t *testing.T) {
	dir := t.TempDir()
	stage := filepath.Join(dir, "marshal.service")
	p := Plan{
		StagePath:   stage,
		Content:     "UNIT-BODY",
		NeedsRoot:   true,
		PostInstall: []Cmd{{Name: "sudo", Args: []string{"cp", stage, "/etc/systemd/system/marshal.service"}}},
	}
	var buf bytes.Buffer
	if err := StageAndPrint(p, &buf); err != nil {
		t.Fatalf("StageAndPrint: %v", err)
	}
	data, err := os.ReadFile(stage)
	if err != nil || string(data) != "UNIT-BODY" {
		t.Fatalf("staged = %q err=%v", data, err)
	}
	if !strings.Contains(buf.String(), "sudo cp "+stage) {
		t.Fatalf("output missing sudo cp: %q", buf.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `go test ./internal/startup/ -run 'TestApply|TestRemove|TestStage' -v`
Expected: PASS (the implementations already exist from Task 1).

- [ ] **Step 3: Commit**

```bash
git add internal/startup/install_test.go
git commit -m "test(startup): cover Apply/Remove/StageAndPrint side effects

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: CLI commands + registration

**Files:**
- Create: `cmd/marshal/startup.go`
- Modify: `cmd/marshal/main.go` (add `startupCmd()`, `unstartupCmd()` to `root.AddCommand`)
- Test: `cmd/marshal/startup_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/marshal/startup_test.go`:

```go
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// startup --system never touches the real init system: it stages the unit file
// under the state dir and prints the sudo block. Safe to run in CI on the host OS
// (both darwin and linux stage+print for --system).
func TestStartupSystemStagesAndPrints(t *testing.T) {
	home := t.TempDir()
	data := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", data)

	root := rootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"startup", "--system"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "sudo") {
		t.Fatalf("expected a sudo block, got:\n%s", out.String())
	}
	staged, _ := filepath.Glob(filepath.Join(data, "marshal", "*"))
	if len(staged) == 0 {
		t.Fatalf("no staged unit file under %s/marshal", data)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/marshal/ -run TestStartupSystem -v`
Expected: FAIL — `unknown command "startup"` (command not registered yet).

- [ ] **Step 3: Implement the commands**

Create `cmd/marshal/startup.go`:

```go
package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"marshal/internal/startup"
	"marshal/internal/store"
)

func resolveConfig(system bool) (startup.Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return startup.Config{}, fmt.Errorf("locate marshal binary: %w", err)
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	u, err := user.Current()
	if err != nil {
		return startup.Config{}, fmt.Errorf("resolve current user: %w", err)
	}
	st, err := store.New()
	if err != nil {
		return startup.Config{}, err
	}
	return startup.Config{
		Binary:   exe,
		User:     u.Username,
		Home:     u.HomeDir,
		XDGData:  os.Getenv("XDG_DATA_HOME"),
		System:   system,
		StageDir: st.Dir(),
		UID:      os.Getuid(),
	}, nil
}

func startupCmd() *cobra.Command {
	var system bool
	cmd := &cobra.Command{
		Use:   "startup",
		Short: "Install a boot service that runs marshald at startup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(system)
			if err != nil {
				return err
			}
			plat, err := startup.Detect(runtime.GOOS)
			if err != nil {
				return err
			}
			plan := plat.InstallPlan(cfg)
			out := cmd.OutOrStdout()
			if plan.NeedsRoot {
				return startup.StageAndPrint(plan, out)
			}
			if err := startup.Apply(plan, startup.ExecRunner{Out: out, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			fmt.Fprintf(out, "Installed %s\n", plan.UnitPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "install a system-level (root) service")
	return cmd
}

func unstartupCmd() *cobra.Command {
	var system bool
	cmd := &cobra.Command{
		Use:   "unstartup",
		Short: "Remove the boot service installed by `marshal startup`",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(system)
			if err != nil {
				return err
			}
			plat, err := startup.Detect(runtime.GOOS)
			if err != nil {
				return err
			}
			plan := plat.RemovePlan(cfg)
			out := cmd.OutOrStdout()
			if plan.NeedsRoot {
				fmt.Fprintln(out, "Run these commands to remove the system service:")
				fmt.Fprintln(out)
				for _, c := range plan.PostRemove {
					fmt.Fprintf(out, "  %s\n", c)
				}
				fmt.Fprintln(out)
				return nil
			}
			if err := startup.Remove(plan, startup.ExecRunner{Out: out, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed %s\n", plan.UnitPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "remove the system-level (root) service")
	return cmd
}
```

- [ ] **Step 4: Register the commands**

In `cmd/marshal/main.go`, add `startupCmd(),` and `unstartupCmd(),` to the `root.AddCommand(...)` list (place them after `resurrectCmd(),` and before `killCmd(),`):

```go
		saveCmd(),
		resurrectCmd(),
		startupCmd(),
		unstartupCmd(),
		killCmd(),
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./cmd/marshal/ -run TestStartupSystem -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/marshal/startup.go cmd/marshal/main.go cmd/marshal/startup_test.go
git commit -m "feat(cli): add marshal startup/unstartup boot-service commands

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Full gate + manual smoke (host OS)

- [ ] **Step 1: Run the full gate**

```bash
go build ./...
go test ./... -race -count=1
go vet ./...
gofmt -l .
```
Expected: build ok; all tests pass under race; vet clean; `gofmt -l .` prints nothing.

- [ ] **Step 2: Manual smoke — user-level install on this macOS host**

```bash
go build -o marshal ./cmd/marshal
export XDG_DATA_HOME=/tmp/m4smoke && rm -rf "$XDG_DATA_HOME"
./marshal startup
launchctl list | grep com.marshal.daemon        # expect the agent listed
ls ~/Library/LaunchAgents/com.marshal.daemon.plist
./marshal unstartup
launchctl list | grep com.marshal.daemon || echo "removed"
ls ~/Library/LaunchAgents/com.marshal.daemon.plist 2>/dev/null || echo "file gone"
```
Expected: `startup` writes the LaunchAgent and bootstraps it; `unstartup` boots it out and deletes the file. (If `launchctl bootstrap` reports the service already loaded, run `./marshal unstartup` first.)

- [ ] **Step 3: Manual smoke — `--system` prints, does not execute**

```bash
./marshal startup --system
ls "$XDG_DATA_HOME"/marshal/com.marshal.daemon.plist   # staged, not installed
```
Expected: a staged plist under the state dir and a printed `sudo …` block; nothing installed to `/Library/LaunchDaemons`.

- [ ] **Step 4: Commit any formatting fixes** (only if `gofmt -l .` listed files)

```bash
gofmt -w .
git add -A && git commit -m "style: gofmt"
```

---

## Task 7: Handoff + finish branch

- [ ] **Step 1: Write the handoff**

Create `docs/handoffs/2026-06-17-m4-startup.md` covering: M4 complete, what was added (`internal/startup` + `startup`/`unstartup` CLI), the four artifact variants, how to build/run/test, the manual smoke results, deferred items (non-systemd Linux unsupported; `launchctl load -w` fallback documented; Windows still deferred), and the next step (merge to `main`; M-series of agent-core complete → next is sub-project #2, the metrics/log pipeline, which absorbs the deferred log items).

- [ ] **Step 2: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill: full gate green, then a local `--no-ff` merge of `m4-startup` into `main` (no git remote — same as M1/M2/M3).

---

## Self-Review notes

- **Spec coverage:** privilege model (§2)→Tasks 2,3,5; artifacts (§4)→Tasks 2,3; CLI (§5)→Task 5; detection (§3.2)→Task 1; testing (§6)→Tasks 1–6. The design's `Plan.PostInstall []string` is refined to `[]Cmd` here (structured name+args) so the same list both runs and prints cleanly — a deliberate, noted refinement.
- **Type consistency:** `Config`, `Cmd`, `Plan`, `Platform`, `Runner`, `ExecRunner`, `Apply`, `Remove`, `StageAndPrint`, `Detect`/`detect` are defined once in Task 1 and used unchanged in Tasks 2–5. `systemd`/`launchd` are unexported structs; `Label`/`UnitPath`/`StagePath` names match across tasks.
- **No placeholders:** every step has full code and exact commands.
