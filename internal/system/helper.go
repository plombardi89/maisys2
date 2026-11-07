package system

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"time"
)

// HelperCommand is the hidden maisys command that runs the elevated helper
// on Windows (see ServeElevated).
const HelperCommand = "__elevated-helper"

// helperPipePrefix starts the name of every elevated helper's pipe. The
// rest is random.
const helperPipePrefix = `\\.\pipe\maisys-helper-`

// helperPipe matches the names of the elevated helper's pipes.
var helperPipe = regexp.MustCompile(`^` + regexp.QuoteMeta(helperPipePrefix) + `[0-9a-f]{32}$`)

// newHelperPipeName returns a new random name for an elevated helper's
// pipe.
func newHelperPipeName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return helperPipePrefix + hex.EncodeToString(b), nil
}

// checkHelperPipe checks that name is an elevated helper's pipe, so the
// helper can't be pointed at any other.
func checkHelperPipe(name string) error {
	if !helperPipe.MatchString(name) {
		return fmt.Errorf("%q isn't the name of an elevated helper's pipe", name)
	}
	return nil
}

// The elevated helper's protocol. maisys starts the helper elevated, with
// the name of a pipe and a one-time secret on its command line, and the
// helper connects to the pipe. Each side then sends JSON messages, one per
// line:
//
//   - the helper says hello: the protocol version and the secret, which
//     shows it's the helper maisys started, or why it can't serve;
//   - maisys sends a command to run (a helperCommand), and the helper runs
//     it, sending its output as it comes (helperOutput), then what it did
//     (a helperResult). One command runs at a time;
//   - while a command runs, maisys may ask the helper to stop it;
//   - maisys says it's done, or closes the pipe, and the helper exits,
//     stopping the command that's running, if there is one.
//
// The helper runs nothing but the commands it's sent on its own pipe.

// helperProtocol is the version of the elevated helper's protocol. maisys
// only starts the helper from its own executable, so both ends always have
// the same version; it's checked all the same.
const helperProtocol = 1

// How long maisys waits for the elevated helper.
const (
	// helperHelloWait is how long the helper has to say hello once it's
	// connected.
	helperHelloWait = 30 * time.Second
	// helperCancelWait is how long a command the helper was asked to stop
	// has to stop, before maisys gives up on the helper.
	helperCancelWait = 15 * time.Second
	// helperExitWait is how long the helper has to exit once maisys is
	// done with it.
	helperExitWait = 10 * time.Second
)

// helperRequest is a message from maisys to the elevated helper. One of its
// fields is set.
type helperRequest struct {
	// Run is a command to run.
	Run *helperCommand `json:"run,omitempty"`
	// Cancel asks the helper to stop the command that's running.
	Cancel bool `json:"cancel,omitempty"`
	// Done tells the helper to exit.
	Done bool `json:"done,omitempty"`
}

// helperCommand is a command for the elevated helper to run.
type helperCommand struct {
	// Name is the program, looked up in the helper's PATH unless it's a
	// path.
	Name string `json:"name"`
	// Args are its arguments.
	Args []string `json:"args,omitempty"`
	// Dir is its working directory. Empty means the helper's.
	Dir string `json:"dir,omitempty"`
	// Env is extra environment, as NAME=value, on top of the helper's own
	// and LC_ALL=C.
	Env []string `json:"env,omitempty"`
	// Stdin is its input.
	Stdin []byte `json:"stdin,omitempty"`
}

// helperReply is a message from the elevated helper to maisys. One of its
// fields is set.
type helperReply struct {
	// Hello is the helper's first message.
	Hello *helperHello `json:"hello,omitempty"`
	// Output is some of the running command's output.
	Output *helperOutput `json:"output,omitempty"`
	// Result is what the command did, once it's finished.
	Result *helperResult `json:"result,omitempty"`
}

// helperHello is the elevated helper's first message.
type helperHello struct {
	// Protocol is the helper's protocol version.
	Protocol int `json:"protocol"`
	// Secret is the secret from its command line.
	Secret string `json:"secret"`
	// Error, if set, is why the helper can't serve, such as not running
	// as admin. It exits after saying so.
	Error string `json:"error,omitempty"`
}

// helperOutput is some of a command's output.
type helperOutput struct {
	// Stderr is set for error output.
	Stderr bool `json:"stderr,omitempty"`
	// Data is the output.
	Data []byte `json:"data"`
}

