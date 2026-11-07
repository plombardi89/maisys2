package shell

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/checkout"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/link"
	"github.com/plombardi89/maisys2/internal/resource/loginshell"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/shells"
)

// OhMyZshRepo is the git repo oh-my-zsh is cloned from.
const OhMyZshRepo = "https://github.com/ohmyzsh/ohmyzsh"

// snippetDir is the folder each shell's snippets are copied into, followed
// by the shell's name.
const snippetDir = "~/.config/maisys/shell"

// binDir is the first folder on PATH in every shell: maisys and the
// commands of the tools it installs are there.
const binDir = "~/.local/bin"

var _ engine.Module = Module{}

// DependsOn returns nothing: the shells' packages are installed together
// with every other module's, and the files the shell module writes only
// refer to what other modules install.
func (Module) DependsOn() []string { return nil }

// Resources returns the shell module's resources, in the order they're
// applied: the packages of the enabled shells; oh-my-zsh and its custom
// themes and plugins; for each enabled shell, its snippets, its
// environment file and its rc file; and the login shell.
//
// The environment is that of every enabled module that implements
// shells.Contributor, in module order, after ~/.local/bin on PATH.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the shell module can't use a config of type %T", cfg)
	}
	env, err := environment(rt)
	if err != nil {
		return nil, err
	}

	var rs []resource.Resource
	var osName config.OS
	if rt.Machine != nil {
		osName = rt.Machine.OS
	}
	var packages []string
	for _, sh := range c.shells() {
		if !sh.settings.Enabled {
			continue
		}
		name := sh.Package(osName)
		if name == "" {
			return nil, fmt.Errorf("maisys can't install %s on %q", sh.Name(), osName)
		}
		packages = append(packages, name)
	}
	rs = append(rs, pkg.Set(rt, "shell", packages)...)

	omz, err := c.ohMyZsh(rt)
	if err != nil {
		return nil, err
	}
	rs = append(rs, omz...)

	for _, sh := range c.shells() {
		if !sh.settings.Enabled {
			continue
		}
		files, err := c.files(rt, sh, env)
		if err != nil {
			return nil, err
		}
		rs = append(rs, files...)
	}

	for _, sh := range c.shells() {
		if sh.settings.Enabled && sh.Name() == c.Login {
			rs = append(rs, &loginshell.Shell{Path: sh.Binary()})
		}
	}
	return rs, nil
}

// environment collects the environment of every enabled module that adds
// to it, in module order, after ~/.local/bin on PATH.
func environment(rt *resource.Runtime) (shells.Env, error) {
	envs := []shells.Env{{Path: []string{binDir}}}
	if rt.Machine == nil {
		return shells.Merge(envs...), nil
	}
	for _, mod := range rt.Modules {
		contributor, ok := mod.(shells.Contributor)
		name := mod.Info().Name
		cfg := rt.Machine.Modules[name]
		if !ok || cfg == nil || !cfg.IsEnabled() {
			continue
		}
		env, err := contributor.ShellEnv(cfg, rt)
		if err == nil {
			err = env.Check()
		}
		if err != nil {
			return shells.Env{}, fmt.Errorf("the shell environment of module %s: %w", name, err)
		}
		envs = append(envs, env)
	}
	return shells.Merge(envs...), nil
}

