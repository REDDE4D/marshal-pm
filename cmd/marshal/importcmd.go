package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/REDDE4D/marshal-pm/internal/config"
	"github.com/REDDE4D/marshal-pm/internal/pm2import"
)

// importCmd groups importers from other process managers.
func importCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "import",
		Short: "Import app definitions from other process managers",
	}
	c.AddCommand(importPM2Cmd())
	return c
}

// importPM2Cmd converts a PM2 ecosystem file to a marshal.yaml.
func importPM2Cmd() *cobra.Command {
	var out string
	var splitEnv bool
	c := &cobra.Command{
		Use:   "pm2 <ecosystem.config.js|.json|.yaml>",
		Short: "Convert a PM2 ecosystem file to a marshal.yaml",
		Long: "Convert a PM2 ecosystem file to a marshal.yaml.\n\n" +
			"JSON and YAML ecosystem files are read directly; .js/.cjs files are\n" +
			"evaluated with node (which must be on PATH) so dynamic config — env\n" +
			"loaders, spreads, etc. — resolves exactly as it would under PM2.\n\n" +
			"Output goes to stdout by default; redirect it or use -o. Fields with no\n" +
			"Marshal equivalent (cluster mode, watch, cron_restart, …) are reported as\n" +
			"warnings on stderr.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eco, err := pm2import.Load(args[0])
			if err != nil {
				return err
			}
			cfg, warns := pm2import.Convert(eco)
			if len(cfg.Apps) == 0 {
				return fmt.Errorf("no apps found in %s", args[0])
			}

			// --split-env: write each app's resolved env to a 0600 <name>.env file
			// (next to the output) and reference it via env_file, keeping secrets
			// out of the generated marshal.yaml.
			if splitEnv {
				dir := "."
				if out != "" {
					dir = filepath.Dir(out)
				}
				files, werr := cfg.SplitEnvFiles(dir)
				if werr != nil {
					return werr
				}
				for _, f := range files {
					fmt.Fprintf(cmd.ErrOrStderr(), "marshal: wrote %s\n", filepath.Join(dir, f))
				}
			}

			data, err := cfg.YAML()
			if err != nil {
				return err
			}
			// Soft-validate the result so the user is told if it won't load.
			if _, perr := config.Parse(data); perr != nil {
				warns = append(warns, "generated config did not validate: "+perr.Error())
			}

			if out != "" {
				// 0600: the env: block may contain secrets resolved from the ecosystem.
				if err := os.WriteFile(out, data, 0o600); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "marshal: wrote %d app(s) to %s\n", len(cfg.Apps), out)
			} else {
				if _, err := cmd.OutOrStdout().Write(data); err != nil {
					return err
				}
			}
			for _, w := range warns {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+w)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&out, "output", "o", "", "write to a file (0600) instead of stdout")
	c.Flags().BoolVar(&splitEnv, "split-env", false, "write each app's env to a 0600 <name>.env file and reference it via env_file (keeps secrets out of the marshal.yaml)")
	return c
}
