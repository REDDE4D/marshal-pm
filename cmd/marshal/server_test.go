package main

import "testing"

func TestServerCmdInvalidListen(t *testing.T) {
	cmd := serverCmd()
	cmd.SetArgs([]string{"--listen", "127.0.0.1:99999"}) // port out of range -> Listen errors fast
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a listen error for an invalid port")
	}
}
