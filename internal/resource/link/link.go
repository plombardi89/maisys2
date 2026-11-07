// Package link manages symlinks, such as the ones in ~/.local/bin that
// point at the active version of each tool.
package link

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
)

// Kind is the kind of resource a Link is.
const Kind = "symlink"

// DetailTarget is the key of a link record's Details that holds what the
// link points at.
const DetailTarget = "target"

// Link is a symlink. Its check reports a missing link, one that points
// somewhere else, and a file in its place, which applying backs up to
// <path>.maisys-bak.<timestamp> before replacing it with the link.
type Link struct {
	// Path is where the link goes: "~/..." in your home folder, or an
	// absolute path.
	Path string
	// Target is what the link points at: "~/..." in your home folder, an
	// absolute path, or a path relative to the link's folder.
	Target string
}

var _ resource.Resource = (*Link)(nil)

// ID returns the link's ID, such as "symlink:~/.local/bin/go".
func (l *Link) ID() string { return Kind + ":" + file.Display(l.Path) }

// Kind returns "symlink".
func (l *Link) Kind() string { return Kind }

// Targets returns the link's path, as file.Display shows it.
func (l *Link) Targets() []string { return []string{file.Display(l.Path)} }

// Privileged returns false: links are made as you.
func (l *Link) Privileged() bool { return false }

// Check compares the link with what it should point at.
func (l *Link) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	in, err := l.inspect(rt)
	if err != nil {
		return resource.Change{}, err
	}
	return in.change(), nil
}

// Apply makes the link, replacing what's at its path: a link that points
// elsewhere, or a file, which it backs up first. The link is made under a
// temporary name and renamed into place, so the path is never missing.
func (l *Link) Apply(_ context.Context, rt *resource.Runtime) (state.Record, error) {
	in, err := l.inspect(rt)
	if err != nil {
		return state.Record{}, err
	}
	rec := state.Record{
		Paths:   []string{in.display},
		Details: map[string]string{DetailTarget: in.target},
	}
	if in.change().Action == resource.NoChange {
		return rec, nil
	}
	if in.file {
		now := time.Now
		if rt.Now != nil {
			now = rt.Now
		}
		if _, err := file.Backup(in.path, now()); err != nil {
			return state.Record{}, err
		}
	}
	dir := filepath.Dir(in.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return state.Record{}, fmt.Errorf("making %s: %w", in.display, err)
	}
	tmp := filepath.Join(dir, "."+filepath.Base(in.path)+".maisys-link.tmp")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return state.Record{}, fmt.Errorf("making %s: %w", in.display, err)
	}
	if err := os.Symlink(in.target, tmp); err != nil {
		return state.Record{}, fmt.Errorf("making %s: %w", in.display, err)
	}
	if err := os.Rename(tmp, in.path); err != nil {
		_ = os.Remove(tmp)
		return state.Record{}, fmt.Errorf("making %s: %w", in.display, err)
	}
	return rec, nil
}

// inspection is what a link's check found.
type inspection struct {
	path    string // the link's absolute path
	display string // the link's path as the plan shows it
	target  string // what the link should point at
	shown   string // the target as the plan shows it
	exists  bool   // whether there's a link at path
	current string // what the link there points at
	was     string // current as the plan shows it
	file    bool   // whether there's a file at path instead
}

func (l *Link) inspect(rt *resource.Runtime) (*inspection, error) {
	home := rt.Paths.Home
	path, err := file.Expand(home, l.Path)
	if err != nil {
		return nil, err
	}
	in := &inspection{path: path, display: file.Display(l.Path), target: l.Target}
	if l.Target == "" {
		return nil, fmt.Errorf("%s has no target", in.display)
	}
	if !filepath.IsAbs(l.Target) {
		// A relative target stays relative, unless it's in the home folder.
		if expanded, err := file.Expand(home, l.Target); err == nil {
			in.target = expanded
		}
	}
	in.shown = file.Short(home, in.target)

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return in, nil
	case err != nil:
		return nil, fmt.Errorf("reading %s: %w", in.display, err)
	case info.Mode()&fs.ModeSymlink != 0:
		in.exists = true
		if in.current, err = os.Readlink(path); err != nil {
			return nil, fmt.Errorf("reading %s: %w", in.display, err)
		}
		in.was = file.Short(home, in.current)
	case info.Mode().IsRegular():
		in.file = true
	case info.IsDir():
		return nil, fmt.Errorf("%s is a folder, not a link", in.display)
	default:
		return nil, fmt.Errorf("%s isn't a link or a file", in.display)
	}
	return in, nil
}

// change returns what applying would do.
func (in *inspection) change() resource.Change {
	switch {
	case in.file:
		return resource.Change{Action: resource.Warn, Summary: in.display + " is a file (will back up and replace with a link to " + in.shown + ")"}
	case !in.exists:
		return resource.Change{Action: resource.Create, Summary: in.display + " → " + in.shown}
	case in.current != in.target:
		return resource.Change{Action: resource.Update, Summary: in.display + " → " + in.shown + " (was " + in.was + ")"}
	default:
		return resource.Change{}
	}
}

// Remover deletes links once they're taken out of the config, if they
// still point where maisys made them point. Links changed since are left
// in place, with a note in the plan.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "symlink".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the link rec records is deleted.
func (Remover) CheckRemove(_ context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	c, _, err := checkRemove(rt, rec)
	return c, err
}

// Remove deletes the link rec records, if it still points where maisys
// made it point.
func (Remover) Remove(_ context.Context, rt *resource.Runtime, rec state.Record) error {
	c, path, err := checkRemove(rt, rec)
	if err != nil || c.Action != resource.Remove {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("deleting %s: %w", c.Summary, err)
	}
	return nil
}

// checkRemove returns what removing the link rec records does, and the
// link's absolute path.
func checkRemove(rt *resource.Runtime, rec state.Record) (resource.Change, string, error) {
	if len(rec.Paths) == 0 {
		return resource.Change{}, "", errors.New("the state file's record has no path")
	}
	display := rec.Paths[0]
	path, err := file.Expand(rt.Paths.Home, display)
	if err != nil {
		return resource.Change{}, "", err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resource.Change{}, path, nil
	case err != nil:
		return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
	case info.Mode()&fs.ModeSymlink == 0:
		return leftInPlace(display), path, nil
	}
	current, err := os.Readlink(path)
	if err != nil {
		return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
	}
	if current != rec.Details[DetailTarget] {
		return leftInPlace(display), path, nil
	}
	return resource.Change{Action: resource.Remove, Summary: display}, path, nil
}

// leftInPlace is a removal that leaves what's at display alone, because
// it's not the link maisys made.
func leftInPlace(display string) resource.Change {
	return resource.Change{Notes: []string{display + " changed outside maisys (left in place)"}}
}
