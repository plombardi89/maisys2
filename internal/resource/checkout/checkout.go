// Package checkout keeps git checkouts of repos at a ref: a branch, a tag or
// a commit. oh-my-zsh and its custom themes and plugins are checkouts.
//
// A branch is followed: every check asks the repo what the branch points to
// (with git ls-remote, which changes nothing), and the plan shows the move,
// such as "~ aphrodite a1b2c3d → d4e5f6a (master)". Tags and commits stay
// where they are once they're checked out: a check compares the checkout
// with the commit the state file recorded, without asking the repo again.
//
// A checkout is detached at its commit, so nothing else moves it, and
// shallow where it can be. A new checkout is cloned into a folder next to
// its destination and renamed into place, so a clone that fails leaves
// nothing behind. Local changes to tracked files are discarded when the
// checkout is updated, and the plan warns about them first; untracked and
// ignored files, such as oh-my-zsh's custom folder, are kept. A destination
// that's something else, such as a folder that isn't a checkout of the
// repo, is backed up to <dir>.maisys-bak.<time> and replaced.
//
// Repos must be public: git never asks for credentials and no credential
// helper is used, so a repo that needs signing in fails instead.
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
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Kind is the kind of resource a Checkout is.
const Kind = "git-checkout"

// The kinds of ref a checkout can be at, as its record's ref_type says.
const (
	refBranch = "branch"
	refTag    = "tag"
	refCommit = "commit"
)

// Checkout is a git checkout of a repo at a ref. Its ID is "git-checkout:"
// followed by its directory.
type Checkout struct {
	// Name names the checkout in the plan, such as "aphrodite".
	Name string
	// Repo is the repo's URL, a public HTTPS URL.
	Repo string
	// Ref is a branch, a tag, or a full or abbreviated commit hash. A name
	// that's both a branch and a tag is the branch.
	Ref string
	// Dir is where the checkout goes, as an absolute path.
	Dir string
	// Git is the git binary. Empty means git from PATH.
	Git string
	// Env is extra environment for git, as NAME=value.
	Env []string

	// found is what Check found, for Apply.
	found *found
}

var _ resource.Resource = (*Checkout)(nil)

// found is what a check found: what's on disk and what it should be.
type found struct {
	disk   disk
	target target
}

// diskState is what's at a checkout's directory.
type diskState int

const (
	// missing means there's nothing there.
	missing diskState = iota
	// empty is an empty directory.
	empty
	// checkedOut is a checkout of the repo.
	checkedOut
	// moved is a checkout maisys made of the repo the config used to name.
	moved
	// other is anything else, which is backed up and replaced.
	other
)

// disk is what a check found at a checkout's directory.
type disk struct {
	state diskState
	// head is the commit checked out, for checkedOut and moved.
	head string
	// origin is the URL of the checkout's origin, for checkedOut and moved.
	origin string
	// dirty reports whether tracked files have changes, for checkedOut and
	// moved.
	dirty bool
	// why says what's there instead, for other, such as "isn't a git
	// checkout".
	why string
}

// target is the commit a checkout should be at.
type target struct {
	// kind is refBranch, refTag or refCommit.
	kind string
	// commit is the commit's full hash, or "" for an abbreviated commit
	// hash that hasn't been fetched yet.
	commit string
	// abbrev is the abbreviated commit hash, if commit is "".
	abbrev string
}

// at reports whether head, a full commit hash, is the target.
func (t target) at(head string) bool {
	if t.commit != "" {
		return head == t.commit
	}
	return t.abbrev != "" && strings.HasPrefix(head, t.abbrev)
}

// ID returns "git-checkout:" followed by the checkout's directory.
func (c *Checkout) ID() string { return Kind + ":" + c.Dir }

// Kind returns "git-checkout".
func (c *Checkout) Kind() string { return Kind }

// Targets returns the checkout's directory.
func (c *Checkout) Targets() []string { return []string{c.Dir} }

// Privileged returns false: checkouts are the user's.
func (c *Checkout) Privileged() bool { return false }

