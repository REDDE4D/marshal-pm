package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"marshal/internal/audit"
)

func serverAuditCmd() *cobra.Command {
	var dataDir string
	var limit int
	var failures bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show recent dashboard login attempts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			path := filepath.Join(dataDir, "login-audit.log")
			events, err := audit.Read(path, audit.ReadOptions{Limit: limit, FailuresOnly: failures})
			if err != nil {
				return err
			}
			if len(events) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "no login attempts recorded")
				return nil
			}
			for _, e := range events {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%-19s\t%s\t%s\n",
					e.Time.Local().Format(time.RFC3339), e.Outcome, e.User, e.IP)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.Flags().IntVar(&limit, "limit", 50, "show at most the most recent N attempts (0 = all)")
	cmd.Flags().BoolVar(&failures, "failures", false, "show only failed/locked attempts")
	return cmd
}
