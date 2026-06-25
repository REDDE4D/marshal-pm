package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v3"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/pb"
)

func fleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Operate on the central server / fleet",
	}
	cmd.AddCommand(fleetPsCmd())
	cmd.AddCommand(fleetMetricsCmd())
	cmd.AddCommand(fleetLogsCmd())
	cmd.AddCommand(fleetStartCmd())
	cmd.AddCommand(fleetSelectorCmd("stop", "Stop an app/instance on one agent",
		func(t string) *pb.ControlOp {
			return &pb.ControlOp{Op: &pb.ControlOp_Stop{Stop: &pb.Selector{Target: t}}}
		}))
	cmd.AddCommand(fleetSelectorCmd("restart", "Restart an app/instance on one agent",
		func(t string) *pb.ControlOp {
			return &pb.ControlOp{Op: &pb.ControlOp_Restart{Restart: &pb.Selector{Target: t}}}
		}))
	cmd.AddCommand(fleetSelectorCmd("delete", "Delete an app/instance on one agent",
		func(t string) *pb.ControlOp {
			return &pb.ControlOp{Op: &pb.ControlOp_Delete{Delete: &pb.Selector{Target: t}}}
		}))
	return cmd
}

func fleetPsCmd() *cobra.Command {
	var serverAddr, fingerprintFlag, tokenFlag string
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List processes across all connected agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			addr, fp, token := resolveServerAuth(serverAddr, fingerprintFlag, tokenFlag)
			conn, err := dialFleet(addr, fp)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).ListFleet(authCtx(ctx, token), &pb.ListFleetRequest{})
			if err != nil {
				return err
			}
			printFleet(cmd, resp)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().StringVar(&fingerprintFlag, "fingerprint", "", "pinned server cert SHA-256 fingerprint (default $MARSHAL_FINGERPRINT)")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "admin token (default $MARSHAL_TOKEN)")
	return cmd
}

func fleetMetricsCmd() *cobra.Command {
	var serverAddr, fingerprintFlag, tokenFlag string
	var since, bucket time.Duration
	var cpuOnly, memOnly bool
	cmd := &cobra.Command{
		Use:   "metrics <agent> <name|id>",
		Short: "Show CPU/memory history for an app/instance on one agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, fp, token := resolveServerAuth(serverAddr, fingerprintFlag, tokenFlag)
			conn, err := dialFleet(addr, fp)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).FleetMetricsHistory(authCtx(ctx, token), &pb.FleetMetricsHistoryRequest{
				AgentName: args[0],
				Selector:  args[1],
				SinceMs:   since.Milliseconds(),
				BucketMs:  bucket.Milliseconds(),
			})
			if err != nil {
				return err
			}
			printMetrics(cmd.OutOrStdout(), resp, args[0]+"/"+args[1], since, cpuOnly, memOnly)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().StringVar(&fingerprintFlag, "fingerprint", "", "pinned server cert SHA-256 fingerprint (default $MARSHAL_FINGERPRINT)")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "admin token (default $MARSHAL_TOKEN)")
	cmd.Flags().DurationVar(&since, "since", time.Hour, "history window (e.g. 30m, 6h)")
	cmd.Flags().DurationVar(&bucket, "bucket", 0, "bucket width (0 = auto)")
	cmd.Flags().BoolVar(&cpuOnly, "cpu", false, "show only CPU")
	cmd.Flags().BoolVar(&memOnly, "mem", false, "show only memory")
	return cmd
}

func fleetLogsCmd() *cobra.Command {
	var serverAddr, fingerprintFlag, tokenFlag string
	var lines int
	var stdoutOnly, stderrOnly bool
	var grepFlag string
	cmd := &cobra.Command{
		Use:   "logs <agent> <name|label>",
		Short: "Show recent captured logs for an app/instance on one agent",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			streamSel, err := streamFromFlags(stdoutOnly, stderrOnly)
			if err != nil {
				return err
			}
			addr, fp, token := resolveServerAuth(serverAddr, fingerprintFlag, tokenFlag)
			conn, err := dialFleet(addr, fp)
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).FleetLogsHistory(authCtx(ctx, token), &pb.FleetLogsHistoryRequest{
				AgentName: args[0],
				Selector:  args[1],
				Lines:     int32(lines),
				Stream:    streamSel,
				Grep:      grepFlag,
			})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, ln := range resp.GetLines() {
				fmt.Fprintf(out, "%s#%d | %s\n", ln.GetName(), ln.GetInstanceId(), ln.GetLine())
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().StringVar(&fingerprintFlag, "fingerprint", "", "pinned server cert SHA-256 fingerprint (default $MARSHAL_FINGERPRINT)")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "admin token (default $MARSHAL_TOKEN)")
	cmd.Flags().IntVarP(&lines, "lines", "n", 15, "number of lines to show")
	cmd.Flags().BoolVar(&stdoutOnly, "stdout", false, "show only stdout")
	cmd.Flags().BoolVar(&stderrOnly, "stderr", false, "show only stderr")
	cmd.Flags().StringVar(&grepFlag, "grep", "", "only lines containing this substring (case-insensitive)")
	return cmd
}

// resolveServer picks the server address: explicit flag, then $MARSHAL_SERVER,
// then localhost:9000.
func resolveServer(flag string) string {
	if flag != "" {
		return flag
	}
	if env := os.Getenv("MARSHAL_SERVER"); env != "" {
		return env
	}
	return "localhost:9000"
}

// firstNonEmpty returns the first non-empty string from vals.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// cliConfig holds optional defaults loaded from ~/.config/marshal/cli.yaml.
type cliConfig struct {
	Server      string `yaml:"server"`
	Token       string `yaml:"token"`
	Fingerprint string `yaml:"fingerprint"`
}

