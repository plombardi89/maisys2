package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
)

// ErrNeedsPassword means a command has to run as root, but sudo needs a
// password and no sudo session has been started. Plans never ask for a
// password, so they show such checks as checked during apply.
var ErrNeedsPassword = errors.New("sudo needs a password")

// errNoSudo is the error for running a command as root where maisys doesn't
// use sudo: on Windows, steps that need admin rights run in an elevated
// helper instead.
var errNoSudo = errors.New("running commands as root needs sudo, which maisys only uses on Linux")

// SudoStatus is whether, and how, sudo can run commands as root.
type SudoStatus string

// The sudo statuses CheckSudo reports.
const (
	// SudoMissing means sudo isn't installed.
	SudoMissing SudoStatus = "missing"
	// SudoNotAllowed means the user may not use sudo.
	SudoNotAllowed SudoStatus = "not allowed"
	// SudoNeedsPassword means sudo works once the user types their
	// password, which only StartSudo asks for.
	SudoNeedsPassword SudoStatus = "needs a password"
	// SudoNoPassword means sudo works without a password right now:
	// either it's set up that way, or the user typed it recently.
	SudoNoPassword SudoStatus = "works without a password"
)

// DefaultSudoRefresh is how often a started sudo session is kept alive.
const DefaultSudoRefresh = 60 * time.Second

// SudoOptions set up the sudo session.
type SudoOptions struct {
	// Path is the sudo binary. Empty means sudo from PATH.
	Path string
	// OS is the machine's OS, which decides the commands a SudoError
	// suggests.
	OS config.OS
	// User is the user maisys runs as, for a SudoError's commands. Empty
	// means the current user.
	User string
	// Stdin, Stdout and Stderr are the terminal where StartSudo's sudo -v
	// may ask for the user's password.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// Refresh is how often a started session is kept alive with
	// sudo -n -v. Zero means DefaultSudoRefresh.
	Refresh time.Duration
}

// SudoError means sudo is missing, or the user may not use it, so nothing
// can run as root. It says what to run as root to fix that.
type SudoError struct {
	// Status is SudoMissing or SudoNotAllowed.
	Status SudoStatus
	// User is the user who needs to use sudo.
	User string
	// OS is the machine's OS.
	OS config.OS
	// Message is what sudo said, if anything.
	Message string
}

// Commands returns the commands to run as root to install sudo, if it's
// missing, and let the user use it. It returns nil for an OS whose
// commands maisys doesn't know.
func (e *SudoError) Commands() []string {
	var install []string
	var group string
	switch e.OS {
	case config.Fedora:
		install, group = []string{"dnf install -y sudo"}, "wheel"
	case config.Ubuntu:
		install, group = []string{"apt-get update", "apt-get install -y sudo"}, "sudo"
	default:
		return nil
	}
	var cmds []string
	if e.Status == SudoMissing {
		cmds = install
	}
	return append(cmds, "usermod -aG "+group+" "+quote(e.User))
}

func (e *SudoError) Error() string {
	var b strings.Builder
	if e.Status == SudoMissing {
		b.WriteString("sudo isn't installed")
	} else {
		fmt.Fprintf(&b, "%s may not use sudo", e.User)
		if e.Message != "" {
			fmt.Fprintf(&b, " (%s)", e.Message)
		}
	}
	b.WriteString(", and some steps need to run as root.")
	cmds := e.Commands()
	if cmds == nil {
		if e.Status == SudoMissing {
			b.WriteString(" Install sudo as root, and")
		} else {
			b.WriteString(" As root,")
		}
		fmt.Fprintf(&b, " add %s to the group that may use sudo (wheel or sudo). Then log out and back in, and run maisys sync again.", e.User)
		return b.String()
	}
	these := "these commands"
	if len(cmds) == 1 {
		these = "this command"
	}
	fmt.Fprintf(&b, "\nTo fix this, run %s as root, then log out and back in and run maisys sync again:\n  %s", these, strings.Join(cmds, "\n  "))
	fmt.Fprintf(&b, "\nFor example, with root's password:\n  su - -c %s", quote(strings.Join(cmds, " && ")))
	return b.String()
}

