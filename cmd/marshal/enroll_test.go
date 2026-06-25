package main

import (
	"io"
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestEnrollWritesServerConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cmd := enrollCmd()
	cmd.SetArgs([]string{"srv:9000", "--token", "enr", "--fingerprint", "AA:BB", "--name", "h1"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	st, err := store.New()
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := st.LoadServer()
	if sc == nil || sc.Address != "srv:9000" || sc.Token != "enr" || sc.Fingerprint != "AA:BB" || sc.Name != "h1" {
		t.Fatalf("server config = %+v", sc)
	}
}

func TestEnrollRequiresTokenAndPin(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	noTok := enrollCmd()
	noTok.SetArgs([]string{"srv:9000", "--fingerprint", "AA"})
	noTok.SetOut(io.Discard)
	noTok.SetErr(io.Discard)
	if err := noTok.Execute(); err == nil {
		t.Error("expected error without --token")
	}
	noPin := enrollCmd()
	noPin.SetArgs([]string{"srv:9000", "--token", "enr"})
	noPin.SetOut(io.Discard)
	noPin.SetErr(io.Discard)
	if err := noPin.Execute(); err == nil {
		t.Error("expected error without --fingerprint/--ca")
	}
}

func TestUnenrollClearsServerConfig(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	st, _ := store.New()
	if err := st.SaveServer(&config.ServerConfig{Address: "srv:9000", Token: "enr"}); err != nil {
		t.Fatal(err)
	}
	cmd := unenrollCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unenroll: %v", err)
	}
	if sc, _ := st.LoadServer(); sc != nil {
		t.Fatalf("server config still present: %+v", sc)
	}
}
