package tool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/state"
)

// Remover deletes the tools taken out of the config: every version of the
// tool, and its commands in ~/.local/bin that still point into its folder.
// A command that now points somewhere else is left alone.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns Kind.
func (Remover) Kind() string { return Kind }

// CheckRemove says the tool will be removed, such as "kubectl 1.36.0", or
// that there's nothing left to remove.
func (Remover) CheckRemove(_ context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	dir, links, err := recorded(rt, rec)
	if err != nil {
		return resource.Change{}, err
	}
	_, statErr := os.Lstat(dir)
	switch {
	case statErr == nil:
	case !errors.Is(statErr, fs.ErrNotExist):
		return resource.Change{}, statErr
	case !anyInto(links, dir):
		return resource.Change{Action: resource.NoChange}, nil
	}
	name := strings.TrimPrefix(rec.ID, Kind+":")
	return resource.Change{Action: resource.Remove, Summary: describe(name, rec.Details["version"])}, nil
}

// Remove deletes the tool's commands that point into its folder, then the
// folder.
func (Remover) Remove(_ context.Context, rt *resource.Runtime, rec state.Record) error {
	dir, links, err := recorded(rt, rec)
	if err != nil {
		return err
	}
	for _, path := range links {
		if !pointsInto(path, dir) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("deleting %s: %w", display(rt, path), err)
		}
	}
	if err := download.RemoveAll(dir); err != nil {
		return fmt.Errorf("deleting %s: %w", display(rt, dir), err)
	}
	return nil
}

// recorded returns the folder of the tool rec records, and its commands. The
// record's first path is the folder of the version in use; the folder must
// be one of maisys's tool folders, so a damaged state file can't make
// maisys delete anything else.
func recorded(rt *resource.Runtime, rec state.Record) (string, []string, error) {
	if len(rec.Paths) == 0 {
		return "", nil, fmt.Errorf("the state file's record of %s has no paths", rec.ID)
	}
	dir := filepath.Dir(filepath.Clean(rec.Paths[0]))
	if rt == nil || rt.Paths.Home == "" || filepath.Dir(dir) != ToolsDir(rt.Paths.Home) || !validName(filepath.Base(dir)) {
		return "", nil, fmt.Errorf("the state file's record of %s points at %s, which isn't a maisys tool's folder: not deleting it", rec.ID, rec.Paths[0])
	}
	return dir, rec.Paths[1:], nil
}

// anyInto reports whether any of paths is a symlink into dir.
func anyInto(paths []string, dir string) bool {
	for _, p := range paths {
		if pointsInto(p, dir) {
			return true
		}
	}
	return false
}

// pointsInto reports whether path is a symlink to something in dir.
func pointsInto(path, dir string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	_, ok := inside(target, dir)
	return ok
}
