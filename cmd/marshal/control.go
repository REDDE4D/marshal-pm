package main

import (
	"context"
	"fmt"
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
	return &pb.AppSpec{
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
	return selectorCmd("describe <name|id>", "Show detail for an app/instance",
		func(ctx context.Context, c pb.DaemonClient, sel *pb.Selector) (*pb.ProcList, error) {
			return c.Describe(ctx, sel)
		})
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

// printProcs renders a ProcList as an aligned table.
func printProcs(cmd *cobra.Command, list *pb.ProcList) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tINST\tSTATE\tPID\tUPTIME\tRESTARTS")
	for _, p := range list.GetProcs() {
		uptime := "-"
		if p.GetUptimeMs() > 0 {
			uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%d\t%s\t%d\n",
			p.GetId(), p.GetName(), p.GetInstanceId(), p.GetState(), p.GetPid(), uptime, p.GetRestarts())
	}
	_ = w.Flush()
}
