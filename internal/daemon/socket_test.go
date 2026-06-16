package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleSocketDeletesDeadFile(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "marshald.sock")
	// A plain file with no listener simulates a stale socket.
	if err := os.WriteFile(sock, nil, 0o644); err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}
	removeStaleSocket(sock)
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("stale socket not removed (stat err = %v)", err)
	}
}

func TestRemoveStaleSocketPreservesLiveSocket(t *testing.T) {
	// macOS limits Unix socket paths to ~104 bytes (sun_path), and t.TempDir()
	// can exceed that. Use a short base dir so net.Listen succeeds.
	dir, err := os.MkdirTemp("", "ms")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	lis, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	removeStaleSocket(sock)
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("live socket was removed: %v", err)
	}
}

func TestRemoveStaleSocketNoFileIsNoop(t *testing.T) {
	removeStaleSocket(filepath.Join(t.TempDir(), "nope.sock"))
	// no panic / no error == pass
}
