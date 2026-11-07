package shells

import (
	"fmt"
	"slices"

	"github.com/plombardi89/maisys2/internal/config"
)

// The paths the shells' files refer to.
const (
	// OhMyZshDir is where oh-my-zsh is checked out.
	OhMyZshDir = "~/.oh-my-zsh"
	// OhMyZshCustomDir is oh-my-zsh's custom folder ($ZSH_CUSTOM), which
	// holds the custom themes and plugins.
	OhMyZshCustomDir = OhMyZshDir + "/custom"
	// SecretsFile is sourced last by the rc files maisys writes for bash
	// and zsh, if it exists.
	SecretsFile = "~/secrets.sh"
)

// Shell is the adapter for one shell: how it's installed, and how maisys
// writes its environment and rc files. bash, zsh and fish are built in
// (see All); adding a shell means adding an adapter.
type Shell interface {
	// Name is the shell's name, as the shell module's config names it,
	// such as "zsh".
	Name() string
	// Package is the package that installs the shell on os, or "" if
	// maisys can't install it there.
	Package(os config.OS) string
	// Binary is the shell's path, as a login shell.
	Binary() string
	// EnvFile is where the shell's environment file goes, as "~/...".
	EnvFile() string
	// RenderEnv writes the environment file for cfg. It warns about the
	// init steps the shell has no version of, which it leaves out.
	RenderEnv(cfg Config) ([]byte, []Warning)
	// RCFile is the rc file maisys can manage, as "~/...", or "" if the
	// shell has none: then the environment file holds everything.
	RCFile() string
	// RenderRC writes the rc file for cfg. It loads the environment file.
	RenderRC(cfg Config) []byte
}

// All returns the built-in shells, in the order the shell module lists
// them.
func All() []Shell {
	return []Shell{Bash{}, Zsh{}, Fish{}}
}

// Config is what a shell's files are written from: the shell's own
// settings from the shell module, and the environment of every enabled
// module.
type Config struct {
	// Env is the environment of every enabled module (see Merge).
	Env Env
	// Variables are the shell's own variables, exported before anything
	// else. Their values are written as Env's are.
	Variables []Variable
	// PreSnippets are files to source after Variables, before oh-my-zsh
	// and the environment, and Snippets files to source after everything
	// else, as "~/..." or absolute paths.
	PreSnippets, Snippets []string
	// ManageRC reports whether maisys writes the shell's rc file, which
	// then sets the variables, sources the snippets and loads oh-my-zsh,
	// and the environment file only holds Env. Otherwise the environment
	// file holds everything. Shells without an rc file ignore it.
	ManageRC bool
	// OhMyZsh, if it isn't nil, loads oh-my-zsh from OhMyZshDir. Only zsh
	// has it.
	OhMyZsh *OhMyZsh
}

// OhMyZsh is how zsh loads oh-my-zsh.
type OhMyZsh struct {
	// Theme is the theme, or "" for none.
	Theme string
	// Plugins are the plugins to load, in order.
	Plugins []string
}

// Warning is an init step a shell leaves out because the step has no
// version for it.
type Warning struct {
	// Shell is the shell's name, and Step the init step's.
	Shell, Step string
}

// String says what's left out, as the plan shows it, such as "fish: sdkman
// has no fish integration (skipped)".
func (w Warning) String() string {
	return fmt.Sprintf("%s: %s has no %s integration (skipped)", w.Shell, w.Step, w.Shell)
}

// Bash is the adapter for bash.
type Bash struct{}

var _ Shell = Bash{}

// Name returns "bash".
func (Bash) Name() string { return "bash" }

// Package returns "bash" on Fedora and Ubuntu.
func (Bash) Package(os config.OS) string { return linuxPackage(os, "bash") }

// Binary returns /usr/bin/bash.
func (Bash) Binary() string { return "/usr/bin/bash" }

