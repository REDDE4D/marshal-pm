package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/REDDE4D/marshal-pm/internal/client"
	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/pb"
	"github.com/REDDE4D/marshal-pm/internal/store"
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

// selectorCmd builds stop/restart/delete, which share the same shape. The
// argument is a selector (name/id/all) or — like `marshal start` — a path to a
// marshal.yaml, in which case every app it defines is targeted.
func selectorCmd(use, short string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, fromFile, err := targetsFromArg(args[0])
			if err != nil {
				return err
			}
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				agg := &pb.ProcList{}
				for _, t := range targets {
					list, err := call(ctx, c, &pb.Selector{Target: t})
					if err != nil {
						// When expanding a config file, an app that isn't running
						// shouldn't abort the others — warn and keep going. A single
						// explicit selector still fails hard.
						if fromFile {
							fmt.Fprintf(cmd.ErrOrStderr(), "marshal: %s: %v\n", t, err)
							continue
						}
						return err
					}
					agg.Procs = append(agg.Procs, list.GetProcs()...)
				}
				printProcs(cmd, agg)
				return nil
			})
		},
	}
}

// targetsFromArg resolves a stop/restart/delete argument. A path to an existing
// .yaml/.yml file expands to the names of the apps it defines (fromFile=true);
// anything else is a single literal selector (name/id/all).
func targetsFromArg(arg string) (targets []string, fromFile bool, err error) {
	if !isConfigFile(arg) {
		return []string{arg}, false, nil
	}
	cfg, err := config.Load(arg)
	if err != nil {
		return nil, true, err
	}
	if len(cfg.Apps) == 0 {
		return nil, true, fmt.Errorf("no apps found in %s", arg)
	}
	names := make([]string, 0, len(cfg.Apps))
	for _, a := range cfg.Apps {
		names = append(names, a.Name)
	}
	return names, true, nil
}

// isConfigFile reports whether arg points at an existing YAML file on disk.
func isConfigFile(arg string) bool {
	switch strings.ToLower(filepath.Ext(arg)) {
	case ".yaml", ".yml":
	default:
		return false
	}
	info, err := os.Stat(arg)
	return err == nil && !info.IsDir()
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

// printProcs renders a ProcList as a bordered table, colorizing state when the
// destination is a terminal.
func printProcs(cmd *cobra.Command, list *pb.ProcList) {
	out := cmd.OutOrStdout()
	renderProcTable(out, list, isTerminal(out))
}

var procTableHeaders = []string{"ID", "NAME", "INST", "STATE", "PID", "CPU", "MEM", "UPTIME", "RESTARTS"}

const procStateColumn = 3 // index of STATE in procTableHeaders

// renderProcTable draws a box-drawing table for a ProcList. When color is true
// the STATE cell is ANSI-colored (green online / red errored or stopped /
// yellow otherwise); padding is computed from the uncolored text so columns
// stay aligned.
func renderProcTable(w io.Writer, list *pb.ProcList, color bool) {
	rows := make([][]string, 0, len(list.GetProcs()))
	states := make([]string, 0, len(list.GetProcs()))
	for _, p := range list.GetProcs() {
		uptime, cpu, mem := "-", "-", "-"
		if p.GetUptimeMs() > 0 {
			uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
		}
		if p.GetState() == "online" {
			cpu = fmt.Sprintf("%.1f%%", p.GetCpu())
			mem = humanizeBytes(p.GetMem())
		}
		rows = append(rows, []string{
			strconv.Itoa(int(p.GetId())), p.GetName(), strconv.Itoa(int(p.GetInstanceId())),
			p.GetState(), strconv.Itoa(int(p.GetPid())), cpu, mem, uptime, strconv.Itoa(int(p.GetRestarts())),
		})
		states = append(states, p.GetState())
	}

	widths := make([]int, len(procTableHeaders))
	for i, h := range procTableHeaders {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > widths[i] {
				widths[i] = n
			}
		}
	}

	fmt.Fprint(w, tableBorder("┌", "┬", "┐", widths))
	fmt.Fprint(w, tableRow(procTableHeaders, widths, -1, ""))
	fmt.Fprint(w, tableBorder("├", "┼", "┤", widths))
	for i, r := range rows {
		colorCode := ""
		if color {
			colorCode = stateColor(states[i])
		}
		fmt.Fprint(w, tableRow(r, widths, procStateColumn, colorCode))
	}
	fmt.Fprint(w, tableBorder("└", "┴", "┘", widths))
}

// tableBorder draws a horizontal rule with the given corner/junction runes,
// each column padded by one space on each side (hence width+2).
func tableBorder(left, mid, right string, widths []int) string {
	var b strings.Builder
	b.WriteString(left)
	for i, w := range widths {
		b.WriteString(strings.Repeat("─", w+2))
		if i < len(widths)-1 {
			b.WriteString(mid)
		}
	}
	b.WriteString(right + "\n")
	return b.String()
}

// tableRow renders one left-aligned row. If colorCode is non-empty, the cell at
// colorIdx is wrapped in it (and reset), with padding kept outside the color so
// widths line up.
func tableRow(cells []string, widths []int, colorIdx int, colorCode string) string {
	var b strings.Builder
	b.WriteString("│")
	for i, c := range cells {
		pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c))
		b.WriteString(" ")
		if i == colorIdx && colorCode != "" {
			b.WriteString(colorCode + c + "\x1b[0m" + pad)
		} else {
			b.WriteString(c + pad)
		}
		b.WriteString(" │")
	}
	b.WriteString("\n")
	return b.String()
}

// stateColor maps a process state to an ANSI color code.
func stateColor(state string) string {
	switch state {
	case "online":
		return "\x1b[32m" // green
	case "errored", "stopped":
		return "\x1b[31m" // red
	default:
		return "\x1b[33m" // yellow (launching, restarting, …)
	}
}

// isTerminal reports whether w is a terminal, so color is safe to emit.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
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
