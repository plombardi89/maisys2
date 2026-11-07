// Package unit manages systemd units: system units in
// /etc/systemd/system, written as root, and user units in
// ~/.config/systemd/user. A unit's file is kept as the file package keeps
// files, and the unit is enabled and started.
package unit

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Unit is.
const Kind = "systemd-unit"

// The folders unit files go in.
const (
	SystemDir = "/etc/systemd/system"
	UserDir   = "~/.config/systemd/user"
)

// The keys of a unit record's Details, besides the file's (see
// file.DetailHash).
const (
	// DetailName is the unit's name, such as "x410.service".
	DetailName = "unit"
	// DetailScope is "system" or "user".
	DetailScope = "scope"
)

// Unit is a systemd unit. Its check reports a unit file to write (with the
// file package's drift check) and a unit that isn't enabled or running.
// Applying writes the file, reloads systemd, and enables and starts the
// unit; a unit whose file changed is restarted.
type Unit struct {
	// Name is the unit's name, such as "x410.service".
	Name string
	// User makes it a user unit, managed with systemctl --user, rather
	// than a system unit, which needs root.
	User bool
	// Content returns the unit file's contents.
	Content file.Content
	// Dir is the folder the unit file goes in. Empty means
	// /etc/systemd/system, or ~/.config/systemd/user for a user unit.
	Dir string
}

var _ resource.Resource = (*Unit)(nil)

// ID returns the unit's ID, such as "systemd-unit:x410.service", or
// "systemd-unit:user/x410.service" for a user unit.
func (u *Unit) ID() string {
	if u.User {
		return Kind + ":user/" + u.Name
	}
	return Kind + ":" + u.Name
}

// Kind returns "systemd-unit".
func (u *Unit) Kind() string { return Kind }

// Targets returns the unit file's path.
func (u *Unit) Targets() []string { return u.file().Targets() }

// Privileged reports whether it's a system unit, which needs root.
func (u *Unit) Privileged() bool { return !u.User }

// file returns the unit's file.
func (u *Unit) file() *file.File {
	var p string
	switch {
	case u.Dir != "":
		p = filepath.Join(u.Dir, u.Name)
	case u.User:
		p = path.Join(UserDir, u.Name)
	default:
		p = path.Join(SystemDir, u.Name)
	}
	return &file.File{Path: p, Content: u.Content, Mode: 0o644, Root: !u.User, Key: u.ID()}
}

// describe returns how the plan names the unit.
func (u *Unit) describe() string {
	if u.User {
		return "systemd user unit " + u.Name
	}
	return "systemd unit " + u.Name
}

// Check compares the unit file, and whether the unit is enabled and
// running.
func (u *Unit) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := u.validate(); err != nil {
		return resource.Change{}, err
	}
	fc, err := u.file().Check(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	switch fc.Action {
	case resource.NoChange:
	case resource.Create:
		return resource.Change{Action: resource.Create, Summary: u.describe()}, nil
	case resource.Update:
		return resource.Change{Action: resource.Update, Summary: u.describe() + " (update the unit file)"}, nil
	default:
		return fc, nil
	}

	enabled, active, err := u.status(ctx, rt.Runner)
	if err != nil {
		return resource.Change{}, err
	}
	var todo string
	switch {
	case !enabled && !active:
		todo = "enable and start"
	case !enabled:
		todo = "enable"
	case !active:
		todo = "start"
	default:
		return resource.Change{}, nil
	}
	return resource.Change{Action: resource.Update, Summary: u.describe() + " (" + todo + ")"}, nil
}

// Apply writes the unit file if it needs writing, then reloads systemd
// and enables the unit and restarts it, or else enables and starts it.
func (u *Unit) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if err := u.validate(); err != nil {
		return state.Record{}, err
	}
	f := u.file()
	fc, err := f.Check(ctx, rt)
	if err != nil {
		return state.Record{}, err
	}
	rec, err := f.Apply(ctx, rt)
	if err != nil {
		return state.Record{}, err
	}
	rec.Details[DetailName] = u.Name
	rec.Details[DetailScope] = scope(u.User)

	steps := [][]string{{"enable", "--now", u.Name}}
	if fc.Action != resource.NoChange {
		steps = [][]string{{"daemon-reload"}, {"enable", u.Name}, {"restart", u.Name}}
	}
	for _, args := range steps {
		if _, err := rt.Runner.Run(ctx, systemctl(u.User, args...)); err != nil {
			return state.Record{}, err
		}
	}
	return rec, nil
}

