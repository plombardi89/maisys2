package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/settings"
)

// errNotSetUp is returned by a first sync that doesn't say which config
// repo to use.
var errNotSetUp = errors.New("this machine isn't set up for maisys yet: run `maisys sync CONFIG-URL` to clone your config repo, " +
	"`maisys sync --dir PATH` to use a clone you already have, or `maisys config init` to start a new config repo")

// findRepo finds the config repo for this run. On the first run, it clones
// CONFIG-URL or uses the clone given with --dir. On later runs, it's the
// one in the local settings, unless CONFIG-URL or --dir names another one
// to switch to.
func (s *syncRun) findRepo() error {
	paths, err := settings.DefaultPaths(s.g.goos, s.g.getenv)
	if err != nil {
		return err
	}
	s.paths = paths
	dir, err := s.g.absPath(s.opts.dir, paths.Home)
	if err != nil {
		return err
	}
	if s.url != "" && isLocalPath(s.url) {
		// git saves a relative path as it's given, which only works from
		// the folder it was given in.
		if s.url, err = s.g.absPath(s.url, paths.Home); err != nil {
			return err
		}
	}

	saved, err := settings.Load(paths.SettingsFile())
	switch {
	case errors.Is(err, settings.ErrNoSettings):
		if s.url == "" && dir == "" {
			return errNotSetUp
		}
		if dir == "" {
			dir = paths.DefaultRepoDir()
		}
		return s.useClone(s.url, dir)
	case err != nil:
		return err
	}
	s.saved = saved

	newURL := s.url != "" && !sameURL(s.url, saved.RepoURL)
	newDir := dir != "" && !sameDir(dir, saved.RepoDir)
	switch {
	case newURL || newDir:
		s.switched = true
		if dir == "" {
			dir = paths.DefaultRepoDir()
		}
		return s.useClone(s.url, dir)
	case s.url != "" && saved.RepoDir != "":
		if state, _ := folderStateOf(saved.RepoDir); state != folderInUse {
			return s.useClone(s.url, saved.RepoDir)
		}
	}

	if saved.RepoDir == "" {
		return fmt.Errorf("%s doesn't say where the config repo is: run `maisys sync --dir PATH` to use a clone, or `maisys sync CONFIG-URL` to clone one",
			paths.SettingsFile())
	}
	if info, err := os.Stat(saved.RepoDir); err != nil || !info.IsDir() {
		msg := fmt.Sprintf("the config repo %s doesn't exist (it's set in %s)", saved.RepoDir, paths.SettingsFile())
		if saved.RepoURL != "" {
			return fmt.Errorf("%s: run `maisys sync %s` to clone it again", msg, gitrepo.Redact(saved.RepoURL))
		}
		return fmt.Errorf("%s: run `maisys sync --dir PATH` to use another clone", msg)
	}
	s.repo = &configRepo{Repo: newRepo(saved.RepoDir, saved.RepoURL), dir: saved.RepoDir, settings: saved}
	return nil
}

