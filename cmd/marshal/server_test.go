package main

import (
	"strings"
	"testing"
)

func TestServerCmdInvalidListen(t *testing.T) {
	cmd := serverCmd()
	cmd.SetArgs([]string{"--listen", "127.0.0.1:99999"}) // port out of range -> Listen errors fast
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a listen error for an invalid port")
	}
}

func TestServerCmdHasDataDirFlag(t *testing.T) {
	cmd := serverCmd()
	if cmd.Flags().Lookup("data-dir") == nil {
		t.Fatal("server command missing --data-dir flag")
	}
	if cmd.Flags().Lookup("listen") == nil {
		t.Fatal("server command missing --listen flag")
	}
	if !strings.Contains(cmd.Short, "central server") {
		t.Fatalf("unexpected Short: %q", cmd.Short)
	}
}
