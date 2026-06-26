package client

import (
	"testing"

	"github.com/REDDE4D/marshal-pm/internal/store"
)

func TestConnectExistingNoDaemon(t *testing.T) {
	st := store.NewAt(t.TempDir()) // fresh dir → no socket → no daemon
	_, _, err := ConnectExisting(st)
	if err == nil {
		t.Fatal("expected error when no daemon is running")
	}
}
