package cli

import (
	"fmt"
	"io"

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
		newConfigInitCommand(g),
		newConfigValidateCommand(g),
	)
	return cmd
}

// showConfig writes this machine's merged config as TOML, after a comment
// that says which machine and config repo it comes from.
func showConfig(cmd *cobra.Command, g *globalOptions, explain bool) error {
	r, err := g.openRepo(cmd, false)
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

func newConfigInitCommand(g *globalOptions) *cobra.Command {
	var opts initOptions
	cmd := &cobra.Command{
		Use:   "init [DIR]",
		Short: "Create a new config repo",
		Long: `Create a starter config repo in DIR (default ~/maisys-config), with a machine
file for this machine, and commit it with git. DIR must be empty or not
exist yet. If this machine isn't set up for maisys yet, the new repo
becomes its config. Otherwise the local settings are left as they are, and
"maisys sync --dir DIR" switches to the new repo.

The machine is named after --machine, or else MAISYS_MACHINE, or else the
hostname. Its machine file declares this system's os and os_version. Every
module is in the profiles as a commented-out example. On Windows, the
machine file also creates a WSL distro, set up from a Linux machine file
that's written too.

With --remote, the repo is pushed to URL, which must already exist: create
an empty repo for it first.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				opts.dir = args[0]
			}
			return initConfig(cmd, g, opts)
		},
	}
	cmd.Flags().StringVar(&opts.machine, "machine", "", "name of the machine file to create for this machine")
	cmd.Flags().StringVar(&opts.remote, "remote", "", "set this existing repo as origin and push to it")
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

Keys maisys doesn't know are errors here, though other commands ignore
them, so that a config written for a newer maisys still works.

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
// machine's, reporting unknown keys too. It prints each problem on its own
// line.
func validateConfig(cmd *cobra.Command, g *globalOptions, all bool) error {
	out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
	r, err := g.openRepo(cmd, true)
	if err != nil {
		return err
	}

	if all {
		warnings, err := r.ValidateAll()
		printWarnings(errOut, warnings)
		if err != nil {
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
	m, err := r.Validate(name)
	if m != nil {
		printWarnings(errOut, m.Warnings)
	}
	if err != nil {
		return problems(errOut, err)
	}
	_, err = fmt.Fprintf(out, "maisys: the config for %s is valid\n", name)
	return err
}

// printWarnings prints warnings about the config, such as deprecated keys.
func printWarnings(w io.Writer, warnings []string) {
	for _, warning := range warnings {
		_, _ = fmt.Fprintf(w, "maisys: warning: %s\n", warning)
	}
}
