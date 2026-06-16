package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"marshal/internal/daemon"
	"marshal/internal/store"
)

func daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Run marshald in the foreground (used internally and by boot services)",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := store.New()
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, st)
		},
	}
}

// runDaemonForTest runs the daemon serve loop with an explicit context and store.
// It exists so e2e tests can run the daemon in-process with hermetic teardown.
func runDaemonForTest(ctx context.Context, st *store.Store) error {
	return daemon.Run(ctx, st)
}
