package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/server"
	"github.com/REDDE4D/marshal-pm/internal/startup"
)

// serverStartupCmd installs (or removes) a boot service for the fleet server +
// dashboard — the server-side counterpart of `marshal startup` (which runs the
// per-host agent). With --self-enroll it installs the single-host quickstart.
func serverStartupCmd() *cobra.Command {
	var system, remove bool
	var httpListen, selfEnroll string
	cmd := &cobra.Command{
		Use:   "startup",
		Short: "Install a boot service that runs the fleet server + dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(system)
			if err != nil {
				return err
			}
			cfg.Label = "marshal-server"
			args := []string{"server", "--http-listen", httpListen}
			if selfEnroll != "" {
				abs, aerr := filepath.Abs(selfEnroll)
				if aerr != nil {
					return aerr
				}
				args = append(args, "--self-enroll", abs)
			}
			cfg.Args = args

			plat, err := startup.Detect(runtime.GOOS)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if remove {
				plan := plat.RemovePlan(cfg)
				if plan.NeedsRoot {
					fmt.Fprintln(out, "Run these commands to remove the system service:")
					fmt.Fprintln(out)
					for _, c := range plan.PostRemove {
						fmt.Fprintf(out, "  %s\n", c.Display())
					}
					return nil
				}
				if err := startup.Remove(plan, startup.ExecRunner{Out: out, Err: cmd.ErrOrStderr()}); err != nil {
					return err
				}
				fmt.Fprintf(out, "Removed %s\n", plan.UnitPath)
				return nil
			}

			// A service can't prompt for a password — nudge the user to set one.
			if has, _ := server.HasDashboardUserDir(defaultServerDataDir()); !has {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: no dashboard password set — run `marshal server passwd` so the dashboard is usable")
			}

			plan := plat.InstallPlan(cfg)
			if plan.NeedsRoot {
				return startup.StageAndPrint(plan, out)
			}
			if err := startup.Apply(plan, startup.ExecRunner{Out: out, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			fmt.Fprintf(out, "Installed %s (runs: marshal %s)\n", plan.UnitPath, strings.Join(cfg.Args, " "))
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "install a system-level (root) service")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the server boot service instead of installing it")
	cmd.Flags().StringVar(&httpListen, "http-listen", ":9001", "dashboard address")
	cmd.Flags().StringVar(&selfEnroll, "self-enroll", "", "also run a local agent for this marshal.yaml (single-host quickstart)")
	return cmd
}
