package cli

import "github.com/spf13/cobra"

type syncOptions struct {
	machine string
	dir     string
	dryRun  bool
	yes     bool
	only    []string
	local   bool
}

func newSyncCommand(g *globalOptions) *cobra.Command {
	var opts syncOptions
	cmd := &cobra.Command{
		Use:   "sync [CONFIG-URL]",
		Short: "Bring this machine in line with its config",
		Long: `Update maisys, pull the config repo, show what would change, and apply it after
you confirm.

The first run clones CONFIG-URL (or uses the clone given with --dir), picks the
machine file for this machine and saves both choices. Later runs need no
arguments.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !opts.local {
				if err := updateSelf(cmd, g); err != nil {
					return err
				}
			}
			return notImplemented(cmd, args)
		},
	}

	f := cmd.Flags()
	f.StringVar(&opts.machine, "machine", "", "machine file to use; the choice is saved for later runs")
	f.StringVar(&opts.dir, "dir", "", "where the config repo is cloned (default ~/maisys-config)")
	f.BoolVarP(&opts.dryRun, "dry-run", "n", false, "show the plan without changing anything; exits 2 if there are changes")
	f.BoolVarP(&opts.yes, "yes", "y", false, "apply without asking for confirmation")
	f.StringSliceVar(&opts.only, "only", nil, "limit the sync to these modules")
	f.BoolVar(&opts.local, "local", false, "don't update maisys or pull the config repo")
	return cmd
}