// useClone makes the clone in dir the config repo. With rawURL, dir must
// be missing or empty, in which case rawURL is cloned into it, or already
// a clone of rawURL. Without rawURL, dir must be a clone already, and its
// remote, if it has one, is the config repo's URL. It checks the repo's
// maisys.toml, and if there's none, removes a clone it just made, so the
// next try starts clean.
func (s *syncRun) useClone(rawURL, dir string) error {
	ctx, errOut := s.cmd.Context(), s.cmd.ErrOrStderr()
	opts, err := s.g.gitOptions(s.paths)
	if err != nil {
		return err
	}

	repoURL := rawURL
	var keepDir bool // dir was an empty folder before the clone
	if rawURL == "" {
		if s.clone, err = gitrepo.Open(ctx, dir, opts); err != nil {
			return err
		}
		if repoURL, err = s.clone.RemoteURL(ctx); err != nil {
			return err
		}
		s.g.debugf(errOut, "using the config repo in %s", s.clone.Dir)
	} else {
		state, err := folderStateOf(dir)
		if err != nil {
			return err
		}
		if state == folderInUse {
			if s.clone, err = existingClone(ctx, rawURL, dir, opts); err != nil {
				return err
			}
			s.g.debugf(errOut, "using the clone of %s in %s", gitrepo.Redact(rawURL), s.clone.Dir)
		} else {
			s.g.debugf(errOut, "cloning %s into %s", gitrepo.Redact(rawURL), dir)
			if s.clone, err = gitrepo.Clone(ctx, rawURL, dir, opts); err != nil {
				return err
			}
			s.cloned, keepDir = true, state == folderEmpty
		}
	}

	s.repo = &configRepo{Repo: newRepo(s.clone.Dir, repoURL), dir: s.clone.Dir}
	err = s.repo.checkMarker(errOut, s.warned)
	if !errors.Is(err, config.ErrNotConfigRepo) {
		return err
	}
	what := s.clone.Dir
	if rawURL != "" {
		what = gitrepo.Redact(rawURL)
	}
	err = fmt.Errorf("%s isn't a maisys config repo: it has no %s at the top", what, markerFile)
	if s.cloned {
		if rmErr := removeClone(s.clone.Dir, keepDir); rmErr != nil {
			_, _ = fmt.Fprintf(errOut, "maisys: warning: couldn't remove the clone in %s: %v\n", s.clone.Dir, rmErr)
		}
	}
	return err
}

// existingClone opens the clone of rawURL in dir, and fails, asking for
// --dir, if dir holds anything else.
func existingClone(ctx context.Context, rawURL, dir string, opts gitrepo.Options) (*gitrepo.Repo, error) {
	clone, err := gitrepo.Open(ctx, dir, opts)
	var remote string
	if err == nil {
		remote, err = clone.RemoteURL(ctx)
	}
	var why string
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case err != nil:
		why = "it isn't empty"
	case remote == "":
		why = "it holds a clone with no remote"
	case !sameURL(remote, rawURL):
		why = "it holds a clone of " + gitrepo.Redact(remote)
	default:
		return clone, nil
	}
	return nil, fmt.Errorf("can't clone %s into %s, because the folder is in use (%s): choose another folder for it with --dir",
		gitrepo.Redact(rawURL), dir, why)
}

// chooseMachine selects the machine, and saves the local settings if this
// is the first run, the config repo has changed, or the machine has.
func (s *syncRun) chooseMachine() error {
	out, errOut := s.cmd.OutOrStdout(), s.cmd.ErrOrStderr()
	var old string
	if s.saved != nil {
		old = s.saved.Machine
		keep := *s.saved
		if s.switched && !s.repo.hasMachine(old) {
			// The saved machine comes first only if the new config repo
			// has it.
			keep.Machine = ""
		}
		s.repo.settings = &keep
	}
	name, source, err := s.g.selectMachine(s.cmd, s.repo, machineRequest{flag: s.opts.machine, hasFlag: true, pick: true})
	if err != nil {
		return err
	}
	s.machine = name
	s.g.debugf(errOut, "machine %s (from %s)", name, source)

	// MAISYS_MACHINE only chooses the machine for one run.
	newMachine := name != old && source != fromEnv
	if s.saved != nil && !s.switched && !newMachine {
		return nil
	}
	next := &settings.Settings{RepoDir: s.repo.dir, RepoURL: s.repo.URL, Machine: name}
	if err := next.Save(s.paths.SettingsFile()); err != nil {
		return err
	}
	s.repo.settings = next

	if s.saved == nil {
		_, err := fmt.Fprintf(out, "maisys: set up this machine as %s (from %s), with the config repo %s\n", name, source, s.describeSetup())
		return err
	}
	if s.switched {
		from, to := describeRepo(s.saved.RepoURL, s.saved.RepoDir), describeRepo(s.repo.URL, s.repo.dir)
		if from == to {
			from, to = s.saved.RepoDir, s.repo.dir
		}
		if _, err := fmt.Fprintf(out, "maisys: config repo: %s → %s\n", from, to); err != nil {
			return err
		}
	}
	switch {
	case name == old:
		return nil
	case old == "":
		_, err = fmt.Fprintf(out, "maisys: machine: %s (from %s)\n", name, source)
	default:
		_, err = fmt.Fprintf(out, "maisys: machine: %s (was %s)\n", name, old)
	}
	return err
}

