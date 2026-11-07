// Package sudoers manages the rule in /etc/sudoers.d that lets a user run
// sudo without a password. The rule is checked with visudo -cf before it's
// put in place. Once it's taken out of the config, it's deleted only if the
// user can still use sudo without it, being in the wheel (Fedora) or sudo
// (Ubuntu) group: maisys never takes away your sudo access.
//
// Only root may read /etc/sudoers.d, so the rule is read with sudo. While
// sudo needs a password, a plan says it's checked during apply.
package sudoers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Rule is.
const Kind = "sudoers"

// Dir is the folder sudo reads extra rules from.
const Dir = "/etc/sudoers.d"

// The keys of a rule record's Details.
const (
	// DetailUser is the user the rule is for.
	DetailUser = "user"
	// DetailHash is the sha256 of the rule maisys wrote, in hex.
	DetailHash = "sha256"
)

// mode is the permissions sudo wants its files to have.
const mode = 0o440

// validUser matches the user names maisys writes rules for.
var validUser = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Rule is the rule that lets User run every command as root without a
// password, in /etc/sudoers.d/USER. Its check reports:
//
//   - "+ /etc/sudoers.d/USER (passwordless sudo for USER)" if it's missing;
//   - "~ ..." if maisys wrote it and it should change, or its permissions
//     aren't 0440 and owned by root;
//   - "! ... changed outside maisys (will back up and overwrite)" or
//     "! ... exists (will back up and replace)" for a file maisys didn't
//     write.
//
// Applying backs a file maisys didn't write up to
// <file>.maisys-bak.<timestamp>, which sudo ignores because its name has a
// dot in it, then checks the new rule with visudo -cf and puts it in place.
type Rule struct {
	// User is the user who may run sudo without a password.
	User string
	// Dir is the folder the rule goes in. Empty means /etc/sudoers.d.
	Dir string
}

var _ resource.Resource = (*Rule)(nil)

// Content returns the rule for user.
func Content(user string) []byte {
	return []byte(user + " ALL=(ALL) NOPASSWD: ALL\n")
}

// FileName returns the name of the file in /etc/sudoers.d that holds the
// rule for user: the user's name, with dots turned into underscores,
// because sudo skips files with a dot in their name.
func FileName(user string) string {
	return strings.ReplaceAll(user, ".", "_")
}

// ID returns the rule's ID, such as "sudoers:phil".
func (r *Rule) ID() string { return Kind + ":" + r.User }

// Kind returns "sudoers".
func (r *Rule) Kind() string { return Kind }

// Targets returns the rule's file.
func (r *Rule) Targets() []string { return []string{r.path()} }

// Privileged returns true: the file belongs to root.
func (r *Rule) Privileged() bool { return true }

func (r *Rule) path() string {
	dir := r.Dir
	if dir == "" {
		dir = Dir
	}
	return filepath.Join(dir, FileName(r.User))
}

// describe returns how the plan names the rule.
func (r *Rule) describe() string {
	return fmt.Sprintf("%s (passwordless sudo for %s)", r.path(), r.User)
}

// Check reads the rule's file as root and compares it with the rule.
func (r *Rule) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	in, err := r.inspect(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	return in.change(r), nil
}

// Apply writes the rule, once visudo has checked it, backing up what's
// there first if maisys didn't write it. It returns the rule's record: the
// user and the hash of what it wrote.
func (r *Rule) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	in, err := r.inspect(ctx, rt)
	if err != nil {
		return state.Record{}, err
	}
	rec := state.Record{
		Paths:    []string{in.path},
		Details:  map[string]string{DetailUser: r.User, DetailHash: hash(in.want)},
		TookOver: in.prev.TookOver || in.exists && !in.recorded(),
	}
	if in.change(r).Action == resource.NoChange {
		return rec, nil
	}
	if in.needsBackup() {
		now := time.Now
		if rt.Now != nil {
			now = rt.Now
		}
		backup := in.path + ".maisys-bak." + now().Format("20060102-150405")
		if _, err := rt.Runner.Run(ctx, system.Cmd{Name: "cp", Args: []string{"-p", "--", in.path, backup}, Root: true, Label: "backing up " + in.path}); err != nil {
			return state.Record{}, err
		}
	}
	err = system.InstallFileChecked(ctx, rt.Runner, in.want, in.path, mode, func(ctx context.Context, tmp string) error {
		_, err := rt.Runner.Run(ctx, system.Cmd{Name: "visudo", Args: []string{"-cf", tmp}, Root: true, Label: "checking the sudoers rule with visudo"})
		return err
	})
	if err != nil {
		return state.Record{}, err
	}
	return rec, nil
}

// inspection is what a rule's check found.
type inspection struct {
	path   string
	want   []byte
	exists bool
	cur    []byte
	// perms is the file's permissions and owner, as "440 root:root".
	perms string
	prev  state.Record
}

func (r *Rule) inspect(ctx context.Context, rt *resource.Runtime) (*inspection, error) {
	if !validUser.MatchString(r.User) {
		return nil, fmt.Errorf("can't write a sudoers rule for the user %q", r.User)
	}
	in := &inspection{path: r.path(), want: Content(r.User)}
	if rt.Previous != nil {
		in.prev, _ = rt.Previous(r.ID())
	}
	var err error
	in.cur, in.perms, in.exists, err = read(ctx, rt.Runner, in.path)
	if err != nil {
		return nil, err
	}
	return in, nil
}

// recorded reports whether there's a record of what maisys last wrote.
func (in *inspection) recorded() bool {
	return in.prev.Details[DetailHash] != ""
}

