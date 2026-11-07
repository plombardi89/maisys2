//go:build !windows

package system

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
)

// fakeSudoRun is a fake sudo for one test.
type fakeSudoRun struct {
	path  string // the fake sudo binary
	log   string // where it records its arguments
	stamp string // its record of a typed password
}

// useFakeSudo makes the test binary act as sudo in mode (see fakeSudo)
// when it's run as SudoOptions.Path.
func useFakeSudo(t *testing.T, mode string) *fakeSudoRun {
	t.Helper()
	dir := t.TempDir()
	s := &fakeSudoRun{path: testBinary(t), log: filepath.Join(dir, "sudo.log"), stamp: filepath.Join(dir, "stamp")}
	t.Setenv(fakeEnv, "sudo")
	t.Setenv("FAKE_SUDO_MODE", mode)
	t.Setenv("FAKE_SUDO_LOG", s.log)
	t.Setenv("FAKE_SUDO_STAMP", s.stamp)
	return s
}

// calls returns the arguments of every time the fake sudo ran.
func (s *fakeSudoRun) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(s.log)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for line := range strings.Lines(string(data)) {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, args)
	}
	return calls
}

// count returns how many times the fake sudo ran with args.
func (s *fakeSudoRun) count(t *testing.T, args ...string) int {
	t.Helper()
	n := 0
	for _, c := range s.calls(t) {
		if slices.Equal(c, args) {
			n++
		}
	}
	return n
}

