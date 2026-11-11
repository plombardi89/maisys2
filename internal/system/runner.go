// Package system runs the commands maisys needs: as the user, or as root on
// Linux through a sudo session that never asks for a password while
// planning. Every command's command line, exit code and output go to the
// run's log.
package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

// Cmd is a command to run. Its command line and output go to the run's
// log, so neither may hold secrets.
type Cmd struct {
	// Name is the program, looked up in PATH unless it's a path.
	Name string
	// Args are the program's arguments.
	Args []string
	// Dir is the working directory. Empty means maisys's own.
	Dir string
	// Env is extra environment, as NAME=value. Commands run with maisys's
	// own environment plus LC_ALL=C, which Env can override. A command run
	// as root gets only LC_ALL=C and Env, because sudo resets the rest.
	Env []string
	// Stdin is the command's input. Nil means none.
	Stdin io.Reader
	// Root runs the command as root, with sudo -n. Only Linux has it.
	Root bool
	// Label says what the command does in a few words, such as
	// "dnf install", for errors. Empty means the command line.
	Label string
	// AllowExit lists the exit codes other than 0 that aren't errors, such
	// as 1 for "rpm -q" of a package that isn't installed.
	AllowExit []int
	// Interactive runs the command where the terminal's Ctrl-C reaches it,
	// as it reaches maisys. Only a command that may ask you something
	// needs it, such as sudo -v asking for your password. Other commands
	// run in a process group of their own (on Windows, without Ctrl-C), so
	// that after a Ctrl-C the step in progress can finish: maisys stops
	// them itself when their context is cancelled. They mustn't read from
	// the terminal.
	Interactive bool
}

// String returns the command line, quoted for a POSIX shell where needed.
func (c Cmd) String() string {
	return quoteArgs(append([]string{c.Name}, c.Args...))
}

// what returns how errors name the command.
func (c Cmd) what() string {
	if c.Label != "" {
		return c.Label
	}
	return c.String()
}

// Result is what a command did.
type Result struct {
	// ExitCode is the command's exit code, or -1 if it didn't start or was
	// stopped by a signal.
	ExitCode int
	// Stdout and Stderr are the command's output and error output.
	Stdout, Stderr []byte
}

// Runner runs commands. Tests use a fake one (systemtest.FakeRunner).
type Runner interface {
	// Run runs c and waits for it to finish. An exit code other than 0
	// that c doesn't allow is an *ExitError, returned with the result.
	// While no sudo session is started, a command run as root never asks
	// for a password: it fails with ErrNeedsPassword instead.
	Run(ctx context.Context, c Cmd) (Result, error)
}

// SudoRunner is a Runner with a sudo session for the commands it runs as
// root.
type SudoRunner interface {
	Runner
	// CheckSudo reports whether sudo is installed and works without a
	// password right now, without ever asking for one. If sudo is missing
	// or the user may not use it, the error is a *SudoError saying what to
	// run as root to fix that; any other error means sudo couldn't be
	// checked or doesn't work.
	CheckSudo(ctx context.Context) (SudoStatus, error)
	// StartSudo starts the session that applying runs in: unless sudo
	// already works without a password, it runs sudo -v, the only command
	// that may ask for one, and it keeps sudo's session alive until
	// StopSudo or until ctx is done.
	StartSudo(ctx context.Context) error
	// StopSudo stops keeping the session alive.
	StopSudo()
}

// ExitError is a command that exited with a code it didn't allow.
type ExitError struct {
	// Label is the command's Label, or its command line.
	Label string
	// Command is the command line, without sudo.
	Command string
	// Code is the exit code.
	Code int
	// Tail is the last lines of the command's error output, or of its
	// output if it wrote no errors.
	Tail []string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s failed with exit code %d", e.Label, e.Code)
	switch len(e.Tail) {
	case 0:
		return msg
	case 1:
		return msg + ": " + e.Tail[0]
	}
	return msg + ":\n  " + strings.Join(e.Tail, "\n  ")
}

// tailLines is how many lines of output an ExitError keeps.
const tailLines = 5

