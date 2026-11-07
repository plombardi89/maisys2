package system

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		allow     []int
		label     string
		stdout    string
		stderr    string
		wantErr   string // "" means no error
		wantTail  []string
		wantLabel string
	}{
		{name: "success", code: "0", stdout: "out\n", stderr: "err\n"},
		{
			name: "failure", code: "3", stdout: "out\n", stderr: "one\ntwo\n", label: "doing it",
			wantErr: "doing it failed with exit code 3:\n  one\n  two", wantTail: []string{"one", "two"}, wantLabel: "doing it",
		},
		{
			name: "failure without error output", code: "2", stdout: "out\n",
			wantErr: "failed with exit code 2: out", wantTail: []string{"out"},
		},
		{name: "allowed exit code", code: "1", allow: []int{1, 100}, stdout: "out\n"},
		{
			name: "other exit code", code: "4", allow: []int{1, 100},
			wantErr: "failed with exit code 4",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewRunner(Options{})
			c := helperCmd(t, "exit", tt.code, tt.stdout, tt.stderr)
			c.AllowExit = tt.allow
			c.Label = tt.label
			res, err := r.Run(t.Context(), c)

			if got := fmt.Sprint(res.ExitCode); got != tt.code {
				t.Errorf("ExitCode = %s, want %s", got, tt.code)
			}
			if string(res.Stdout) != tt.stdout || string(res.Stderr) != tt.stderr {
				t.Errorf("Stdout, Stderr = %q, %q; want %q, %q", res.Stdout, res.Stderr, tt.stdout, tt.stderr)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				return
			}
			exitErr, ok := errors.AsType[*ExitError](err)
			if !ok {
				t.Fatalf("Run() error = %v, want an *ExitError", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Run() error = %q, want it to contain %q", err, tt.wantErr)
			}
			if fmt.Sprint(exitErr.Code) != tt.code || !slices.Equal(exitErr.Tail, tt.wantTail) || exitErr.Command != c.String() {
				t.Errorf("ExitError = %+v, want code %s, tail %q and command %q", exitErr, tt.code, tt.wantTail, c.String())
			}
			if wantLabel := cmp.Or(tt.wantLabel, c.String()); exitErr.Label != wantLabel {
				t.Errorf("ExitError.Label = %q, want %q", exitErr.Label, wantLabel)
			}
		})
	}
}

func TestRunEnvDirAndStdin(t *testing.T) {
	r := NewRunner(Options{})
	run := func(c Cmd) string {
		t.Helper()
		res, err := r.Run(t.Context(), c)
		if err != nil {
			t.Fatalf("Run(%s): %v", c, err)
		}
		return strings.TrimSpace(string(res.Stdout))
	}

	if got := run(helperCmd(t, "env", "LC_ALL")); got != "C" {
		t.Errorf("LC_ALL = %q, want C", got)
	}
	c := helperCmd(t, "env", "LC_ALL")
	c.Env = append(c.Env, "LC_ALL=C.UTF-8")
	if got := run(c); got != "C.UTF-8" {
		t.Errorf("LC_ALL with Env = %q, want C.UTF-8", got)
	}
	t.Setenv("SYSTEM_TEST_INHERITED", "yes")
	if got := run(helperCmd(t, "env", "SYSTEM_TEST_INHERITED")); got != "yes" {
		t.Errorf("an inherited variable = %q, want yes", got)
	}

	dir := t.TempDir()
	c = helperCmd(t, "pwd")
	c.Dir = dir
	got, err := filepath.EvalSymlinks(run(c))
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(dir); got != want {
		t.Errorf("working directory = %q, want %q", got, want)
	}

	c = helperCmd(t, "cat")
	c.Stdin = strings.NewReader("some input\n")
	if got := run(c); got != "some input" {
		t.Errorf("output of cat = %q, want the input", got)
	}
	if got := run(helperCmd(t, "cat")); got != "" {
		t.Errorf("output of cat without input = %q, want nothing", got)
	}
}