// Check compares the checkout with the commit its ref points to. For a
// branch, it asks the repo with git ls-remote; for a tag or a commit
// checked out before, it uses the state file's record.
func (c *Checkout) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	c.found = nil
	if err := c.validate(); err != nil {
		return resource.Change{}, err
	}
	d, err := c.inspect(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	t, err := c.resolve(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	c.found = &found{disk: d, target: t}
	return c.change(rt, d, t), nil
}

// validate checks the checkout's fields.
func (c *Checkout) validate() error {
	switch {
	case c.Name == "":
		return errors.New("a git checkout has no name")
	case c.Repo == "" || strings.HasPrefix(c.Repo, "-"):
		return fmt.Errorf("%s: %q isn't a repo URL", c.Name, c.Repo)
	case c.Ref == "" || strings.HasPrefix(c.Ref, "-") || strings.ContainsAny(c.Ref, " \t\r\n~^:?*[\\") || strings.Contains(c.Ref, ".."):
		return fmt.Errorf("%s: %q isn't a branch, tag or commit", c.Name, c.Ref)
	case !filepath.IsAbs(c.Dir):
		return fmt.Errorf("%s: the checkout's directory %q isn't an absolute path", c.Name, c.Dir)
	}
	return nil
}

// change describes what applying the checkout would do.
func (c *Checkout) change(rt *resource.Runtime, d disk, t target) resource.Change {
	home := rt.Paths.Home
	var ch resource.Change
	switch d.state {
	case missing, empty:
		return resource.Change{Action: resource.Create, Summary: fmt.Sprintf("%s (%s)", c.Name, c.refLabel())}
	case other:
		return resource.Change{
			Action:  resource.Warn,
			Summary: fmt.Sprintf("%s: %s %s (will back up and replace)", c.Name, tilde(home, c.Dir), d.why),
		}
	case moved:
		ch = resource.Change{Action: resource.Update, Summary: fmt.Sprintf("%s %s → %s (%s)", c.Name, d.origin, c.Repo, c.refLabel())}
	case checkedOut:
		if !t.at(d.head) {
			ch = resource.Change{Action: resource.Update, Summary: fmt.Sprintf("%s %s → %s", c.Name, short(d.head), t.label())}
			if t.kind != refCommit {
				ch.Summary += " (" + c.Ref + ")"
			}
		}
	}
	if d.dirty {
		note := tilde(home, c.Dir) + " has local changes (will be discarded)"
		if ch.Action == resource.NoChange {
			return resource.Change{Action: resource.Warn, Summary: note}
		}
		ch.Notes = append(ch.Notes, note)
	}
	return ch
}

// refLabel returns how the plan shows the checkout's ref: a full commit
// hash shortened, anything else as it is.
func (c *Checkout) refLabel() string {
	if isFullHash(c.Ref) {
		return short(c.Ref)
	}
	return c.Ref
}

// label returns how the plan shows the target commit.
func (t target) label() string {
	if t.commit != "" {
		return short(t.commit)
	}
	return t.abbrev
}

// inspect finds what's at the checkout's directory.
func (c *Checkout) inspect(ctx context.Context, rt *resource.Runtime) (disk, error) {
	fi, err := os.Lstat(c.Dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return disk{state: missing}, nil
	case err != nil:
		return disk{}, err
	case fi.Mode()&fs.ModeSymlink != 0:
		return disk{state: other, why: "is a symlink"}, nil
	case !fi.IsDir():
		return disk{state: other, why: "isn't a directory"}, nil
	}
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return disk{}, err
	}
	if len(entries) == 0 {
		return disk{state: empty}, nil
	}
	if _, err := os.Lstat(filepath.Join(c.Dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		return disk{state: other, why: "isn't a git checkout"}, nil
	} else if err != nil {
		return disk{}, err
	}

	g := c.git(rt)
	head, ok, err := g.revParse(ctx, c.Dir, "HEAD")
	if _, broken := errors.AsType[*system.ExitError](err); broken || (err == nil && !ok) {
		return disk{state: other, why: "isn't a git checkout"}, nil
	} else if err != nil {
		return disk{}, err
	}
	res, err := g.run(ctx, c.Dir, []int{1}, "config", "--get", "remote.origin.url")
	if err != nil {
		return disk{}, err
	}
	origin := strings.TrimSpace(string(res.Stdout))
	res, err = g.run(ctx, c.Dir, nil, "--no-optional-locks", "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return disk{}, err
	}
	d := disk{state: checkedOut, head: head, origin: origin, dirty: strings.TrimSpace(string(res.Stdout)) != ""}

	if !sameRepo(origin, c.Repo) {
		if prev, ok := c.previous(rt); ok && sameRepo(prev.Details["repo"], origin) {
			d.state = moved
		} else {
			return disk{state: other, why: "isn't a checkout of " + c.Repo}, nil
		}
	}
	return d, nil
}

// previous returns the state file's record of the checkout, if there is
// one.
func (c *Checkout) previous(rt *resource.Runtime) (state.Record, bool) {
	if rt.Previous == nil {
		return state.Record{}, false
	}
	return rt.Previous(c.ID())
}

// resolve finds the commit the checkout's ref points to: a full commit hash
// is itself; a tag or a commit the state file recorded for the same repo
// and ref stays where it was; anything else is looked up in the repo.
func (c *Checkout) resolve(ctx context.Context, rt *resource.Runtime) (target, error) {
	if isFullHash(c.Ref) {
		return target{kind: refCommit, commit: strings.ToLower(c.Ref)}, nil
	}
	if prev, ok := c.previous(rt); ok && sameRepo(prev.Details["repo"], c.Repo) && prev.Details["ref"] == c.Ref &&
		(prev.Details["ref_type"] == refTag || prev.Details["ref_type"] == refCommit) && isFullHash(prev.Details["commit"]) {
		return target{kind: prev.Details["ref_type"], commit: prev.Details["commit"]}, nil
	}

	heads, tags := "refs/heads/"+c.Ref, "refs/tags/"+c.Ref
	res, err := c.git(rt).run(ctx, "", nil, "ls-remote", "--", c.Repo, heads, tags, tags+"^{}")
	if err != nil {
		return target{}, fmt.Errorf("couldn't look up %s in %s: %w", c.Ref, c.Repo, err)
	}
	refs := map[string]string{}
	for line := range strings.Lines(string(res.Stdout)) {
		if hash, name, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			refs[name] = hash
		}
	}
	switch {
	case refs[heads] != "":
		return target{kind: refBranch, commit: refs[heads]}, nil
	case refs[tags+"^{}"] != "":
		return target{kind: refTag, commit: refs[tags+"^{}"]}, nil
	case refs[tags] != "":
		return target{kind: refTag, commit: refs[tags]}, nil
	case isHex(c.Ref) && len(c.Ref) >= 4:
		return target{kind: refCommit, abbrev: strings.ToLower(c.Ref)}, nil
	}
	return target{}, fmt.Errorf("%s isn't a branch or tag of %s", c.Ref, c.Repo)
}

