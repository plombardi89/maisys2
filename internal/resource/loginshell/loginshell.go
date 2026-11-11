// Package loginshell sets your login shell.
package loginshell

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Shell is.
const Kind = "login-shell"

// ShellsFile lists the shells that may be login shells.
const ShellsFile = "/etc/shells"

// The keys of a login shell record's Details.
const (
	// DetailUser is the user whose shell it is.
	DetailUser = "user"
	// DetailShell is the shell's path.
	DetailShell = "shell"
)

// Shell is a user's login shell. Applying it runs usermod as root, once
// it's checked that the shell is listed in /etc/shells: installing a shell's
// package usually lists it, so that's only checked when applying, after
// packages are installed.
type Shell struct {
	// User is whose login shell it is. Empty means the user maisys runs
	// as.
	User string
	// Path is the shell, such as "/usr/bin/zsh".
	Path string
	// ShellsFile is the list of login shells. Empty means /etc/shells.
	ShellsFile string
}

var _ resource.Resource = (*Shell)(nil)

// ID returns "login-shell".
func (s *Shell) ID() string { return Kind }

// Kind returns "login-shell".
func (s *Shell) Kind() string { return Kind }

// Targets returns nothing: the shell is set in the user database.
func (s *Shell) Targets() []string { return nil }

// Privileged returns true: usermod runs as root.
func (s *Shell) Privileged() bool { return true }

// Check compares the user's login shell with Path.
func (s *Shell) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	_, current, err := s.current(ctx, rt.Runner)
	if err != nil {
		return resource.Change{}, err
	}
	if same(current, s.Path) {
		return resource.Change{}, nil
	}
	return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("login shell %s → %s", current, s.Path)}, nil
}

// Apply sets the user's login shell to Path, if it's listed in
// /etc/shells.
func (s *Shell) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	name, current, err := s.current(ctx, rt.Runner)
	if err != nil {
		return state.Record{}, err
	}
	rec := state.Record{Details: map[string]string{DetailUser: name, DetailShell: s.Path}}
	if same(current, s.Path) {
		return rec, nil
	}
	if err := s.listed(); err != nil {
		return state.Record{}, err
	}
	_, err = rt.Runner.Run(ctx, system.Cmd{
		Name:  "usermod",
		Args:  []string{"-s", s.Path, name},
		Root:  true,
		Label: "setting the login shell",
	})
	if err != nil {
		return state.Record{}, err
	}
	return rec, nil
}

// current returns the user's name and login shell.
func (s *Shell) current(ctx context.Context, r system.Runner) (name, shell string, err error) {
	if s.Path == "" || !strings.HasPrefix(s.Path, "/") {
		return "", "", fmt.Errorf("the login shell must be an absolute path, not %q", s.Path)
	}
	name = s.User
	if name == "" {
		u, err := user.Current()
		if err != nil {
			return "", "", fmt.Errorf("finding your user name: %w", err)
		}
		name = u.Username
	}
	res, err := r.Run(ctx, system.Cmd{Name: "getent", Args: []string{"passwd", name}, AllowExit: []int{2}})
	if err != nil {
		return "", "", err
	}
	fields := strings.Split(strings.TrimSpace(firstLine(res.Stdout)), ":")
	if res.ExitCode == 2 || len(fields) < 7 {
		return "", "", fmt.Errorf("user %s isn't in the user database", name)
	}
	shell = fields[6]
	if shell == "" {
		// An empty shell means the default one.
		shell = "/bin/sh"
	}
	return name, shell, nil
}

// listed returns an error unless the shell is listed in /etc/shells.
func (s *Shell) listed() error {
	name := s.ShellsFile
	if name == "" {
		name = ShellsFile
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return fmt.Errorf("reading the list of login shells: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line == s.Path {
			return nil
		}
	}
	return fmt.Errorf("%s isn't listed in %s, so it can't be a login shell", s.Path, name)
}

// same reports whether a and b are the same shell: the same path, or the
// same name in folders that are the same, such as /bin/zsh and
// /usr/bin/zsh where /bin links to /usr/bin. (/bin/sh and /bin/bash may be
// the same file, but they aren't the same shell.)
func same(a, b string) bool {
	if a == b {
		return true
	}
	if filepath.Base(a) != filepath.Base(b) {
		return false
	}
	ai, err := os.Stat(filepath.Dir(a))
	if err != nil {
		return false
	}
	bi, err := os.Stat(filepath.Dir(b))
	return err == nil && os.SameFile(ai, bi)
}

// firstLine returns the first line of out.
func firstLine(out []byte) string {
	line, _, _ := bytes.Cut(out, []byte("\n"))
	return string(line)
}

// Remover handles login shell records once the login shell is taken out of
// the config: the shell is left as it is, and the record is dropped
// without a line in the plan.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "login-shell".
func (Remover) Kind() string { return Kind }

// CheckRemove returns no change: the login shell is left as it is.
func (Remover) CheckRemove(context.Context, *resource.Runtime, state.Record) (resource.Change, error) {
	return resource.Change{}, nil
}

// Remove does nothing.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }
