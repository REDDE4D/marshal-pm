package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/REDDE4D/marshal-pm/internal/server"
)

func serverPasswdCmd() *cobra.Command {
	var dataDir, user string
	cmd := &cobra.Command{
		Use:   "passwd",
		Short: "Set the dashboard login password",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = defaultServerDataDir()
			}
			pw, err := readPassword(cmd)
			if err != nil {
				return err
			}
			if err := server.SetDashboardPassword(dataDir, user, pw); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dashboard user %q set\n", user)
			return nil
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "server data directory")
	cmd.Flags().StringVar(&user, "user", "admin", "dashboard username")
	return cmd
}

// readPassword reads a password with no echo when stdin is a terminal (with a
// confirmation), or a single line from stdin otherwise (piped/scripted/tests).
func readPassword(cmd *cobra.Command) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(cmd.OutOrStdout(), "New password: ")
		p1, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		fmt.Fprint(cmd.OutOrStdout(), "Confirm:      ")
		p2, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(cmd.OutOrStdout())
		if err != nil {
			return "", err
		}
		if string(p1) != string(p2) {
			return "", errors.New("passwords do not match")
		}
		if len(p1) == 0 {
			return "", errors.New("empty password")
		}
		return string(p1), nil
	}
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return "", errors.New("no password on stdin")
	}
	pw := strings.TrimRight(sc.Text(), "\r\n")
	if pw == "" {
		return "", errors.New("empty password")
	}
	return pw, nil
}
