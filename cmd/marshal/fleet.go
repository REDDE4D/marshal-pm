package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"marshal/internal/pb"
)

func fleetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Operate on the central server / fleet",
	}
	cmd.AddCommand(fleetPsCmd())
	return cmd
}

func fleetPsCmd() *cobra.Command {
	var serverAddr string
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List processes across all connected agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			conn, err := grpc.NewClient(resolveServer(serverAddr),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				return err
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			resp, err := pb.NewFleetClient(conn).ListFleet(ctx, &pb.ListFleetRequest{})
			if err != nil {
				return err
			}
			printFleet(cmd, resp)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverAddr, "server", "", "central server address (default $MARSHAL_SERVER or localhost:9000)")
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

// printFleet renders fleet state grouped by agent.
func printFleet(cmd *cobra.Command, resp *pb.ListFleetResponse) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "AGENT\tSTATUS\tID\tNAME\tINST\tSTATE\tPID\tUPTIME\tRESTARTS")
	for _, a := range resp.GetAgents() {
		status := "offline"
		if a.GetConnected() {
			status = "online"
		} else if a.GetLastSeenUnix() > 0 {
			status = fmt.Sprintf("offline %s", time.Since(time.Unix(a.GetLastSeenUnix(), 0)).Round(time.Second))
		}
		if len(a.GetProcs()) == 0 {
			fmt.Fprintf(w, "%s\t%s\t-\t-\t-\t-\t-\t-\t-\n", a.GetAgentName(), status)
			continue
		}
		for _, p := range a.GetProcs() {
			uptime := "-"
			if p.GetUptimeMs() > 0 {
				uptime = (time.Duration(p.GetUptimeMs()) * time.Millisecond).Round(time.Second).String()
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\t%d\t%s\t%d\n",
				a.GetAgentName(), status, p.GetId(), p.GetName(), p.GetInstanceId(),
				p.GetState(), p.GetPid(), uptime, p.GetRestarts())
		}
	}
	_ = w.Flush()
}
