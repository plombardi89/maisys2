// Package settings reads and writes a machine's local settings, and knows
// where maisys keeps its files on each OS.
package settings

import (
	"errors"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Paths are the folders maisys keeps its own files in.
type Paths struct {
	// Home is the user's home folder.
	Home string
	// ConfigDir holds the local settings and the config repo token.
	ConfigDir string
	// StateDir holds the state file, the lock and the logs.
	StateDir string
	// CacheDir holds downloads and other files maisys can fetch again.
	CacheDir string

	// goos is the OS whose path separator the methods use. Empty means the
	// running OS.
	goos string
}

// DefaultPaths returns where maisys keeps its files on goos (a GOOS value),
// reading environment variables with getenv:
//
//   - Linux (and other Unix systems) follow the XDG base directory spec,
//     as install.sh does: ${XDG_CONFIG_HOME:-$HOME/.config}/maisys,
//     ${XDG_STATE_HOME:-$HOME/.local/state}/maisys and
//     ${XDG_CACHE_HOME:-$HOME/.cache}/maisys.
//   - Windows uses %APPDATA%\maisys for settings, %LOCALAPPDATA%\maisys for
//     state and %LOCALAPPDATA%\maisys\cache for caches.
//
// Paths use goos's path separator.
func DefaultPaths(goos string, getenv func(string) string) (Paths, error) {
	var missing []string
	need := func(name string) string {
		v := getenv(name)
		if v == "" {
			missing = append(missing, name)
		}
		return v
	}
	p := Paths{goos: goos}
	if goos == "windows" {
		p.Home = need("USERPROFILE")
		appData := need("APPDATA")
		localAppData := need("LOCALAPPDATA")
		p.ConfigDir = joinPath(goos, appData, "maisys")
		p.StateDir = joinPath(goos, localAppData, "maisys")
		p.CacheDir = joinPath(goos, p.StateDir, "cache")
	} else {
		p.Home = need("HOME")
		xdg := func(name string, def ...string) string {
			base := getenv(name)
			if base == "" {
				base = joinPath(goos, append([]string{p.Home}, def...)...)
			}
			return joinPath(goos, base, "maisys")
		}
		p.ConfigDir = xdg("XDG_CONFIG_HOME", ".config")
		p.StateDir = xdg("XDG_STATE_HOME", ".local", "state")
		p.CacheDir = xdg("XDG_CACHE_HOME", ".cache")
	}
	if len(missing) > 0 {
		return Paths{}, errors.New("can't find where to keep maisys's files: " + describeMissing(missing))
	}
	return p, nil
}

func describeMissing(names []string) string {
	if len(names) == 1 {
		return "the " + names[0] + " environment variable isn't set"
	}
	return "the " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " environment variables aren't set"
}

// SettingsFile returns the local settings file.
func (p Paths) SettingsFile() string {
	return p.join(p.ConfigDir, "maisys.toml")
}

// CredentialsFile returns the file holding the config repo token, if one
// was entered at the prompt.
func (p Paths) CredentialsFile() string {
	return p.join(p.ConfigDir, "git-credentials")
}

// DefaultRepoDir returns where the config repo is cloned unless another
// folder is chosen.
func (p Paths) DefaultRepoDir() string {
	return p.join(p.Home, "maisys-config")
}

// StateFile returns the state file, which records what maisys manages.
func (p Paths) StateFile() string {
	return p.join(p.StateDir, "state.json")
}

// LockFile returns the lock file that stops two syncs running at once.
func (p Paths) LockFile() string {
	return p.join(p.StateDir, "lock")
}

// LogsDir returns the folder holding each run's command output.
func (p Paths) LogsDir() string {
	return p.join(p.StateDir, "logs")
}

func (p Paths) join(elem ...string) string {
	return joinPath(p.goos, elem...)
}

// joinPath joins elem with goos's path separator. For the running OS it's
// filepath.Join.
func joinPath(goos string, elem ...string) string {
	if goos == "" || goos == runtime.GOOS {
		return filepath.Join(elem...)
	}
	if goos != "windows" {
		return path.Join(elem...)
	}
	p := elem[0]
	for _, e := range elem[1:] {
		p = strings.TrimRight(p, `\/`) + `\` + e
	}
	return p
}