// helperResult is what a command the elevated helper ran did.
type helperResult struct {
	// Started reports whether the command started.
	Started bool `json:"started"`
	// ExitCode is its exit code, or -1 if it didn't start or was stopped.
	ExitCode int `json:"exit_code"`
	// Error is why the command didn't start.
	Error string `json:"error,omitempty"`
	// NotFound is set if it didn't start because the program isn't
	// installed.
	NotFound bool `json:"not_found,omitempty"`
}

// errHelperClosed is the error for running a command after the connection
// to the elevated helper is closed.
var errHelperClosed = errors.New("the elevated helper has stopped")

// helperClient is maisys's end of the connection to the elevated helper.
// It runs one command at a time.
type helperClient struct {
	conn io.ReadWriteCloser
	dec  *json.Decoder

	// mu is held while a command runs, and guards err.
	mu sync.Mutex
	// err is why the connection can't be used any more, once it can't.
	err error

	// sendMu guards enc and running.
	sendMu sync.Mutex
	enc    *json.Encoder
	// running reports whether a command is running, which only then may
	// be asked to stop.
	running bool
}

// connectHelper waits for the elevated helper's hello on conn, for up to
// wait, and checks that it's the helper maisys started: that it has the
// same protocol version and knows secret. It returns the client for the
// connection, or closes conn.
func connectHelper(conn io.ReadWriteCloser, secret string, wait time.Duration) (*helperClient, error) {
	c := &helperClient{conn: conn, dec: json.NewDecoder(conn), enc: json.NewEncoder(conn)}
	timer := time.AfterFunc(wait, func() { _ = conn.Close() })
	var reply helperReply
	err := c.dec.Decode(&reply)
	if !timer.Stop() {
		err = fmt.Errorf("it didn't answer within %s", wait)
	}
	hello := reply.Hello
	switch {
	case err != nil:
	case hello == nil:
		err = errors.New("it didn't say hello")
	case subtle.ConstantTimeCompare([]byte(hello.Secret), []byte(secret)) != 1:
		err = errors.New("it didn't know the secret it was started with, so it isn't the helper maisys started")
	case hello.Protocol != helperProtocol:
		err = fmt.Errorf("it speaks version %d of the protocol, not %d", hello.Protocol, helperProtocol)
	case hello.Error != "":
		err = errors.New(hello.Error)
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

// send sends a request to the helper.
func (c *helperClient) send(req helperRequest) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.enc.Encode(req)
}

// run has the helper run cmd, and returns what it did. The command's output
// goes to stdout and stderr as it comes. If ctx is cancelled, the helper is
// asked to stop the command; if it doesn't within helperCancelWait, maisys
// gives up on the helper. An error means the helper is gone: every later
// command fails with it too.
func (c *helperClient) run(ctx context.Context, cmd helperCommand, stdout, stderr io.Writer) (helperResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return helperResult{ExitCode: -1}, c.err
	}

	finished := make(chan struct{})
	defer close(finished)
	stop := context.AfterFunc(ctx, func() {
		if !c.cancel() {
			return
		}
		select {
		case <-finished:
		case <-time.After(helperCancelWait):
			_ = c.conn.Close()
		}
	})
	defer stop()

	c.sendMu.Lock()
	err := c.enc.Encode(helperRequest{Run: &cmd})
	c.running = err == nil
	c.sendMu.Unlock()
	if err != nil {
		return c.fail(err)
	}
	// Once the command's finished, a late cancel mustn't stop the next one.
	defer func() {
		c.sendMu.Lock()
		c.running = false
		c.sendMu.Unlock()
	}()
	for {
		var reply helperReply
		if err := c.dec.Decode(&reply); err != nil {
			return c.fail(err)
		}
		switch {
		case reply.Output != nil:
			w := stdout
			if reply.Output.Stderr {
				w = stderr
			}
			if w != nil {
				_, _ = w.Write(reply.Output.Data)
			}
		case reply.Result != nil:
			return *reply.Result, nil
		default:
			return c.fail(errors.New("it sent a message maisys doesn't understand"))
		}
	}
}

// cancel asks the helper to stop the command that's running, if one is,
// and reports whether it asked.
func (c *helperClient) cancel() bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.running && c.enc.Encode(helperRequest{Cancel: true}) == nil
}

