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

// exitCodeError makes Execute return code without printing anything, for
// example to pass on the exit code of a restarted maisys.
type exitCodeError struct {
	code int
}

func (e exitCodeError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

// globalOptions holds settings that apply to every command.
type globalOptions struct {
	verbose bool
	color   bool
	args    []string // the command line, without the program name
	getenv  func(string) string
}

func newGlobalOptions(args []string, getenv func(string) string) *globalOptions {
	return &globalOptions{
		// https://no-color.org: any non-empty NO_COLOR value turns colour off.
		color:  getenv("NO_COLOR") == "",
		args:   args,
		getenv: getenv,
	}
}

// debugf prints a message only with --verbose.
func (g *globalOptions) debugf(w io.Writer, format string, a ...any) {
	if g.verbose {
		_, _ = fmt.Fprintf(w, "maisys: "+format+"\n", a...)
	}
}

// Execute runs the maisys command line with args (without the program name)
// and returns the process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	root := newRootCommand(newGlobalOptions(args, getenv))
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	if err := root.ExecuteContext(ctx); err != nil {
		var exit exitCodeError
		if errors.As(err, &exit) {
			return exit.code
		}
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
