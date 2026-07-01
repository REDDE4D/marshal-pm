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
		Name:             a.Name,
		Cmd:              a.Cmd,
		Args:             a.Args,
		Cwd:              a.Cwd,
		Instances:        int32(a.Instances),
		Env:              a.Env,
		Restart:          string(a.Restart),
		MaxRestarts:      int32(a.MaxRestarts),
		KillTimeout:      a.KillTimeout.Duration.String(),
		MaxMemoryRestart: int64(a.MaxMemoryRestart.Bytes),
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
		Short: "Start app(s) defined in a marshal.yaml file under the local daemon",
		Long: "Start app(s) from a marshal.yaml under the local daemon — the classic,\n" +
			"single-host workflow. These apps appear in `marshal list` but NOT in a\n" +
			"central-server dashboard: the local daemon and the fleet are separate.\n\n" +
			"To run apps on an enrolled agent so they show up in the dashboard, use\n" +
			"`marshal fleet start <agent> <marshal.yaml>` against the server instead.",
		Args: cobra.ExactArgs(1),
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
// arguments can be selectors (name/id/all), comma-separated lists, or paths to a
// marshal.yaml; each expands to one or more targets.
func selectorCmd(use, short string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSelector(cmd, args, call)
		},
	}
}

func restartCmd() *cobra.Command {
	var updateEnv bool
	cmd := &cobra.Command{
		Use:   "restart <name|id|all|marshal.yaml>...",
		Short: "Restart app(s); with --update-env, reload env from a marshal.yaml first",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if updateEnv {
				return runRestartUpdateEnv(cmd, args) // implemented in Group 3, Task 9
			}
			return runSelector(cmd, args, func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
				return c.Restart(ctx, sel)
			})
		},
	}
	cmd.Flags().BoolVar(&updateEnv, "update-env", false,
		"re-read env/env_file from the given marshal.yaml and apply it on restart")
	return cmd
}

// runSelector expands args into targets and applies call to each. With multiple
// targets (or a config-file expansion) an errored target warns and the loop
// continues, returning a non-zero exit if any failed; a single explicit target
// fails hard.
func runSelector(cmd *cobra.Command, args []string, call func(context.Context, pb.DaemonClient, *pb.Selector) (*pb.ProcList, error)) error {
	targets, multi, err := expandSelectorArgs(args)
	if err != nil {
		return err
	}
	return withClient(func(ctx context.Context, c pb.DaemonClient) error {
		agg := &pb.ProcList{}
		failed := false
		for _, t := range targets {
			list, err := call(ctx, c, &pb.Selector{Target: t})
			if err != nil {
				if multi {
					fmt.Fprintf(cmd.ErrOrStderr(), "marshal: %s: %v\n", t, err)
					failed = true
					continue
				}
				return err
			}
			agg.Procs = append(agg.Procs, list.GetProcs()...)
		}
		printProcs(cmd, agg)
		if failed {
			return fmt.Errorf("one or more targets failed")
		}
		return nil
	})
}

// flushCmd clears captured logs for app(s). The selector argument is optional
// and defaults to "all" (matching `pm2 flush`).
func flushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "flush [name|id|all]",
		Short: "Clear captured logs for app(s) (default: all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "all"
			if len(args) == 1 {
				target = args[0]
			}
			return withClient(func(ctx context.Context, c pb.DaemonClient) error {
				ack, err := c.Flush(ctx, &pb.Selector{Target: target})
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "marshal:", ack.GetMessage())
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

// expandSelectorArgs turns CLI args — each possibly a comma-separated list, a
// name/id, or a marshal.yaml path — into a flat, de-duplicated target list.
// multi is true when more than one target results or any arg was a config file,
// which switches callers to warn-and-continue error handling. "all" anywhere
// short-circuits to a single "all" target.
func expandSelectorArgs(args []string) (targets []string, multi bool, err error) {
	seen := map[string]bool{}
	fromFile := false
	for _, raw := range args {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			ts, ff, e := targetsFromArg(part)
			if e != nil {
				return nil, false, e
			}
			fromFile = fromFile || ff
			for _, t := range ts {
				if !seen[t] {
					seen[t] = true
					targets = append(targets, t)
				}
			}
		}
	}
	if len(targets) == 0 {
		return nil, false, fmt.Errorf("no targets given")
	}
	for _, t := range targets {
		if t == "all" {
			return []string{"all"}, false, nil
		}
	}
	return targets, fromFile || len(targets) > 1, nil
}

// enrollmentHeader returns a one-line status string indicating whether the host
// is configured to enroll with a central server. A nil or empty-address config
// means not enrolled.
func enrollmentHeader(sc *config.ServerConfig) string {
	if sc != nil && sc.Address != "" {
		return "enrolled → " + sc.Address
	}
	return "not enrolled"
}

func listCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Show all managed processes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Best-effort enrollment header: skip silently if the store can't be opened.
			if st, err := store.New(); err == nil {
				sc, _ := st.LoadServer()
				fmt.Fprintln(cmd.OutOrStdout(), enrollmentHeader(sc))
			}
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

// logPalette is the set of ANSI foreground colors cycled for per-app log prefixes.
var logPalette = []string{
	"\x1b[36m", // cyan
	"\x1b[32m", // green
	"\x1b[33m", // yellow
	"\x1b[35m", // magenta
	"\x1b[34m", // blue
	"\x1b[91m", // bright red
}

const ansiReset = "\x1b[0m"

// labelColor maps a label to a stable color from logPalette (FNV-1a hash).
func labelColor(label string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(label); i++ {
		h ^= uint32(label[i])
		h *= 16777619
	}
	return logPalette[int(h)%len(logPalette)]
}

// printLogLine writes a tagged log line: stdout lines to stdout, stderr to stderr.
// The "name#idx" prefix is colorized when the destination is a terminal.
func printLogLine(cmd *cobra.Command, ln *pb.LogLine) {
	w := cmd.OutOrStdout()
	if ln.GetStderr() {
		w = cmd.ErrOrStderr()
	}
	prefix := fmt.Sprintf("%s#%d", ln.GetName(), ln.GetInstanceId())
	if isTerminal(w) {
		prefix = labelColor(prefix) + prefix + ansiReset
	}
	fmt.Fprintf(w, "%s | %s\n", prefix, ln.GetLine())
}

// runRestartUpdateEnv is implemented in Group 3 (Task 9).
func runRestartUpdateEnv(cmd *cobra.Command, args []string) error {
	return fmt.Errorf("--update-env not yet implemented")
}
