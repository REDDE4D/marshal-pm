package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"marshal/internal/server"
)

func serverCmd() *cobra.Command {
	var listen string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the Marshal central server (fleet aggregation)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			lis, err := net.Listen("tcp", listen)
			if err != nil {
				return fmt.Errorf("listen %s: %w", listen, err)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			fmt.Fprintf(cmd.OutOrStdout(), "marshal server: listening on %s\n", lis.Addr())
			return server.Serve(ctx, lis, server.NewRegistry())
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":9000", "address to listen on")
	return cmd
}
