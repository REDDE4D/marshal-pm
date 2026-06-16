// Command marshal is the foreground supervisor CLI (milestone M1).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/config"
	"marshal/internal/manager"
	"marshal/internal/version"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "marshal",
		Short:   "Marshal — a free process supervisor",
		Version: version.String(),
	}
	root.AddCommand(runCmd())
	return root
}

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run <marshal.yaml>",
		Short: "Run and supervise apps in the foreground until interrupted",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			m := manager.New(cfg)

			// Periodic status line until shutdown.
			go func() {
				ticker := time.NewTicker(2 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						printStatus(cmd, m)
					}
				}
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "marshal: supervising %d app(s); press Ctrl-C to stop\n", len(cfg.Apps))
			m.Run(ctx) // blocks until ctx canceled and all instances stop
			fmt.Fprintln(cmd.OutOrStdout(), "marshal: all processes stopped")
			return nil
		},
	}
}

func printStatus(cmd *cobra.Command, m *manager.Manager) {
	for _, s := range m.Snapshot() {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %-10s pid=%d restarts=%d\n",
			s.Label, s.State, s.Pid, s.Restarts)
	}
}
