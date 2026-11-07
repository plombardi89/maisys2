package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHelperMessages checks how the elevated helper's messages look on the
// wire, one JSON object per line.
func TestHelperMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want string
	}{
		{
			name: "hello",
			msg:  helperReply{Hello: &helperHello{Protocol: 1, Secret: "c0ffee"}},
			want: `{"hello":{"protocol":1,"secret":"c0ffee"}}`,
		},
		{
			name: "hello that can't serve",
			msg:  helperReply{Hello: &helperHello{Protocol: 1, Secret: "c0ffee", Error: "the elevated helper isn't running as admin"}},
			want: `{"hello":{"protocol":1,"secret":"c0ffee","error":"the elevated helper isn't running as admin"}}`,
		},
		{
			name: "run",
			msg: helperRequest{Run: &helperCommand{
				Name: "wsl.exe", Args: []string{"--install", "--no-distribution"}, Dir: `C:\`, Env: []string{"WSL_UTF8=1"}, Stdin: []byte("y\n"),
			}},
			want: `{"run":{"name":"wsl.exe","args":["--install","--no-distribution"],"dir":"C:\\","env":["WSL_UTF8=1"],"stdin":"eQo="}}`,
		},
		{name: "cancel", msg: helperRequest{Cancel: true}, want: `{"cancel":true}`},
		{name: "done", msg: helperRequest{Done: true}, want: `{"done":true}`},
		{
			name: "output",
			msg:  helperReply{Output: &helperOutput{Data: []byte("Installing: Windows Subsystem for Linux\r\n")}},
			want: `{"output":{"data":"SW5zdGFsbGluZzogV2luZG93cyBTdWJzeXN0ZW0gZm9yIExpbnV4DQo="}}`,
		},
		{
			name: "error output",
			msg:  helperReply{Output: &helperOutput{Stderr: true, Data: []byte("x")}},
			want: `{"output":{"stderr":true,"data":"eA=="}}`,
		},
		{
			name: "result",
			msg:  helperReply{Result: &helperResult{Started: true, ExitCode: 3010}},
			want: `{"result":{"started":true,"exit_code":3010}}`,
		},
		{
			name: "result of a command that isn't installed",
			msg:  helperReply{Result: &helperResult{ExitCode: -1, Error: `exec: "dism.exe": executable file not found in %PATH%`, NotFound: true}},
			want: `{"result":{"started":false,"exit_code":-1,"error":"exec: \"dism.exe\": executable file not found in %PATH%","not_found":true}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := json.NewEncoder(&b).Encode(tt.msg); err != nil {
				t.Fatal(err)
			}
			if got := b.String(); got != tt.want+"\n" {
				t.Errorf("encoded:\n%s\nwant\n%s", got, tt.want)
			}
			back := newLike(tt.msg)
			if err := json.Unmarshal(b.Bytes(), back); err != nil {
				t.Fatal(err)
			}
			again, err := json.Marshal(back)
			if err != nil || string(again) != tt.want {
				t.Errorf("after decoding: %s, %v; want %s", again, err, tt.want)
			}
		})
	}
}

// newLike returns a pointer to a new zero value of msg's type.
func newLike(msg any) any {
	switch msg.(type) {
	case helperRequest:
		return &helperRequest{}
	default:
		return &helperReply{}
	}
}

func TestHelperPipeName(t *testing.T) {
	name, err := newHelperPipeName()
	if err != nil {
		t.Fatal(err)
	}
	other, err := newHelperPipeName()
	if err != nil {
		t.Fatal(err)
	}
	if name == other || checkHelperPipe(name) != nil || checkHelperPipe(other) != nil {
		t.Errorf("newHelperPipeName() = %q, then %q; want two different helper pipes", name, other)
	}
	for _, bad := range []string{
		"",
		`\\.\pipe\maisys-helper-`,
		`\\.\pipe\maisys-helper-0123456789abcdef0123456789ABCDEF`,
		`\\.\pipe\maisys-0123456789abcdef0123456789abcdef`,
		`\\server\pipe\maisys-helper-0123456789abcdef0123456789abcdef`,
		`\\.\pipe\maisys-helper-0123456789abcdef0123456789abcdef\x`,
	} {
		if err := checkHelperPipe(bad); err == nil {
			t.Errorf("checkHelperPipe(%q) = nil, want an error", bad)
		}
	}
	if err := ServeElevated(t.Context(), `\\.\pipe\elsewhere`, "secret"); err == nil {
		t.Error("the helper would serve on a pipe that isn't a helper's")
	}
}

// serving is an elevated helper served in the test, over an in-memory
// connection.
type serving struct {
	client *helperClient
	done   chan error
}

// serve starts a helper with secret, elevated or not, and connects to it,
// expecting it to say hello with "s3cret".
func serve(t *testing.T, secret string, elevated bool) (*serving, error) {
	t.Helper()
	conn, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveHelper(context.Background(), server, secret, elevated)
		_ = server.Close()
	}()
	client, err := connectHelper(conn, "s3cret", 10*time.Second)
	if err != nil {
		select {
		case serveErr := <-done:
			t.Logf("the helper stopped: %v", serveErr)
		case <-time.After(10 * time.Second):
			t.Error("the helper didn't stop after a failed hello")
		}
		return nil, err
	}
	s := &serving{client: client, done: done}
	t.Cleanup(func() { s.client.close() })
	return s, nil
}

