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

	"marshal/internal/server"
)

func serverCmd() *cobra.Command {
	var listen, dataDir string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Marshal central server (fleet aggregation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			lis, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen %s: %w", listen, err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(cmd.OutOrStdout(), "marshal server: listening on %s, data %s\n", lis.Addr(), dataDir)
			return server.ServeDir(ctx, lis, dataDir)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "address to listen on")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "metric storage directory (default $XDG_DATA_HOME/marshal-server)")
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