// CheckExit returns nil if res's exit code is 0 or one c allows, and an
// *ExitError otherwise.
func CheckExit(c Cmd, res Result) error {
	if res.ExitCode == 0 || slices.Contains(c.AllowExit, res.ExitCode) {
		return nil
	}
	out := res.Stderr
	if len(bytes.TrimSpace(out)) == 0 {
		out = res.Stdout
	}
	return &ExitError{Label: c.what(), Command: c.String(), Code: res.ExitCode, Tail: lastLines(out, tailLines)}
}

// lastLines returns the last n lines of out that aren't blank, trimmed.
func lastLines(out []byte, n int) []string {
	var lines []string
	for line := range strings.Lines(string(out)) {
		// Progress output redraws a line with \r; keep what's left at the end.
		line = strings.TrimRight(line, "\r\n")
		if i := strings.LastIndexByte(line, '\r'); i >= 0 {
			line = line[i+1:]
		}
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines[max(0, len(lines)-n):]
}

// Options control how an ExecRunner runs commands.
type Options struct {
	// Log receives every command's command line, output and exit code, one
	// command at a time. It may be nil.
	Log io.Writer
	// Verbose, if not nil, receives each command line and its output as it
	// runs, a line at a time.
	Verbose io.Writer
	// Sudo sets up the sudo session for commands run as root.
	Sudo SudoOptions
}

// ExecRunner runs commands on the live system. It's safe for concurrent
// use.
type ExecRunner struct {
	opts      Options
	logMu     sync.Mutex
	verboseMu sync.Mutex
	sudo      sudoSession
}

var _ SudoRunner = (*ExecRunner)(nil)

// NewRunner returns a runner for the live system.
func NewRunner(opts Options) *ExecRunner {
	r := &ExecRunner{opts: opts}
	r.sudo.r = r
	return r
}

// Run runs c. See Runner.
func (r *ExecRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if !c.Root {
		return r.run(ctx, c, nil)
	}
	sudo, err := r.sudo.forRoot(ctx)
	if err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("%s: %w", c.what(), err)
	}
	res, err := r.run(ctx, c.viaSudo(sudo), nil)
	if msg := sudoMessage(res); res.ExitCode == 1 && strings.HasPrefix(msg, "sudo: ") && passwordRequired(msg) {
		return res, fmt.Errorf("%s: %w", c.what(), r.sudo.passwordNeeded())
	}
	if ee, ok := errors.AsType[*ExitError](err); ok {
		// Errors name the command, not sudo.
		ee.Command = c.String()
	}
	return res, err
}

// viaSudo returns c run as root through sudo.
func (c Cmd) viaSudo(sudo string) Cmd {
	args := make([]string, 0, len(c.Env)+len(c.Args)+5)
	args = append(args, "-n", "--", "env", "LC_ALL=C")
	args = append(args, c.Env...)
	args = append(args, c.Name)
	args = append(args, c.Args...)
	c.Label = c.what()
	c.Name, c.Args, c.Env, c.Root = sudo, args, nil, false
	return c
}

// terminal is where a command that may ask for a password reads and writes,
// instead of having its output captured.
type terminal struct {
	stdin          io.Reader
	stdout, stderr io.Writer
}