// stopped waits for the helper to stop, and returns how it did.
func (s *serving) stopped(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the helper didn't stop")
		return nil
	}
}

// command returns a helperCommand that runs the test binary as a helper
// with args (see helper in main_test.go).
func command(t *testing.T, args ...string) helperCommand {
	t.Helper()
	c := helperCmd(t, args...)
	return helperCommand{Name: c.Name, Args: c.Args, Env: c.Env}
}

func TestHelperRuns(t *testing.T) {
	s, err := serve(t, "s3cret", true)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	res, err := s.client.run(t.Context(), command(t, "exit", "3", "out\n", "err\n"), &stdout, &stderr)
	if err != nil || !res.Started || res.ExitCode != 3 || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Errorf("run() = %+v, %v, output %q, error output %q; want exit code 3, out and err", res, err, stdout.String(), stderr.String())
	}

	c := command(t, "cat")
	c.Stdin = []byte("some input\n")
	stdout.Reset()
	if res, err := s.client.run(t.Context(), c, &stdout, nil); err != nil || res.ExitCode != 0 || stdout.String() != "some input\n" {
		t.Errorf("run(cat) = %+v, %v, output %q; want the input", res, err, stdout.String())
	}

	c = command(t, "env", "SYSTEM_TEST_VAR")
	c.Env = append(c.Env, "SYSTEM_TEST_VAR=yes")
	stdout.Reset()
	if _, err := s.client.run(t.Context(), c, &stdout, nil); err != nil || stdout.String() != "yes\n" {
		t.Errorf("run(env) output = %q, %v; want the variable it was given", stdout.String(), err)
	}

	c = command(t, "pwd")
	c.Dir = t.TempDir()
	stdout.Reset()
	if _, err := s.client.run(t.Context(), c, &stdout, nil); err != nil {
		t.Errorf("run(pwd) error = %v", err)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(stdout.String()))
	if want, _ := filepath.EvalSymlinks(c.Dir); got != want {
		t.Errorf("working directory = %q, want %q", got, want)
	}

	res, err = s.client.run(t.Context(), helperCommand{Name: "maisys-test-no-such-command"}, nil, nil)
	if err != nil || res.Started || !res.NotFound || res.ExitCode != -1 {
		t.Errorf("run() of a missing command = %+v, %v; want one that didn't start because it isn't installed", res, err)
	}
	c = command(t, "pwd")
	c.Dir = filepath.Join(t.TempDir(), "missing")
	if res, err := s.client.run(t.Context(), c, nil, nil); err != nil || res.Started || res.NotFound || res.Error == "" {
		t.Errorf("run() in a missing folder = %+v, %v; want one that didn't start, for another reason", res, err)
	}

	s.client.close()
	if err := s.stopped(t); err != nil {
		t.Errorf("the helper stopped with %v, want nil once it's done", err)
	}
	if _, err := s.client.run(t.Context(), command(t, "exit", "0", "", ""), nil, nil); !errors.Is(err, errHelperClosed) {
		t.Errorf("run() after close() error = %v, want errHelperClosed", err)
	}
}

