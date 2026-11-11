package shell

import (
	"errors"
	"io/fs"
	"maps"
	"regexp"
	"slices"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/shells"
)

var (
	_ config.Checker  = (*Config)(nil)
	_ config.Targeter = (*Config)(nil)
)

// shellFiles is one shell's adapter and settings.
type shellFiles struct {
	shells.Shell
	settings Settings
	manageRC bool
}

func (c *Config) shells() []shellFiles {
	return []shellFiles{
		{Shell: shells.Bash{}, settings: c.Bash.Settings, manageRC: c.Bash.ManageRC},
		{Shell: shells.Zsh{}, settings: c.Zsh.Settings, manageRC: c.Zsh.ManageRC},
		{Shell: shells.Fish{}, settings: c.Fish.Settings},
	}
}

// nameRE matches the names of oh-my-zsh's themes and plugins.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._+-]*$`)

// validName reports whether name can be the name of an oh-my-zsh theme or
// plugin, which is also the name of its folder.
func validName(name string) bool {
	return nameRE.MatchString(name)
}

// Check checks the login shell, oh-my-zsh, and the variables and snippets
// of every enabled shell: that each snippet is a file in the config repo,
// and that no two are copied to the same file.
func (c *Config) Check(check *config.Check) {
	enabled := map[string]bool{}
	var names []string
	for _, sh := range c.shells() {
		names = append(names, sh.Name())
		enabled[sh.Name()] = sh.settings.Enabled
		if !sh.settings.Enabled {
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(sh.settings.Variables)) {
			if !shells.ValidName(name) {
				check.Errorf(config.JoinKey(sh.Name(), "variables", name), "isn't a valid variable name: use letters, digits and underscores, not starting with a digit")
			}
		}
		for _, p := range sh.settings.PreSnippets {
			checkSnippet(check, sh.Name()+".pre_snippets", p)
		}
		for _, p := range sh.settings.Snippets {
			checkSnippet(check, sh.Name()+".snippets", p)
		}
		if _, err := sh.snippets(); err != nil {
			if c, ok := errors.AsType[*clash](err); ok {
				list := "snippets"
				if slices.Contains(sh.settings.PreSnippets, c.second) {
					list = "pre_snippets"
				}
				check.ItemErrorf(sh.Name()+"."+list, c.second, "lists %q, which would be copied to %s like %q: move one of them", c.second, c.target, c.first)
			}
		}
	}

	switch {
	case c.Login == "":
	case !slices.Contains(names, c.Login):
		check.Errorf("login", "must be bash, zsh or fish, not %q", c.Login)
	case !enabled[c.Login]:
		check.Errorf("login", "is %s, but machine %q doesn't enable shell.%s", c.Login, check.Machine.Name, c.Login)
	default:
	}

	omz := c.Zsh.OhMyZsh
	if !omz.Enabled {
		return
	}
	if !c.Zsh.Enabled {
		check.Errorf("zsh.oh_my_zsh.enabled", "is true, but machine %q doesn't enable shell.zsh, which oh-my-zsh needs", check.Machine.Name)
	}
	if omz.Theme != "" && !validName(omz.Theme) {
		check.Errorf("zsh.oh_my_zsh.theme", "%q isn't a valid theme name", omz.Theme)
	}
	for _, plugin := range omz.Plugins {
		if !validName(plugin) {
			check.ItemErrorf("zsh.oh_my_zsh.plugins", plugin, "lists %q, which isn't a valid plugin name", plugin)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(omz.CustomThemes)) {
		theme := omz.CustomThemes[name]
		key := config.JoinKey("zsh", "oh_my_zsh", "custom_themes", name)
		if !validName(name) {
			check.Errorf(key, "has a name that isn't a valid theme name: use letters, digits, dots, dashes and underscores")
		}
		required(check, key+".repo", theme.Repo, "the URL of the git repo the theme is in")
		required(check, key+".ref", theme.Ref, "the branch, tag or commit to check out")
		required(check, key+".file", theme.File, "the theme file in the repo, such as \"aphrodite.zsh-theme\"")
		if theme.File != "" && (!fs.ValidPath(theme.File) || theme.File == ".") {
			check.Errorf(key+".file", "is %q, which isn't a path inside the theme's repo", theme.File)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(omz.CustomPlugins)) {
		plugin := omz.CustomPlugins[name]
		key := config.JoinKey("zsh", "oh_my_zsh", "custom_plugins", name)
		if !validName(name) {
			check.Errorf(key, "has a name that isn't a valid plugin name: use letters, digits, dots, dashes and underscores")
		}
		required(check, key+".repo", plugin.Repo, "the URL of the git repo the plugin is in")
		required(check, key+".ref", plugin.Ref, "the branch, tag or commit to check out")
	}
}

func required(check *config.Check, key, value, what string) {
	if value == "" {
		check.Errorf(key, "is missing: set it to %s", what)
	}
}

// checkSnippet checks that p, an item of the list at key, is a file in the
// config repo.
func checkSnippet(check *config.Check, key, p string) {
	if !fs.ValidPath(p) || p == "." {
		check.ItemErrorf(key, p, "lists %q, which isn't a path inside the config repo", p)
		return
	}
	info, err := fs.Stat(check.Repo.FS, p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		check.ItemErrorf(key, p, "lists %q, which doesn't exist in the config repo", p)
	case err != nil:
		check.ItemErrorf(key, p, "lists %q, which can't be read: %v", p, err)
	case info.IsDir():
		check.ItemErrorf(key, p, "lists %q, which is a directory, not a file", p)
	default:
	}
}

// Targets returns the environment file of every enabled shell, its rc file
// if maisys manages it, and the copies of its snippets; and oh-my-zsh's
// folder, if it's enabled.
func (c *Config) Targets(*config.Machine) []config.Target {
	var targets []config.Target
	for _, sh := range c.shells() {
		if !sh.settings.Enabled {
			continue
		}
		targets = append(targets, config.Target{Path: sh.EnvFile(), Key: config.JoinKey("shell", sh.Name(), "enabled")})
		if sh.RCFile() != "" && sh.manageRC {
			targets = append(targets, config.Target{Path: sh.RCFile(), Key: config.JoinKey("shell", sh.Name(), "manage_rc")})
		}
		var copies []string
		for _, list := range []string{"pre_snippets", "snippets"} {
			items := sh.settings.PreSnippets
			if list == "snippets" {
				items = sh.settings.Snippets
			}
			for _, p := range items {
				target := snippetFile(sh.Name(), p)
				if !slices.Contains(copies, target) {
					copies = append(copies, target)
					targets = append(targets, config.Target{Path: target, Key: config.JoinKey("shell", sh.Name(), list)})
				}
			}
		}
	}
	if c.Zsh.Enabled && c.Zsh.OhMyZsh.Enabled {
		targets = append(targets, config.Target{Path: shells.OhMyZshDir + "/", Key: config.JoinKey("shell", "zsh", "oh_my_zsh", "enabled")})
	}
	return targets
}
