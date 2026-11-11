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
	"slices"
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
	// DetailPending is "true" if maisys wrote the unit file while it
	// couldn't reach systemd, until a sync that can reach it reloads
	// systemd and restarts the unit, so it runs what the file now says.
	DetailPending = "pending"
)

// bootedDir is there while systemd runs the system, as sd_booted(3)
// checks. Tests replace it.
var bootedDir = "/run/systemd/system"

// Unit is a systemd unit. Its check reports a unit file to write (with the
// file package's drift check) and a unit that isn't enabled or running; a
// oneshot service without RemainAfterExit, which is inactive once it's
// run, only needs enabling, unless it failed. Applying writes the file,
// reloads systemd, and enables and starts the unit; a unit whose file
// changed is restarted.
//
// While maisys can't reach systemd, because it isn't running the system,
// as in WSL until /etc/wsl.conf's [boot] systemd takes effect, or, for a
// user unit, because this session can't reach your user manager, as under
// podman exec, applying only writes the file. The plan says the next sync
// that can reach systemd enables and starts the unit, and if the file
// changed, the record says so (DetailPending), so that sync reloads
// systemd and restarts the unit too.
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
// running. If both are right but the state file's record of the unit file
// is missing or out of date, the change has the record applying would
// leave. It fails if systemctl doesn't print the unit's state, as when it
// can't reach systemd. While maisys can't reach systemd, it only compares
// the file, and notes that the unit is enabled and started later.
func (u *Unit) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if err := u.validate(); err != nil {
		return resource.Change{}, err
	}
	fc, err := u.file().Check(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	var c resource.Change
	switch fc.Action {
	case resource.NoChange:
	case resource.Create:
		c = resource.Change{Action: resource.Create, Summary: u.describe()}
	case resource.Update:
		c = resource.Change{Action: resource.Update, Summary: u.describe() + " (update the unit file)"}
	default:
		c = fc
	}
	if when := waiting(u.User); when != "" {
		c.Notes = append(c.Notes, when+" enables and starts "+u.Name)
		if fc.Record != nil {
			u.details(fc.Record, u.pending(rt))
			c.Record = fc.Record
		}
		return c, nil
	}
	if fc.Action != resource.NoChange {
		return c, nil
	}

	enabled, started, err := u.status(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	var todo string
	switch {
	case !enabled && !started:
		todo = "enable and start"
	case !enabled:
		todo = "enable"
	case !started:
		todo = "start"
	case u.pending(rt):
		todo = "restart with its new unit file"
	default:
		if fc.Record != nil {
			u.details(fc.Record, false)
		}
		return resource.Change{Record: fc.Record}, nil
	}
	return resource.Change{Action: resource.Update, Summary: u.describe() + " (" + todo + ")"}, nil
}

// details adds the unit's name and scope to rec, its file's record, and
// whether systemd is still to be told about the file.
func (u *Unit) details(rec *state.Record, pending bool) {
	rec.Details[DetailName] = u.Name
	rec.Details[DetailScope] = scope(u.User)
	if pending {
		rec.Details[DetailPending] = "true"
	}
}

// pending reports whether the state file's record says systemd is still
// to be told about the unit file maisys wrote.
func (u *Unit) pending(rt *resource.Runtime) bool {
	if rt.Previous == nil {
		return false
	}
	rec, ok := rt.Previous(u.ID())
	return ok && rec.Details[DetailPending] == "true"
}

// Apply writes the unit file if it needs writing, then reloads systemd
// and enables the unit and restarts it, or else enables and starts it.
// While maisys can't reach systemd, it only writes the file.
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
	reload := fc.Action != resource.NoChange || u.pending(rt)
	if waiting(u.User) != "" {
		u.details(&rec, reload)
		return rec, nil
	}
	u.details(&rec, false)

	steps := [][]string{{"enable", "--now", u.Name}}
	if reload {
		steps = [][]string{{"daemon-reload"}, {"enable", u.Name}, {"restart", u.Name}}
	}
	for _, args := range steps {
		if _, err := rt.Runner.Run(ctx, systemctl(u.User, args...)); err != nil {
			return state.Record{}, err
		}
	}
	return rec, nil
}

// waiting returns why maisys can't reach systemd to manage a unit (a user
// unit if user is set) and which sync will, such as "systemd isn't
// running: once it is, the next maisys sync", or "" if it can reach it.
// systemd doesn't run the system in WSL until /etc/wsl.conf's [boot]
// systemd takes effect, nor in most containers. systemctl --user reaches
// your user manager at $XDG_RUNTIME_DIR/systemd/private, which a session
// without a login, such as podman exec's, doesn't have.
func waiting(user bool) string {
	if info, err := os.Stat(bootedDir); err != nil || !info.IsDir() {
		return "systemd isn't running: once it is, the next maisys sync"
	}
	if !user {
		return ""
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		if _, err := os.Stat(filepath.Join(dir, "systemd", "private")); err == nil {
			return ""
		}
	}
	return "this session can't reach your systemd user manager: the next maisys sync run in a login session"
}

func (u *Unit) validate() error {
	if u.Name == "" || strings.ContainsAny(u.Name, `/\`) || !strings.Contains(u.Name, ".") {
		return fmt.Errorf("%q isn't a systemd unit name, such as \"x410.service\"", u.Name)
	}
	return nil
}

// The states systemctl is-enabled and is-active print, as systemctl
// --state=help lists them, with "not-found", which is-enabled prints for a
// unit without a file.
var (
	unitFileStates = []string{
		"enabled", "enabled-runtime", "linked", "linked-runtime", "alias", "masked", "masked-runtime",
		"static", "disabled", "indirect", "generated", "transient", "bad", "not-found",
	}
	activeStates = []string{"active", "reloading", "refreshing", "inactive", "failed", "activating", "deactivating", "maintenance"}
)

// status reports whether the unit is enabled, and whether it's started:
// running, or, for an enabled oneshot service (see oneshot), run without
// failing, which leaves it inactive.
func (u *Unit) status(ctx context.Context, rt *resource.Runtime) (enabled, started bool, err error) {
	state, err := u.state(ctx, rt.Runner, "is-enabled", unitFileStates, "enabled")
	if err != nil {
		return false, false, err
	}
	switch state {
	// Static, indirect and generated units are started by something else,
	// and can't be enabled themselves.
	case "enabled", "static", "indirect", "generated", "alias":
		enabled = true
	}

	if state, err = u.state(ctx, rt.Runner, "is-active", activeStates, "active"); err != nil {
		return false, false, err
	}
	switch state {
	case "active", "reloading", "refreshing":
		started = true
	case "inactive":
		// An enabled oneshot service ran when maisys enabled it, or at boot.
		if enabled {
			content, err := u.Content(rt)
			if err != nil {
				return false, false, err
			}
			started = oneshot(u.Name, content)
		}
	}
	return enabled, started, nil
}

// oneshot reports whether content is the unit file of a service of Type
// oneshot without RemainAfterExit, which is inactive again once it's run,
// as systemd reads it: the last value of a key in [Service] counts, an
// empty one resets it, and a service without Type or ExecStart is a
// oneshot.
func oneshot(name string, content []byte) bool {
	if !strings.HasSuffix(name, ".service") {
		return false
	}
	settings := serviceSettings(content)
	typ := settings["Type"]
	if typ == "" && settings["ExecStart"] == "" {
		typ = "oneshot"
	}
	switch strings.ToLower(settings["RemainAfterExit"]) {
	case "1", "yes", "y", "true", "t", "on":
		return false
	}
	return typ == "oneshot"
}

// serviceSettings returns the last value of each key in the [Service]
// section of the unit file content. Lines starting with # or ; are
// comments, and a line ending with a backslash goes on in the next one.
func serviceSettings(content []byte) map[string]string {
	settings := map[string]string{}
	var section, continued string
	for line := range strings.Lines(string(content)) {
		line = strings.TrimSpace(line)
		if line == "" && continued == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if start, ok := strings.CutSuffix(line, `\`); ok {
			continued += start + " "
			continue
		}
		line, continued = continued+line, ""
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && section == "Service" {
			settings[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return settings
}

// state runs systemctl's verb, is-enabled or is-active, for the unit, and
// returns the state it prints: one of known, or else, if it exits with 0,
// ok, the state that means, for a state newer than maisys. Anything else,
// such as systemctl's error when it can't reach systemd, is an error that
// says what systemctl printed, so the plan doesn't count on systemctl
// working when it's applied.
func (u *Unit) state(ctx context.Context, r system.Runner, verb string, known []string, ok string) (string, error) {
	c := systemctl(u.User, verb, u.Name)
	c.AllowExit = []int{1, 2, 3, 4}
	res, err := r.Run(ctx, c)
	if err != nil {
		return "", err
	}
	state := firstLine(res.Stdout)
	switch {
	case slices.Contains(known, state):
		return state, nil
	case res.ExitCode == 0:
		return ok, nil
	}
	c.AllowExit = nil
	return "", system.CheckExit(c, res)
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
// of the config, then reloads systemd. While maisys can't reach systemd
// (see Unit), it leaves a unit, and its record, to the next sync that can:
// deleting only the file would leave the unit enabled, or running under a
// user manager this session can't reach, with nothing to stop it later.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "systemd-unit".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the unit rec records is still there, and if
// maisys can't reach systemd to remove it, notes when it's removed.
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
	if when := waiting(user); when != "" {
		return resource.Change{Action: resource.Orphan, Summary: u.describe(), Notes: []string{when + " stops, disables and deletes " + name}}, nil
	}
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