func TestHelperHello(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		elevated bool
		want     string
	}{
		{name: "the wrong secret", secret: "guess", elevated: true, want: "it didn't know the secret it was started with"},
		{name: "not elevated", secret: "s3cret", want: "the elevated helper isn't running as admin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := serve(t, tt.secret, tt.elevated)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("connectHelper() error = %v, want one saying %q", err, tt.want)
			}
		})
	}
}

// fakeHelper answers on conn as an elevated helper that says hello with
// hello, if it isn't nil, and then reads requests until conn closes.
func fakeHelper(conn net.Conn, hello *helperReply) {
	if hello != nil {
		if err := json.NewEncoder(conn).Encode(hello); err != nil {
			return
		}
	}
	_, _ = io.Copy(io.Discard, conn)
}

func TestHelperHelloFails(t *testing.T) {
	tests := []struct {
		name  string
		hello *helperReply
		want  string
	}{
		{name: "no hello", want: "it didn't answer within"},
		{name: "another message", hello: &helperReply{Result: &helperResult{}}, want: "it didn't say hello"},
		{name: "another protocol", hello: &helperReply{Hello: &helperHello{Protocol: 2, Secret: "s3cret"}}, want: "it speaks version 2 of the protocol, not 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, server := net.Pipe()
			go fakeHelper(server, tt.hello)
			_, err := connectHelper(conn, "s3cret", 200*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("connectHelper() error = %v, want one saying %q", err, tt.want)
			}
			if _, err := conn.Write([]byte("x")); err == nil {
				t.Error("the connection wasn't closed")
			}
		})
	}
}

func TestHelperCancel(t *testing.T) {
	s, err := serve(t, "s3cret", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := s.client.run(ctx, command(t, "sleep"), nil, nil)
	if err != nil || !res.Started || res.ExitCode == 0 {
		t.Errorf("run() of a command that's stopped = %+v, %v; want a result for a command that was stopped", res, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("stopping the command took %s", took)
	}
	// The helper carries on.
	var stdout bytes.Buffer
	if res, err := s.client.run(t.Context(), command(t, "exit", "0", "again", ""), &stdout, nil); err != nil || res.ExitCode != 0 || stdout.String() != "again" {
		t.Errorf("run() after a stopped command = %+v, %v, output %q; want it to work", res, err, stdout.String())
	}
}

// TestHelperConnectionLost closes the connection while the helper runs a
// command: the helper stops it and exits, and maisys fails the command,
// and every command after it.
func TestHelperConnectionLost(t *testing.T) {
	s, err := serve(t, "s3cret", true)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, func() { _ = s.client.conn.Close() })
	start := time.Now()
	if _, err := s.client.run(t.Context(), command(t, "sleep"), nil, nil); err == nil {
		t.Error("run() with the connection closed: no error")
	}
	if err := s.stopped(t); err == nil {
		t.Error("the helper stopped without an error, after its connection broke")
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("the helper took %s to stop the command and exit", took)
	}
	if _, err := s.client.run(t.Context(), command(t, "exit", "0", "", ""), nil, nil); err == nil {
		t.Error("run() after the connection was lost: no error")
	}
}

// testAdmin returns a runner with an admin session, as on Windows, whose
// elevated helper runs in the test. launches counts the helpers started.
func testAdmin(t *testing.T, opts Options, elevated bool) (r *ExecRunner, launches *atomic.Int32) {
	t.Helper()
	r = NewRunner(opts)
	launches = &atomic.Int32{}
	r.admin = &adminSession{r: r, elevated: elevated, launch: func(_ context.Context, secret string) (*helperProcess, error) {
		launches.Add(1)
		conn, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = serveHelper(context.Background(), server, secret, true)
			_ = server.Close()
		}()
		return &helperProcess{conn: conn, wait: func(timeout time.Duration) error {
			select {
			case <-done:
				return nil
			case <-time.After(timeout):
				return errors.New("the helper didn't exit")
			}
		}}, nil
	}}
	t.Cleanup(r.StopSudo)
	return r, launches
}