// files returns the files of an enabled shell: its snippets, its
// environment file, and its rc file if maisys manages it. The plan shows
// the init steps the shell leaves out under its environment file.
func (c *Config) files(rt *resource.Runtime, sh shellFiles, env shells.Env) ([]resource.Resource, error) {
	snippets, err := sh.snippets()
	if err != nil {
		return nil, err
	}
	var rs []resource.Resource
	for _, s := range snippets {
		if rt.RepoFS == nil {
			return nil, errors.New("the config repo's files aren't available, so the shells' snippets can't be copied")
		}
		files, err := file.Source{FS: rt.RepoFS, Path: s.source, Target: s.target}.Files()
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			rs = append(rs, f)
		}
	}

	target := func(p string) string { return snippetFile(sh.Name(), p) }
	vars := slices.Sorted(maps.Keys(sh.settings.Variables))
	cfg := shells.Config{
		Env:         env,
		Variables:   make([]shells.Variable, len(vars)),
		PreSnippets: mapped(sh.settings.PreSnippets, target),
		Snippets:    mapped(sh.settings.Snippets, target),
		ManageRC:    sh.manageRC,
	}
	for i, name := range vars {
		cfg.Variables[i] = shells.Variable{Name: name, Value: sh.settings.Variables[name]}
	}
	if omz := c.Zsh.OhMyZsh; sh.Name() == "zsh" && omz.Enabled {
		cfg.OhMyZsh = &shells.OhMyZsh{Theme: omz.Theme, Plugins: omz.Plugins}
	}

	data, warnings := sh.RenderEnv(cfg)
	notes := make([]string, len(warnings))
	for i, w := range warnings {
		notes[i] = w.String()
	}
	rs = append(rs, &noted{File: &file.File{Path: sh.EnvFile(), Content: file.Bytes(data)}, notes: notes})
	if sh.manageRC && sh.RCFile() != "" {
		rs = append(rs, &file.File{Path: sh.RCFile(), Content: file.Bytes(sh.RenderRC(cfg))})
	}
	return rs, nil
}

func mapped(items []string, f func(string) string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = f(item)
	}
	return out
}

// snippet is a snippet in the config repo, and where it's copied.
type snippet struct {
	source, target string
}

// snippets returns the shell's snippets, pre-snippets first, each once. It
// returns a *clash if two of them would be copied to the same file.
func (sh shellFiles) snippets() ([]snippet, error) {
	var out []snippet
	for _, p := range slices.Concat(sh.settings.PreSnippets, sh.settings.Snippets) {
		s := snippet{source: p, target: snippetFile(sh.Name(), p)}
		i := slices.IndexFunc(out, func(o snippet) bool { return o.target == s.target })
		switch {
		case i < 0:
			out = append(out, s)
		case out[i].source != s.source:
			return nil, &clash{shell: sh.Name(), first: out[i].source, second: s.source, target: s.target}
		}
	}
	return out, nil
}

// clash is two snippets of a shell that would be copied to the same file.
type clash struct {
	shell, first, second, target string
}

func (e *clash) Error() string {
	return fmt.Sprintf("the %s snippets %s and %s would both be copied to %s", e.shell, e.first, e.second, e.target)
}

// snippetFile returns where a snippet of shell, the file p in the config
// repo, is copied: into the shell's folder in snippetDir, without the
// "shell/<shell>/" p starts with, if it does.
func snippetFile(shell, p string) string {
	return snippetDir + "/" + shell + "/" + strings.TrimPrefix(path.Clean(p), "shell/"+shell+"/")
}

