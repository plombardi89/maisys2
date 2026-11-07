package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
)

// findingTheConfig explains, in help text, which config the config
// commands read.
const findingTheConfig = `The config repo is the one the working directory is in, or else the one in
the local settings. The machine is the one MAISYS_MACHINE names, or else the
one in the local settings, or else the one named after the hostname.`

func newConfigCommand(g *globalOptions) *cobra.Command {
	var explain bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show, create or check the config",
		Long: `Show the merged config for this machine: its machine file and the profiles
it lists, on top of each module's defaults. With --explain, a comment after
each value says which file set it.

` + findingTheConfig + `

The subcommands create a new config repo and check an existing one.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showConfig(cmd, g, explain)
		},
	}
	cmd.Flags().BoolVar(&explain, "explain", false, "show which file set each value")

	cmd.AddCommand(
		newConfigInitCommand(),
		newConfigValidateCommand(g),
	)
	return cmd
}

// showConfig writes this machine's merged config as TOML, after a comment
// that says which machine and config repo it comes from.
func showConfig(cmd *cobra.Command, g *globalOptions, explain bool) error {
	r, err := g.openRepo(cmd)
	if err != nil {
		return err
	}
	name, source, err := g.selectMachine(cmd, r, machineRequest{})
	if err != nil {
		return err
	}
	m, err := r.LoadMachine(name)
	if err != nil {
		return problems(cmd.ErrOrStderr(), err)
	}
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "# machine %s (from %s), config repo %s\n\n", name, source, r.dir); err != nil {
		return err
	}
	return config.Render(out, m, r.Modules, modules.Docs(), explain)
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

func newConfigValidateCommand(g *globalOptions) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check the config for errors",
		Long: `Check this machine's config for errors, or with --all, every machine's config
and every profile. Each problem is printed with the file, line and column
it's at. The exit code is 1 if there are any.

` + findingTheConfig + `

With --all, every machine is checked, so none is chosen.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return validateConfig(cmd, g, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "check every machine in the config repo, not just this one")
	return cmd
}

// validateConfig checks this machine's config, or with all, every
// machine's. It prints each problem on its own line.
func validateConfig(cmd *cobra.Command, g *globalOptions, all bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	r, err := g.openRepo(cmd)
	if err != nil {
		return err
	}

	if all {
		if err := r.ValidateAll(); err != nil {
			return problems(errOut, err)
		}
		names, err := r.MachineNames()
		if err != nil {
			return err
		}
		if len(names) == 1 {
			_, err = fmt.Fprintf(out, "maisys: the only machine, %s, is valid\n", names[0])
		} else {
			_, err = fmt.Fprintf(out, "maisys: all %d machines are valid\n", len(names))
		}
		return err
	}

	name, source, err := g.selectMachine(cmd, r, machineRequest{})
	if err != nil {
		return err
	}
	g.debugf(errOut, "machine %s (from %s)", name, source)
	if _, err := r.Validate(name); err != nil {
		return problems(errOut, err)
	}
	_, err = fmt.Fprintf(out, "maisys: the config for %s is valid\n", name)
	return err
}
