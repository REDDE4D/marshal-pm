package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/client"
	"marshal/internal/config"
	"marshal/internal/pb"
	"marshal/internal/store"
)

// withClient connects to (or spawns) the daemon and runs fn.
func withClient(fn func(context.Context, pb.DaemonClient) error) error {
	st, err := store.New()
	if err != nil {
		return err
	}
	c, conn, err := client.Connect(st)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fn(ctx, c)
}

func appToSpec(a config.App) *pb.AppSpec {
	spec := &pb.AppSpec{
		Name:        a.Name,
		Cmd:         a.Cmd,
		Args:        a.Args,
		Cwd:         a.Cwd,
		Instances:   int32(a.Instances),
		Env:         a.Env,
		Restart:     string(a.Restart),
		MaxRestarts: int32(a.MaxRestarts),
		KillTimeout: a.KillTimeout.Duration.String(),
	}
	if a.Logs != nil {
		lr := &pb.LogRetention{}
		if a.Logs.MaxSizeMB != nil {
			v := int32(*a.Logs.MaxSizeMB)
			lr.MaxSizeMb = &v
		}
		if a.Logs.MaxBackups != nil {
			v := int32(*a.Logs.MaxBackups)
			lr.MaxBackups = &v
		}
		if a.Logs.MaxAgeDays != nil {
			v := int32(*a.Logs.MaxAgeDays)
			lr.MaxAgeDays = &v
		}
		if a.Logs.Compress != nil {
			v := *a.Logs.Compress
			lr.Compress = &v
		}
		spec.Logs = lr
	}
	return spec
}

func streamFromFlags(stdoutOnly, stderrOnly bool) (pb.LogStream, error) {
	switch {
	case stdoutOnly && stderrOnly:
		return pb.LogStream_LOG_STREAM_UNSPECIFIED, fmt.Errorf("--stdout and --stderr are mutually exclusive")
	case stdoutOnly:
		return pb.LogStream_LOG_STREAM_STDOUT, nil
	case stderrOnly:
		return pb.LogStream_LOG_STREAM_STDERR, nil
	default:
		return pb.LogStream_LOG_STREAM_UNSPECIFIED, nil
	}
}

// persistServer writes the central-server config to the store so the
// (auto-spawned) daemon picks it up at startup. No-op without a server block.
func persistServer(st *store.Store, cfg *config.Config) error {
	if cfg.Server == nil {
		return nil
	}
	return st.SaveServer(cfg.Server)
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <marshal.yaml>",
		Short: "Start app(s) defined in a marshal.yaml file under the daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}
			st, err := store.New()
			if err != nil {
				return err
			}
			if err := persistServer(st, cfg); err != nil {
				return err
			}
			specs := make([]*pb.AppSpec, 0, len(cfg.Apps))
			for _, a := range cfg.Apps {
				specs = append(specs, appToSpec(a))
			}
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.Start(ctx, &pb.StartRequest{Apps: specs})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
}

// selectorCmd builds stop/restart/delete, which share the same shape.
func selectorCmd(use, short string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := call(ctx, c, &pb.Selector{Target: args[0]})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
}

func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show all managed processes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.List(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
	return cmd
}

func describeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <name|id>",
		Short: "Show detail for an app/instance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.Describe(ctx, &pb.Selector{Target: args[0]})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				// Best-effort last-hour history; never fail describe on its absence.
				resp, err := c.MetricsHistory(ctx, &pb.MetricsHistoryRequest{
					Selector: args[0],
					SinceMs:  time.Hour.Milliseconds(),
				})
				if err == nil && len(resp.GetBuckets()) > 0 {
					cpu := make([]float64, len(resp.Buckets))
					mem := make([]float64, len(resp.Buckets))
					for i, b := range resp.Buckets {
						cpu[i] = b.GetCpuAvg()
						mem[i] = float64(b.GetMemAvg())
					}
					out := cmd.OutOrStdout()
					fmt.Fprintf(out, "\nCPU (1h)  %s\n", sparkline(cpu))
					fmt.Fprintf(out, "MEM (1h)  %s\n", sparkline(mem))
				}
				return nil
			})
		},
	}
}

func saveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save",
		Short: "Persist the current app list to dump.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				ack, err := c.Save(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "marshal:", ack.GetMessage())
				return nil
			})
		},
	}
}

func resurrectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resurrect",
		Short: "Restore apps from dump.json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				list, err := c.Resurrect(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				printProcs(cmd, list)
				return nil
			})
		},
	}
}

func killCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill",
		Short: "Stop the daemon and all managed processes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				ack, err := c.Kill(ctx, &pb.Empty{})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "marshal:", ack.GetMessage())
				return nil
			})
		},
	}
}

func humanizeBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	suffixes := []string{"KB", "MB", "GB", "TB"}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len(suffixes)-1; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%s", float64(b)/float64(div), suffixes[exp])
}

// printProcs renders a ProcList as an aligned table.
func printProcs(cmd *cobra.Command, list *pb.ProcList) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tINST\tSTATE\tPID\tCPU\tMEM\tUPTIME\tRESTARTS")
	for _, p := range list.GetProcs() {
		uptime, cpu, mem := "-", "-", "-"
		if p.GetUptimeMs() > 0 {
			uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
		}
		if p.GetState() == "online" {
			cpu = fmt.Sprintf("%.1f%%", p.GetCpu())
			mem = humanizeBytes(p.GetMem())
		}
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%d\t%s\t%s\t%s\t%d\n",
			p.GetId(), p.GetName(), p.GetInstanceId(), p.GetState(), p.GetPid(), cpu, mem, uptime, p.GetRestarts())
	}
	_ = w.Flush()
}

func logsCmd() *cobra.Command {
	var lines int
	var follow bool
	var stdoutOnly, stderrOnly bool
	cmd := &cobra.Command{
		Use:   "logs <name|id|all>",
		Short: "Stream captured stdout/stderr for app(s)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streamSel, errFlag := streamFromFlags(stdoutOnly, stderrOnly)
			if errFlag != nil {
				return errFlag
			}
			st, err := store.New()
			if err != nil {
				return err
			}
			c, conn, err := client.Connect(st)
			if err != nil {
				return err
			}
			defer conn.Close()

			// Follow streams until Ctrl-C; one-shot backfill gets a 30s cap.
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			if !follow {
				var c2 context.CancelFunc
				ctx, c2 = context.WithTimeout(ctx, 30*time.Second)
				defer c2()
			}

			stream, err := c.Logs(ctx, &pb.LogRequest{Target: args[0], Lines: int32(lines), Follow: follow, Stream: streamSel})
			if err != nil {
				return err
			}
			for {
				ln, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					if ctx.Err() != nil {
						return nil // expected on Ctrl-C
					}
					return err
				}
				printLogLine(cmd, ln)
			}
		},
	}
	cmd.Flags().IntVarP(&lines, "lines", "n", 15, "number of backfilled lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "stream new lines as they arrive")
	cmd.Flags().BoolVar(&stdoutOnly, "stdout", false, "show only stdout")
	cmd.Flags().BoolVar(&stderrOnly, "stderr", false, "show only stderr")
	return cmd
}

// printLogLine writes a tagged log line: stdout lines to stdout, stderr to stderr.
func printLogLine(cmd *cobra.Command, ln *pb.LogLine) {
	w := cmd.OutOrStdout()
	if ln.GetStderr() {
		w = cmd.ErrOrStderr()
	}
	fmt.Fprintf(w, "%s#%d | %s\n", ln.GetName(), ln.GetInstanceId(), ln.GetLine())
}