func TestRunLog(t *testing.T) {
	var log, verbose bytes.Buffer
	r := NewRunner(Options{Log: &log, Verbose: &verbose})
	c := helperCmd(t, "exit", "5", "first\nno newline", "an error\n")
	c.AllowExit = []int{5}
	if _, err := r.Run(t.Context(), c); err != nil {
		t.Fatal(err)
	}

	pattern := `^\d\d:\d\d:\d\d \$ ` + regexp.QuoteMeta(c.String()) + `\n` +
		`(?s:.*)` + // output and error output, in the order they came
		`\d\d:\d\d:\d\d exit code 5 after \d.*\n\n$`
	if !regexp.MustCompile(pattern).Match(log.Bytes()) {
		t.Errorf("log =\n%s\nwant it to match %s", log.String(), pattern)
	}
	for _, want := range []string{"first\n", "an error\n", "no newline"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log doesn't contain %q:\n%s", want, log.String())
		}
	}

	if !strings.HasPrefix(verbose.String(), "$ "+c.String()+"\n") {
		t.Errorf("verbose output doesn't start with the command line:\n%s", verbose.String())
	}
	for _, want := range []string{"\nfirst\n", "\nno newline\n", "\nan error\n"} {
		if !strings.Contains(verbose.String(), want) {
			t.Errorf("verbose output doesn't contain the line %q:\n%s", want, verbose.String())
		}
	}
}

// TestRunConcurrent runs commands at the same time, checking that each
// one's entry in the log is in one piece.
func TestRunConcurrent(t *testing.T) {
	var log lockedBuffer
	r := NewRunner(Options{Log: &log, Verbose: &lockedBuffer{}})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			c := helperCmd(t, "exit", "0", fmt.Sprintf("output %d\n", i), "")
			if _, err := r.Run(t.Context(), c); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	for i := range 8 {
		want := fmt.Sprintf(`\$ .* exit 0 'output %d\n' ''\noutput %d\n\d\d:\d\d:\d\d exit code 0`, i, i)
		if !regexp.MustCompile(want).Match(log.Bytes()) {
			t.Errorf("log doesn't have command %d in one piece:\n%s", i, log.Bytes())
		}
	}
}

