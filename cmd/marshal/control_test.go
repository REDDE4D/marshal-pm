package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

// A bare name/id/all is a single literal selector; a path to a marshal.yaml
// expands to the names of the apps it defines, so `marshal stop marshal.yaml`
// works like the matching `marshal start marshal.yaml`.
func TestTargetsFromArg(t *testing.T) {
	for _, lit := range []string{"api", "all", "3"} {
		tg, fromFile, err := targetsFromArg(lit)
		if err != nil || fromFile || len(tg) != 1 || tg[0] != lit {
			t.Fatalf("targetsFromArg(%q) = %v, fromFile=%v, err=%v", lit, tg, fromFile, err)
		}
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "marshal.yaml")
	if err := os.WriteFile(p, []byte("apps:\n  - name: Alpha\n    cmd: ./a\n  - name: Beta\n    cmd: ./b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tg, fromFile, err := targetsFromArg(p)
	if err != nil {
		t.Fatalf("targetsFromArg(file): %v", err)
	}
	if !fromFile {
		t.Errorf("expected fromFile=true for a yaml path")
	}
	if strings.Join(tg, ",") != "Alpha,Beta" {
		t.Errorf("targets = %v, want [Alpha Beta]", tg)
	}
}

func TestRenderProcTableBorders(t *testing.T) {
	list := &pb.ProcList{Procs: []*pb.ProcInfo{
		{Id: 0, Name: "SurvivorAdmin", State: "online", Pid: 1234, Cpu: 0.5, Mem: 30 * 1024 * 1024, UptimeMs: 62000},
		{Id: 1, Name: "MasterAdmin", State: "errored", Restarts: 3},
	}}
	var buf bytes.Buffer
	renderProcTable(&buf, list, false)
	out := buf.String()
	for _, want := range []string{"┌", "┐", "└", "┘", "┼", "│", "NAME", "STATE", "SurvivorAdmin", "online", "errored"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q:\n%s", want, out)
		}
	}
	// color disabled → no ANSI escapes (safe for pipes/files).
	if strings.Contains(out, "\x1b[") {
		t.Errorf("color was disabled but ANSI escapes are present:\n%q", out)
	}
	// Every rendered line must have the same display width (aligned box).
	var width int
	for i, ln := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		w := utf8.RuneCountInString(ln)
		if i == 0 {
			width = w
		} else if w != width {
			t.Fatalf("line %d width %d != %d:\n%s", i, w, width, out)
		}
	}
}

func TestRenderProcTableColorsState(t *testing.T) {
	list := &pb.ProcList{Procs: []*pb.ProcInfo{
		{Name: "A", State: "online"},
		{Name: "B", State: "errored"},
	}}
	var buf bytes.Buffer
	renderProcTable(&buf, list, true)
	out := buf.String()
	if !strings.Contains(out, "\x1b[32m") { // green online
		t.Errorf("online state should be green:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[31m") { // red errored
		t.Errorf("errored state should be red:\n%q", out)
	}
	if !strings.Contains(out, "\x1b[0m") {
		t.Errorf("missing ANSI reset:\n%q", out)
	}
}

func TestAppToSpecWritesLogs(t *testing.T) {
	sz := 50
	comp := false
	a := config.App{Name: "api", Cmd: "./api", Logs: &config.LogRetention{MaxSizeMB: &sz, Compress: &comp}}
	spec := appToSpec(a)
	if spec.GetLogs() == nil || spec.GetLogs().MaxSizeMb == nil || *spec.GetLogs().MaxSizeMb != 50 {
		t.Fatalf("max_size_mb not sent: %+v", spec.GetLogs())
	}
	if spec.GetLogs().Compress == nil || *spec.GetLogs().Compress != false {
		t.Fatalf("compress:false not sent")
	}
	if spec.GetLogs().MaxBackups != nil {
		t.Fatalf("omitted max_backups must stay nil")
	}
	// No logs block -> nil, not an empty message.
	if appToSpec(config.App{Name: "web", Cmd: "./web"}).GetLogs() != nil {
		t.Fatalf("absent logs block must send nil")
	}
}

func TestStreamFromFlags(t *testing.T) {
	if s, err := streamFromFlags(false, false); err != nil || s != pb.LogStream_LOG_STREAM_UNSPECIFIED {
		t.Fatalf("merged: got %v err %v", s, err)
	}
	if s, err := streamFromFlags(true, false); err != nil || s != pb.LogStream_LOG_STREAM_STDOUT {
		t.Fatalf("stdout: got %v err %v", s, err)
	}
	if s, err := streamFromFlags(false, true); err != nil || s != pb.LogStream_LOG_STREAM_STDERR {
		t.Fatalf("stderr: got %v err %v", s, err)
	}
	if _, err := streamFromFlags(true, true); err == nil {
		t.Fatal("both flags must be rejected")
	}
}

func TestEnrollmentHeader(t *testing.T) {
	if got := enrollmentHeader(&config.ServerConfig{Address: "srv:9000"}); !strings.Contains(got, "srv:9000") || !strings.Contains(got, "enrolled") {
		t.Errorf("header = %q", got)
	}
	if got := enrollmentHeader(nil); !strings.Contains(got, "not enrolled") {
		t.Errorf("header = %q", got)
	}
}

func TestPersistServer(t *testing.T) {
	st := store.NewAt(t.TempDir())
	if err := persistServer(st, &config.Config{Server: &config.ServerConfig{Address: "srv:9000"}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Address != "srv:9000" {
		t.Fatalf("got %+v", got)
	}

	st2 := store.NewAt(t.TempDir())
	if err := persistServer(st2, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	if g, _ := st2.LoadServer(); g != nil {
		t.Fatalf("expected no fleet.json for a config without a server block, got %+v", g)
	}
}
