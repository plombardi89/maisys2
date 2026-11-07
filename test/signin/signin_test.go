//go:build linux

// The test only runs on Linux, where the prompt's test types the token into
// a pseudo-terminal once maisys has turned its echo off, which it checks
// with Linux's TCGETS.

package signin_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/test/fixture"
)

// The private fixture repo's HTTPS URL, and a read-only access token for
// it. CI sets them from the repo variable and the repo secret of the same
// names.
const (
	urlEnv   = "MAISYS_FIXTURE_URL"
	tokenEnv = "MAISYS_FIXTURE_TOKEN"
)

// TestSignIn has maisys clone a private copy of the end-to-end tests'
// fixture over HTTPS, as the first sync on a machine does, each time in a
// home folder of its own, with no git credential helper:
//
//   - without a token, the clone must fail to sign in, so the repo really
//     is private;
//   - with the token in MAISYS_CONFIG_TOKEN, it must clone, and a later
//     sync must pull, without the token being written anywhere: a sync
//     without it must fail to pull;
//   - in a terminal, maisys must ask for a token. The token typed in must
//     be saved in ~/.config/maisys/git-credentials, readable only by the
//     user, and the clone set up to use it, so a later sync without a
//     terminal pulls with it.
//
// Each sync is a dry run of the files module (--only files) for the
// fixture's test machine for this system, so the test changes nothing
// outside its home folders. It only runs when MAISYS_FIXTURE_URL and
// MAISYS_FIXTURE_TOKEN are set; make signin-test runs it.
func TestSignIn(t *testing.T) {
	repoURL, token := os.Getenv(urlEnv), os.Getenv(tokenEnv)
	if repoURL == "" || token == "" {
		t.Skipf("set %s to the HTTPS URL of a private copy of the fixture (go run ./test/cmd/fixture DIR builds it), and %s to a read-only access token for it", urlEnv, tokenEnv)
	}
	u, err := url.Parse(repoURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		t.Fatalf("%s is %q: it must be an HTTPS URL, without credentials", urlEnv, repoURL)
	}
	ctx := testContext(t)
	s := &signIn{maisys: buildMaisys(ctx, t), url: repoURL, host: u.Host, token: token, machine: liveMachine(t)}
	first := []string{"sync", s.url, "--machine", s.machine, "--dry-run", "--only", "files"}
	later := []string{"sync", "--dry-run", "--only", "files"}

	t.Run("without a token", func(t *testing.T) {
		home := t.TempDir()
		out, code := s.run(ctx, t, home, nil, first...)
		if code != 1 || !strings.Contains(out, "couldn't sign in") {
			t.Errorf("maisys sync without a token exited with %d, want it to fail to sign in: is %s private?", code, s.url)
		}
	})

	t.Run("MAISYS_CONFIG_TOKEN", func(t *testing.T) {
		home := t.TempDir()
		withToken := []string{"MAISYS_CONFIG_TOKEN=" + s.token}
		s.dryRun(ctx, t, home, withToken, first...)
		s.checkCloned(t, home)
		if _, err := os.Stat(credentialsFile(home)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("with MAISYS_CONFIG_TOKEN, maisys saved credentials in %s (%v), want none", credentialsFile(home), err)
		}
		if helpers := s.credentialHelpers(ctx, t, home); len(helpers) > 0 {
			t.Errorf("with MAISYS_CONFIG_TOKEN, maisys set up the clone's credential helpers %q, want none", helpers)
		}
		s.checkTokenSaved(t, home, "")

		s.dryRun(ctx, t, home, withToken, later...)
		out, code := s.run(ctx, t, home, nil, later...)
		if code != 1 || !strings.Contains(out, "couldn't sign in") {
			t.Errorf("maisys sync without MAISYS_CONFIG_TOKEN exited with %d, want it to fail to pull: nothing should have kept the token", code)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		home := t.TempDir()
		_, code := s.runInTerminal(ctx, t, home, first...)
		checkDryRun(t, code)
		s.checkCloned(t, home)

		file := credentialsFile(home)
		info, err := os.Stat(file)
		if err != nil {
			t.Fatalf("maisys didn't save the token: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %04o, want 0600", file, perm)
		}
		// git's store helper rewrites the file, percent-encoding a port's
		// colon.
		data, err := os.ReadFile(file)
		hosts := []string{"@" + s.host + "\n", "@" + strings.ReplaceAll(s.host, ":", "%3a") + "\n"}
		if err != nil || !strings.Contains(string(data), hosts[0]) && !strings.Contains(string(data), hosts[1]) {
			t.Errorf("%s has no credentials for %s (%v)", file, s.host, err)
		}
		s.checkTokenSaved(t, home, file)
		want := []string{"", "store --file '" + file + "'"}
		if helpers := s.credentialHelpers(ctx, t, home); !slices.Equal(helpers, want) {
			t.Errorf("the clone's credential helpers are %q, want %q", helpers, want)
		}

		// Without a terminal, maisys can't ask again.
		s.dryRun(ctx, t, home, nil, later...)
	})
}

// signIn runs maisys for TestSignIn.
type signIn struct {
	maisys  string
	url     string
	host    string
	token   string
	machine string
}

// env is the environment maisys runs with: this test's, with home as the
// home folder, and without what would give maisys or git other settings,
// credentials or tokens, plus extra.
func (s *signIn) env(home string, extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "HOME", name == "NO_COLOR", name == "GH_TOKEN", name == "GITHUB_TOKEN",
			strings.HasPrefix(name, "XDG_"), strings.HasPrefix(name, "MAISYS_"), strings.HasPrefix(name, "GIT_CONFIG"):
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "NO_COLOR=1"), extra...)
}