// Apply clones the checkout, or updates it: it fetches the ref if the
// checkout isn't at its commit, and checks the commit out, discarding
// local changes to tracked files.
func (c *Checkout) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if c.found == nil {
		if _, err := c.Check(ctx, rt); err != nil {
			return state.Record{}, err
		}
	}
	f := c.found
	var head string
	var err error
	if f.disk.state == checkedOut {
		head, err = c.update(ctx, rt, f)
	} else {
		head, err = c.clone(ctx, rt, f)
	}
	if err != nil {
		return state.Record{}, err
	}
	return state.Record{
		Paths: []string{c.Dir},
		Details: map[string]string{
			"name":     c.Name,
			"repo":     c.Repo,
			"ref":      c.Ref,
			"ref_type": f.target.kind,
			"commit":   head,
		},
	}, nil
}

// update brings the existing checkout to its target, and returns the
// commit it's at.
func (c *Checkout) update(ctx context.Context, rt *resource.Runtime, f *found) (string, error) {
	g := c.git(rt)
	head := f.disk.head
	if !f.target.at(head) {
		var err error
		if head, err = c.fetch(ctx, g, c.Dir, f.target, false); err != nil {
			return "", err
		}
	}
	if err := g.checkout(ctx, c.Dir, head); err != nil {
		return "", fmt.Errorf("couldn't check out %s in %s: %w", short(head), c.Dir, err)
	}
	return head, nil
}

// stagePrefix is how the folders new checkouts are cloned into start, next
// to the checkout's directory.
func (c *Checkout) stagePrefix() string {
	return "." + filepath.Base(c.Dir) + ".maisys-"
}

// clone makes a new checkout in a folder next to the checkout's directory,
// and renames it into place: after removing an empty directory, backing up
// something that isn't a checkout of the repo, or deleting a checkout of
// the repo the config used to name. It returns the commit checked out.
func (c *Checkout) clone(ctx context.Context, rt *resource.Runtime, f *found) (head string, err error) {
	parent, base := filepath.Dir(c.Dir), filepath.Base(c.Dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	c.removeStages(parent)
	stage, err := os.MkdirTemp(parent, c.stagePrefix()+"*")
	if err != nil {
		return "", err
	}
	defer func() {
		if rerr := os.RemoveAll(stage); rerr != nil && err == nil {
			err = rerr
		}
	}()

	work := filepath.Join(stage, base)
	if err := os.Mkdir(work, 0o777); err != nil {
		return "", err
	}
	g := c.git(rt)
	if _, err := g.run(ctx, work, nil, "init", "--quiet"); err != nil {
		return "", fmt.Errorf("couldn't clone %s: %w", c.Repo, err)
	}
	if _, err := g.run(ctx, work, nil, "remote", "add", "origin", c.Repo); err != nil {
		return "", fmt.Errorf("couldn't clone %s: %w", c.Repo, err)
	}
	if head, err = c.fetch(ctx, g, work, f.target, true); err != nil {
		return "", err
	}
	if err := g.checkout(ctx, work, head); err != nil {
		return "", fmt.Errorf("couldn't check out %s from %s: %w", short(head), c.Repo, err)
	}

	var aside string
	switch f.disk.state {
	case empty:
		if err := os.Remove(c.Dir); err != nil {
			return "", err
		}
	case other:
		aside = c.Dir + ".maisys-bak." + now(rt).Format("20060102-150405")
	case moved:
		aside = filepath.Join(stage, base+".old")
	case missing, checkedOut:
	}
	if aside != "" {
		if err := os.Rename(c.Dir, aside); err != nil {
			return "", fmt.Errorf("couldn't move %s out of the way: %w", c.Dir, err)
		}
	}
	if err := os.Rename(work, c.Dir); err != nil {
		if aside != "" {
			_ = os.Rename(aside, c.Dir)
		}
		return "", err
	}
	return head, nil
}

// removeStages deletes the folders earlier clones of the checkout were
// made in, if a crash left any behind.
func (c *Checkout) removeStages(parent string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), c.stagePrefix()) {
			_ = os.RemoveAll(filepath.Join(parent, e.Name()))
		}
	}
}