// TestAdminRouting checks that a runner with an admin session runs
// commands as admin in the elevated helper once it's started, and refuses
// them before, without starting it.
func TestAdminRouting(t *testing.T) {
	var log, verbose lockedBuffer
	r, launches := testAdmin(t, Options{Log: &log, Verbose: &verbose}, false)
	root := helperCmd(t, "exit", "0", "as admin\n", "")
	root.Root, root.Label = true, "installing WSL"

	// Planning.
	if status, err := r.CheckSudo(t.Context()); status != SudoNeedsPassword || err != nil {
		t.Errorf("CheckSudo() = %q, %v; want SudoNeedsPassword, as the UAC prompt is needed", status, err)
	}
	_, err := r.Run(t.Context(), root)
	if !errors.Is(err, ErrNeedsAdmin) || !errors.Is(err, ErrNeedsPassword) || !strings.HasPrefix(err.Error(), "installing WSL: ") {
		t.Errorf("Run() as admin while planning: error = %v, want ErrNeedsAdmin, which is an ErrNeedsPassword, naming the command", err)
	}
	if n := launches.Load(); n != 0 {
		t.Fatalf("planning started the helper %d times", n)
	}

	// Applying.
	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatalf("StartSudo() error = %v", err)
	}
	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatalf("StartSudo() a second time: error = %v", err)
	}
	if n := launches.Load(); n != 1 {
		t.Errorf("the helper was started %d times, want once", n)
	}
	if status, err := r.CheckSudo(t.Context()); status != SudoNoPassword || err != nil {
		t.Errorf("CheckSudo() with the helper running = %q, %v; want SudoNoPassword", status, err)
	}
	res, err := r.Run(t.Context(), root)
	if err != nil || res.ExitCode != 0 || string(res.Stdout) != "as admin\n" {
		t.Errorf("Run() as admin = %+v, %v; want its output", res, err)
	}
	user := helperCmd(t, "exit", "0", "as the user\n", "")
	if res, err := r.Run(t.Context(), user); err != nil || string(res.Stdout) != "as the user\n" {
		t.Errorf("Run() as the user = %+v, %v", res, err)
	}

	failing := helperCmd(t, "exit", "103", "", "Installation failed.\n")
	failing.Root = true
	_, err = r.Run(t.Context(), failing)
	if ee, ok := errors.AsType[*ExitError](err); !ok || ee.Code != 103 || ee.Command != failing.String() || !slices.Equal(ee.Tail, []string{"Installation failed."}) {
		t.Errorf("Run() as admin of a failing command: error = %#v, want an *ExitError for it", err)
	}
	_, err = r.Run(t.Context(), Cmd{Name: "maisys-test-no-such-command", Root: true, Label: "testing"})
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "testing: maisys-test-no-such-command isn't installed") {
		t.Errorf("Run() as admin of a missing command: error = %v, want exec.ErrNotFound saying it isn't installed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Run(ctx, root); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() as admin with a cancelled context: error = %v, want context.Canceled", err)
	}

	r.StopSudo()
	if _, err := r.Run(t.Context(), root); !errors.Is(err, ErrNeedsAdmin) {
		t.Errorf("Run() as admin after StopSudo: error = %v, want ErrNeedsAdmin", err)
	}

	logged := log.buf.String()
	wantLog := regexp.MustCompile(`\$ ` + regexp.QuoteMeta(root.String()) + `   \(as admin\)\nas admin\n\d\d:\d\d:\d\d exit code 0`)
	if !wantLog.MatchString(logged) {
		t.Errorf("log doesn't show the command run as admin, with its output:\n%s", logged)
	}
	if !strings.Contains(logged, "$ "+user.String()+"\n") {
		t.Errorf("log doesn't show the command run as the user:\n%s", logged)
	}
	if want := "$ " + root.String() + "   (as admin)\nas admin\n"; !strings.Contains(verbose.buf.String(), want) {
		t.Errorf("verbose output doesn't stream the command run as admin:\n%s", verbose.buf.String())
	}
}