// sudoSession is an ExecRunner's sudo session.
type sudoSession struct {
	r *ExecRunner

	mu      sync.Mutex
	path    string     // the sudo binary, once found
	checked bool       // whether status and err are up to date
	status  SudoStatus // from the last check
	err     error      // why sudo can't be used, from the last check
	stop    context.CancelFunc
	done    chan struct{} // closed when the refresher stops
}

// CheckSudo checks sudo. See SudoRunner.
func (r *ExecRunner) CheckSudo(ctx context.Context) (SudoStatus, error) {
	s := &r.sudo
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.check(ctx)
}

// StartSudo starts the sudo session. See SudoRunner.
func (r *ExecRunner) StartSudo(ctx context.Context) error {
	s := &r.sudo
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sudoSupported {
		return errNoSudo
	}
	if s.stop != nil {
		return nil
	}
	path, err := s.find()
	if err != nil {
		s.checked, s.status, s.err = true, SudoMissing, s.sudoError(SudoMissing, "")
		return s.err
	}
	opts := r.opts.Sudo
	// Where sudo already works without a password, as with a NOPASSWD
	// rule, sudo -v isn't needed, and could even ask for one: by default,
	// it only doesn't if every rule for the user is NOPASSWD, and a user in
	// the wheel or sudo group has that group's rule too. sudo -n -v then
	// only refreshes a password typed a while ago.
	res, err := r.run(ctx, Cmd{Name: path, Args: []string{"-n", "true"}, Label: "sudo -n true", AllowExit: []int{1}, sudo: true}, nil)
	if err == nil && res.ExitCode == 0 {
		_, _ = r.run(ctx, Cmd{Name: path, Args: []string{"-n", "-v"}, Label: "sudo -n -v", sudo: true}, nil)
	} else {
		term := &terminal{stdin: opts.Stdin, stdout: opts.Stdout, stderr: opts.Stderr}
		res, err = r.run(ctx, Cmd{Name: path, Args: []string{"-v"}, Label: "sudo -v", Interactive: true, sudo: true}, term)
		if err != nil {
			if _, ok := errors.AsType[*ExitError](err); !ok {
				return fmt.Errorf("couldn't start sudo: %w", err)
			}
			msg := sudoMessage(res)
			if notAllowed(msg) {
				s.checked, s.status, s.err = true, SudoNotAllowed, s.sudoError(SudoNotAllowed, msg)
				return s.err
			}
			if msg == "" {
				msg = fmt.Sprintf("exit code %d", res.ExitCode)
			}
			return fmt.Errorf("sudo couldn't check your password: %s", msg)
		}
	}
	s.checked, s.status, s.err = true, SudoNoPassword, nil

	refresh := opts.Refresh
	if refresh <= 0 {
		refresh = DefaultSudoRefresh
	}
	ctx, s.stop = context.WithCancel(ctx)
	s.done = make(chan struct{})
	go s.keepAlive(ctx, path, refresh, s.done)
	return nil
}

// StopSudo stops the sudo session. See SudoRunner.
func (r *ExecRunner) StopSudo() {
	s := &r.sudo
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	// Outside a session, sudo is checked again before it's used.
	s.checked = false
	s.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
}

// keepAlive refreshes sudo's session every interval until ctx is done.
// Failures are only logged: a command that then needs a password fails
// with ErrNeedsPassword.
func (s *sudoSession) keepAlive(ctx context.Context, sudo string, interval time.Duration, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.r.run(ctx, Cmd{Name: sudo, Args: []string{"-n", "-v"}, Label: "sudo -n -v", sudo: true}, nil)
		}
	}
}

// forRoot returns the sudo binary for running a command as root. Outside a
// session, it fails with ErrNeedsPassword unless sudo works without a
// password, checking that the first time.
func (s *sudoSession) forRoot(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sudoSupported {
		return "", errNoSudo
	}
	if s.stop != nil {
		return s.path, nil
	}
	if !s.checked {
		if _, err := s.check(ctx); err != nil {
			return "", err
		}
	}
	switch s.status {
	case SudoNoPassword:
		return s.path, nil
	case SudoNeedsPassword:
		return "", ErrNeedsPassword
	default:
		return "", s.err
	}
}