// run runs c as the user. With term, it runs c in the terminal: its output
// goes there, and only its error output is also captured.
func (r *ExecRunner) run(ctx context.Context, c Cmd, term *terminal) (Result, error) {
	line := c.String()
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(append(os.Environ(), "LC_ALL=C"), c.Env...)
	cmd.Stdin = c.Stdin
	cmd.SysProcAttr = procAttr(c.Interactive)
	cmd.Cancel = func() error { return interrupt(cmd.Process) }
	cmd.WaitDelay = 10 * time.Second

	var stdout, stderr bytes.Buffer
	combined := &lockedBuffer{}
	var streams []*lineWriter
	if term != nil {
		cmd.Stdin = term.stdin
		cmd.Stdout = term.stdout
		cmd.Stderr = multiWriter(term.stderr, &stderr, combined)
	} else {
		outs := []io.Writer{&stdout, combined}
		errs := []io.Writer{&stderr, combined}
		if r.opts.Verbose != nil {
			r.verbose([]byte("$ " + line + "\n"))
			streams = []*lineWriter{{write: r.verbose}, {write: r.verbose}}
			outs = append(outs, streams[0])
			errs = append(errs, streams[1])
		}
		cmd.Stdout = multiWriter(outs...)
		cmd.Stderr = multiWriter(errs...)
	}

	start := time.Now()
	err := checkDir(c.Dir)
	if err == nil {
		err = cmd.Run()
	}
	for _, s := range streams {
		s.flush()
	}
	res := Result{ExitCode: -1, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}

	var outcome string
	switch {
	case cmd.ProcessState == nil:
		outcome = "didn't start: " + err.Error()
		if pe, ok := errors.AsType[*fs.PathError](err); ok && pe.Op == "chdir" {
			err = fmt.Errorf("%s: %w", c.what(), err)
		} else if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("%s: %s isn't installed: %w", c.what(), c.Name, err)
		} else {
			err = fmt.Errorf("%s: %w", c.what(), err)
		}
	case ctx.Err() != nil:
		outcome = fmt.Sprintf("stopped (%v), exit code %d", ctx.Err(), res.ExitCode)
		err = fmt.Errorf("%s: %w", c.what(), ctx.Err())
	default:
		outcome = fmt.Sprintf("exit code %d", res.ExitCode)
		err = CheckExit(c, res)
	}
	r.logCommand(start, line, combined.Bytes(), outcome)
	return res, err
}

// checkDir returns an error with Op "chdir" if dir isn't empty and can't be
// found. os.StartProcess checks this itself only for commands started
// without a SysProcAttr; for the others, a missing working directory would
// look like a missing command.
func checkDir(dir string) error {
	if dir == "" {
		return nil
	}
	_, err := os.Stat(dir)
	if pe, ok := errors.AsType[*fs.PathError](err); ok {
		pe.Op = "chdir"
	}
	return err
}

// logCommand writes one command to the log in a single write, so commands
// running at the same time don't mix.
func (r *ExecRunner) logCommand(start time.Time, line string, out []byte, outcome string) {
	if r.opts.Log == nil {
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s $ %s\n", start.Format("15:04:05"), line)
	b.Write(out)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "%s %s after %s\n\n", time.Now().Format("15:04:05"), outcome, time.Since(start).Round(time.Millisecond))
	r.logMu.Lock()
	defer r.logMu.Unlock()
	// The log is a record for later; failing to write it doesn't stop a sync.
	_, _ = r.opts.Log.Write(b.Bytes())
}

// verbose writes whole lines to the Verbose writer.
func (r *ExecRunner) verbose(p []byte) {
	r.verboseMu.Lock()
	defer r.verboseMu.Unlock()
	_, _ = r.opts.Verbose.Write(p)
}

// multiWriter is io.MultiWriter, leaving out nil writers. It returns nil if
// every writer is nil.
func multiWriter(ws ...io.Writer) io.Writer {
	ws = slices.DeleteFunc(ws, func(w io.Writer) bool { return w == nil })
	switch len(ws) {
	case 0:
		return nil
	case 1:
		return ws[0]
	}
	return io.MultiWriter(ws...)
}

// lockedBuffer is a buffer two goroutines can write to, as a command's
// output and error output are.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}

// lineWriter passes on what's written to it a whole line at a time, so lines
// from commands running at the same time don't mix.
type lineWriter struct {
	write   func([]byte)
	pending []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.pending = append(l.pending, p...)
	if i := bytes.LastIndexByte(l.pending, '\n'); i >= 0 {
		l.write(l.pending[:i+1])
		l.pending = slices.Clone(l.pending[i+1:])
	}
	return len(p), nil
}

// flush writes the last line, if it has no newline.
func (l *lineWriter) flush() {
	if len(l.pending) > 0 {
		l.write(append(l.pending, '\n'))
		l.pending = nil
	}
}

// quoteArgs joins args into a command line, quoting them for a POSIX shell
// where needed.
func quoteArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = quote(a)
	}
	return strings.Join(quoted, " ")
}

func quote(s string) string {
	if s == "" {
		return "''"
	}
	safe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r)
	}
	if !strings.ContainsFunc(s, func(r rune) bool { return !safe(r) }) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