// ohMyZsh returns oh-my-zsh's resources, if it's enabled: its checkout,
// then each custom theme's checkout and the link to its theme file, then
// each custom plugin's checkout, by name.
func (c *Config) ohMyZsh(rt *resource.Runtime) ([]resource.Resource, error) {
	omz := c.Zsh.OhMyZsh
	if !c.Zsh.Enabled || !omz.Enabled {
		return nil, nil
	}
	dir, err := file.Expand(rt.Paths.Home, shells.OhMyZshDir)
	if err != nil {
		return nil, err
	}
	custom, err := file.Expand(rt.Paths.Home, shells.OhMyZshCustomDir)
	if err != nil {
		return nil, err
	}

	rs := []resource.Resource{&verified{
		Checkout: &checkout.Checkout{Name: "oh-my-zsh", Repo: OhMyZshRepo, Ref: omz.Ref, Dir: dir},
		verify:   omz.included,
	}}
	for _, name := range slices.Sorted(maps.Keys(omz.CustomThemes)) {
		theme := omz.CustomThemes[name]
		if !validName(name) {
			return nil, fmt.Errorf("%q isn't a valid oh-my-zsh theme name", name)
		}
		if !fs.ValidPath(theme.File) || theme.File == "." {
			return nil, fmt.Errorf("theme %s: %q isn't a file in its repo", name, theme.File)
		}
		rs = append(rs,
			&verified{
				Checkout: &checkout.Checkout{Name: name, Repo: theme.Repo, Ref: theme.Ref, Dir: filepath.Join(custom, "themes", name)},
				verify:   theme.has(name),
			},
			&link.Link{
				Path:   shells.OhMyZshCustomDir + "/themes/" + name + ".zsh-theme",
				Target: name + "/" + theme.File,
			},
		)
	}
	for _, name := range slices.Sorted(maps.Keys(omz.CustomPlugins)) {
		plugin := omz.CustomPlugins[name]
		if !validName(name) {
			return nil, fmt.Errorf("%q isn't a valid oh-my-zsh plugin name", name)
		}
		rs = append(rs, &checkout.Checkout{Name: name, Repo: plugin.Repo, Ref: plugin.Ref, Dir: filepath.Join(custom, "plugins", name)})
	}
	return rs, nil
}

// included checks that oh-my-zsh, checked out in dir, includes the theme
// and plugins that aren't custom.
func (omz OhMyZsh) included(dir string) error {
	var errs []error
	if theme := omz.Theme; theme != "" && theme != "random" && !hasKey(omz.CustomThemes, theme) {
		if !exists(filepath.Join(dir, "themes", theme+".zsh-theme"), false) {
			errs = append(errs, fmt.Errorf("oh-my-zsh has no theme %q: set shell.zsh.oh_my_zsh.theme to one of its themes, or add it to custom_themes", theme))
		}
	}
	for _, plugin := range omz.Plugins {
		if hasKey(omz.CustomPlugins, plugin) {
			continue
		}
		if !validName(plugin) || !exists(filepath.Join(dir, "plugins", plugin), true) {
			errs = append(errs, fmt.Errorf("oh-my-zsh has no plugin %q: take it out of shell.zsh.oh_my_zsh.plugins, or add it to custom_plugins", plugin))
		}
	}
	return errors.Join(errs...)
}

// has returns a check that the theme called name, checked out in a
// folder, has its theme file.
func (t CustomTheme) has(name string) func(dir string) error {
	return func(dir string) error {
		if !exists(filepath.Join(dir, filepath.FromSlash(t.File)), false) {
			return fmt.Errorf("theme %s: %s has no file %q at %s: set shell.zsh.oh_my_zsh.custom_themes.%s.file to the theme's file", name, t.Repo, t.File, t.Ref, name)
		}
		return nil
	}
}

func hasKey[V any](m map[string]V, key string) bool {
	_, ok := m[key]
	return ok
}

// exists reports whether there's a folder at p if dir is set, and a file
// otherwise.
func exists(p string, dir bool) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir() == dir
}

// verified is a git checkout with more to check once it's at its ref.
// Its ID and kind are the checkout's, so it's removed as one.
type verified struct {
	*checkout.Checkout
	// verify checks the checkout in its folder.
	verify func(dir string) error
}

// Check checks the checkout, and if it's at its ref, verifies it. A
// checkout that's about to change is verified at the next sync, once
// it's changed.
func (v *verified) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	c, err := v.Checkout.Check(ctx, rt)
	if err != nil || c.Action != resource.NoChange {
		return c, err
	}
	return c, v.verify(v.Dir)
}

// noted is a file whose change carries notes for the plan, such as the
// init steps an environment file leaves out. Its ID and kind are the
// file's.
type noted struct {
	*file.File
	notes []string
}

// Check checks the file, and adds the notes to its change.
func (n *noted) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	c, err := n.File.Check(ctx, rt)
	if err != nil {
		return c, err
	}
	c.Notes = append(c.Notes, n.notes...)
	return c, nil
}
