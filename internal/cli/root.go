// Package cli defines the maisys command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

// Exit codes returned by Execute.
const (
	exitOK    = 0
	exitError = 1
	// exitChanges is sync --dry-run's exit code when the plan has changes.
	exitChanges = 2
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

// environment is what maisys uses from outside itself: the standard
// streams, environment variables, the working directory and the live
// system. Execute uses the process's own; tests make their own.
type environment struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	getwd  func() (string, error)
	// detect detects the system maisys is running on.
	detect func() (*facts.System, error)
	// isTerminal reports whether stdin and stdout are both terminals, so
	// maisys can ask questions.
	isTerminal func() bool
	// goos is the OS maisys runs on, as a GOOS value. It decides where
	// maisys keeps its files.
	goos string
	// gitEnv is extra environment for every git command maisys runs.
	// Tests use it to keep git away from the real user's config.
	gitEnv []string
	// findGit returns the git binary's path (gitrepo.Find).
	findGit func() (string, error)
	// prompt asks for a config repo token in a terminal. It's nil when
	// maisys can't ask questions.
	prompt func(ctx context.Context, url string) (string, error)
	// stdoutTerminal reports whether stdout is a terminal, where the plan
	// can be in colour.
	stdoutTerminal func() bool
	// abort is closed on a second Ctrl-C, after the first has cancelled
	// the command's context: sync then stops the step in progress too. It
	// may be nil.
	abort <-chan struct{}
	// now returns the current time.
	now func() time.Time
	// modules are every module maisys has (modules.All()).
	modules []config.Module
	// removers remove what's taken out of the config, by kind.
	removers resource.Removers
	// newRunner returns the runner for a sync's commands
	// (system.NewRunner).
	newRunner func(opts system.Options) system.Runner
}

// globalOptions holds settings that apply to every command, and the
// environment the commands run in.
type globalOptions struct {
	*environment

	verbose bool
	color   bool
	args    []string // the command line, without the program name

	// in reads stdin. Every question maisys asks shares it, so none of
	// them loses input another one has buffered.
	in *bufio.Reader
	// system is the live system, once detected.
	system *facts.System
}

func newGlobalOptions(args []string, env *environment) *globalOptions {
	return &globalOptions{
		environment: env,
		// https://no-color.org: any non-empty NO_COLOR value turns colour off.
		color: env.getenv("NO_COLOR") == "",
		args:  args,
		in:    bufio.NewReader(env.stdin),
	}
}

// debugf prints a message only with --verbose.
func (g *globalOptions) debugf(w io.Writer, format string, a ...any) {
	if g.verbose {
		_, _ = fmt.Fprintf(w, "maisys: "+format+"\n", a...)
	}
}

// liveSystem returns the system maisys is running on, detecting it the
// first time.
func (g *globalOptions) liveSystem() (*facts.System, error) {
	if g.system == nil {
		s, err := g.detect()
		if err != nil {
			return nil, fmt.Errorf("detecting this system: %w", err)
		}
		g.system = s
	}
	return g.system, nil
}

// Execute runs the maisys command line with args (without the program name)
// and returns the process exit code. Cancelling ctx, as the first Ctrl-C
// does, stops maisys after the step in progress; closing abort, as a second
// Ctrl-C does, stops that step too.
func Execute(ctx context.Context, abort <-chan struct{}, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	return execute(ctx, args, processEnvironment(abort, stdout, stderr, getenv))
}

// processEnvironment returns the environment of the maisys process, with
// stdout, stderr and getenv for its streams and environment variables.
func processEnvironment(abort <-chan struct{}, stdout, stderr io.Writer, getenv func(string) string) *environment {
	outTerminal := isTerminal(stdout)
	terminal := isTerminal(os.Stdin) && outTerminal
	var prompt func(ctx context.Context, url string) (string, error)
	if terminal {
		prompt = gitrepo.TerminalPrompt(os.Stdin, stdout)
	}
	return &environment{
		stdin:  os.Stdin,
		stdout: stdout,
		stderr: stderr,
		getenv: getenv,
		getwd:  os.Getwd,
		detect: facts.Detect,
		isTerminal: func() bool {
			return terminal
		},
		goos:    runtime.GOOS,
		findGit: gitrepo.Find,
		prompt:  prompt,
		stdoutTerminal: func() bool {
			return outTerminal
		},
		abort:   abort,
		now:     time.Now,
		modules: modules.All(),
		newRunner: func(opts system.Options) system.Runner {
			return system.NewRunner(opts)
		},
	}
}

// isTerminal reports whether f, a stream, is a terminal.
func isTerminal(f any) bool {
	fd, ok := f.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(fd.Fd()))
}

// execute is Execute in env.
func execute(ctx context.Context, args []string, env *environment) int {
	root := newRootCommand(newGlobalOptions(args, env))
	root.SetArgs(args)
	root.SetIn(env.stdin)
	root.SetOut(env.stdout)
	root.SetErr(env.stderr)

	if err := root.ExecuteContext(ctx); err != nil {
		if exit, ok := errors.AsType[exitCodeError](err); ok {
			return exit.code
		}
		_, _ = fmt.Fprintf(env.stderr, "maisys: %v\n", err)
		if errors.Is(err, config.ErrUnknownKey) {
			_, _ = fmt.Fprintf(env.stderr, "maisys: an unknown key may mean the config needs a newer maisys than this one (%s)\n", buildVersion())
		}
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

// problemsError reports how many problems a command found in the config,
// after it has printed them. It wraps the problems, so errors.Is sees
// what they wrap.
type problemsError struct {
	count int
	err   error
}

func (e problemsError) Error() string {
	if e.count == 1 {
		return "1 problem found"
	}
	return fmt.Sprintf("%d problems found", e.count)
}

func (e problemsError) Unwrap() error {
	return e.err
}

// problems prints each problem in err, which may be several joined with
// errors.Join, on its own line of w, and returns a problemsError for them.
// Config problems already start with file:line:column.
func problems(w io.Writer, err error) error {
	n := 0
	for line := range strings.Lines(err.Error()) {
		line = strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(line) == "" {
			continue
		}
		_, _ = fmt.Fprintln(w, line)
		n++
	}
	return problemsError{count: n, err: err}
}
