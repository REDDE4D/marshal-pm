package main

import (
	"strings"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestPrepareSelfEnrollUsesDefaultStore(t *testing.T) {
	st := store.NewAt(t.TempDir())
	if err := prepareSelfEnroll(st, "9000", "enr", "fp", "h1"); err != nil {
		t.Fatal(err)
	}
	sc, err := st.LoadServer()
	if err != nil {
		t.Fatal(err)
	}
	if sc == nil || sc.Address != "localhost:9000" || sc.Token != "enr" || sc.Fingerprint != "fp" {
		t.Fatalf("server block = %+v", sc)
	}
}

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
