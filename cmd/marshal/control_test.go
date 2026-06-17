package main

import (
	"testing"

	"marshal/internal/config"
	"marshal/internal/pb"
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
