package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/settings"
)

// markerFile marks the top of a config repo.
const markerFile = "maisys.toml"

// errNoConfigRepo is returned when there's no config repo to work on.
var errNoConfigRepo = errors.New("no config repo: run `maisys sync CONFIG-URL` to set one up, `maisys config init` to start one, or run this inside a config repo")

// configRepo is the config repo a command works on.
type configRepo struct {
	*config.Repo
	// dir is the repo's folder.
	dir string
	// settings are the local settings, or nil if there are none.
	settings *settings.Settings
}

// openRepo finds the config repo for commands that read it without
// changing anything: the working directory or the closest folder above it
// that has a maisys.toml, or else the repo_dir in the local settings. It
// reads the repo's maisys.toml, prints its warnings, and fails if this
// maisys can't read the config.
func (g *globalOptions) openRepo(cmd *cobra.Command) (*configRepo, error) {
	errOut := cmd.ErrOrStderr()

	paths, pathsErr := settings.DefaultPaths(g.goos, g.getenv)
	var settingsFile string
	if pathsErr == nil {
		settingsFile = paths.SettingsFile()
	}
	dir, err := g.repoAbove(settingsFile)
	if err != nil {
		return nil, err
	}

	r := &configRepo{}
	if pathsErr == nil {
		s, err := settings.Load(settingsFile)
		switch {
		case errors.Is(err, settings.ErrNoSettings):
		case err != nil:
			return nil, err
		default:
			r.settings = s
		}
	}

	var url, source string
	switch {
	case dir != "":
		source = "the working directory"
		if r.settings != nil && sameDir(dir, r.settings.RepoDir) {
			url = r.settings.RepoURL
		}
	case r.settings != nil && r.settings.RepoDir != "":
		dir, url = r.settings.RepoDir, r.settings.RepoURL
		source = "the local settings, " + settingsFile
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("the config repo %s doesn't exist (it's set in %s)", dir, settingsFile)
		}
	case pathsErr != nil:
		return nil, fmt.Errorf("%w; %w", errNoConfigRepo, pathsErr)
	default:
		return nil, errNoConfigRepo
	}
	g.debugf(errOut, "config repo %s (from %s)", dir, source)

	r.dir = dir
	r.Repo = g.newRepo(dir, url)
	if err := r.checkMarker(errOut, nil); err != nil {
		return nil, notConfigRepo(err, dir)
	}
	return r, nil
}

// newRepo returns the config repo in dir, whose URL is url.
func (g *globalOptions) newRepo(dir, url string) *config.Repo {
	return &config.Repo{FS: os.DirFS(dir), Modules: g.modules, URL: url}
}

// checkMarker reads the repo's maisys.toml, prints its warnings, and fails
// if this maisys can't read the config. warned, if not nil, holds the
// warnings already printed, which aren't printed again. A repo without a
// maisys.toml fails with an error that wraps config.ErrNotConfigRepo.
func (r *configRepo) checkMarker(errOut io.Writer, warned map[string]bool) error {
	marker, err := r.ReadMarker()
	switch {
	case errors.Is(err, config.ErrNotConfigRepo):
		return err
	case err != nil:
		return problems(errOut, err)
	}
	warnings, err := marker.Check(buildVersion())
	for _, w := range warnings {
		if warned[w] {
			continue
		}
		if warned != nil {
			warned[w] = true
		}
		_, _ = fmt.Fprintf(errOut, "maisys: warning: %s\n", w)
	}
	if err != nil {
		return problems(errOut, err)
	}
	return nil
}

// notConfigRepo adds the folder to err if it says the repo in dir has no
// maisys.toml.
func notConfigRepo(err error, dir string) error {
	if errors.Is(err, config.ErrNotConfigRepo) {
		return fmt.Errorf("%s: %w", dir, err)
	}
	return err
}

// repoAbove returns the working directory, or the closest folder above it,
// that has a maisys.toml, or "" if none does. The local settings file,
// settingsFile, is also called maisys.toml, but doesn't count.
func (g *globalOptions) repoAbove(settingsFile string) (string, error) {
	wd, err := g.getwd()
	if err != nil {
		return "", fmt.Errorf("finding the working directory: %w", err)
	}
	var settingsInfo os.FileInfo
	if settingsFile != "" {
		settingsInfo, _ = os.Stat(settingsFile)
	}
	for dir := filepath.Clean(wd); ; {
		info, err := os.Stat(filepath.Join(dir, markerFile))
		if err == nil && info.Mode().IsRegular() && (settingsInfo == nil || !os.SameFile(info, settingsInfo)) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// sameDir reports whether a and b are the same folder.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(ai, bi)
}
