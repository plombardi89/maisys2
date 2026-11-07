package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// ErrNeedsAdmin means a command has to run as admin on Windows, but maisys
// isn't elevated and the elevated helper, which only applying starts, isn't
// running. Plans never start it, so they show such checks as checked during
// apply. It's an ErrNeedsPassword too (errors.Is), so code that handles
// sudo's case handles this one.
var ErrNeedsAdmin error = needsAdminError{}

type needsAdminError struct{}

func (needsAdminError) Error() string {
	return "this needs admin rights, which maisys only asks for while applying"
}

func (needsAdminError) Is(target error) bool {
	return target == ErrNeedsPassword
}

// errUACDeclined is the error for a UAC prompt that was declined.
var errUACDeclined = errors.New("the UAC prompt was declined, so the changes that need admin rights can't be made: " +
	"run maisys sync again and choose Yes")

// adminSession runs an ExecRunner's commands that need admin rights on
// Windows: directly if maisys is elevated already, and otherwise in the
// elevated helper, a copy of maisys started elevated, which StartSudo
// starts and StopSudo stops, so a sync shows one UAC prompt. Like sudo's
// session, it never prompts while planning: until the helper is started,
// those commands fail with ErrNeedsAdmin.
//
// If you aren't an admin and type an admin's credentials at the UAC prompt,
// the helper runs as that admin, not as you. So it only runs the commands
// it's sent, with the paths it's given, and those commands mustn't touch
// your own files or settings (your profile, %APPDATA% or HKEY_CURRENT_USER),
// which maisys changes unelevated.
type adminSession struct {
	r *ExecRunner
	// elevated reports whether maisys is running elevated, so it runs the
	// commands itself.
	elevated bool
	// launch starts the elevated helper, which shows the UAC prompt, with
	// secret, which it says hello with.
	launch func(ctx context.Context, secret string) (*helperProcess, error)

	mu     sync.Mutex
	helper *helperProcess
	client *helperClient
}

// helperProcess is an elevated helper that's been started and has
// connected.
type helperProcess struct {
	// conn is the connection to it.
	conn io.ReadWriteCloser
	// wait waits for up to timeout for the helper to exit, once its
	// connection is closed, and releases it.
	wait func(timeout time.Duration) error
}

// check reports whether commands can run as admin now, or need the UAC
// prompt.
func (s *adminSession) check() SudoStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.elevated || s.client != nil {
		return SudoNoPassword
	}
	return SudoNeedsPassword
}

// start starts the elevated helper, unless maisys is elevated or it's
// started already.
func (s *adminSession) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.elevated || s.client != nil {
		return nil
	}
	secret, err := newSecret()
	if err != nil {
		return fmt.Errorf("couldn't start the elevated helper: %w", err)
	}
	p, err := s.launch(ctx, secret)
	if err != nil {
		return err
	}
	client, err := connectHelper(p.conn, secret, helperHelloWait)
	if err != nil {
		_ = p.wait(helperExitWait)
		return fmt.Errorf("couldn't start the elevated helper: %w", err)
	}
	s.helper, s.client = p, client
	return nil
}

// stop stops the elevated helper, if it's running.
func (s *adminSession) stop() {
	s.mu.Lock()
	p, client := s.helper, s.client
	s.helper, s.client = nil, nil
	s.mu.Unlock()
	if client == nil {
		return
	}
	client.close()
	_ = p.wait(helperExitWait)
}

// run runs c as admin: directly if maisys is elevated, in the elevated
// helper if it's started, and otherwise not at all, failing with
// ErrNeedsAdmin.
func (s *adminSession) run(ctx context.Context, c Cmd) (Result, error) {
	if s.elevated {
		c.Root = false
		return s.r.run(ctx, c, nil)
	}
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return Result{ExitCode: -1}, fmt.Errorf("%s: %w", c.what(), ErrNeedsAdmin)
	}
	return s.r.runAdmin(ctx, client, c)
}

// runAdmin runs c in the elevated helper, as run runs a command as the
// user: its output is streamed with Verbose, and logged, marked as run as
// admin.
func (r *ExecRunner) runAdmin(ctx context.Context, client *helperClient, c Cmd) (Result, error) {
	line := c.String() + "   (as admin)"
	req := helperCommand{Name: c.Name, Args: c.Args, Dir: c.Dir, Env: c.Env}
	start := time.Now()
	if c.Stdin != nil {
		data, err := io.ReadAll(c.Stdin)
		if err != nil {
			r.logCommand(start, line, nil, "didn't start: couldn't read its input: "+err.Error())
			return Result{ExitCode: -1}, fmt.Errorf("%s: reading its input: %w", c.what(), err)
		}
		req.Stdin = data
	}
	if err := ctx.Err(); err != nil {
		r.logCommand(start, line, nil, "didn't start: "+err.Error())
		return Result{ExitCode: -1}, fmt.Errorf("%s: %w", c.what(), err)
	}

	out := r.newOutput(line, c.Stream)
	stdout, stderr := out.writers()
	hr, err := client.run(ctx, req, stdout, stderr)
	out.flush()
	res := Result{ExitCode: hr.ExitCode, Stdout: out.stdout.Bytes(), Stderr: out.stderr.Bytes()}

	var outcome string
	switch {
	case err != nil:
		res.ExitCode = -1
		outcome = "didn't finish: " + err.Error()
		err = fmt.Errorf("%s: %w", c.what(), err)
	case !hr.Started:
		res.ExitCode = -1
		outcome = "didn't start: " + hr.Error
		if hr.NotFound {
			err = fmt.Errorf("%s: %s isn't installed: %w", c.what(), c.Name, exec.ErrNotFound)
		} else {
			err = fmt.Errorf("%s: %s", c.what(), hr.Error)
		}
	case ctx.Err() != nil:
		outcome = fmt.Sprintf("stopped (%v), exit code %d", ctx.Err(), res.ExitCode)
		err = fmt.Errorf("%s: %w", c.what(), ctx.Err())
	default:
		outcome = fmt.Sprintf("exit code %d", res.ExitCode)
		err = CheckExit(c, res)
	}
	r.logCommand(start, line, out.combined.Bytes(), outcome)
	return res, err
}

// newSecret returns a random one-time secret for the elevated helper.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