// TestAdminElevated checks that a runner that's elevated already runs
// commands as admin itself, without the helper.
func TestAdminElevated(t *testing.T) {
	r, launches := testAdmin(t, Options{}, true)
	if status, err := r.CheckSudo(t.Context()); status != SudoNoPassword || err != nil {
		t.Errorf("CheckSudo() = %q, %v; want SudoNoPassword", status, err)
	}
	c := helperCmd(t, "exit", "0", "direct\n", "")
	c.Root = true
	if res, err := r.Run(t.Context(), c); err != nil || string(res.Stdout) != "direct\n" {
		t.Errorf("Run() as admin while planning = %+v, %v; want it run directly", res, err)
	}
	if err := r.StartSudo(t.Context()); err != nil {
		t.Errorf("StartSudo() error = %v", err)
	}
	r.StopSudo()
	if n := launches.Load(); n != 0 {
		t.Errorf("the helper was started %d times, want never", n)
	}
}

func TestAdminStartFails(t *testing.T) {
	tests := []struct {
		name   string
		launch func(ctx context.Context, secret string) (*helperProcess, error)
		want   string
	}{
		{
			name:   "the UAC prompt is declined",
			launch: func(context.Context, string) (*helperProcess, error) { return nil, errUACDeclined },
			want:   errUACDeclined.Error(),
		},
		{
			name: "an impostor",
			launch: func(_ context.Context, secret string) (*helperProcess, error) {
				conn, server := net.Pipe()
				go fakeHelper(server, &helperReply{Hello: &helperHello{Protocol: helperProtocol, Secret: secret + "x"}})
				return &helperProcess{conn: conn, wait: func(time.Duration) error { return nil }}, nil
			},
			want: "couldn't start the elevated helper: it didn't know the secret it was started with",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner(Options{})
			r.admin = &adminSession{r: r, launch: tt.launch}
			err := r.StartSudo(t.Context())
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("StartSudo() error = %v, want %q", err, tt.want)
			}
			c := helperCmd(t, "exit", "0", "", "")
			c.Root = true
			if _, err := r.Run(t.Context(), c); !errors.Is(err, ErrNeedsAdmin) {
				t.Errorf("Run() as admin after StartSudo failed: error = %v, want ErrNeedsAdmin", err)
			}
			r.StopSudo()
		})
	}
}

// TestAdminConcurrent runs commands as admin at the same time: the helper
// runs them one at a time, and each gets its own output.
func TestAdminConcurrent(t *testing.T) {
	r, _ := testAdmin(t, Options{}, false)
	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			out := strings.Repeat(string(rune('a'+i)), 10)
			c := helperCmd(t, "exit", "0", out, "")
			c.Root = true
			if res, err := r.Run(t.Context(), c); err != nil || string(res.Stdout) != out {
				t.Errorf("Run() %d = %q, %v; want %q", i, res.Stdout, err, out)
			}
		})
	}
	wg.Wait()
}
