package cli

import "github.com/spf13/cobra"

func newConfigCommand(_ *globalOptions) *cobra.Command {
	var explain bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show, create or check the config",
		Long: `Show the merged config for this machine.

The subcommands create a new config repo and check an existing one.`,
		Args: cobra.NoArgs,
		RunE: notImplemented,
	}
	cmd.Flags().BoolVar(&explain, "explain", false, "show which file set each value")

	cmd.AddCommand(
		newConfigInitCommand(),
		newConfigValidateCommand(),
	)
	return cmd
}

func newConfigInitCommand() *cobra.Command {
	var machine, remote string
	cmd := &cobra.Command{
		Use:   "init [DIR]",
		Short: "Create a new config repo",
		Long: `Create a starter config repo in DIR (default ~/maisys-config), with a machine
file for this machine, and save it as the config for this machine.`,
		Args: cobra.MaximumNArgs(1),
		RunE: notImplemented,
	}
	cmd.Flags().StringVar(&machine, "machine", "", "name of the machine file to create for this machine")
	cmd.Flags().StringVar(&remote, "remote", "", "set this existing repo as origin and push to it")
	return cmd
}

func newConfigValidateCommand() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the config for errors",
		Args:  cobra.NoArgs,
		RunE:  notImplemented,
	}
	cmd.Flags().BoolVar(&all, "all", false, "check every machine in the config repo, not just this one")
	return cmd
}
