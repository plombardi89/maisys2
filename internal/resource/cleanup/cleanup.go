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
	"slices"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Leftover is.
const Kind = "cleanup"

// Leftover is a file or folder to delete if it's there. Its check reports
// "- PATH" while it's there, and applying deletes it, with everything in
// it.
type Leftover struct {
	// Path is the file or folder: "~/..." in your home folder, or an
	// absolute path.
	Path string
	// Root deletes it as root, for a path outside your home folder.
	Root bool
	// SHA256, if it isn't empty, lists the sha256 sums in hex the file may
	// have: a file with other contents, or a folder, is left alone.
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
	_, there, err := l.inspect(rt)
	if err != nil || !there {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Remove, Summary: file.Display(l.Path)}, nil
}

// Apply deletes the leftover, if it's there.
func (l *Leftover) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	path, there, err := l.inspect(rt)
	if err != nil || !there {
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

// inspect returns the leftover's absolute path, and whether it's there to
// delete.
func (l *Leftover) inspect(rt *resource.Runtime) (string, bool, error) {
	path, err := file.Expand(rt.Paths.Home, l.Path)
	if err != nil {
		return "", false, err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return path, false, nil
	case err != nil:
		return "", false, fmt.Errorf("reading %s: %w", file.Display(l.Path), err)
	case len(l.SHA256) == 0:
		return path, true, nil
	case !info.Mode().IsRegular():
		return path, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("reading %s: %w", file.Display(l.Path), err)
	}
	sum := sha256.Sum256(data)
	return path, slices.Contains(l.SHA256, hex.EncodeToString(sum[:])), nil
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
