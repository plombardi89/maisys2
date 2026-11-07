// Package systemtest provides a fake system.SudoRunner for tests of code
// that runs commands.
package systemtest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/system"
)

// Handler answers a command for a FakeRunner. The FakeRunner turns an exit
// code the command doesn't allow into an *system.ExitError, as the real
// runner does.
type Handler func(ctx context.Context, c system.Cmd) (system.Result, error)

// Respond returns a Handler for a command that writes stdout and exits with
// code.
func Respond(code int, stdout string) Handler {
	return func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{ExitCode: code, Stdout: []byte(stdout)}, nil
	}
}

// Fail returns a Handler for a command that writes stderr and exits with
// code.
func Fail(code int, stderr string) Handler {
	return func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{ExitCode: code, Stderr: []byte(stderr)}, nil
	}
}

// Call is a command a FakeRunner was asked to run.
type Call struct {
	// Cmd is the command.
	Cmd system.Cmd
	// InSession reports whether the sudo session was started, the only
	// time sudo may ask for a password.
	InSession bool
	// Ran reports whether the command ran. A command to run as root
	// doesn't while sudo needs a password and no session is started, and
	// an unexpected command doesn't either.
	Ran bool
}

// FakeRunner is a system.SudoRunner that answers commands from handlers
// instead of running them, and records every command it's asked to run. It
// models the sudo session: until StartSudo, a command to run as root runs
// only if SudoStatus is system.SudoNoPassword, and otherwise fails as the
// real runner's would, without asking for a password. On Windows, the
// session it models is the elevated helper, and SudoNoPassword means maisys
// is elevated. A command no handler matches fails the test. It's safe for
// concurrent use.
type FakeRunner struct {
	// SudoStatus is what CheckSudo reports. Empty means
	// system.SudoNoPassword.
	SudoStatus system.SudoStatus
	// StartErr, if set, is what StartSudo returns, as when the user types
	// the wrong password.
	StartErr error
	// OS and User go into the *system.SudoError for a missing sudo or a
	// user who may not use it. With OS config.Windows, a command to run as
	// admin outside the session fails with system.ErrNeedsAdmin, as the
	// real runner's does there.
	OS   config.OS
	User string

	t        testing.TB
	mu       sync.Mutex
	handlers []route
	calls    []Call
	checks   int
	starts   int
	started  bool
}

var _ system.SudoRunner = (*FakeRunner)(nil)

// route is a Handler with the commands it answers.
type route struct {
	prefix []string // the name, then the first arguments
	h      Handler
}

// NewFakeRunner returns a FakeRunner with no handlers, which reports
// unexpected commands as failures of tb.
func NewFakeRunner(tb testing.TB) *FakeRunner {
	tb.Helper()
	return &FakeRunner{t: tb}
}

// On answers with h every command that starts with prefix: the command's
// name, then its first arguments, separated by spaces, such as "rpm -q".
// When several prefixes match, the longest wins, and then the latest.
func (f *FakeRunner) On(prefix string, h Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, route{prefix: strings.Fields(prefix), h: h})
}

// Run answers c from the handlers. See system.Runner.
func (f *FakeRunner) Run(ctx context.Context, c system.Cmd) (system.Result, error) {
	c.Args = slices.Clone(c.Args)
	c.Env = slices.Clone(c.Env)
	if err := ctx.Err(); err != nil {
		return system.Result{ExitCode: -1}, err
	}

	f.mu.Lock()
	call := Call{Cmd: c, InSession: f.started}
	var blocked error
	if c.Root && !f.started {
		blocked = f.rootError()
	}
	h := f.match(c)
	call.Ran = blocked == nil && h != nil
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	if blocked != nil {
		return system.Result{ExitCode: -1}, fmt.Errorf("%s: %w", label(c), blocked)
	}
	if h == nil {
		if f.t != nil {
			f.t.Errorf("FakeRunner: unexpected command: %s", c)
		}
		return system.Result{ExitCode: -1}, fmt.Errorf("unexpected command: %s", c)
	}
	res, err := h(ctx, c)
	if err != nil {
		return res, err
	}
	return res, system.CheckExit(c, res)
}

// match returns the handler for c, or nil. f.mu must be held.
func (f *FakeRunner) match(c system.Cmd) Handler {
	line := append([]string{c.Name}, c.Args...)
	var best *route
	for i := range f.handlers {
		r := &f.handlers[i]
		if len(r.prefix) <= len(line) && slices.Equal(r.prefix, line[:len(r.prefix)]) &&
			(best == nil || len(r.prefix) >= len(best.prefix)) {
			best = r
		}
	}
	if best == nil {
		return nil
	}
	return best.h
}

// CheckSudo reports SudoStatus. See system.SudoRunner.
func (f *FakeRunner) CheckSudo(context.Context) (system.SudoStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.status(), f.sudoError()
}

// StartSudo starts the fake sudo session, unless sudo is missing, the user
// may not use it, or StartErr is set. See system.SudoRunner.
func (f *FakeRunner) StartSudo(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if err := f.sudoError(); err != nil {
		return err
	}
	if f.StartErr != nil {
		return f.StartErr
	}
	f.started = true
	return nil
}

// StopSudo stops the fake sudo session. See system.SudoRunner.
func (f *FakeRunner) StopSudo() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = false
}

// Calls returns every command the runner was asked to run, in order.
func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// Commands returns the command lines of the commands that ran, in order.
func (f *FakeRunner) Commands() []string {
	return f.lines(func(c Call) bool { return c.Ran })
}

// RootCommands returns the command lines of the commands that ran as root,
// in order.
func (f *FakeRunner) RootCommands() []string {
	return f.lines(func(c Call) bool { return c.Ran && c.Cmd.Root })
}

// SudoChecks returns how many times CheckSudo was called.
func (f *FakeRunner) SudoChecks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

// SudoStarts returns how many times StartSudo was called: the only times
// sudo may ask for a password.
func (f *FakeRunner) SudoStarts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

func (f *FakeRunner) lines(keep func(Call) bool) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var lines []string
	for _, c := range f.calls {
		if keep(c) {
			lines = append(lines, c.Cmd.String())
		}
	}
	return lines
}

// status returns SudoStatus, or its default. f.mu must be held.
func (f *FakeRunner) status() system.SudoStatus {
	if f.SudoStatus == "" {
		return system.SudoNoPassword
	}
	return f.SudoStatus
}

// rootError returns why a command can't run as root outside a session, or
// nil if it can. f.mu must be held.
func (f *FakeRunner) rootError() error {
	if f.status() == system.SudoNeedsPassword {
		if f.OS == config.Windows {
			return system.ErrNeedsAdmin
		}
		return system.ErrNeedsPassword
	}
	return f.sudoError()
}

// sudoError returns the error for a missing sudo or a user who may not use
// it, or nil. f.mu must be held.
func (f *FakeRunner) sudoError() error {
	switch s := f.status(); s {
	case system.SudoMissing, system.SudoNotAllowed:
		return &system.SudoError{Status: s, User: f.User, OS: f.OS}
	default:
		return nil
	}
}

// label returns how errors name c, as the real runner does.
func label(c system.Cmd) string {
	if c.Label != "" {
		return c.Label
	}
	return c.String()
}
