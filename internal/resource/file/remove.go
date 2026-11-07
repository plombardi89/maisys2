package file

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Remover removes files once they're taken out of the config, following
// the removal rules: a file maisys created is deleted, unless it's been
// changed since maisys wrote it, and a file maisys took over is left in
// place. Files left in place only get a note in the plan, since nothing
// changes; their records are dropped, so maisys stops managing them.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "file".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the file rec records is deleted or left in
// place.
func (Remover) CheckRemove(_ context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	c, _, err := checkRemove(rt, rec)
	return c, err
}

// Remove deletes the file rec records, if it's still the one maisys wrote.
func (Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	c, path, err := checkRemove(rt, rec)
	if err != nil || c.Action != resource.Remove {
		return err
	}
	if rec.Details[DetailRoot] == "true" {
		_, err = rt.Runner.Run(ctx, system.Cmd{Name: "rm", Args: []string{"-f", "--", path}, Root: true, Label: "deleting " + path})
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("deleting %s: %w", c.Summary, err)
	}
	return nil
}

// checkRemove returns what removing the file rec records does, and the
// file's absolute path.
func checkRemove(rt *resource.Runtime, rec state.Record) (resource.Change, string, error) {
	if len(rec.Paths) == 0 {
		return resource.Change{}, "", errNoPath
	}
	display := rec.Paths[0]
	path, err := Expand(rt.Paths.Home, display)
	if err != nil {
		return resource.Change{}, "", err
	}
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return resource.Change{}, path, nil
	case err != nil:
		return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
	case rec.TookOver:
		return leftInPlace(display + " left in place; no longer managed"), path, nil
	case !info.Mode().IsRegular():
		return leftInPlace(display + " changed outside maisys (left in place)"), path, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return resource.Change{}, "", fmt.Errorf("reading %s: %w", display, err)
	}
	if want := rec.Details[DetailHash]; want == "" || hash(data) != want {
		return leftInPlace(display + " changed outside maisys (left in place)"), path, nil
	}
	return resource.Change{Action: resource.Remove, Summary: display, Privileged: rec.Details[DetailRoot] == "true"}, path, nil
}

// leftInPlace is a removal that leaves the file where it is, with note
// saying so.
func leftInPlace(note string) resource.Change {
	return resource.Change{Notes: []string{note}}
}
