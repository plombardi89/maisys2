// Package shell is the shell module.
package shell

import "github.com/plombardi89/maisys2/internal/config"

// Module is the shell module.
type Module struct{}

// Info describes the shell module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "shell",
		Summary: "Installs bash, zsh and fish, sets the login shell, writes each shell's environment and rc files, and manages oh-my-zsh.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the shell module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{
		Bash: Bash{ManageRC: true},
		Zsh: Zsh{
			ManageRC: true,
			OhMyZsh:  OhMyZsh{Ref: "master", Theme: "robbyrussell"},
		},
	}
}

// Config is the shell module's config. Shells are off unless enabled.
type Config struct {
	config.Toggle
	// Login is the login shell: "bash", "zsh" or "fish", which must be
	// enabled. Without it, the login shell isn't changed.
	Login string `toml:"login"`
	// Bash configures bash.
	Bash Bash `toml:"bash"`
	// Zsh configures zsh.
	Zsh Zsh `toml:"zsh"`
	// Fish configures fish, which loads maisys's environment from
	// ~/.config/fish/conf.d, so its config.fish is never changed.
	Fish Fish `toml:"fish"`
}

// Settings are the settings every shell has.
type Settings struct {
	// Enabled installs the shell and writes its environment file.
	Enabled bool `toml:"enabled"`
	// Variables are set before anything else is loaded, so they can
	// configure what comes after, such as oh-my-zsh.
	Variables map[string]string `toml:"variables"`
	// PreSnippets are files in the config repo, in the shell's own syntax,
	// that are sourced after variables and before oh-my-zsh and maisys's
	// environment.
	PreSnippets []string `toml:"pre_snippets" merge:"union"`
	// Snippets are files in the config repo, in the shell's own syntax,
	// that are sourced after everything else.
	Snippets []string `toml:"snippets" merge:"union"`
}

// Bash is the bash config.
type Bash struct {
	Settings
	// ManageRC lets maisys write ~/.bashrc. Set it to false to keep your
	// own, and source ~/.config/maisys/shell/env.sh from it.
	ManageRC bool `toml:"manage_rc"`
}

// Zsh is the zsh config.
type Zsh struct {
	Settings
	// ManageRC lets maisys write ~/.zshrc. Set it to false to keep your
	// own, and source ~/.config/maisys/shell/env.zsh from it.
	ManageRC bool `toml:"manage_rc"`
	// OhMyZsh configures oh-my-zsh.
	OhMyZsh OhMyZsh `toml:"oh_my_zsh"`
}

// Fish is the fish config.
type Fish struct {
	Settings
}

// OhMyZsh is the oh-my-zsh config.
type OhMyZsh struct {
	// Enabled installs oh-my-zsh, with its own updater turned off, and
	// loads it from ~/.zshrc.
	Enabled bool `toml:"enabled"`
	// Ref is the oh-my-zsh branch, tag or commit to check out. A branch is
	// updated on every sync.
	Ref string `toml:"ref"`
	// Theme is the theme to use: one oh-my-zsh includes, or one from
	// custom_themes.
	Theme string `toml:"theme"`
	// Plugins lists the plugins to load: ones oh-my-zsh includes, or ones
	// from custom_plugins.
	Plugins []string `toml:"plugins" merge:"union"`
	// CustomThemes are themes from other git repos, by theme name.
	CustomThemes map[string]CustomTheme `toml:"custom_themes"`
	// CustomPlugins are plugins from other git repos, by plugin name.
	CustomPlugins map[string]CustomPlugin `toml:"custom_plugins"`
}

// CustomTheme is an oh-my-zsh theme from a git repo.
type CustomTheme struct {
	// Repo is the URL of the git repo.
	Repo string `toml:"repo"`
	// Ref is the branch, tag or commit to check out. A branch is updated
	// on every sync.
	Ref string `toml:"ref"`
	// File is the theme file in the repo, such as "aphrodite.zsh-theme".
	File string `toml:"file"`
}

// CustomPlugin is an oh-my-zsh plugin from a git repo.
type CustomPlugin struct {
	// Repo is the URL of the git repo.
	Repo string `toml:"repo"`
	// Ref is the branch, tag or commit to check out. A branch is updated
	// on every sync.
	Ref string `toml:"ref"`
}
