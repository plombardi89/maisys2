// Package cleanup deletes files and folders left behind by something maisys
// replaces, such as the Ansible setup the legacy module cleans up after.
// The plan lists each thing it deletes, so it's confirmed first.
package cleanup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Leftover is.
const Kind = "cleanup"

// Leftover is a file or folder to delete if it's there. Its check reports
// "- PATH" while it's there, and applying deletes it, with everything in
// it. A symlink at the path is deleted, not what it points to. But if a
// folder the path is in is a symlink (one below the home folder, for a
// path in it), the path is left in place with a note, so nothing is
// deleted from wherever the symlink points, such as a dotfiles repo.
type Leftover struct {
	// Path is the file or folder: "~/..." in your home folder, or an
	// absolute path.
	Path string
	// Root deletes it as root, for a path outside your home folder.
	Root bool
	// SHA256, if it isn't empty, lists the sha256 sums in hex the file may
	// have: a file with other contents, a folder or a symlink is left
	// alone.
	SHA256 []string
}

var _ resource.Resource = (*Leftover)(nil)

// ID returns the leftover's ID, such as "cleanup:/usr/local/go".
func (l *Leftover) ID() string { return Kind + ":" + file.Display(l.Path) }

// Kind returns "cleanup".
func (l *Leftover) Kind() string { return Kind }

// Targets returns nothing: the path isn't kept, but deleted.
func (l *Leftover) Targets() []string { return nil }

// Privileged reports whether it's deleted as root.
func (l *Leftover) Privileged() bool { return l.Root }

// Check reports whether the leftover is there to delete.
func (l *Leftover) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	c, _, err := l.check(rt)
	return c, err
}

// Apply deletes the leftover, if it's there.
func (l *Leftover) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	c, path, err := l.check(rt)
	if err != nil || c.Action != resource.Remove {
		return state.Record{Paths: []string{file.Display(l.Path)}}, err
	}
	if l.Root {
		_, err = rt.Runner.Run(ctx, system.Cmd{Name: "rm", Args: []string{"-rf", "--", path}, Root: true, Label: "deleting " + file.Display(l.Path)})
	} else if err = os.RemoveAll(path); err != nil {
		err = fmt.Errorf("deleting %s: %w", file.Display(l.Path), err)
	}
	if err != nil {
		return state.Record{}, err
	}
	return state.Record{Paths: []string{file.Display(l.Path)}}, nil
}

// check returns what deleting the leftover does, and its absolute path.
func (l *Leftover) check(rt *resource.Runtime) (resource.Change, string, error) {
	display := file.Display(l.Path)
	path, err := file.Expand(rt.Paths.Home, l.Path)
	if err != nil {
		return resource.Change{}, "", err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resource.Change{}, path, nil
	case err != nil:
		return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
	}
	if len(l.SHA256) > 0 {
		if !info.Mode().IsRegular() {
			return resource.Change{}, path, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
		}
		if sum := sha256.Sum256(data); !slices.Contains(l.SHA256, hex.EncodeToString(sum[:])) {
			return resource.Change{}, path, nil
		}
	}
	link, err := symlinkedFolder(rt.Paths.Home, path)
	if err != nil {
		return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
	}
	if link != "" {
		note := fmt.Sprintf("%s is in %s, which is a symlink (left in place)", display, file.Short(rt.Paths.Home, link))
		return resource.Change{Notes: []string{note}}, path, nil
	}
	return resource.Change{Action: resource.Remove, Summary: display}, path, nil
}

// symlinkedFolder returns the first of the folders path is in that's a
// symlink, or "" if none is. It looks at those below home if path is in
// it, and at those below the root otherwise.
func symlinkedFolder(home, path string) (string, error) {
	base := filepath.VolumeName(path) + string(filepath.Separator)
	if rel, err := filepath.Rel(home, path); home != "" && err == nil && filepath.IsLocal(rel) {
		base = home
	}
	rel, err := filepath.Rel(base, filepath.Dir(path))
	if err != nil || rel == "." {
		return "", err
	}
	dir := base
	for name := range strings.SplitSeq(rel, string(filepath.Separator)) {
		dir = filepath.Join(dir, name)
		info, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return dir, nil
		}
	}
	return "", nil
}

// Remover handles the records of leftovers once they're taken out of the
// config, such as when the legacy module is turned off: there's nothing to
// undo, so their records are dropped without a line in the plan.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "cleanup".
func (Remover) Kind() string { return Kind }

// CheckRemove returns no change.
func (Remover) CheckRemove(context.Context, *resource.Runtime, state.Record) (resource.Change, error) {
	return resource.Change{}, nil
}

// Remove does nothing.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }
