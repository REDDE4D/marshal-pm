package main

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"marshal/internal/startup"
	"marshal/internal/store"
)

func resolveConfig(system bool) (startup.Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return startup.Config{}, fmt.Errorf("locate marshal binary: %w", err)
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	u, err := user.Current()
	if err != nil {
		return startup.Config{}, fmt.Errorf("resolve current user: %w", err)
	}
	st, err := store.New()
	if err != nil {
		return startup.Config{}, err
	}
	return startup.Config{
		Binary:   exe,
		User:     u.Username,
		Home:     u.HomeDir,
		XDGData:  os.Getenv("XDG_DATA_HOME"),
		System:   system,
		StageDir: st.Dir(),
		UID:      os.Getuid(),
	}, nil
}

func startupCmd() *cobra.Command {
	var system bool
	cmd := &cobra.Command{
		Use:   "startup",
		Short: "Install a boot service that runs marshald at startup",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(system)
			if err != nil {
				return err
			}
			plat, err := startup.Detect(runtime.GOOS)
			if err != nil {
				return err
			}
			plan := plat.InstallPlan(cfg)
			out := cmd.OutOrStdout()
			if plan.NeedsRoot {
				return startup.StageAndPrint(plan, out)
			}
			if err := startup.Apply(plan, startup.ExecRunner{Out: out, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			fmt.Fprintf(out, "Installed %s\n", plan.UnitPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "install a system-level (root) service")
	return cmd
}

func unstartupCmd() *cobra.Command {
	var system bool
	cmd := &cobra.Command{
		Use:   "unstartup",
		Short: "Remove the boot service installed by `marshal startup`",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(system)
			if err != nil {
				return err
			}
			plat, err := startup.Detect(runtime.GOOS)
			if err != nil {
				return err
			}
			plan := plat.RemovePlan(cfg)
			out := cmd.OutOrStdout()
			if plan.NeedsRoot {
				fmt.Fprintln(out, "Run these commands to remove the system service:")
				fmt.Fprintln(out)
				for _, c := range plan.PostRemove {
					fmt.Fprintf(out, "  %s\n", c.Display())
				}
				fmt.Fprintln(out)
				return nil
			}
			if err := startup.Remove(plan, startup.ExecRunner{Out: out, Err: cmd.ErrOrStderr()}); err != nil {
				return err
			}
			fmt.Fprintf(out, "Removed %s\n", plan.UnitPath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "remove the system-level (root) service")
	return cmd
}
