// Package cli defines the maisys command line.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/buildinfo"
)

// Exit codes returned by Execute.
const (
	exitOK    = 0
	exitError = 1
)

var errNotImplemented = errors.New("not implemented yet")

// globalOptions holds settings that apply to every command.
type globalOptions struct {
	verbose bool
	color   bool
}

func newGlobalOptions(getenv func(string) string) *globalOptions {
	// https://no-color.org: any non-empty NO_COLOR value turns colour off.
	return &globalOptions{color: getenv("NO_COLOR") == ""}
}

// Execute runs the maisys command line with args (without the program name)
// and returns the process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	root := newRootCommand(newGlobalOptions(getenv))
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "maisys: %v\n", err)
		return exitError
	}
	return exitOK
}

func newRootCommand(g *globalOptions) *cobra.Command {
	root := &cobra.Command{
		Use:   "maisys",
		Short: "Set up and maintain machines from a config repo",
		Long: `maisys installs and configures this machine from a config repo kept in git,
and keeps it up to date with "maisys sync".`,
		Version:       buildinfo.String(),
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.SetVersionTemplate("maisys {{.Version}}\n")
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, cmd.CommandPath())
	})
	root.PersistentFlags().BoolVarP(&g.verbose, "verbose", "v", false, "show the output of every command maisys runs")

	root.AddCommand(
		newSyncCommand(g),
		newConfigCommand(g),
		newStatusCommand(g),
	)
	return root
}

// notImplemented is the RunE of commands that exist but don't work yet.
func notImplemented(cmd *cobra.Command, _ []string) error {
	name := strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()+" ")
	return fmt.Errorf("%q is %w", name, errNotImplemented)
}