func TestRunCancel(t *testing.T) {
	var log bytes.Buffer
	r := NewRunner(Options{Log: &log})
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	res, err := r.Run(ctx, helperCmd(t, "sleep"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run() error = %v, want context.DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("Run() took %s to stop the command", took)
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want the command to have been stopped")
	}
	if !strings.Contains(log.String(), "stopped (context deadline exceeded)") {
		t.Errorf("log doesn't say the command was stopped:\n%s", log.String())
	}
}

func TestRunStartErrors(t *testing.T) {
	var log bytes.Buffer
	r := NewRunner(Options{Log: &log})

	_, err := r.Run(t.Context(), Cmd{Name: "maisys-test-no-such-command", Label: "testing"})
	if !errors.Is(err, exec.ErrNotFound) || !strings.Contains(err.Error(), "testing: maisys-test-no-such-command isn't installed") {
		t.Errorf("Run() of a missing command: error = %v, want exec.ErrNotFound saying it isn't installed", err)
	}
	if !strings.Contains(log.String(), "$ maisys-test-no-such-command\n") || !strings.Contains(log.String(), "didn't start") {
		t.Errorf("log doesn't record the missing command:\n%s", log.String())
	}

	c := helperCmd(t, "pwd")
	c.Dir = filepath.Join(t.TempDir(), "missing")
	_, err = r.Run(t.Context(), c)
	if err == nil || strings.Contains(err.Error(), "isn't installed") {
		t.Errorf("Run() in a missing directory: error = %v, want one that doesn't say the command isn't installed", err)
	}
}

func TestExitErrorMessage(t *testing.T) {
	tests := []struct {
		res  Result
		want string
	}{
		{Result{ExitCode: 1}, "dnf install failed with exit code 1"},
		{Result{ExitCode: 1, Stderr: []byte("\n  Error: no match  \n\n")}, "dnf install failed with exit code 1: Error: no match"},
		{Result{ExitCode: 2, Stdout: []byte("only output\n"), Stderr: []byte(" \n")}, "dnf install failed with exit code 2: only output"},
		{Result{ExitCode: 3, Stderr: []byte("progress 10%\rprogress 100%\rfailed\n")}, "dnf install failed with exit code 3: failed"},
		{
			Result{ExitCode: 4, Stderr: []byte("1\n2\n3\n4\n5\n6\n7\n")},
			"dnf install failed with exit code 4:\n  3\n  4\n  5\n  6\n  7",
		},
	}
	for _, tt := range tests {
		err := CheckExit(Cmd{Name: "dnf", Args: []string{"install", "jq"}, Label: "dnf install"}, tt.res)
		if err == nil || err.Error() != tt.want {
			t.Errorf("CheckExit(%+v) = %v, want %q", tt.res, err, tt.want)
		}
	}
	if err := CheckExit(Cmd{Name: "rpm", AllowExit: []int{1}}, Result{ExitCode: 1}); err != nil {
		t.Errorf("CheckExit() with an allowed exit code = %v", err)
	}
}

func TestCmdString(t *testing.T) {
	tests := []struct {
		c    Cmd
		want string
	}{
		{Cmd{Name: "dnf", Args: []string{"install", "-y", "jq"}}, "dnf install -y jq"},
		{Cmd{Name: "sh", Args: []string{"-c", "echo $HOME"}}, `sh -c 'echo $HOME'`},
		{Cmd{Name: "echo", Args: []string{"it's", ""}}, `echo 'it'\''s' ''`},
		{Cmd{Name: "install", Args: []string{"-m", "0440", "/tmp/a_b", "user@host:x=1,2+3%"}}, "install -m 0440 /tmp/a_b user@host:x=1,2+3%"},
	}
	for _, tt := range tests {
		if got := tt.c.String(); got != tt.want {
			t.Errorf("String() = %s, want %s", got, tt.want)
		}
	}
}

// TestAdminOnWindows checks that the runner on Windows runs commands as
// admin through its admin session rather than sudo: directly if the tests
// run elevated, as on CI, and otherwise not until the elevated helper is
// started, which this test never does, as it would show a UAC prompt.
func TestAdminOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("maisys uses sudo everywhere but Windows")
	}
	r := NewRunner(Options{})
	if r.admin == nil {
		t.Fatal("the runner has no admin session")
	}
	c := helperCmd(t, "exit", "0", "as admin", "")
	c.Root = true
	status, err := r.CheckSudo(t.Context())
	if err != nil {
		t.Fatalf("CheckSudo() error = %v", err)
	}
	if r.admin.elevated {
		if status != SudoNoPassword {
			t.Errorf("CheckSudo() elevated = %q, want SudoNoPassword", status)
		}
		if res, err := r.Run(t.Context(), c); err != nil || string(res.Stdout) != "as admin" {
			t.Errorf("Run() as admin, elevated = %+v, %v; want it run directly", res, err)
		}
		if err := r.StartSudo(t.Context()); err != nil {
			t.Errorf("StartSudo() elevated: error = %v", err)
		}
		r.StopSudo()
	} else {
		if status != SudoNeedsPassword {
			t.Errorf("CheckSudo() unelevated = %q, want SudoNeedsPassword", status)
		}
		if _, err := r.Run(t.Context(), c); !errors.Is(err, ErrNeedsAdmin) {
			t.Errorf("Run() as admin, unelevated: error = %v, want ErrNeedsAdmin", err)
		}
	}
	if err := InstallFile(t.Context(), r, []byte("x"), `C:\x`, 0o644); !errors.Is(err, errNoSudo) {
		t.Errorf("InstallFile() error = %v, want errNoSudo", err)
	}
}
