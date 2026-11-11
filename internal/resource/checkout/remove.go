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
	"github.com/plombardi89/maisys2/internal/system"
)

// Remover deletes the checkouts taken out of the config, as the removal
// rules say. A directory that's no longer a git checkout of the repo
// maisys cloned, such as your own clone of another repo, is left in place.
type Remover struct {
	// Git is the git binary, used to read the checkout's origin and to
	// warn about local changes that deleting it loses. Empty means git
	// from PATH.
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
	present, why, err := r.removable(ctx, rt, rec, dir)
	if err != nil || !present {
		return resource.Change{}, err
	}
	shown := tilde(rt.Paths.Home, dir)
	if why != "" {
		return resource.Change{Action: resource.Warn, Summary: fmt.Sprintf("%s %s (left in place)", shown, why)}, nil
	}
	ch := resource.Change{Action: resource.Remove, Summary: fmt.Sprintf("%s (%s)", cmp.Or(rec.Details["name"], filepath.Base(dir)), shown)}
	// The note is only a courtesy, so a checkout git can't read is deleted
	// without one.
	if res, err := r.git(rt).run(ctx, dir, nil, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no"); err == nil && strings.TrimSpace(string(res.Stdout)) != "" {
		ch.Notes = append(ch.Notes, shown+" has local changes (will be deleted too)")
	}
	return ch, nil
}

// Remove deletes the checkout rec records, unless it's no longer a git
// checkout of the repo rec records.
func (r Remover) Remove(ctx context.Context, rt *resource.Runtime, rec state.Record) error {
	dir := recordDir(rec)
	present, why, err := r.removable(ctx, rt, rec, dir)
	if err != nil || !present || why != "" {
		return err
	}
	return os.RemoveAll(dir)
}

// git returns the gitRunner for the remover.
func (r Remover) git(rt *resource.Runtime) gitRunner {
	return gitRunner{runner: rt.Runner, git: r.Git, env: r.Env}
}

// recordDir returns the directory of the checkout rec records.
func recordDir(rec state.Record) string {
	if len(rec.Paths) > 0 {
		return rec.Paths[0]
	}
	dir, _ := strings.CutPrefix(rec.ID, Kind+":")
	return dir
}

// removable reports whether there's something at dir, where rec's
// checkout was, and if it isn't a git checkout of the repo rec records,
// why not.
func (r Remover) removable(ctx context.Context, rt *resource.Runtime, rec state.Record, dir string) (present bool, why string, err error) {
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
	res, err := r.git(rt).run(ctx, dir, []int{1}, "config", "--get", "remote.origin.url")
	if _, broken := errors.AsType[*system.ExitError](err); broken {
		return true, "isn't a git checkout any more", nil
	} else if err != nil {
		return false, "", err
	}
	if repo := rec.Details["repo"]; !sameRepo(strings.TrimSpace(string(res.Stdout)), repo) {
		return true, "isn't a checkout of " + cmp.Or(repo, "the repo maisys cloned") + " any more", nil
	}
	return true, "", nil
}
