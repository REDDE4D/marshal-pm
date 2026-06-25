package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/server"
)

func serverFingerprintCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "fingerprint",
		Short: "Print the server's TLS certificate fingerprint",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			fp, err := server.FingerprintForDir(dataDir)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), fp)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	return cmd
}

func serverTokenCmd() *cobra.Command {
	var dataDir, rotate string
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Rotate the enroll or admin token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			if rotate == "" {
				return fmt.Errorf("specify --rotate enroll|admin")
			}
			tok, err := server.RotateToken(dataDir, rotate)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "new %s token: %s\n", rotate, tok)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.Flags().StringVar(&rotate, "rotate", "", "which token to rotate: enroll|admin")
	return cmd
}

func serverAgentCmd() *cobra.Command {
	var dataDir string
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage enrolled agents",
	}
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List enrolled agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			agents, err := server.ListAgents(dataDir)
			if err != nil {
				return err
			}
			for _, a := range agents {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tenrolled %s\n", a.Name,
					time.Unix(a.EnrolledAt, 0).Format(time.RFC3339))
			}
			return nil
		},
	}
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Revoke an enrolled agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			ok, err := server.RemoveAgent(dataDir, args[0])
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no such agent %q", args[0])
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", args[0])
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.AddCommand(ls, rm)
	return cmd
}