// describeSetup describes the config repo the first run set up.
func (s *syncRun) describeSetup() string {
	switch {
	case s.repo.URL == "":
		return s.repo.dir + " (it has no remote)"
	case s.cloned:
		return gitrepo.Redact(s.repo.URL) + ", cloned into " + s.repo.dir
	default:
		return gitrepo.Redact(s.repo.URL) + ", in " + s.repo.dir
	}
}

// describeRepo describes a config repo by its URL, or by its folder if it
// has no remote.
func describeRepo(rawURL, dir string) string {
	if rawURL == "" {
		return dir
	}
	return gitrepo.Redact(rawURL)
}

// absPath returns path as an absolute path, with a leading "~" replaced by
// home, and relative paths taken from the working directory. It returns ""
// for "".
func (g *globalOptions) absPath(path, home string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") || (g.goos == "windows" && strings.HasPrefix(path, `~\`)) {
		path = filepath.Join(home, path[1:])
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	wd, err := g.getwd()
	if err != nil {
		return "", fmt.Errorf("finding the working directory: %w", err)
	}
	return filepath.Join(wd, path), nil
}

// folderState says whether a repo can be cloned into a folder.
type folderState int

const (
	folderMissing folderState = iota
	folderEmpty
	folderInUse // it has files in it, or isn't a folder
)

func folderStateOf(dir string) (folderState, error) {
	f, err := os.Open(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return folderMissing, nil
	}
	if err != nil {
		return folderInUse, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Readdirnames(1); errors.Is(err, io.EOF) {
		return folderEmpty, nil
	}
	return folderInUse, nil
}

// removeClone removes the clone in dir. If keepDir is set, dir was an empty
// folder before the clone, so it's emptied but kept.
func removeClone(dir string, keepDir bool) error {
	if !keepDir {
		return os.RemoveAll(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
	}
	return errors.Join(errs...)
}

// isLocalPath reports whether rawURL is a path rather than a URL, the way
// git tells them apart: it has no scheme, and no colon before its first
// slash, as scp-like URLs such as git@github.com:me/config.git have.
func isLocalPath(rawURL string) bool {
	if strings.Contains(rawURL, "://") {
		return false
	}
	if filepath.IsAbs(rawURL) || filepath.VolumeName(rawURL) != "" {
		return true
	}
	colon := strings.IndexByte(rawURL, ':')
	slash := strings.IndexAny(rawURL, "/"+string(filepath.Separator))
	return colon < 0 || (slash >= 0 && slash < colon)
}

// sameURL reports whether a and b are the same repo's URL, ignoring a
// trailing "/" or ".git", and the case of the scheme and host.
func sameURL(a, b string) bool {
	return normalURL(a) == normalURL(b)
}

func normalURL(rawURL string) string {
	s := strings.TrimSuffix(strings.TrimRight(rawURL, "/"), ".git")
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil {
			return s
		}
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		return u.String()
	case isLocalPath(s):
		return s
	}
	// An scp-like URL: [user@]host:path.
	host, path, _ := strings.Cut(s, ":")
	user, at := "", strings.LastIndexByte(host, '@')
	if at >= 0 {
		user, host = host[:at+1], host[at+1:]
	}
	return user + strings.ToLower(host) + ":" + path
}
