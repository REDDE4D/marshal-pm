package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/daemon"
	"github.com/REDDE4D/marshal-pm/internal/server"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

// runSelfEnroll boots the fleet server + dashboard and an in-process agent that
// enrolls against it and supervises the apps in yamlPath — the single-host
// "just give me a dashboard" path, all in one process.
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

	// Prepare the in-process agent's store: the localhost server block + the apps
	// (daemon.Run auto-resurrects from the store).
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("parse --listen %q: %w", listen, err)
	}
	name, _ := os.Hostname()
	if name == "" {
		name = "local"
	}
	agentDir := filepath.Join(dataDir, "agent")
	st := store.NewAt(agentDir)
	if err := st.EnsureDir(); err != nil {
		return err
	}
	if err := st.SaveServer(&config.ServerConfig{
		Address: "localhost:" + port, Name: name, Token: enroll, Fingerprint: fp,
	}); err != nil {
		return err
	}
	if err := st.Save(cfg.Apps); err != nil {
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
	fmt.Fprintf(out, "marshal: enrolling local agent %q with %d app(s); Ctrl-C to stop\n", name, len(cfg.Apps))

	// Run the server and the agent concurrently; the agent's fleet client retries
	// until the server is listening, so startup order doesn't matter.
	errCh := make(chan error, 2)
	go func() { errCh <- server.ServeDir(ctx, lis, dataDir, tlsCert, tlsKey, httpListen) }()
	go func() { errCh <- daemon.Run(ctx, st) }()

	<-ctx.Done()
	// Surface the first non-context error from either side.
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				return err
			}
		default:
		}
	}
	fmt.Fprintln(out, "marshal: stopped")
	return nil
}
