package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/fleetauth"
	"github.com/REDDE4D/marshal-pm/internal/store"
)

// enrollCmd points this host's daemon at a central server, so all of its apps
// appear in that server's dashboard. The running daemon picks the change up
// within the fleet poll interval; no restart needed.
func enrollCmd() *cobra.Command {
	var token, fingerprint, ca, name string
	c := &cobra.Command{
		Use:   "enroll <server-address>",
		Short: "Enroll this host's daemon with a central server (so its apps appear in the dashboard)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				return fmt.Errorf("--token is required (mint one on the server: marshal server token --rotate enroll)")
			}
			if fingerprint == "" && ca == "" {
				return fmt.Errorf("one of --fingerprint or --ca is required to pin the server's TLS certificate")
			}
			if _, err := fleetauth.ClientTLS(fingerprint, ca); err != nil {
				return fmt.Errorf("invalid TLS pin: %w", err)
			}
			if name == "" {
				if h, err := os.Hostname(); err == nil {
					name = h
				}
			}
			st, err := store.New()
			if err != nil {
				return err
			}
			// A fresh enrollment must not reuse a stale per-agent token.
			if err := st.ClearServer(); err != nil {
				return err
			}
			if err := st.SaveServer(&config.ServerConfig{
				Address: args[0], Name: name, Token: token, Fingerprint: fingerprint, CA: ca,
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "marshal: enrolled with %s as %q — a running daemon will connect within a few seconds.\n", args[0], name)
			return nil
		},
	}
	c.Flags().StringVar(&token, "token", "", "enrollment token minted on the server")
	c.Flags().StringVar(&fingerprint, "fingerprint", "", "pinned server cert SHA-256 fingerprint")
	c.Flags().StringVar(&ca, "ca", "", "CA file to verify the server cert (alternative to --fingerprint)")
	c.Flags().StringVar(&name, "name", "", "agent name reported to the server (default: hostname)")
	return c
}

// unenrollCmd disconnects this host's daemon from its central server.
func unenrollCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unenroll",
		Short: "Disconnect this host's daemon from its central server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := store.New()
			if err != nil {
				return err
			}
			if err := st.ClearServer(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "marshal: unenrolled — a running daemon will drop the connection within a few seconds.")
			return nil
		},
	}
}
