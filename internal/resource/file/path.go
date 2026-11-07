package file

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// Expand returns the absolute path p names: a path in the home folder home
// written as "~/..." (or "~\..." on Windows, where home is %USERPROFILE%),
// or an absolute path. A path in the home folder can't leave it.
func Expand(home, p string) (string, error) {
	rest, ok := cutHome(p)
	if !ok {
		if filepath.IsAbs(p) {
			return filepath.Clean(p), nil
		}
		return "", fmt.Errorf("%q must start with ~/ or be an absolute path", p)
	}
	if home == "" {
		return "", fmt.Errorf("can't find where %s is: the home folder isn't known", p)
	}
	rest = filepath.FromSlash(rest)
	if rest != "" && !filepath.IsLocal(rest) {
		return "", fmt.Errorf("%q must be inside your home folder", p)
	}
	return filepath.Join(home, rest), nil
}

// Display returns how the plan and the state file show p: a path in the
// home folder as "~/" and the rest of it with forward slashes, on every
// OS, and any other path cleaned.
func Display(p string) string {
	rest, ok := cutHome(p)
	if !ok {
		return filepath.Clean(p)
	}
	rest = path.Clean(filepath.ToSlash(rest))
	if rest == "." || rest == "" {
		return "~"
	}
	return "~/" + rest
}

// Short returns the absolute path p as "~/..." if it's in the home folder
// home, and p otherwise.
func Short(home, p string) string {
	if home == "" || !filepath.IsAbs(p) {
		return p
	}
	rel, err := filepath.Rel(home, p)
	switch {
	case err != nil:
		return p
	case rel == ".":
		return "~"
	case filepath.IsLocal(rel):
		return "~/" + filepath.ToSlash(rel)
	default:
		return p
	}
}

// cutHome returns what follows "~/" in p (or "~\" on Windows), and whether
// p is in the home folder at all.
func cutHome(p string) (string, bool) {
	if p == "~" {
		return "", true
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return rest, true
	}
	if runtime.GOOS == "windows" {
		return strings.CutPrefix(p, `~\`)
	}
	return "", false
}

// errNoPath is the error for a record that doesn't say where its thing is.
var errNoPath = errors.New("the state file's record has no path")