// loadCLIConfig reads $XDG_CONFIG_HOME/marshal/cli.yaml (falling back to
// ~/.config/marshal/cli.yaml). A missing or unreadable file returns a zero
// cliConfig without error.
func loadCLIConfig() cliConfig {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return cliConfig{}
		}
		base = filepath.Join(home, ".config")
	}
	b, err := os.ReadFile(filepath.Join(base, "marshal", "cli.yaml"))
	if err != nil {
		return cliConfig{}
	}
	var c cliConfig
	_ = yaml.Unmarshal(b, &c)
	return c
}

// resolveServerAuth resolves the server address, pinned fingerprint, and admin
// token from flags, then env (MARSHAL_SERVER / MARSHAL_FINGERPRINT /
// MARSHAL_TOKEN), then ~/.config/marshal/cli.yaml, then built-in defaults.
func resolveServerAuth(serverFlag, fpFlag, tokenFlag string) (addr, fingerprint, token string) {
	cfg := loadCLIConfig()
	addr = firstNonEmpty(serverFlag, os.Getenv("MARSHAL_SERVER"), cfg.Server, "localhost:9000")
	fingerprint = firstNonEmpty(fpFlag, os.Getenv("MARSHAL_FINGERPRINT"), cfg.Fingerprint)
	token = firstNonEmpty(tokenFlag, os.Getenv("MARSHAL_TOKEN"), cfg.Token)
	return
}

// authCtx returns a context with the admin token appended as outgoing metadata.
func authCtx(parent context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(parent, "marshal-token", token)
}

// dialFleet builds a TLS gRPC client connection to the server.
func dialFleet(addr, fingerprint string) (*grpc.ClientConn, error) {
	cfg, err := fleetauth.ClientTLS(fingerprint, "")
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
}

// printFleet renders fleet state grouped by agent.
func printFleet(cmd *cobra.Command, resp *pb.ListFleetResponse) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tSTATUS\tID\tNAME\tINST\tSTATE\tPID\tCPU\tMEM\tUPTIME\tRESTARTS")
	for _, a := range resp.GetAgents() {
		status := "offline"
		if a.GetConnected() {
			status = "online"
		} else if a.GetLastSeenUnix() > 0 {
			status = fmt.Sprintf("offline %s", time.Since(time.Unix(a.GetLastSeenUnix(), 0)).Round(time.Second))
		}
		if len(a.GetProcs()) == 0 {
			fmt.Fprintf(w, "%s\t%s\t-\t-\t-\t-\t-\t-\t-\t-\t-\n", a.GetAgentName(), status)
			continue
		}
		for _, p := range a.GetProcs() {
			uptime, cpu, mem := "-", "-", "-"
			if p.GetUptimeMs() > 0 {
				uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
			}
			if p.GetState() == "online" {
				cpu = fmt.Sprintf("%.1f%%", p.GetCpu())
				mem = humanizeBytes(p.GetMem())
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\t%d\t%s\t%s\t%s\t%d\n",
				a.GetAgentName(), status, p.GetId(), p.GetName(), p.GetInstanceId(),
				p.GetState(), p.GetPid(), cpu, mem, uptime, p.GetRestarts())
		}
	}
	_ = w.Flush()
}

// fleetControl dials the server, sends one control op to an agent, and prints
// the resulting process table (or the agent's error).
func fleetControl(cmd *cobra.Command, serverAddr, fingerprintFlag, tokenFlag string, timeout time.Duration, agent string, op *pb.ControlOp) error {
	addr, fp, token := resolveServerAuth(serverAddr, fingerprintFlag, tokenFlag)
	conn, err := dialFleet(addr, fp)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	resp, err := pb.NewFleetClient(conn).FleetControl(authCtx(ctx, token), &pb.FleetControlRequest{
		AgentName: agent, Op: op,
	})
	if err != nil {
		return err
	}
	res := resp.GetResult()
	if !res.GetOk() {
		return errors.New(res.GetError())
	}
	printProcs(cmd, &pb.ProcList{Procs: res.GetProcs()})
	return nil
}

func fleetSelectorCmd(use, short string, build func(target string) *pb.ControlOp) *cobra.Command {
	var serverAddr, fingerprintFlag, tokenFlag string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:          use + " <agent> <name|id|all>",
		Short:        short,
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fleetControl(cmd, serverAddr, fingerprintFlag, tokenFlag, timeout, args[0], build(args[1]))
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().StringVar(&fingerprintFlag, "fingerprint", "", "pinned server cert SHA-256 fingerprint (default $MARSHAL_FINGERPRINT)")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "admin token (default $MARSHAL_TOKEN)")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "command timeout (a timeout does not guarantee the command did not run on the agent)")
	return cmd
}

func fleetStartCmd() *cobra.Command {
	var serverAddr, fingerprintFlag, tokenFlag string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:          "start <agent> <marshal.yaml>",
		Short:        "Deploy and start app(s) from a marshal.yaml on one agent",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[1])
			if err != nil {
				return err
			}
			specs := make([]*pb.AppSpec, 0, len(cfg.Apps))
			for _, a := range cfg.Apps {
				specs = append(specs, appToSpec(a))
			}
			op := &pb.ControlOp{Op: &pb.ControlOp_Start{Start: &pb.StartRequest{Apps: specs}}}
			return fleetControl(cmd, serverAddr, fingerprintFlag, tokenFlag, timeout, args[0], op)
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
	cmd.Flags().StringVar(&fingerprintFlag, "fingerprint", "", "pinned server cert SHA-256 fingerprint (default $MARSHAL_FINGERPRINT)")
	cmd.Flags().StringVar(&tokenFlag, "token", "", "admin token (default $MARSHAL_TOKEN)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "command timeout (a timeout does not guarantee the command did not run on the agent)")
	return cmd
}
