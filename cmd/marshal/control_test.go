package main

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

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
