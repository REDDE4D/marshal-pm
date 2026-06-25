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

	"github.com/REDDE4D/marshal-pm/internal/server"
)

func serverCmd() *cobra.Command {
	var listen, dataDir, tlsCert, tlsKey, httpListen, selfEnroll string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Marshal central server (fleet aggregation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if selfEnroll != "" {
				// Single-host quickstart: server + dashboard + a local agent
				// running the given marshal.yaml, all in one process.
				return runSelfEnroll(cmd, dataDir, listen, httpListen, tlsCert, tlsKey, selfEnroll)
			}
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			// Load (or generate) the TLS cert to print the fingerprint on startup.
			// ServeDir will load it again from the same paths (idempotent).
			_, fp, err := server.LoadOrCreateCert(dataDir, tlsCert, tlsKey)
			if err != nil {
				return fmt.Errorf("load tls cert: %w", err)
			}
			if err := server.InitAuthPrint(dataDir, cmd.OutOrStdout()); err != nil {
				return fmt.Errorf("init auth: %w", err)
			}
			lis, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen %s: %w", listen, err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(cmd.OutOrStdout(), "marshal server: listening on %s, data %s\n", lis.Addr(), dataDir)
			fmt.Fprintf(cmd.OutOrStdout(), "marshal server: cert fingerprint %s\n", fp)
			if httpListen != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "marshal server: dashboard on %s\n", httpListen)
			}
			return server.ServeDir(ctx, lis, dataDir, tlsCert, tlsKey, httpListen)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "address to listen on")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "metric storage directory (default $XDG_DATA_HOME/marshal-server)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "path to TLS cert PEM (default <data-dir>/cert.pem, generated if absent)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "path to TLS key PEM (default <data-dir>/key.pem)")
	cmd.Flags().StringVar(&httpListen, "http-listen", "", "address for the web dashboard (e.g. :9001); disabled if empty")
	cmd.Flags().StringVar(&selfEnroll, "self-enroll", "", "single-host quickstart: also run a local agent for this marshal.yaml and show it in the dashboard (dashboard defaults to :9001)")
	cmd.AddCommand(serverFingerprintCmd(), serverTokenCmd(), serverAgentCmd(), serverPasswdCmd(), serverAuditCmd(), serverStartupCmd())
	return cmd
}

// defaultServerDataDir resolves $XDG_DATA_HOME/marshal-server, falling back to
// $HOME/.local/share/marshal-server, mirroring the agent store's resolution.
func defaultServerDataDir() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "marshal-server")
	}
	return filepath.Join(os.Getenv("HOME"), ".local", "share", "marshal-server")
}
