package shell

import (
	"errors"
	"io/fs"
	"maps"
	"slices"

	"github.com/plombardi89/maisys2/internal/config"
)

var (
	_ config.Checker  = (*Config)(nil)
	_ config.Targeter = (*Config)(nil)
)

// shellFiles is one shell's settings, and the files maisys writes for it.
type shellFiles struct {
	name     string
	settings Settings
	manageRC bool
	// env is the environment file; rc is the rc file maisys can manage, if
	// any.
	env, rc string
}

func (c *Config) shells() []shellFiles {
	return []shellFiles{
		{name: "bash", settings: c.Bash.Settings, manageRC: c.Bash.ManageRC, env: "~/.config/maisys/shell/env.sh", rc: "~/.bashrc"},
		{name: "zsh", settings: c.Zsh.Settings, manageRC: c.Zsh.ManageRC, env: "~/.config/maisys/shell/env.zsh", rc: "~/.zshrc"},
		{name: "fish", settings: c.Fish.Settings, env: "~/.config/fish/conf.d/maisys.fish"},
	}
}

// Check checks the login shell, oh-my-zsh, and that every snippet of an
// enabled shell is a file in the config repo.
func (c *Config) Check(check *config.Check) {
	enabled := map[string]bool{}
	var names []string
	for _, sh := range c.shells() {
		names = append(names, sh.name)
		enabled[sh.name] = sh.settings.Enabled
		if !sh.settings.Enabled {
			continue
		}
		for _, p := range sh.settings.PreSnippets {
			checkSnippet(check, sh.name+".pre_snippets", p)
		}
		for _, p := range sh.settings.Snippets {
			checkSnippet(check, sh.name+".snippets", p)
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
	for _, name := range slices.Sorted(maps.Keys(omz.CustomThemes)) {
		theme := omz.CustomThemes[name]
		key := config.JoinKey("zsh", "oh_my_zsh", "custom_themes", name)
		required(check, key+".repo", theme.Repo, "the URL of the git repo the theme is in")
		required(check, key+".ref", theme.Ref, "the branch, tag or commit to check out")
		required(check, key+".file", theme.File, "the theme file in the repo, such as \"aphrodite.zsh-theme\"")
	}
	for _, name := range slices.Sorted(maps.Keys(omz.CustomPlugins)) {
		plugin := omz.CustomPlugins[name]
		key := config.JoinKey("zsh", "oh_my_zsh", "custom_plugins", name)
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

// Targets returns the environment file of every enabled shell, and its rc
// file if maisys manages it.
func (c *Config) Targets(*config.Machine) []config.Target {
	var targets []config.Target
	for _, sh := range c.shells() {
		if !sh.settings.Enabled {
			continue
		}
		targets = append(targets, config.Target{Path: sh.env, Key: config.JoinKey("shell", sh.name, "enabled")})
		if sh.rc != "" && sh.manageRC {
			targets = append(targets, config.Target{Path: sh.rc, Key: config.JoinKey("shell", sh.name, "manage_rc")})
		}
	}
	return targets
}