// passwordNeeded records that sudo asked for a password, and returns the
// error for the command that needed it.
func (s *sudoSession) passwordNeeded() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		return fmt.Errorf("sudo's session ran out, so %w again; run maisys sync again", ErrNeedsPassword)
	}
	s.checked, s.status, s.err = true, SudoNeedsPassword, nil
	return ErrNeedsPassword
}

// check runs check and remembers its answer. s.mu must be held.
func (s *sudoSession) check(ctx context.Context) (SudoStatus, error) {
	if !sudoSupported {
		return "", errNoSudo
	}
	status, err := s.probe(ctx)
	if ctx.Err() == nil {
		s.checked, s.status, s.err = true, status, err
	}
	return status, err
}

// probe finds out whether sudo works without a password, without ever
// asking for one.
func (s *sudoSession) probe(ctx context.Context) (SudoStatus, error) {
	path, err := s.find()
	if err != nil {
		return SudoMissing, s.sudoError(SudoMissing, "")
	}
	res, err := s.r.run(ctx, Cmd{Name: path, Args: []string{"-n", "true"}, Label: "sudo -n true", AllowExit: []int{1}, sudo: true}, nil)
	if err != nil {
		return "", fmt.Errorf("couldn't check whether sudo works: %w", err)
	}
	if res.ExitCode == 0 {
		return SudoNoPassword, nil
	}
	msg := sudoMessage(res)
	switch {
	case notAllowed(msg):
		return SudoNotAllowed, s.sudoError(SudoNotAllowed, msg)
	case !passwordRequired(msg):
		return "", fmt.Errorf("sudo doesn't work: %s", msg)
	}

	// sudo asks for a password before it says whether the user may use it
	// at all, but listing what they may run sometimes says so without one.
	res, err = s.r.run(ctx, Cmd{Name: path, Args: []string{"-n", "-l"}, Label: "sudo -n -l", AllowExit: []int{1}, sudo: true}, nil)
	if err != nil {
		return "", fmt.Errorf("couldn't check whether sudo works: %w", err)
	}
	if msg := sudoMessage(res); res.ExitCode != 0 && notAllowed(msg) {
		return SudoNotAllowed, s.sudoError(SudoNotAllowed, msg)
	}
	return SudoNeedsPassword, nil
}

// find returns the sudo binary. s.mu must be held.
func (s *sudoSession) find() (string, error) {
	if s.path != "" {
		return s.path, nil
	}
	name := s.r.opts.Sudo.Path
	if name == "" {
		name = "sudo"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	s.path = path
	return path, nil
}

func (s *sudoSession) sudoError(status SudoStatus, msg string) *SudoError {
	opts := s.r.opts.Sudo
	name := opts.User
	if name == "" {
		name = currentUser()
	}
	return &SudoError{Status: status, User: name, OS: opts.OS, Message: msg}
}

// currentUser returns the name of the user maisys runs as.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	return "your user"
}

// sudoMessage returns the last line sudo wrote, which says why it failed.
func sudoMessage(res Result) string {
	lines := lastLines(res.Stderr, 1)
	if len(lines) == 0 {
		lines = lastLines(res.Stdout, 1)
	}
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}

// passwordRequired reports whether sudo's message says it needs a password.
// It knows the messages of sudo and of sudo-rs, Ubuntu's sudo since 25.10.
func passwordRequired(msg string) bool {
	for _, m := range []string{"a password is required", "a terminal is required", "interactive authentication is required"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// notAllowed reports whether sudo's message says the user may not use it.
// It knows the messages of sudo and of sudo-rs, which says so without
// asking for a password.
func notAllowed(msg string) bool {
	for _, m := range []string{"is not in the sudoers file", "may not run sudo", "is not allowed to execute", "is not allowed to run sudo", "I'm afraid I can't do that"} {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
