// Package client dials marshald over its Unix socket, auto-spawning the daemon
// when it is not already running.
package client

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const spawnTimeout = 3 * time.Second

// Connect returns a connected Daemon client, spawning the daemon if needed.
// The caller must Close the returned conn.
func Connect(st *store.Store) (pb.DaemonClient, *grpc.ClientConn, error) {
	if !alive(st.SocketPath()) {
		if err := spawn(st); err != nil {
			return nil, nil, err
		}
		if err := waitReady(st.SocketPath()); err != nil {
			return nil, nil, err
		}
	}
	conn, err := grpc.NewClient("unix:"+st.SocketPath(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial daemon: %w", err)
	}
	return pb.NewDaemonClient(conn), conn, nil
}

// alive reports whether something is accepting connections on the socket.
func alive(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// spawn launches `marshal daemon` detached, with output to the daemon log.
func spawn(st *store.Store) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate marshal binary: %w", err)
	}
	if err := st.EnsureDir(); err != nil {
		return err
	}
	logf, err := os.OpenFile(st.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logf.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach into its own session
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}
	return nil
}

// waitReady polls the socket until the daemon answers or the timeout elapses.
func waitReady(sock string) error {
	deadline := time.Now().Add(spawnTimeout)
	for time.Now().Before(deadline) {
		if alive(sock) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready within %s", spawnTimeout)
}