// fail records that the connection failed with err, closes it, and returns
// the error for the command that was running. c.mu must be held.
func (c *helperClient) fail(err error) (helperResult, error) {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, os.ErrClosed) {
		c.err = errHelperClosed
	} else {
		c.err = fmt.Errorf("lost the connection to the elevated helper: %w", err)
	}
	_ = c.conn.Close()
	return helperResult{ExitCode: -1}, c.err
}

// close tells the helper it's done, and closes the connection.
func (c *helperClient) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		timer := time.AfterFunc(helperExitWait, func() { _ = c.conn.Close() })
		_ = c.send(helperRequest{Done: true})
		timer.Stop()
		c.err = errHelperClosed
	}
	_ = c.conn.Close()
}

// helperServer is the elevated helper's end of the connection.
type helperServer struct {
	mu  sync.Mutex
	enc *json.Encoder
}

// send sends a reply to maisys.
func (s *helperServer) send(reply helperReply) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(reply)
}

// helperJob is a request the helper's reader passes on to be carried out.
type helperJob struct {
	cmd    *helperCommand // nil for Done
	ctx    context.Context
	cancel context.CancelFunc
}

// serveHelper is the elevated helper: it says hello on conn with secret,
// then runs the commands maisys sends, one at a time, until maisys says
// it's done or closes conn. If the helper isn't elevated, it only says so.
func serveHelper(ctx context.Context, conn io.ReadWriter, secret string, elevated bool) error {
	s := &helperServer{enc: json.NewEncoder(conn)}
	hello := &helperHello{Protocol: helperProtocol, Secret: secret}
	if !elevated {
		hello.Error = "the elevated helper isn't running as admin"
	}
	if err := s.send(helperReply{Hello: hello}); err != nil {
		return err
	}
	if hello.Error != "" {
		return errors.New(hello.Error)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan helperJob)
	readErr := make(chan error, 1)
	go s.read(ctx, cancel, conn, jobs, readErr)
	for job := range jobs {
		if job.cmd == nil {
			return nil
		}
		res := runHelperCommand(job.ctx, *job.cmd, helperWriter{s: s}, helperWriter{s: s, stderr: true})
		job.cancel()
		if err := s.send(helperReply{Result: &res}); err != nil {
			return err
		}
	}
	if err := <-readErr; !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// read reads maisys's requests from r, passing them on to jobs, which it
// closes when it stops, and stopping the command that's running when it's
// asked to. When r fails or closes, it stops every command with stop, and
// sends the error to errc.
func (s *helperServer) read(ctx context.Context, stop context.CancelFunc, r io.Reader, jobs chan<- helperJob, errc chan<- error) {
	defer close(jobs)
	dec := json.NewDecoder(r)
	cancelRunning := func() {}
	for {
		var req helperRequest
		err := dec.Decode(&req)
		switch {
		case err != nil:
		case req.Cancel:
			cancelRunning()
			continue
		case req.Done:
			errc <- io.EOF
			select {
			case jobs <- helperJob{}:
			case <-ctx.Done():
			}
			return
		case req.Run != nil:
			jobCtx, cancel := context.WithCancel(ctx)
			cancelRunning = cancel
			select {
			case jobs <- helperJob{cmd: req.Run, ctx: jobCtx, cancel: cancel}:
				continue
			case <-ctx.Done():
				cancel()
				err = ctx.Err()
			}
		default:
			err = errors.New("maisys sent a request the helper doesn't understand")
		}
		stop()
		errc <- err
		return
	}
}

// helperWriter sends a command's output to maisys.
type helperWriter struct {
	s      *helperServer
	stderr bool
}

func (w helperWriter) Write(p []byte) (int, error) {
	if err := w.s.send(helperReply{Output: &helperOutput{Stderr: w.stderr, Data: p}}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// runHelperCommand runs c for the elevated helper, as the helper's user,
// with its output going to stdout and stderr.
func runHelperCommand(ctx context.Context, c helperCommand, stdout, stderr io.Writer) helperResult {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	cmd.Env = append(append(os.Environ(), "LC_ALL=C"), c.Env...)
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = procAttr(false, false)
	cmd.Cancel = func() error { return interrupt(cmd.Process) }
	cmd.WaitDelay = 10 * time.Second

	err := checkDir(c.Dir)
	if err == nil {
		err = cmd.Run()
	}
	if cmd.ProcessState == nil {
		return helperResult{ExitCode: -1, Error: err.Error(), NotFound: notInstalled(err)}
	}
	return helperResult{Started: true, ExitCode: cmd.ProcessState.ExitCode()}
}