func TestCheckSudo(t *testing.T) {
	tests := []struct {
		mode       string
		want       SudoStatus
		wantErr    string
		wantSudo   bool // whether the error is a *SudoError
		wantCalled [][]string
	}{
		{mode: "nopasswd", want: SudoNoPassword, wantCalled: [][]string{{"-n", "true"}}},
		{mode: "password", want: SudoNeedsPassword, wantCalled: [][]string{{"-n", "true"}, {"-n", "-l"}}},
		{
			mode: "notallowed", want: SudoNotAllowed, wantSudo: true,
			wantErr:    "tester may not use sudo (Sorry, user tester may not run sudo on testhost.)",
			wantCalled: [][]string{{"-n", "true"}, {"-n", "-l"}},
		},
		{
			mode: "broken", wantErr: "sudo doesn't work: sudo: effective uid is not 0",
			wantCalled: [][]string{{"-n", "true"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			sudo := useFakeSudo(t, tt.mode)
			var log bytes.Buffer
			r := NewRunner(Options{Log: &log, Sudo: SudoOptions{Path: sudo.path, User: "tester", OS: config.Fedora}})
			got, err := r.CheckSudo(t.Context())
			if got != tt.want {
				t.Errorf("CheckSudo() = %q, want %q", got, tt.want)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("CheckSudo() error = %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("CheckSudo() error = %v, want it to contain %q", err, tt.wantErr)
			}
			if _, ok := errors.AsType[*SudoError](err); ok != tt.wantSudo {
				t.Errorf("CheckSudo() error = %#v, want a *SudoError: %t", err, tt.wantSudo)
			}
			if calls := sudo.calls(t); !reflect.DeepEqual(calls, tt.wantCalled) {
				t.Errorf("sudo ran with %q, want %q", calls, tt.wantCalled)
			}
			if !strings.Contains(log.String(), "$ "+sudo.path+" -n true\n") {
				t.Errorf("the log doesn't record the check:\n%s", log.String())
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		r := NewRunner(Options{Sudo: SudoOptions{Path: filepath.Join(t.TempDir(), "sudo"), User: "tester", OS: config.Ubuntu}})
		got, err := r.CheckSudo(t.Context())
		sudoErr, ok := errors.AsType[*SudoError](err)
		if got != SudoMissing || !ok || sudoErr.Status != SudoMissing || sudoErr.User != "tester" || sudoErr.OS != config.Ubuntu {
			t.Errorf("CheckSudo() = %q, %#v; want SudoMissing and a *SudoError for tester on ubuntu", got, err)
		}
		if _, err := r.Run(t.Context(), Cmd{Name: "true", Root: true}); !errors.Is(err, sudoErr) {
			t.Errorf("Run() as root without sudo: error = %v, want the SudoError", err)
		}
	})
}

// TestPlanNeverPrompts runs commands as root without a sudo session, while
// sudo needs a password: they must fail with ErrNeedsPassword, with sudo
// only ever run with -n.
func TestPlanNeverPrompts(t *testing.T) {
	sudo := useFakeSudo(t, "password")
	marker := filepath.Join(t.TempDir(), "ran")
	r := NewRunner(Options{Sudo: SudoOptions{Path: sudo.path, Stdin: strings.NewReader("secret\n")}})

	for range 3 {
		_, err := r.Run(t.Context(), Cmd{Name: "touch", Args: []string{marker}, Root: true, Label: "touching"})
		if !errors.Is(err, ErrNeedsPassword) || !strings.HasPrefix(err.Error(), "touching: ") {
			t.Fatalf("Run() as root: error = %v, want ErrNeedsPassword naming the command", err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran")
	}
	calls := sudo.calls(t)
	if want := [][]string{{"-n", "true"}, {"-n", "-l"}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("sudo ran with %q, want only one check: %q", calls, want)
	}
	if _, err := os.Stat(sudo.stamp); err == nil {
		t.Error("sudo read the password")
	}
}

func TestRunAsRoot(t *testing.T) {
	sudo := useFakeSudo(t, "nopasswd")
	var log bytes.Buffer
	r := NewRunner(Options{Log: &log, Sudo: SudoOptions{Path: sudo.path}})

	res, err := r.Run(t.Context(), Cmd{Name: "sh", Args: []string{"-c", `echo "$LC_ALL $GREETING"`}, Env: []string{"GREETING=hello there"}, Root: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(res.Stdout); got != "C hello there\n" {
		t.Errorf("output = %q, want LC_ALL and the Env variable", got)
	}
	want := [][]string{{"-n", "true"}, {"-n", "--", "env", "LC_ALL=C", "GREETING=hello there", "sh", "-c", `echo "$LC_ALL $GREETING"`}}
	if calls := sudo.calls(t); !reflect.DeepEqual(calls, want) {
		t.Errorf("sudo ran with %q, want %q", calls, want)
	}
	if !strings.Contains(log.String(), "$ "+sudo.path+" -n -- env LC_ALL=C 'GREETING=hello there' sh -c") {
		t.Errorf("the log doesn't show the command run with sudo:\n%s", log.String())
	}

	c := Cmd{Name: "sh", Args: []string{"-c", "echo oops >&2; exit 3"}, Root: true}
	_, err = r.Run(t.Context(), c)
	exitErr, ok := errors.AsType[*ExitError](err)
	if !ok || exitErr.Code != 3 || exitErr.Command != c.String() || exitErr.Label != c.String() || !slices.Equal(exitErr.Tail, []string{"oops"}) {
		t.Errorf("Run() of a failing command as root: error = %#v, want an *ExitError for %s without sudo", err, c)
	}
	if n := sudo.count(t, "-n", "true"); n != 1 {
		t.Errorf("sudo was checked %d times, want once", n)
	}
}

func TestStartSudo(t *testing.T) {
	sudo := useFakeSudo(t, "password")
	var log bytes.Buffer
	r := NewRunner(Options{Log: &log, Sudo: SudoOptions{
		Path:    sudo.path,
		Stdin:   strings.NewReader("secret\n"),
		Refresh: 20 * time.Millisecond,
	}})
	t.Cleanup(r.StopSudo)

	if _, err := r.Run(t.Context(), Cmd{Name: "true", Root: true}); !errors.Is(err, ErrNeedsPassword) {
		t.Fatalf("Run() as root before StartSudo: error = %v, want ErrNeedsPassword", err)
	}
	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatalf("StartSudo() error = %v", err)
	}
	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatalf("StartSudo() a second time: error = %v", err)
	}
	if n := sudo.count(t, "-v"); n != 1 {
		t.Errorf("sudo -v ran %d times, want once", n)
	}
	for _, c := range sudo.calls(t) {
		if !slices.Contains(c, "-n") && !slices.Equal(c, []string{"-v"}) {
			t.Errorf("sudo ran without -n: %q", c)
		}
	}

	res, err := r.Run(t.Context(), Cmd{Name: "echo", Args: []string{"as root"}, Root: true})
	if err != nil || string(res.Stdout) != "as root\n" {
		t.Fatalf("Run() as root in the session = %q, %v", res.Stdout, err)
	}
	// The refresh goroutine may run sudo -n -v at any time, so look for the
	// command among the calls rather than expecting it last.
	want := []string{"-n", "--", "env", "LC_ALL=C", "echo", "as root"}
	if calls := sudo.calls(t); !slices.ContainsFunc(calls, func(c []string) bool { return slices.Equal(c, want) }) {
		t.Errorf("sudo calls = %q, want one with %q", calls, want)
	}

	eventually(t, func() bool { return sudo.count(t, "-n", "-v") >= 2 })
	r.StopSudo()
	refreshed := sudo.count(t, "-n", "-v")
	time.Sleep(100 * time.Millisecond)
	if n := sudo.count(t, "-n", "-v"); n != refreshed {
		t.Errorf("sudo was refreshed %d more times after StopSudo", n-refreshed)
	}
	if !strings.Contains(log.String(), "$ "+sudo.path+" -v\n") || !strings.Contains(log.String(), "$ "+sudo.path+" -n -v\n") {
		t.Errorf("the log doesn't record sudo -v and its refreshes:\n%s", log.String())
	}

	// After the session, sudo is checked again, and still works while its
	// timestamp lasts.
	checks := sudo.count(t, "-n", "true")
	if _, err := r.Run(t.Context(), Cmd{Name: "true", Root: true}); err != nil {
		t.Errorf("Run() as root after StopSudo: error = %v", err)
	}
	if n := sudo.count(t, "-n", "true"); n != checks+1 {
		t.Errorf("sudo was checked %d times after StopSudo, want once", n-checks)
	}
}

// TestSudoInForeground checks that sudo -v, which may ask for a password,
// runs where the terminal's Ctrl-C reaches it, and that commands run as
// root and the session's refreshes don't.
func TestSudoInForeground(t *testing.T) {
	sudo := useFakeSudo(t, "password")
	groups := filepath.Join(t.TempDir(), "groups")
	t.Setenv("FAKE_SUDO_GROUPS", groups)
	r := NewRunner(Options{Sudo: SudoOptions{Path: sudo.path, Stdin: strings.NewReader("secret\n"), Refresh: 10 * time.Millisecond}})
	t.Cleanup(r.StopSudo)

	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(t.Context(), Cmd{Name: "true", Root: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return sudo.count(t, "-n", "-v") >= 1 })
	r.StopSudo()

	data, err := os.ReadFile(groups)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"-v": "parent", "-n -- env LC_ALL=C true": "own", "-n -v": "own"}
	for line := range strings.Lines(string(data)) {
		args, group, _ := strings.Cut(strings.TrimSpace(line), ": ")
		if w, ok := want[args]; ok && group != w {
			t.Errorf("sudo %s ran in process group %q, want %q", args, group, w)
		}
		delete(want, args)
	}
	if len(want) > 0 {
		t.Errorf("sudo didn't run with %q:\n%s", slices.Sorted(maps.Keys(want)), data)
	}
}

func TestStartSudoStopsWithContext(t *testing.T) {
	sudo := useFakeSudo(t, "nopasswd")
	r := NewRunner(Options{Sudo: SudoOptions{Path: sudo.path, Refresh: 10 * time.Millisecond}})
	ctx, cancel := context.WithCancel(t.Context())
	if err := r.StartSudo(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return sudo.count(t, "-n", "-v") >= 1 })
	cancel()

	done := make(chan struct{})
	go func() {
		r.StopSudo()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("StopSudo() didn't return after the context was cancelled")
	}
	refreshed := sudo.count(t, "-n", "-v")
	time.Sleep(50 * time.Millisecond)
	if n := sudo.count(t, "-n", "-v"); n != refreshed {
		t.Errorf("sudo was refreshed after the context was cancelled")
	}
}

func TestStartSudoFails(t *testing.T) {
	tests := []struct {
		name, mode, password string
		want                 string
		wantStatus           SudoStatus // of the *SudoError, if it is one
	}{
		{name: "wrong password", mode: "password", password: "wrong", want: "sudo couldn't check your password: sudo: 1 incorrect password attempt"},
		{name: "not allowed", mode: "notallowed", password: "secret", want: "tester may not use sudo (tester is not in the sudoers file.)", wantStatus: SudoNotAllowed},
		{name: "broken", mode: "broken", want: "sudo couldn't check your password: sudo: effective uid is not 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sudo := useFakeSudo(t, tt.mode)
			var stderr bytes.Buffer
			r := NewRunner(Options{Sudo: SudoOptions{Path: sudo.path, User: "tester", OS: config.Fedora, Stdin: strings.NewReader(tt.password + "\n"), Stderr: &stderr}})
			t.Cleanup(r.StopSudo)

			err := r.StartSudo(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("StartSudo() error = %v, want it to contain %q", err, tt.want)
			}
			sudoErr, ok := errors.AsType[*SudoError](err)
			if (ok && sudoErr.Status != tt.wantStatus) || ok != (tt.wantStatus != "") {
				t.Errorf("StartSudo() error = %#v, want a *SudoError with status %q", err, tt.wantStatus)
			}
			if stderr.Len() == 0 {
				t.Error("sudo's error output didn't reach the terminal")
			}
			if _, err := r.Run(t.Context(), Cmd{Name: "true", Root: true}); err == nil {
				t.Error("Run() as root after StartSudo failed: no error")
			}
		})
	}

	t.Run("missing", func(t *testing.T) {
		r := NewRunner(Options{Sudo: SudoOptions{Path: filepath.Join(t.TempDir(), "sudo"), User: "tester"}})
		err := r.StartSudo(t.Context())
		if sudoErr, ok := errors.AsType[*SudoError](err); !ok || sudoErr.Status != SudoMissing {
			t.Errorf("StartSudo() error = %v, want a *SudoError for a missing sudo", err)
		}
	})
}

// TestSessionRunsOut checks a root command in a session whose sudo
// timestamp is gone, as after a laptop sleeps.
func TestSessionRunsOut(t *testing.T) {
	sudo := useFakeSudo(t, "password")
	r := NewRunner(Options{Sudo: SudoOptions{Path: sudo.path, Stdin: strings.NewReader("secret\n")}})
	t.Cleanup(r.StopSudo)
	if err := r.StartSudo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sudo.stamp); err != nil {
		t.Fatal(err)
	}
	_, err := r.Run(t.Context(), Cmd{Name: "true", Root: true, Label: "testing"})
	if !errors.Is(err, ErrNeedsPassword) || !strings.Contains(err.Error(), "testing: sudo's session ran out") {
		t.Errorf("Run() as root after the session ran out: error = %v, want ErrNeedsPassword saying so", err)
	}
}

// TestPlanSudoRunsOut checks that a check that finds sudo works without a
// password, followed by a root command that needs one, ends up as
// ErrNeedsPassword for that command and the ones after it.
func TestPlanSudoRunsOut(t *testing.T) {
	sudo := useFakeSudo(t, "password")
	if err := os.WriteFile(sudo.stamp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Options{Sudo: SudoOptions{Path: sudo.path}})
	if got, err := r.CheckSudo(t.Context()); got != SudoNoPassword || err != nil {
		t.Fatalf("CheckSudo() = %q, %v; want SudoNoPassword", got, err)
	}
	if err := os.Remove(sudo.stamp); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Run(t.Context(), Cmd{Name: "true", Root: true}); !errors.Is(err, ErrNeedsPassword) {
			t.Errorf("Run() as root once sudo needs a password: error = %v, want ErrNeedsPassword", err)
		}
	}
	if n := sudo.count(t, "-n", "--", "env", "LC_ALL=C", "true"); n != 1 {
		t.Errorf("sudo ran the command %d times, want once before it knew sudo needs a password", n)
	}
}

// eventually fails the test unless cond becomes true within a few seconds.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for a condition")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