// EnvFile returns ~/.config/maisys/shell/env.sh.
func (Bash) EnvFile() string { return "~/.config/maisys/shell/env.sh" }

// RCFile returns ~/.bashrc.
func (Bash) RCFile() string { return "~/.bashrc" }

// RenderEnv writes env.sh.
func (sh Bash) RenderEnv(cfg Config) ([]byte, []Warning) {
	return sh.posix().env(cfg)
}

// RenderRC writes ~/.bashrc. It loads the system's bash settings first,
// as Fedora's own ~/.bashrc does (Ubuntu's bash loads them itself).
func (sh Bash) RenderRC(cfg Config) []byte {
	return sh.posix().rcFile(cfg)
}

func (sh Bash) posix() posix {
	return posix{
		shell:  sh,
		step:   func(s InitStep) string { return s.Bash },
		rc:     sh.RCFile(),
		system: "/etc/bashrc",
	}
}

// Zsh is the adapter for zsh.
type Zsh struct{}

var _ Shell = Zsh{}

// Name returns "zsh".
func (Zsh) Name() string { return "zsh" }

// Package returns "zsh" on Fedora and Ubuntu.
func (Zsh) Package(os config.OS) string { return linuxPackage(os, "zsh") }

// Binary returns /usr/bin/zsh.
func (Zsh) Binary() string { return "/usr/bin/zsh" }

// EnvFile returns ~/.config/maisys/shell/env.zsh.
func (Zsh) EnvFile() string { return "~/.config/maisys/shell/env.zsh" }

// RCFile returns ~/.zshrc.
func (Zsh) RCFile() string { return "~/.zshrc" }

// RenderEnv writes env.zsh.
func (sh Zsh) RenderEnv(cfg Config) ([]byte, []Warning) {
	return sh.posix().env(cfg)
}

// RenderRC writes ~/.zshrc.
func (sh Zsh) RenderRC(cfg Config) []byte {
	return sh.posix().rcFile(cfg)
}

func (sh Zsh) posix() posix {
	return posix{
		shell:   sh,
		step:    func(s InitStep) string { return s.Zsh },
		rc:      sh.RCFile(),
		ohMyZsh: true,
	}
}

// Fish is the adapter for fish. fish loads every file in
// ~/.config/fish/conf.d by itself, so maisys writes one there and never
// changes config.fish: that file holds everything, and fish has no rc file
// maisys manages.
type Fish struct{}

var _ Shell = Fish{}

// Name returns "fish".
func (Fish) Name() string { return "fish" }

// Package returns "fish" on Fedora and Ubuntu.
func (Fish) Package(os config.OS) string { return linuxPackage(os, "fish") }

// Binary returns /usr/bin/fish.
func (Fish) Binary() string { return "/usr/bin/fish" }

// EnvFile returns ~/.config/fish/conf.d/maisys.fish.
func (Fish) EnvFile() string { return "~/.config/fish/conf.d/maisys.fish" }

// RCFile returns "": fish has no rc file maisys manages.
func (Fish) RCFile() string { return "" }

// RenderEnv writes maisys.fish.
func (sh Fish) RenderEnv(cfg Config) ([]byte, []Warning) {
	return fishEnv(cfg)
}

// RenderRC returns nil: fish has no rc file maisys manages.
func (Fish) RenderRC(Config) []byte { return nil }

// linuxPackage returns name on Fedora and Ubuntu, and "" elsewhere.
func linuxPackage(os config.OS, name string) string {
	if slices.Contains(config.LinuxOS(), os) {
		return name
	}
	return ""
}

// initSteps returns env's init steps in the order they run: in order, with
// those marked Last at the end.
func initSteps(env Env) []InitStep {
	steps := slices.Clone(env.Init)
	slices.SortStableFunc(steps, func(a, b InitStep) int {
		switch {
		case a.Last == b.Last:
			return 0
		case b.Last:
			return -1
		default:
			return 1
		}
	})
	return steps
}
