package files

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
)

var (
	_ config.Checker  = (*Config)(nil)
	_ config.Targeter = (*Config)(nil)
)

var modePattern = regexp.MustCompile(`^[0-7]{3,4}$`)

// Check checks every enabled item: that its source is in the config repo,
// its target is in your home folder, and its mode and eol are valid.
func (c *Config) Check(check *config.Check) {
	for _, name := range slices.Sorted(maps.Keys(c.Items)) {
		item := c.Items[name]
		if !item.Enabled {
			continue
		}
		key := config.JoinKey("items", name)
		checkSource(check, key, item)
		checkTarget(check, key, item.Target)
		if item.Mode != "" && !modePattern.MatchString(item.Mode) {
			check.Errorf(key+".mode", "must be 3 or 4 octal digits, such as \"0600\", not %q", item.Mode)
		}
		if item.EOL != "" && item.EOL != "crlf" {
			check.Errorf(key+".eol", "must be \"crlf\", or left out to keep line endings as they are, not %q", item.EOL)
		}
	}
}

func checkSource(check *config.Check, key string, item Item) {
	src := item.Source
	if src == "" {
		check.Errorf(key, "has no source: set it to a file or directory in the config repo")
		return
	}
	dir := strings.HasSuffix(src, "/")
	name := strings.TrimSuffix(src, "/")
	if !fs.ValidPath(name) || name == "." {
		check.Errorf(key+".source", "must be a path inside the config repo, such as \"files/gitconfig\", not %q", src)
		return
	}
	info, err := fs.Stat(check.Repo.FS, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		check.Errorf(key+".source", "is %q, which doesn't exist in the config repo", src)
	case err != nil:
		check.Errorf(key+".source", "is %q, which can't be read: %v", src, err)
	case dir && !info.IsDir():
		check.Errorf(key+".source", "is %q, which is a file: leave out the trailing / to copy a file", src)
	case !dir && info.IsDir():
		check.Errorf(key+".source", "is %q, which is a directory: end it with / to copy the directory file by file", src)
	case item.Template && dir:
		check.Errorf(key+".template", "is true, but the source %q is a directory: only a file can be a template", src)
	default:
	}
}

func checkTarget(check *config.Check, key, target string) {
	if target == "" {
		check.Errorf(key, "has no target: set it to a path in your home folder, such as \"~/.gitconfig\"")
		return
	}
	if problem := targetProblem(target); problem != "" {
		check.Errorf(key+".target", "%s", problem)
	}
}

// targetProblem describes what's wrong with target, or returns "" if it's
// a path inside your home folder.
func targetProblem(target string) string {
	rest, ok := strings.CutPrefix(target, "~/")
	if !ok {
		return fmt.Sprintf("must start with ~/, because files only go in your home folder, not %q", target)
	}
	// On Windows a backslash separates folders too.
	clean := path.Clean(strings.ReplaceAll(rest, `\`, "/"))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Sprintf("must be a path inside your home folder, not %q", target)
	}
	return ""
}

// Targets returns the target of every enabled item, leaving out targets
// that aren't valid, which Check reports. Directories end in /.
func (c *Config) Targets(*config.Machine) []config.Target {
	var targets []config.Target
	for _, name := range slices.Sorted(maps.Keys(c.Items)) {
		item := c.Items[name]
		if !item.Enabled || item.Target == "" || targetProblem(item.Target) != "" {
			continue
		}
		target := item.Target
		if strings.HasSuffix(item.Source, "/") && !strings.HasSuffix(target, "/") {
			target += "/"
		}
		targets = append(targets, config.Target{Path: target, Key: config.JoinKey("files", "items", name, "target")})
	}
	return targets
}