// run runs maisys without a terminal, with home as its home folder and env
// added to its environment, and returns its output and exit code.
func (s *signIn) run(ctx context.Context, t *testing.T, home string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.CommandContext(ctx, s.maisys, args...)
	cmd.Dir = home
	cmd.Env = s.env(home, env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	code := exitCode(t, cmd.Run())
	return s.show(t, args, out.String(), code), code
}

// dryRun runs maisys as run does, and checks that its dry run worked.
func (s *signIn) dryRun(ctx context.Context, t *testing.T, home string, env []string, args ...string) {
	t.Helper()
	_, code := s.run(ctx, t, home, env, args...)
	checkDryRun(t, code)
}

// checkDryRun fails the test unless a dry run exited with 0 (nothing to
// change) or 2 (changes pending).
func checkDryRun(t *testing.T, code int) {
	t.Helper()
	if code != 0 && code != 2 {
		t.Fatalf("maisys sync --dry-run exited with %d, want 0 or 2", code)
	}
}

// runInTerminal runs maisys in a pseudo-terminal, with home as its home
// folder, and types the token when it asks for one. It returns maisys's
// output and exit code.
func (s *signIn) runInTerminal(ctx context.Context, t *testing.T, home string, args ...string) (string, int) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("no pseudo-terminal: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	var out lockedBuffer
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, ptmx)
		close(copied)
	}()

	cmd := exec.CommandContext(ctx, s.maisys, args...)
	cmd.Dir = home
	cmd.Env = s.env(home)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		_ = tty.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	// Type the token once maisys asks for it, and the terminal no longer
	// shows what's typed.
	typed := false
	timeout := time.After(5 * time.Minute)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var waitErr error
wait:
	for {
		select {
		case waitErr = <-done:
			break wait
		case <-timeout:
			_ = cmd.Process.Kill()
			waitErr = <-done
			t.Error("maisys didn't finish after 5 minutes")
			break wait
		case <-tick.C:
			if !typed && strings.Contains(out.String(), "Token: ") && !echoOn(t, tty) {
				if _, err := ptmx.WriteString(s.token + "\r"); err != nil {
					t.Fatal(err)
				}
				typed = true
			}
		}
	}

	// Closing the terminal ends the copy, now maisys has exited.
	_ = tty.Close()
	select {
	case <-copied:
	case <-time.After(10 * time.Second):
		t.Error("the pseudo-terminal didn't close after 10s")
	}
	code := exitCode(t, waitErr)
	shown := s.show(t, args, out.String(), code)
	if !typed {
		t.Fatal("maisys didn't ask for a token")
	}
	return shown, code
}

// show logs what maisys showed, and fails the test if that includes the
// token, which it hides in the log.
func (s *signIn) show(t *testing.T, args []string, out string, code int) string {
	t.Helper()
	if strings.Contains(out, s.token) {
		t.Error("maisys showed the token")
		out = strings.ReplaceAll(out, s.token, "[the token]")
	}
	t.Logf("maisys %s: exit code %d\n%s", strings.Join(args, " "), code, out)
	return out
}

// checkCloned checks that the config repo was cloned where maisys clones it
// by default.
func (s *signIn) checkCloned(t *testing.T, home string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(home, "maisys-config", "maisys.toml")); err != nil {
		t.Fatalf("maisys didn't clone the config repo: %v", err)
	}
}

// checkTokenSaved fails the test if the token is in any file in home other
// than saved.
func (s *signIn) checkTokenSaved(t *testing.T, home, saved string) {
	t.Helper()
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || path == saved {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(s.token)) {
			t.Errorf("the token was written to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// credentialHelpers returns the credential helpers set in the clone's own
// git config.
func (s *signIn) credentialHelpers(ctx context.Context, t *testing.T, home string) []string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "--get-all", "credential.helper")
	cmd.Dir = filepath.Join(home, "maisys-config")
	cmd.Env = s.env(home)
	out, err := cmd.Output()
	if exitCode(t, err) == 1 {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the clone's credential helpers: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// credentialsFile is where maisys saves a token typed at its prompt.
func credentialsFile(home string) string {
	return filepath.Join(home, ".config", "maisys", "git-credentials")
}

// exitCode returns the exit code of a command that ran, from its error.
func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.Exited() {
		return exitErr.ExitCode()
	}
	t.Fatal(err)
	return -1
}

// echoOn reports whether the terminal shows what's typed.
func echoOn(t *testing.T, tty *os.File) bool {
	t.Helper()
	termios, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("reading the terminal's settings: %v", err)
	}
	return termios.Lflag&unix.ECHO != 0
}

// liveMachine returns the fixture's test machine for this system, and skips
// the test if it has none.
func liveMachine(t *testing.T) string {
	t.Helper()
	sys, err := facts.Detect()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range fixture.Distros {
		if d.OS == sys.OS && d.Version == sys.OSVersion {
			return d.Machine()
		}
		names = append(names, d.Name())
	}
	t.Skipf("the fixture has no test machine for this system, %s %s: run the test on %s", sys.OSID, sys.OSVersion, strings.Join(names, ", "))
	return ""
}

// buildMaisys builds maisys and returns its path. It's a development build,
// so it doesn't update itself.
func buildMaisys(ctx context.Context, t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "maisys")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", bin, "./cmd/maisys")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building maisys: %v\n%s", err, out)
	}
	return bin
}

// testContext returns a context that ends a minute before the test times
// out, so the test can still clean up after a command that hangs.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx := t.Context()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		t.Cleanup(cancel)
	}
	return ctx
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