// needsBackup reports whether applying overwrites a rule maisys didn't
// write.
func (in *inspection) needsBackup() bool {
	return in.exists && !bytes.Equal(in.cur, in.want) && (!in.recorded() || hash(in.cur) != in.prev.Details[DetailHash])
}

// change returns what applying would do.
func (in *inspection) change(r *Rule) resource.Change {
	switch {
	case !in.exists:
		return resource.Change{Action: resource.Create, Summary: r.describe()}
	case bytes.Equal(in.cur, in.want) && in.perms != wantPerms():
		return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("%s (permissions %s → %s)", r.describe(), in.perms, wantPerms())}
	case bytes.Equal(in.cur, in.want):
		return resource.Change{}
	case !in.needsBackup():
		return resource.Change{Action: resource.Update, Summary: r.describe()}
	case in.recorded():
		return resource.Change{Action: resource.Warn, Summary: in.path + " changed outside maisys (will back up and overwrite)"}
	default:
		return resource.Change{Action: resource.Warn, Summary: in.path + " exists (will back up and replace)"}
	}
}

// wantPerms returns the permissions and owner a rule's file should have,
// as read reports them.
func wantPerms() string {
	return fmt.Sprintf("%o root:root", mode)
}

// read reads the file at path as root, and returns what it holds, its
// permissions and owner (as "440 root:root"), and whether it exists.
func read(ctx context.Context, r system.Runner, path string) (data []byte, perms string, exists bool, err error) {
	res, err := r.Run(ctx, system.Cmd{Name: "test", Args: []string{"-e", path}, Root: true, AllowExit: []int{1}, Label: "checking " + path})
	if err != nil || res.ExitCode != 0 {
		return nil, "", false, err
	}
	res, err = r.Run(ctx, system.Cmd{Name: "stat", Args: []string{"-c", "%a %U:%G", "--", path}, Root: true, Label: "checking " + path})
	if err != nil {
		return nil, "", false, err
	}
	perms = strings.TrimSpace(string(res.Stdout))
	res, err = r.Run(ctx, system.Cmd{Name: "cat", Args: []string{"--", path}, Root: true, Label: "reading " + path})
	if err != nil {
		return nil, "", false, err
	}
	return res.Stdout, perms, true, nil
}

// hash returns the sha256 of data, in hex.
func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Remover deletes rules once they're taken out of the config, such as when
// sudo.nopasswd is turned off. A rule changed since maisys wrote it is left
// in place, and so is one for a user who isn't in the wheel (Fedora) or
// sudo (Ubuntu) group, because deleting it would take away their sudo
// access: the plan warns about it, and its record is kept, so a later sync
// deletes it once the user is in the group. The engine removes sudoers
// rules after everything else that needs root.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "sudoers".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the rule rec records is deleted or left in
// place.
func (Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	c, _, err := checkRemove(ctx, rt, rec)
	return c, err
}

// Remove deletes the rule rec records, if it's still the one maisys wrote
// and the user can use sudo without it. A rule that's left in place for
// the user's sake is an error, so its record is kept.
func (Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	c, path, err := checkRemove(ctx, rt, rec)
	switch {
	case err != nil:
		return err
	case c.Action == resource.Orphan:
		return errors.New(strings.Join(c.Notes, " "))
	case c.Action != resource.Remove:
		return nil
	}
	_, err = rt.Runner.Run(ctx, system.Cmd{Name: "rm", Args: []string{"-f", "--", path}, Root: true, Label: "deleting " + path})
	return err
}

// checkRemove returns what removing the rule rec records does, and its
// file.
func checkRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, string, error) {
	user := rec.Details[DetailUser]
	if user == "" {
		_, user, _ = strings.Cut(rec.ID, ":")
	}
	if len(rec.Paths) == 0 || user == "" {
		return resource.Change{}, "", errors.New("the state file's record has no path or user")
	}
	path := rec.Paths[0]
	data, _, exists, err := read(ctx, rt.Runner, path)
	switch {
	case err != nil:
		return resource.Change{}, "", err
	case !exists:
		return resource.Change{}, path, nil
	case hash(data) != rec.Details[DetailHash]:
		return resource.Change{Notes: []string{path + " changed outside maisys (left in place)"}}, path, nil
	}

	groups := sudoGroups(rt)
	in, err := inGroup(ctx, rt.Runner, user, groups)
	if err != nil {
		return resource.Change{}, "", err
	}
	summary := fmt.Sprintf("%s (passwordless sudo for %s)", path, user)
	if !in {
		note := fmt.Sprintf("%s is left in place: %s isn't in the %s group, so deleting it would take away their sudo access. Add them to it as root (usermod -aG %s %s), and the next sync deletes it",
			path, user, strings.Join(groups, " or "), groups[0], user)
		return resource.Change{Action: resource.Orphan, Summary: summary, Notes: []string{note}}, path, nil
	}
	return resource.Change{Action: resource.Remove, Summary: summary, Privileged: true}, path, nil
}

// sudoGroups returns the groups that may use sudo on the machine: wheel on
// Fedora and sudo on Ubuntu, or either elsewhere.
func sudoGroups(rt *resource.Runtime) []string {
	if rt.Facts != nil {
		switch rt.Facts.OS {
		case config.Fedora:
			return []string{"wheel"}
		case config.Ubuntu:
			return []string{"sudo"}
		default:
		}
	}
	return []string{"wheel", "sudo"}
}

// inGroup reports whether user is in one of groups.
func inGroup(ctx context.Context, r system.Runner, user string, groups []string) (bool, error) {
	res, err := r.Run(ctx, system.Cmd{Name: "id", Args: []string{"-Gn", user}, Label: "finding " + user + "'s groups"})
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(strings.Fields(string(res.Stdout)), func(g string) bool { return slices.Contains(groups, g) }), nil
}
