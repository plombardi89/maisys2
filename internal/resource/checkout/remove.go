package checkout

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
)

// Remover deletes the checkouts taken out of the config, as the removal
// rules say. A directory that's no longer a git checkout is left in place.
type Remover struct {
	// Git is the git binary, used to warn about local changes that
	// deleting a checkout loses. Empty means git from PATH.
	Git string
	// Env is extra environment for git, as NAME=value.
	Env []string
}

var _ resource.Remover = Remover{}

// Kind returns "git-checkout".
func (Remover) Kind() string { return Kind }

// CheckRemove says whether the checkout rec records is still there to
// delete, noting local changes that deleting it loses.
func (r Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	dir := recordDir(rec)
	present, why, err := removable(dir)
	if err != nil || !present {
		return resource.Change{}, err
	}
	shown := tilde(rt.Paths.Home, dir)
	if why != "" {
		return resource.Change{Action: resource.Warn, Summary: fmt.Sprintf("%s %s (left in place)", shown, why)}, nil
	}
	ch := resource.Change{Action: resource.Remove, Summary: fmt.Sprintf("%s (%s)", cmp.Or(rec.Details["name"], filepath.Base(dir)), shown)}
	g := gitRunner{runner: rt.Runner, git: r.Git, env: r.Env}
	// The note is only a courtesy, so a checkout git can't read is deleted
	// without one.
	if res, err := g.run(ctx, dir, nil, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no"); err == nil && strings.TrimSpace(string(res.Stdout)) != "" {
		ch.Notes = append(ch.Notes, shown+" has local changes (will be deleted too)")
	}
	return ch, nil
}

// Remove deletes the checkout rec records, unless it's no longer a git
// checkout.
func (Remover) Remove(_ context.Context, _ *resource.Runtime, rec state.Record) error {
	dir := recordDir(rec)
	present, why, err := removable(dir)
	if err != nil || !present || why != "" {
		return err
	}
	return os.RemoveAll(dir)
}

// recordDir returns the directory of the checkout rec records.
func recordDir(rec state.Record) string {
	if len(rec.Paths) > 0 {
		return rec.Paths[0]
	}
	dir, _ := strings.CutPrefix(rec.ID, Kind+":")
	return dir
}

// removable reports whether there's something at dir, and if it isn't a
// git checkout, why not.
func removable(dir string) (present bool, why string, err error) {
	if !filepath.IsAbs(dir) {
		return false, "", nil
	}
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, "", nil
	case err != nil:
		return false, "", err
	case fi.Mode()&fs.ModeSymlink != 0 || !fi.IsDir():
		return true, "isn't a directory any more", nil
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		return true, "isn't a git checkout any more", nil
	} else if err != nil {
		return false, "", err
	}
	return true, "", nil
}
