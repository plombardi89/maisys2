package loginshell_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/loginshell"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

const shells = "# /etc/shells: valid login shells\n/bin/sh\n/bin/bash\n/usr/bin/bash\n/usr/bin/zsh\n"

func newEnv(t *testing.T, passwd string) (*resource.Runtime, *systemtest.FakeRunner, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("login shells are only on Linux")
	}
	shellsFile := filepath.Join(t.TempDir(), "shells")
	if err := os.WriteFile(shellsFile, []byte(shells), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := systemtest.NewFakeRunner(t)
	runner.On("getent passwd phil", systemtest.Respond(0, passwd))
	runner.On("getent passwd", systemtest.Respond(2, ""))
	runner.On("usermod", systemtest.Respond(0, ""))
	return &resource.Runtime{Runner: runner}, runner, shellsFile
}

func TestShell(t *testing.T) {
	tests := []struct {
		name    string
		passwd  string
		path    string
		summary string // the change, if there is one
		usermod string // the command applying runs, if any
	}{
		{
			name:    "another shell",
			passwd:  "phil:x:1000:1000:Phil:/home/phil:/bin/bash\n",
			path:    "/usr/bin/zsh",
			summary: "login shell /bin/bash → /usr/bin/zsh",
			usermod: "usermod -s /usr/bin/zsh phil",
		},
		{
			name:    "no shell set",
			passwd:  "phil:x:1000:1000:Phil:/home/phil:\n",
			path:    "/usr/bin/bash",
			summary: "login shell /bin/sh → /usr/bin/bash",
			usermod: "usermod -s /usr/bin/bash phil",
		},
		{
			name:   "the shell already",
			passwd: "phil:x:1000:1000:Phil:/home/phil:/usr/bin/zsh\n",
			path:   "/usr/bin/zsh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, runner, shellsFile := newEnv(t, tt.passwd)
			s := &loginshell.Shell{User: "phil", Path: tt.path, ShellsFile: shellsFile}
			if s.ID() != "login-shell" || s.Kind() != "login-shell" || !s.Privileged() || s.Targets() != nil {
				t.Errorf("ID() = %q, Kind() = %q, Privileged() = %t, Targets() = %q", s.ID(), s.Kind(), s.Privileged(), s.Targets())
			}

			// Checking never needs root.
			runner.SudoStatus = system.SudoNeedsPassword
			c, err := s.Check(t.Context(), rt)
			runner.SudoStatus = ""
			want := resource.Change{}
			if tt.summary != "" {
				want = resource.Change{Action: resource.Update, Summary: tt.summary}
			}
			if err != nil || c.Action != want.Action || c.Summary != want.Summary {
				t.Errorf("Check() = %+v, %v; want %+v", c, err, want)
			}

			rec, err := s.Apply(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Details[loginshell.DetailUser] != "phil" || rec.Details[loginshell.DetailShell] != tt.path {
				t.Errorf("record = %+v, want the user and the shell", rec)
			}
			got := strings.Join(runner.RootCommands(), "; ")
			if got != tt.usermod {
				t.Errorf("commands run as root = %q, want %q", got, tt.usermod)
			}
		})
	}
}

func TestShellNotListed(t *testing.T) {
	rt, runner, shellsFile := newEnv(t, "phil:x:1000:1000:Phil:/home/phil:/bin/bash\n")
	s := &loginshell.Shell{User: "phil", Path: "/usr/bin/fish", ShellsFile: shellsFile}

	// The plan doesn't check the list: the shell's package may not be
	// installed yet.
	if c, err := s.Check(t.Context(), rt); err != nil || c.Action != resource.Update {
		t.Errorf("Check() = %+v, %v; want an update", c, err)
	}
	_, err := s.Apply(t.Context(), rt)
	if want := "/usr/bin/fish isn't listed in " + shellsFile + ", so it can't be a login shell"; err == nil || err.Error() != want {
		t.Errorf("Apply() error = %v, want %q", err, want)
	}
	if got := runner.RootCommands(); len(got) != 0 {
		t.Errorf("commands run as root = %q, want none", got)
	}
}

func TestShellSameFile(t *testing.T) {
	rt, _, shellsFile := newEnv(t, "")
	dir := t.TempDir()
	zsh := filepath.Join(dir, "usr", "bin", "zsh")
	if err := os.MkdirAll(filepath.Dir(zsh), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zsh, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("usr", "bin"), filepath.Join(dir, "bin")); err != nil {
		t.Fatal(err)
	}
	runner := systemtest.NewFakeRunner(t)
	runner.On("getent passwd phil", systemtest.Respond(0, "phil:x:1000:1000::/home/phil:"+filepath.Join(dir, "bin", "zsh")+"\n"))
	rt.Runner = runner

	s := &loginshell.Shell{User: "phil", Path: zsh, ShellsFile: shellsFile}
	if c, err := s.Check(t.Context(), rt); err != nil || c.Action != resource.NoChange {
		t.Errorf("Check() of the same shell by another path = %+v, %v; want no change", c, err)
	}

	// sh may be the same file as zsh, but it isn't the same shell.
	if err := os.Symlink("zsh", filepath.Join(dir, "usr", "bin", "sh")); err != nil {
		t.Fatal(err)
	}
	runner.On("getent passwd phil", systemtest.Respond(0, "phil:x:1000:1000::/home/phil:"+filepath.Join(dir, "bin", "sh")+"\n"))
	if c, err := s.Check(t.Context(), rt); err != nil || c.Action != resource.Update {
		t.Errorf("Check() of a link to the shell by another name = %+v, %v; want an update", c, err)
	}
}

func TestShellErrors(t *testing.T) {
	rt, _, shellsFile := newEnv(t, "phil:x:1000:1000:Phil:/home/phil:/bin/bash\n")
	for _, s := range []*loginshell.Shell{
		{User: "nobody-here", Path: "/usr/bin/zsh", ShellsFile: shellsFile},
		{User: "phil", Path: "zsh", ShellsFile: shellsFile},
		{User: "phil", Path: "/usr/bin/zsh", ShellsFile: filepath.Join(t.TempDir(), "missing")},
	} {
		_, checkErr := s.Check(t.Context(), rt)
		_, applyErr := s.Apply(t.Context(), rt)
		if applyErr == nil || (checkErr == nil) != (s.Path == "/usr/bin/zsh" && s.User == "phil") {
			t.Errorf("%+v: Check() error = %v, Apply() error = %v", s, checkErr, applyErr)
		}
	}
}

func TestRemover(t *testing.T) {
	var r loginshell.Remover
	rec := state.Record{ID: "login-shell", Kind: "login-shell", Details: map[string]string{"user": "phil", "shell": "/usr/bin/zsh"}}
	c, err := r.CheckRemove(t.Context(), nil, rec)
	if err != nil || c.Action != resource.NoChange || c.Summary != "" || len(c.Notes) != 0 || r.Kind() != "login-shell" {
		t.Errorf("CheckRemove() = %+v, %v; want nothing to do and nothing to show", c, err)
	}
	if err := r.Remove(t.Context(), nil, rec); err != nil {
		t.Errorf("Remove() error = %v", err)
	}
}
