package systemtest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/system"
)

func TestHandlers(t *testing.T) {
	f := NewFakeRunner(t)
	f.On("rpm", Respond(0, "general\n"))
	f.On("rpm -q", Fail(1, "package jq is not installed\n"))
	f.On("rpm -q tmux", Respond(0, "tmux-3.4\n"))
	f.On("rpm -q tmux", Respond(0, "tmux-3.5\n")) // the latest of two equal prefixes wins
	f.On("dnf", func(_ context.Context, c system.Cmd) (system.Result, error) {
		return system.Result{}, errors.New("dnf broke: " + strings.Join(c.Args, " "))
	})

	tests := []struct {
		c          system.Cmd
		wantStdout string
		wantErr    string
	}{
		{c: system.Cmd{Name: "rpm", Args: []string{"--version"}}, wantStdout: "general\n"},
		{c: system.Cmd{Name: "rpm", Args: []string{"-q", "jq"}}, wantErr: "rpm -q jq failed with exit code 1: package jq is not installed"},
		{c: system.Cmd{Name: "rpm", Args: []string{"-q", "jq"}, AllowExit: []int{1}}},
		{c: system.Cmd{Name: "rpm", Args: []string{"-q", "tmux"}}, wantStdout: "tmux-3.5\n"},
		{c: system.Cmd{Name: "dnf", Args: []string{"install", "jq"}}, wantErr: "dnf broke: install jq"},
	}
	for _, tt := range tests {
		res, err := f.Run(t.Context(), tt.c)
		if string(res.Stdout) != tt.wantStdout {
			t.Errorf("Run(%s) output = %q, want %q", tt.c, res.Stdout, tt.wantStdout)
		}
		if got := fmt.Sprint(err); (tt.wantErr == "" && err != nil) || (tt.wantErr != "" && got != tt.wantErr) {
			t.Errorf("Run(%s) error = %v, want %q", tt.c, err, tt.wantErr)
		}
	}
	want := []string{"rpm --version", "rpm -q jq", "rpm -q jq", "rpm -q tmux", "dnf install jq"}
	if got := f.Commands(); !slices.Equal(got, want) {
		t.Errorf("Commands() = %q, want %q", got, want)
	}
	if got := f.RootCommands(); got != nil {
		t.Errorf("RootCommands() = %q, want none", got)
	}
}

// fakeT records the failures a FakeRunner reports.
type fakeT struct {
	testing.TB
	errors []string
}

func (t *fakeT) Errorf(format string, args ...any) {
	t.errors = append(t.errors, fmt.Sprintf(format, args...))
}

func TestUnexpectedCommand(t *testing.T) {
	ft := &fakeT{TB: t}
	f := NewFakeRunner(ft)
	f.On("rpm -q", Respond(0, ""))
	_, err := f.Run(t.Context(), system.Cmd{Name: "rpm", Args: []string{"-e", "jq"}})
	if err == nil || len(ft.errors) != 1 || !strings.Contains(ft.errors[0], "unexpected command: rpm -e jq") {
		t.Errorf("Run() of an unexpected command: error = %v, test failures = %q", err, ft.errors)
	}
	if _, err := f.Run(t.Context(), system.Cmd{Name: "rp"}); err == nil {
		t.Error("Run() of a command that's part of a prefix: no error")
	}
	calls := f.Calls()
	if len(calls) != 2 || calls[0].Ran || calls[1].Ran {
		t.Errorf("Calls() = %+v, want two that didn't run", calls)
	}
	if got := f.Commands(); got != nil {
		t.Errorf("Commands() = %q, want none", got)
	}
}