func (u *Unit) validate() error {
	if u.Name == "" || strings.ContainsAny(u.Name, `/\`) || !strings.Contains(u.Name, ".") {
		return fmt.Errorf("%q isn't a systemd unit name, such as \"x410.service\"", u.Name)
	}
	return nil
}

// status reports whether the unit is enabled and running.
func (u *Unit) status(ctx context.Context, r system.Runner) (enabled, active bool, err error) {
	c := systemctl(u.User, "is-enabled", u.Name)
	c.AllowExit = []int{1}
	res, err := r.Run(ctx, c)
	if err != nil {
		return false, false, err
	}
	switch firstLine(res.Stdout) {
	// Static, indirect and generated units are started by something else,
	// and can't be enabled themselves.
	case "enabled", "static", "indirect", "generated", "alias":
		enabled = true
	}

	c = systemctl(u.User, "is-active", u.Name)
	c.AllowExit = []int{1, 2, 3, 4}
	if res, err = r.Run(ctx, c); err != nil {
		return false, false, err
	}
	return enabled, firstLine(res.Stdout) == "active", nil
}

// firstLine returns the first line of out, trimmed.
func firstLine(out []byte) string {
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line)
}

// scope returns "user" for a user unit, and "system" otherwise.
func scope(user bool) string {
	if user {
		return "user"
	}
	return "system"
}

// systemctl returns the systemctl command with args, for a user unit or a
// system unit, which runs as root.
func systemctl(user bool, args ...string) system.Cmd {
	c := system.Cmd{Name: "systemctl", Args: args, Root: !user, Label: "systemctl " + args[0]}
	if user {
		c.Args = append([]string{"--user"}, args...)
		c.Label = "systemctl --user " + args[0]
	}
	// Checks run as you: reading a unit's state doesn't need root.
	if args[0] == "is-enabled" || args[0] == "is-active" {
		c.Root = false
	}
	return c
}

// Remover stops, disables and deletes systemd units once they're taken out
// of the config, then reloads systemd.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "systemd-unit".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the unit rec records is still there.
func (Remover) CheckRemove(_ context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	name, user, p, err := recorded(rt, rec)
	if err != nil {
		return resource.Change{}, err
	}
	_, err = os.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resource.Change{}, nil
	case err != nil:
		return resource.Change{}, fmt.Errorf("reading %s: %w", rec.Paths[0], err)
	}
	u := Unit{Name: name, User: user}
	return resource.Change{Action: resource.Remove, Summary: u.describe(), Privileged: !user}, nil
}

// Remove stops and disables the unit rec records, deletes its file and
// reloads systemd.
func (Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	name, user, p, err := recorded(rt, rec)
	if err != nil {
		return err
	}
	if _, err := rt.Runner.Run(ctx, systemctl(user, "disable", "--now", name)); err != nil {
		return err
	}
	if user {
		err = os.Remove(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("deleting %s: %w", rec.Paths[0], err)
		}
	} else {
		_, err = rt.Runner.Run(ctx, system.Cmd{Name: "rm", Args: []string{"-f", "--", p}, Root: true, Label: "deleting " + p})
		if err != nil {
			return err
		}
	}
	_, err = rt.Runner.Run(ctx, systemctl(user, "daemon-reload"))
	return err
}

// recorded returns the name, scope and file of the unit rec records.
func recorded(rt *resource.Runtime, rec state.Record) (name string, user bool, unitFile string, err error) {
	name = rec.Details[DetailName]
	if name == "" || len(rec.Paths) == 0 {
		return "", false, "", errors.New("the state file's record has no unit name or file")
	}
	unitFile, err = file.Expand(rt.Paths.Home, rec.Paths[0])
	return name, rec.Details[DetailScope] == "user", unitFile, err
}
