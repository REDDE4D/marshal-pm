package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/client"
	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/server"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

// prepareSelfEnroll writes the localhost server block into the DEFAULT store so
// the local daemon enrolls against the co-located server.
// It does NOT write the apps — those are started via client.Connect + c.Start
// to avoid double-starting (client.Connect auto-spawns the daemon which would
// resurrect apps from the store, then c.Start would start them a second time).
func prepareSelfEnroll(st *store.Store, listenPort, enrollToken, fingerprint, hostname string) error {
	return st.SaveServer(&config.ServerConfig{
		Address:     "localhost:" + listenPort,
		Name:        hostname,
		Token:       enrollToken,
		Fingerprint: fingerprint,
	})
}

// runSelfEnroll boots the fleet server + dashboard in-process, then enrolls the
// ONE default-store daemon against it and starts the yaml's apps on that daemon.
// Ctrl-C stops the server; the daemon and its apps keep running — stop them with
// `marshal stop <name>` / stop the daemon with `marshal kill`.
func runSelfEnroll(cmd *cobra.Command, dataDir, listen, httpListen, tlsCert, tlsKey, yamlPath string) error {
	if dataDir == "" {
		dataDir = defaultServerDataDir()
	}
	if httpListen == "" {
		httpListen = ":9001" // the whole point is the dashboard — on by default
	}
	out := cmd.OutOrStdout()

	// Apps from the user's marshal.yaml (env_file resolved). Any server: block in
	// their file is ignored — we inject our own localhost enrollment below.
	cfg, err := config.Load(yamlPath)
	if err != nil {
		return err
	}
	if len(cfg.Apps) == 0 {
		return fmt.Errorf("no apps found in %s", yamlPath)
	}

	// Cert + fingerprint, then ensure a dashboard password and mint a fresh enroll
	// token — all while the server is down, so it loads them on start.
	_, fp, err := server.LoadOrCreateCert(dataDir, tlsCert, tlsKey)
	if err != nil {
		return fmt.Errorf("load tls cert: %w", err)
	}
	if has, _ := server.HasDashboardUserDir(dataDir); !has {
		fmt.Fprintln(out, "No dashboard password set — choose one:")
		pw, perr := readPassword(cmd)
		if perr != nil {
			return perr
		}
		if err := server.SetDashboardPassword(dataDir, "admin", pw); err != nil {
			return err
		}
	}
	enroll, err := server.RotateToken(dataDir, "enroll")
	if err != nil {
		return fmt.Errorf("mint enroll token: %w", err)
	}

	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("parse --listen %q: %w", listen, err)
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "local"
	}

	// Write the localhost server block into the DEFAULT store (not a separate
	// agent sub-dir). client.Connect will auto-spawn the daemon which will pick
	// up this server block and enroll against the co-located server.
	st, err := store.New()
	if err != nil {
		return err
	}
	if err := prepareSelfEnroll(st, port, enroll, fp, name); err != nil {
		return err
	}

	lis, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(out, "marshal: fleet server on %s (data %s)\n", lis.Addr(), dataDir)
	fmt.Fprintf(out, "marshal: dashboard on https://localhost%s — log in as 'admin'\n", httpListen)
	fmt.Fprintf(out, "marshal: starting %d app(s) on local daemon %q\n", len(cfg.Apps), name)
	fmt.Fprintf(out, "marshal: Ctrl-C stops the server; apps keep running (stop with `marshal stop <name>`, daemon with `marshal kill`)\n")

	// Start the server in the background; the daemon is auto-spawned by
	// client.Connect below.
	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeDir(ctx, lis, dataDir, tlsCert, tlsKey, httpListen) }()

	// Start apps on the local daemon via the normal client path. client.Connect
	// auto-spawns the daemon if it isn't running yet. The daemon will pick up the
	// server block we wrote above and enroll against the in-process server.
	c, conn, err := client.Connect(st)
	if err != nil {
		return fmt.Errorf("connect to local daemon: %w", err)
	}
	defer conn.Close()

	startCtx, startCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startCancel()

	specs := make([]*pb.AppSpec, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		specs = append(specs, appToSpec(a))
	}
	if _, err := c.Start(startCtx, &pb.StartRequest{Apps: specs}); err != nil {
		return fmt.Errorf("start apps: %w", err)
	}
	// Persist apps so the daemon can resurrect them after a restart.
	if _, err := c.Save(startCtx, &pb.Empty{}); err != nil {
		return fmt.Errorf("save apps: %w", err)
	}

	fmt.Fprintf(out, "marshal: apps started — watching server (Ctrl-C to stop server only)\n")

	<-ctx.Done()
	// Block briefly for the server goroutine to finish graceful shutdown so a
	// real Serve-time error (e.g. TLS problem after Listen succeeded) is not
	// silently dropped by an immediate non-blocking default branch.
	select {
	case err := <-errCh:
		if err != nil && ctx.Err() == nil {
			return err
		}
	case <-time.After(2 * time.Second):
	}
	fmt.Fprintln(out, "marshal: server stopped (daemon and apps are still running)")
	return nil
}