func TestSudo(t *testing.T) {
	root := system.Cmd{Name: "dnf", Args: []string{"install", "-y", "jq"}, Root: true, Label: "dnf install"}
	user := system.Cmd{Name: "rpm", Args: []string{"-q", "jq"}}

	tests := []struct {
		status      system.SudoStatus
		wantPlanErr error // from a root command before StartSudo
		wantSudoErr bool  // whether CheckSudo and StartSudo fail with a *system.SudoError
	}{
		{status: ""},
		{status: system.SudoNoPassword},
		{status: system.SudoNeedsPassword, wantPlanErr: system.ErrNeedsPassword},
		{status: system.SudoMissing, wantSudoErr: true},
		{status: system.SudoNotAllowed, wantSudoErr: true},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			f := NewFakeRunner(t)
			f.SudoStatus, f.OS, f.User = tt.status, config.Fedora, "tester"
			f.On("dnf install", Respond(0, ""))
			f.On("rpm -q", Respond(0, ""))

			status, err := f.CheckSudo(t.Context())
			if want := cmp.Or(tt.status, system.SudoNoPassword); status != want {
				t.Errorf("CheckSudo() = %q, want %q", status, want)
			}
			checkSudoError(t, "CheckSudo()", err, tt.wantSudoErr, status)

			// Planning: no session, and nothing may prompt.
			_, err = f.Run(t.Context(), root)
			switch {
			case tt.wantSudoErr:
				checkSudoError(t, "Run() as root", err, true, status)
			case !errors.Is(err, tt.wantPlanErr) || (err != nil && !strings.HasPrefix(err.Error(), "dnf install: ")):
				t.Errorf("Run() as root without a session: error = %v, want %v naming the command", err, tt.wantPlanErr)
			}
			if _, err := f.Run(t.Context(), user); err != nil {
				t.Errorf("Run() as the user: error = %v", err)
			}
			if f.SudoStarts() != 0 {
				t.Error("planning started sudo")
			}

			// Applying.
			err = f.StartSudo(t.Context())
			checkSudoError(t, "StartSudo()", err, tt.wantSudoErr, status)
			_, err = f.Run(t.Context(), root)
			if (err != nil) != tt.wantSudoErr {
				t.Errorf("Run() as root in the session: error = %v", err)
			}
			f.StopSudo()

			var want []string
			if tt.wantPlanErr == nil && !tt.wantSudoErr {
				want = append(want, root.String())
			}
			if !tt.wantSudoErr {
				want = append(want, root.String())
			}
			if got := f.RootCommands(); !slices.Equal(got, want) {
				t.Errorf("RootCommands() = %q, want %q", got, want)
			}
			if f.SudoStarts() != 1 || f.SudoChecks() != 1 {
				t.Errorf("SudoStarts(), SudoChecks() = %d, %d; want 1, 1", f.SudoStarts(), f.SudoChecks())
			}
			for _, c := range f.Calls() {
				if c.Cmd.Root && c.Ran && !c.InSession && status != system.SudoNoPassword {
					t.Errorf("%s ran as root outside the session", c.Cmd)
				}
			}
		})
	}
}

func TestStartErr(t *testing.T) {
	f := NewFakeRunner(t)
	f.SudoStatus = system.SudoNeedsPassword
	f.StartErr = errors.New("wrong password")
	if err := f.StartSudo(t.Context()); !errors.Is(err, f.StartErr) {
		t.Errorf("StartSudo() error = %v, want StartErr", err)
	}
	if _, err := f.Run(t.Context(), system.Cmd{Name: "true", Root: true}); !errors.Is(err, system.ErrNeedsPassword) {
		t.Errorf("Run() as root after StartSudo failed: error = %v, want ErrNeedsPassword", err)
	}
}

func TestCancelled(t *testing.T) {
	f := NewFakeRunner(t)
	f.On("true", Respond(0, ""))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.Run(ctx, system.Cmd{Name: "true"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Run() with a cancelled context: error = %v, want context.Canceled", err)
	}
}

func checkSudoError(t *testing.T, what string, err error, want bool, status system.SudoStatus) {
	t.Helper()
	sudoErr, ok := errors.AsType[*system.SudoError](err)
	switch {
	case !want && err != nil:
		t.Errorf("%s error = %v", what, err)
	case want && (!ok || sudoErr.Status != status || sudoErr.User != "tester" || sudoErr.OS != config.Fedora):
		t.Errorf("%s error = %#v, want a *system.SudoError for %s", what, err, status)
	}
}

// TestAdmin checks the fake runner on Windows, where a command to run as
// admin outside the session (the elevated helper) fails with ErrNeedsAdmin.
func TestAdmin(t *testing.T) {
	f := NewFakeRunner(t)
	f.SudoStatus, f.OS = system.SudoNeedsPassword, config.Windows
	f.On("wsl.exe --install", Respond(0, ""))
	c := system.Cmd{Name: "wsl.exe", Args: []string{"--install", "--no-distribution"}, Root: true, Label: "installing WSL"}

	_, err := f.Run(t.Context(), c)
	if !errors.Is(err, system.ErrNeedsAdmin) || !errors.Is(err, system.ErrNeedsPassword) || !strings.HasPrefix(err.Error(), "installing WSL: ") {
		t.Errorf("Run() as admin without the helper: error = %v, want ErrNeedsAdmin naming the command", err)
	}
	if err := f.StartSudo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Run(t.Context(), c); err != nil {
		t.Errorf("Run() as admin in the helper: error = %v", err)
	}
	f.StopSudo()
	if got := f.RootCommands(); len(got) != 1 {
		t.Errorf("commands run as admin = %q, want one", got)
	}
}
