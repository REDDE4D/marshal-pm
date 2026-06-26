// Command marshal is the control CLI and daemon entry point for Marshal.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/status"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/manager"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/version"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", cliError(err))
		os.Exit(1)
	}
}

// cliError strips the gRPC status wrapper so users see just the message
// (e.g. `no app matching "x"` rather than `rpc error: code = NotFound ...`).
func cliError(err error) string {
	if st, ok := status.FromError(err); ok {
		return st.Message()
	}
	return err.Error()
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:     "marshal",
		Short:   "Marshal — a free process supervisor",
		Version: version.String(),
	}
	root.AddCommand(
		runCmd(),
		daemonCmd(),
		startCmd(),
		selectorCmd("stop <name|id|all>", "Gracefully stop app(s)",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Stop(ctx, sel)
			}),
		selectorCmd("restart <name|id|all>", "Restart app(s)",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Restart(ctx, sel)
			}),
		selectorCmd("delete <name|id|all>", "Stop and remove app(s) from management",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Delete(ctx, sel)
			}),
		selectorCmd("reset <name|id|all>", "Reset restart counter(s) for app(s)",
			func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Reset(ctx, sel)
			}),
		flushCmd(),
		listCmd(),
		describeCmd(),
		logsCmd(),
		metricsCmd(),
		saveCmd(),
		resurrectCmd(),
		startupCmd(),
		unstartupCmd(),
		killCmd(),
		serverCmd(),
		fleetCmd(),
		importCmd(),
		enrollCmd(),
		unenrollCmd(),
	)
	return root
}

// runCmd keeps the M1 foreground supervisor (no daemon), now on the dynamic manager.
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

			m := manager.New(ctx)
			for _, app := range cfg.Apps {
				if _, err := m.Add(app); err != nil {
					return err
				}
			}

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
			<-ctx.Done()
			m.StopAll()
			fmt.Fprintln(cmd.OutOrStdout(), "marshal: all processes stopped")
			return nil
		},
	}
}

func printStatus(cmd *cobra.Command, m *manager.Manager) {
	for _, s := range m.List() {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %-10s pid=%d restarts=%d\n",
			s.Label, s.State, s.Pid, s.Restarts)
	}
}