// fetch fetches the target into the checkout in dir, and returns its
// commit. A new checkout (fresh) and one that's already shallow fetch only
// the commit itself where they can.
func (c *Checkout) fetch(ctx context.Context, g gitRunner, dir string, t target, fresh bool) (string, error) {
	shallow := fresh
	if !fresh {
		var err error
		if shallow, err = g.shallow(ctx, dir); err != nil {
			return "", err
		}
	}
	args := []string{"fetch", "--quiet", "--no-tags"}
	if shallow {
		args = append(args, "--depth", "1")
	}

	var local string
	switch t.kind {
	case refBranch:
		local = "refs/remotes/origin/" + c.Ref
		args = append(args, "--", c.Repo, "+refs/heads/"+c.Ref+":"+local)
	case refTag:
		local = "refs/tags/" + c.Ref
		args = append(args, "--", c.Repo, "+"+local+":"+local)
	default:
		return c.fetchCommit(ctx, g, dir, t, args)
	}
	if _, err := g.run(ctx, dir, nil, args...); err != nil {
		return "", fmt.Errorf("couldn't fetch %s from %s: %w", c.Ref, c.Repo, err)
	}
	head, ok, err := g.revParse(ctx, dir, local)
	if err == nil && !ok {
		err = fmt.Errorf("fetching %s from %s didn't fetch a commit", c.Ref, c.Repo)
	}
	return head, err
}

// fetchCommit fetches a commit the checkout's ref names, unless it's
// already there, and returns its full hash. A full hash is fetched on its
// own if the repo allows that; otherwise, every branch and tag is.
func (c *Checkout) fetchCommit(ctx context.Context, g gitRunner, dir string, t target, args []string) (string, error) {
	rev := cmp.Or(t.commit, t.abbrev)
	if head, ok, err := g.revParse(ctx, dir, rev); err != nil || ok {
		return head, err
	}
	if t.commit != "" {
		if _, err := g.run(ctx, dir, nil, append(args, "--", c.Repo, t.commit)...); err == nil {
			if head, ok, err := g.revParse(ctx, dir, rev); err != nil || ok {
				return head, err
			}
		}
	}

	shallow, err := g.shallow(ctx, dir)
	if err != nil {
		return "", err
	}
	args = []string{"fetch", "--quiet", "--tags"}
	if shallow {
		args = append(args, "--unshallow")
	}
	args = append(args, "--", c.Repo, "+refs/heads/*:refs/remotes/origin/*")
	if _, err := g.run(ctx, dir, nil, args...); err != nil {
		return "", fmt.Errorf("couldn't fetch %s: %w", c.Repo, err)
	}
	head, ok, err := g.revParse(ctx, dir, rev)
	if err == nil && !ok {
		err = fmt.Errorf("%s has no commit %s", c.Repo, c.Ref)
	}
	return head, err
}

// now returns the current time.
func now(rt *resource.Runtime) time.Time {
	if rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// short returns a commit hash abbreviated as the plan shows it.
func short(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// isHex reports whether s is made of lower or upper case hex digits.
func isHex(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F')
	})
}

// isFullHash reports whether s is a full commit hash: SHA-1 or SHA-256.
func isFullHash(s string) bool {
	return (len(s) == 40 || len(s) == 64) && isHex(s)
}

// sameRepo reports whether two repo URLs name the same repo, ignoring a
// trailing slash or ".git".
func sameRepo(a, b string) bool {
	return a != "" && normalizeURL(a) == normalizeURL(b)
}

func normalizeURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	return strings.TrimRight(strings.TrimSuffix(u, ".git"), "/")
}

// tilde returns path with the home folder shown as ~.
func tilde(home, path string) string {
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return "~/" + filepath.ToSlash(rest)
	}
	return path
}
