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
