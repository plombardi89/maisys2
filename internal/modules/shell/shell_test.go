package shell_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/shell"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/checkout"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/link"
	"github.com/plombardi89/maisys2/internal/resource/loginshell"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/removers"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// linuxOnly skips tests of paths and checkouts on Windows, where the shell
// module never runs.
func linuxOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the shell module only runs on Linux")
	}
}

func ids(rs []resource.Resource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID()
	}
	return out
}

// find returns the resource with the given ID.
func find(t *testing.T, rs []resource.Resource, id string) resource.Resource {
	t.Helper()
	i := slices.IndexFunc(rs, func(r resource.Resource) bool { return r.ID() == id })
	if i < 0 {
		t.Fatalf("there's no resource %s in %q", id, ids(rs))
	}
	return rs[i]
}

// TestExample checks the resources of the example config repo's machines,
// whose shell settings come from profiles/base.toml, and what applying
// their files writes.
func TestExample(t *testing.T) {
	linuxOnly(t)
	repoFS := os.DirFS("../testdata/example")
	repo := &config.Repo{FS: repoFS, Modules: modules.All()}
	tests := []struct {
		machine  string
		os       config.OS
		version  string
		snippets []string // the zsh snippets after aliases.zsh
	}{
		{machine: "phil-workp14s", os: config.Ubuntu, version: "26.04"},
		{machine: "workbox-camwin-fc44", os: config.Fedora, version: "44", snippets: []string{"goproxy.zsh"}},
	}
	for _, tt := range tests {
		t.Run(tt.machine, func(t *testing.T) {
			m, err := repo.LoadMachine(tt.machine)
			if err != nil {
				t.Fatal(err)
			}
			home := t.TempDir()
			rt := &resource.Runtime{
				Runner:  systemtest.NewFakeRunner(t),
				Facts:   &facts.System{OS: tt.os, OSID: string(tt.os), OSVersion: tt.version, Arch: config.AMD64},
				Paths:   settings.Paths{Home: home},
				Machine: m,
				Modules: modules.All(),
				RepoFS:  repoFS,
			}
			rs, err := shell.Module{}.Resources(m.Modules["shell"], rt)
			if err != nil {
				t.Fatal(err)
			}

			omz := filepath.Join(home, ".oh-my-zsh")
			want := []string{
				"package:shell/bash",
				"package:shell/zsh",
				"package:shell/fish",
				"git-checkout:" + omz,
				"git-checkout:" + filepath.Join(omz, "custom", "themes", "aphrodite"),
				"symlink:~/.oh-my-zsh/custom/themes/aphrodite.zsh-theme",
				"git-checkout:" + filepath.Join(omz, "custom", "plugins", "zsh-autosuggestions"),
				"git-checkout:" + filepath.Join(omz, "custom", "plugins", "zsh-syntax-highlighting"),
				"file:~/.config/maisys/shell/bash/aliases.sh",
				"file:~/.config/maisys/shell/env.sh",
				"file:~/.config/maisys/shell/zsh/zstyles.zsh",
				"file:~/.config/maisys/shell/zsh/aliases.zsh",
			}
			for _, s := range tt.snippets {
				want = append(want, "file:~/.config/maisys/shell/zsh/"+s)
			}
			want = append(want,
				"file:~/.config/maisys/shell/env.zsh",
				"file:~/.zshrc",
				"file:~/.config/maisys/shell/fish/abbr.fish",
				"file:~/.config/fish/conf.d/maisys.fish",
				"login-shell",
			)
			if got := ids(rs); !slices.Equal(got, want) {
				t.Fatalf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}

			for _, r := range rs[:3] {
				p, ok := r.(*pkg.Package)
				if !ok || p.Name() != strings.TrimPrefix(r.ID(), "package:shell/") {
					t.Errorf("%s is a %T", r.ID(), r)
				}
			}
			checkouts := map[string][2]string{
				omz: {shell.OhMyZshRepo, "master"},
				filepath.Join(omz, "custom", "themes", "aphrodite"):                {"https://github.com/win0err/aphrodite-terminal-theme", "master"},
				filepath.Join(omz, "custom", "plugins", "zsh-autosuggestions"):     {"https://github.com/zsh-users/zsh-autosuggestions", "v0.7.1"},
				filepath.Join(omz, "custom", "plugins", "zsh-syntax-highlighting"): {"https://github.com/zsh-users/zsh-syntax-highlighting", "master"},
			}
			for dir, want := range checkouts {
				c, ok := shell.Unwrap(find(t, rs, "git-checkout:"+dir)).(*checkout.Checkout)
				if !ok || c.Repo != want[0] || c.Ref != want[1] || c.Dir != dir {
					t.Errorf("checkout %s = %+v, want %s at %s", dir, c, want[0], want[1])
				}
			}
			l, ok := find(t, rs, "symlink:~/.oh-my-zsh/custom/themes/aphrodite.zsh-theme").(*link.Link)
			if !ok || l.Target != "aphrodite/aphrodite.zsh-theme" {
				t.Errorf("the theme's link = %+v, want one to aphrodite/aphrodite.zsh-theme", l)
			}
			if s, ok := find(t, rs, "login-shell").(*loginshell.Shell); !ok || s.Path != "/usr/bin/zsh" || s.User != "" {
				t.Errorf("the login shell = %+v, want /usr/bin/zsh for you", s)
			}

			for _, r := range rs {
				if f, ok := shell.Unwrap(r).(*file.File); ok {
					if _, err := f.Apply(t.Context(), rt); err != nil {
						t.Fatal(err)
					}
				}
			}
			read := func(p string) string {
				t.Helper()
				data, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(p)))
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
			for _, s := range append([]string{"zstyles.zsh", "aliases.zsh"}, tt.snippets...) {
				repoData, err := os.ReadFile(filepath.Join("../testdata/example/shell/zsh", s))
				if err != nil {
					t.Fatal(err)
				}
				if got := read(".config/maisys/shell/zsh/" + s); got != string(repoData) {
					t.Errorf("the copy of %s holds %q, want %q", s, got, repoData)
				}
			}

			zshrc := read(".zshrc")
			inOrder(t, "~/.zshrc", zshrc,
				`export HIST_STAMPS="yyyy-mm-dd"`,
				`source "${HOME}/.config/maisys/shell/zsh/zstyles.zsh"`,
				`ZSH_THEME="aphrodite"`,
				"plugins=(git zsh-autosuggestions zsh-syntax-highlighting)",
				`source "${ZSH}/oh-my-zsh.sh"`,
				`source "${HOME}/.config/maisys/shell/env.zsh"`,
				`source "${HOME}/.config/maisys/shell/zsh/aliases.zsh"`,
				`source "${HOME}/secrets.sh"`,
			)
			// bash doesn't manage its rc file, so its environment file
			// sources its snippets.
			inOrder(t, "env.sh", read(".config/maisys/shell/env.sh"),
				`"${HOME}/.local/bin"`,
				`source "${HOME}/.config/maisys/shell/bash/aliases.sh"`,
			)
			inOrder(t, "maisys.fish", read(".config/fish/conf.d/maisys.fish"),
				`"$HOME/.local/bin"`,
				`source "$HOME/.config/maisys/shell/fish/abbr.fish"`,
			)
		})
	}
}

// inOrder checks that text has each of lines, in order.
func inOrder(t *testing.T, name, text string, lines ...string) {
	t.Helper()
	at := 0
	for _, line := range lines {
		i := strings.Index(text[at:], line)
		if i < 0 {
			t.Errorf("%s doesn't have %q after what came before:\n%s", name, line, text)
			return
		}
		at += i + len(line)
	}
}

// snippetRepo is a config repo with a snippet for each shell.
var snippetRepo = fstest.MapFS{
	"shell/bash/a.sh":     {Data: []byte("alias a=b\n")},
	"shell/zsh/a.zsh":     {Data: []byte("alias a=b\n")},
	"shell/zsh/x/y.zsh":   {Data: []byte("alias y=z\n")},
	"x/y.zsh":             {Data: []byte("alias y=z\n")},
	"common/posix.sh":     {Data: []byte("alias p=q\n")},
	"shell/fish/a.fish":   {Data: []byte("abbr a b\n")},
	"shell/fish/b/c.fish": {Data: []byte("abbr c d\n")},
}

func TestResources(t *testing.T) {
	linuxOnly(t)
	tests := []struct {
		name string
		cfg  *shell.Config
		want []string
	}{
		{
			name: "no shells",
			cfg:  &shell.Config{Enabled: true},
		},
		{
			name: "oh-my-zsh without zsh",
			cfg: &shell.Config{
				Enabled: true,
				Zsh:     shell.Zsh{OhMyZsh: shell.OhMyZsh{Enabled: true, Ref: "master"}},
			},
		},
		{
			name: "fish",
			cfg: &shell.Config{
				Enabled: true,
				Fish:    shell.Fish{Settings: shell.Settings{Enabled: true, PreSnippets: []string{"shell/fish/b/c.fish"}, Snippets: []string{"shell/fish/a.fish"}}},
			},
			want: []string{
				"package:shell/fish",
				"file:~/.config/maisys/shell/fish/b/c.fish",
				"file:~/.config/maisys/shell/fish/a.fish",
				"file:~/.config/fish/conf.d/maisys.fish",
			},
		},
		{
			name: "bash as the login shell",
			cfg: &shell.Config{
				Enabled: true,
				Login:   "bash",
				Bash:    shell.Bash{Settings: shell.Settings{Enabled: true}, ManageRC: true},
			},
			want: []string{
				"package:shell/bash",
				"file:~/.config/maisys/shell/env.sh",
				"file:~/.bashrc",
				"login-shell",
			},
		},
		{
			name: "zsh without its rc file, with oh-my-zsh",
			cfg: &shell.Config{
				Enabled: true,
				Zsh: shell.Zsh{
					Settings: shell.Settings{Enabled: true, Snippets: []string{"shell/zsh/a.zsh"}},
					OhMyZsh:  shell.OhMyZsh{Enabled: true, Ref: "master", Theme: "robbyrussell"},
				},
			},
			want: []string{
				"package:shell/zsh",
				"git-checkout:{home}/.oh-my-zsh",
				"file:~/.config/maisys/shell/zsh/a.zsh",
				"file:~/.config/maisys/shell/env.zsh",
			},
		},
		{
			name: "a snippet listed twice, and one from outside shell/",
			cfg: &shell.Config{
				Enabled: true,
				Bash: shell.Bash{
					Settings: shell.Settings{
						Enabled:     true,
						PreSnippets: []string{"shell/bash/a.sh", "common/posix.sh"},
						Snippets:    []string{"shell/bash/a.sh"},
					},
				},
				Zsh: shell.Zsh{Settings: shell.Settings{Enabled: true, Snippets: []string{"common/posix.sh"}}},
			},
			want: []string{
				"package:shell/bash",
				"package:shell/zsh",
				"file:~/.config/maisys/shell/bash/a.sh",
				"file:~/.config/maisys/shell/bash/common/posix.sh",
				"file:~/.config/maisys/shell/env.sh",
				"file:~/.config/maisys/shell/zsh/common/posix.sh",
				"file:~/.config/maisys/shell/env.zsh",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			rt := &resource.Runtime{
				Paths:   settings.Paths{Home: home},
				Machine: &config.Machine{Name: "box", OS: config.Fedora, Modules: map[string]config.ModuleConfig{"shell": tt.cfg}},
				RepoFS:  snippetRepo,
			}
			rs, err := shell.Module{}.Resources(tt.cfg, rt)
			if err != nil {
				t.Fatal(err)
			}
			want := make([]string, len(tt.want))
			for i, id := range tt.want {
				want[i] = strings.ReplaceAll(id, "{home}", home)
			}
			if got := ids(rs); !slices.Equal(got, want) {
				t.Errorf("resources:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

func TestResourcesErrors(t *testing.T) {
	linuxOnly(t)
	zsh := func(snippets ...string) *shell.Config {
		return &shell.Config{Enabled: true, Zsh: shell.Zsh{Settings: shell.Settings{Enabled: true, Snippets: snippets}}}
	}
	tests := []struct {
		name   string
		cfg    config.ModuleConfig
		repoFS fstest.MapFS
		want   string
	}{
		{
			name:   "two snippets copied to the same file",
			cfg:    zsh("shell/zsh/x/y.zsh", "x/y.zsh"),
			repoFS: snippetRepo,
			want:   "the zsh snippets shell/zsh/x/y.zsh and x/y.zsh would both be copied to ~/.config/maisys/shell/zsh/x/y.zsh",
		},
		{
			name: "no config repo",
			cfg:  zsh("shell/zsh/a.zsh"),
			want: "the config repo's files aren't available, so the shells' snippets can't be copied",
		},
		{
			name: "another module's config",
			cfg:  &config.Toggle{Enabled: true},
			want: "the shell module can't use a config of type *config.Toggle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &resource.Runtime{
				Paths:   settings.Paths{Home: t.TempDir()},
				Machine: &config.Machine{Name: "box", OS: config.Fedora},
			}
			if tt.repoFS != nil {
				rt.RepoFS = tt.repoFS
			}
			if _, err := (shell.Module{}).Resources(tt.cfg, rt); err == nil || err.Error() != tt.want {
				t.Errorf("Resources() = %v, want %q", err, tt.want)
			}
		})
	}
}

// contributor is a module that adds to the shell environment.
type contributor struct {
	name string
	env  shells.Env
	err  error
}

func (c contributor) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: c.name, Summary: "A test module.", OS: config.LinuxOS()}
}

func (c contributor) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func (c contributor) ShellEnv(cfg config.ModuleConfig, rt *resource.Runtime) (shells.Env, error) {
	if !cfg.IsEnabled() || rt.Machine == nil {
		return shells.Env{}, errors.New("ShellEnv got the wrong config")
	}
	return c.env, c.err
}

// plain is a module that adds nothing to the shell environment.
type plain struct{}

func (plain) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: "plain", Summary: "A test module.", OS: config.LinuxOS()}
}

func (plain) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func TestEnvironment(t *testing.T) {
	linuxOnly(t)
	golang := contributor{name: "golang", env: shells.Env{
		Path:      []string{"$GOPATH/bin", "~/.local/bin"},
		Variables: []shells.Variable{{Name: "GOPATH", Value: "~/go"}},
	}}
	sdkman := contributor{name: "sdkman", env: shells.Env{
		Init: []shells.InitStep{{Name: "sdkman", Bash: "source ~/.sdkman/bin/sdkman-init.sh", Zsh: "source ~/.sdkman/bin/sdkman-init.sh", Last: true}},
	}}
	off := contributor{name: "off", env: shells.Env{Variables: []shells.Variable{{Name: "OFF", Value: "1"}}}}
	direnv := contributor{name: "direnv", env: shells.Env{
		Init: []shells.InitStep{{Name: "direnv", Bash: `eval "$(direnv hook bash)"`, Zsh: `eval "$(direnv hook zsh)"`, Fish: "direnv hook fish | source"}},
	}}
	cfg := &shell.Config{
		Enabled: true,
		Zsh:     shell.Zsh{Settings: shell.Settings{Enabled: true}, ManageRC: true},
		Fish:    shell.Fish{Settings: shell.Settings{Enabled: true, Variables: map[string]string{"B": "2", "A": "1"}}},
	}
	machine := &config.Machine{Name: "box", OS: config.Ubuntu, Modules: map[string]config.ModuleConfig{
		"golang": &config.Toggle{Enabled: true},
		"sdkman": &config.Toggle{Enabled: true},
		"off":    &config.Toggle{},
		"plain":  &config.Toggle{Enabled: true},
		"shell":  cfg,
		"direnv": &config.Toggle{Enabled: true},
	}}
	home := t.TempDir()
	rt := &resource.Runtime{
		Paths:   settings.Paths{Home: home},
		Machine: machine,
		Modules: []config.Module{golang, sdkman, off, plain{}, shell.Module{}, direnv},
	}
	rs, err := shell.Module{}.Resources(cfg, rt)
	if err != nil {
		t.Fatal(err)
	}

	env := shells.Merge(shells.Env{Path: []string{"~/.local/bin"}}, golang.env, sdkman.env, direnv.env)
	tests := []struct {
		id    string
		shell shells.Shell
		cfg   shells.Config
		notes []string
		rc    bool // whether the file is the rc file
	}{
		{id: "file:~/.config/maisys/shell/env.zsh", shell: shells.Zsh{}, cfg: shells.Config{Env: env, ManageRC: true}},
		{id: "file:~/.zshrc", shell: shells.Zsh{}, cfg: shells.Config{Env: env, ManageRC: true}, rc: true},
		{
			id:    "file:~/.config/fish/conf.d/maisys.fish",
			shell: shells.Fish{},
			cfg:   shells.Config{Env: env, Variables: []shells.Variable{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}}},
			notes: []string{"fish: sdkman has no fish integration (skipped)"},
		},
	}
	for _, tt := range tests {
		r := find(t, rs, tt.id)
		f, ok := shell.Unwrap(r).(*file.File)
		if !ok {
			t.Fatalf("%s is a %T", tt.id, r)
		}
		got, err := f.Content(rt)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := tt.shell.RenderEnv(tt.cfg)
		if tt.rc {
			want = tt.shell.RenderRC(tt.cfg)
		}
		if string(got) != string(want) {
			t.Errorf("%s holds:\n%s\nwant:\n%s", tt.id, got, want)
		}

		c, err := r.Check(t.Context(), rt)
		if err != nil || c.Action != resource.Create || !slices.Equal(c.Notes, tt.notes) {
			t.Errorf("%s: Check() = %+v, %v; want a new file with notes %q", tt.id, c, err, tt.notes)
		}
	}

	t.Run("a module that fails", func(t *testing.T) {
		rt.Modules = []config.Module{golang, contributor{name: "sdkman", err: errors.New("no SDKMAN")}}
		_, err := shell.Module{}.Resources(cfg, rt)
		if err == nil || err.Error() != "the shell environment of module sdkman: no SDKMAN" {
			t.Errorf("Resources() = %v", err)
		}
	})
	t.Run("a module with a bad environment", func(t *testing.T) {
		rt.Modules = []config.Module{contributor{name: "golang", env: shells.Env{Variables: []shells.Variable{{Name: "GO PATH"}}}}}
		_, err := shell.Module{}.Resources(cfg, rt)
		if err == nil || err.Error() != `the shell environment of module golang: "GO PATH" isn't a valid variable name` {
			t.Errorf("Resources() = %v", err)
		}
	})
}

// omzHead is the commit the fake oh-my-zsh checkouts are at.
const omzHead = "1111111111111111111111111111111111111111"

// TestOhMyZshNames checks that, once oh-my-zsh is checked out at its ref,
// the plan fails for a theme or plugin it doesn't include that isn't
// custom, and for a custom theme without its file.
func TestOhMyZshNames(t *testing.T) {
	linuxOnly(t)
	themeRepo := "https://example.com/theme"
	tests := []struct {
		name      string
		omz       shell.OhMyZsh
		cloned    bool   // whether oh-my-zsh is checked out
		remote    string // the commit the ref points to, if it's moved
		want      []string
		wantTheme string // the custom theme's error, if its file is missing
	}{
		{
			name:   "built in and custom",
			omz:    shell.OhMyZsh{Theme: "robbyrussell", Plugins: []string{"git", "mine"}},
			cloned: true,
		},
		{
			name:   "a custom theme",
			omz:    shell.OhMyZsh{Theme: "fancy", Plugins: []string{"git"}},
			cloned: true,
		},
		{
			name:   "no theme, or a random one",
			omz:    shell.OhMyZsh{Theme: "random"},
			cloned: true,
		},
		{
			name:   "unknown names",
			omz:    shell.OhMyZsh{Theme: "robbyrusel", Plugins: []string{"git", "gti", "../plugins/git"}},
			cloned: true,
			want: []string{
				`oh-my-zsh has no theme "robbyrusel": set shell.zsh.oh_my_zsh.theme to one of its themes, or add it to custom_themes`,
				`oh-my-zsh has no plugin "gti": take it out of shell.zsh.oh_my_zsh.plugins, or add it to custom_plugins`,
				`oh-my-zsh has no plugin "../plugins/git": take it out of shell.zsh.oh_my_zsh.plugins, or add it to custom_plugins`,
			},
		},
		{
			name:   "unknown names while oh-my-zsh is moving to another commit",
			omz:    shell.OhMyZsh{Theme: "robbyrusel", Plugins: []string{"gti"}},
			cloned: true,
			remote: "2222222222222222222222222222222222222222",
		},
		{
			name: "unknown names before oh-my-zsh is cloned",
			omz:  shell.OhMyZsh{Theme: "robbyrusel", Plugins: []string{"gti"}},
		},
		{
			name:      "a custom theme without its file",
			omz:       shell.OhMyZsh{Theme: "fancy"},
			cloned:    true,
			wantTheme: `theme fancy: https://example.com/theme has no file "themes/fancy.zsh-theme" at main: set shell.zsh.oh_my_zsh.custom_themes.fancy.file to the theme's file`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			omzDir := filepath.Join(home, ".oh-my-zsh")
			themeDir := filepath.Join(omzDir, "custom", "themes", "fancy")
			if tt.cloned {
				mkdirs(t, filepath.Join(omzDir, ".git"), filepath.Join(omzDir, "plugins", "git"), filepath.Join(themeDir, ".git"))
				write(t, filepath.Join(omzDir, "themes", "robbyrussell.zsh-theme"))
				write(t, filepath.Join(omzDir, "plugins", "gti"))
				if tt.wantTheme == "" {
					write(t, filepath.Join(themeDir, "themes", "fancy.zsh-theme"))
				}
			}
			remote := tt.remote
			if remote == "" {
				remote = omzHead
			}
			origins := map[string]string{omzDir: shell.OhMyZshRepo, themeDir: themeRepo}

			runner := systemtest.NewFakeRunner(t)
			runner.On("git rev-parse", systemtest.Respond(0, omzHead+"\n"))
			runner.On("git --no-optional-locks status", systemtest.Respond(0, ""))
			runner.On("git config --get remote.origin.url", func(_ context.Context, c system.Cmd) (system.Result, error) {
				for _, e := range c.Env {
					if dir, ok := strings.CutPrefix(e, "GIT_WORK_TREE="); ok {
						return system.Result{Stdout: []byte(origins[dir] + "\n")}, nil
					}
				}
				return system.Result{ExitCode: 1}, nil
			})
			runner.On("git -c credential.helper= ls-remote", systemtest.Respond(0, remote+"\trefs/heads/master\n"+remote+"\trefs/heads/main\n"))

			omz := tt.omz
			omz.Enabled, omz.Ref = true, "master"
			omz.CustomThemes = map[string]shell.CustomTheme{"fancy": {Repo: themeRepo, Ref: "main", File: "themes/fancy.zsh-theme"}}
			omz.CustomPlugins = map[string]shell.CustomPlugin{"mine": {Repo: "https://example.com/mine", Ref: "main"}}
			cfg := &shell.Config{Enabled: true, Zsh: shell.Zsh{Settings: shell.Settings{Enabled: true}, OhMyZsh: omz}}
			rt := &resource.Runtime{
				Runner:  runner,
				Paths:   settings.Paths{Home: home},
				Machine: &config.Machine{Name: "box", OS: config.Fedora},
			}
			rs, err := shell.Module{}.Resources(cfg, rt)
			if err != nil {
				t.Fatal(err)
			}

			c, err := find(t, rs, "git-checkout:"+omzDir).Check(t.Context(), rt)
			var got []string
			if err != nil {
				got = strings.Split(err.Error(), "\n")
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("checking oh-my-zsh: %q, want %q", got, tt.want)
			}
			wantAction := resource.NoChange
			switch {
			case !tt.cloned:
				wantAction = resource.Create
			case tt.remote != "":
				wantAction = resource.Update
			}
			if c.Action != wantAction {
				t.Errorf("checking oh-my-zsh: %+v, want %s", c, wantAction)
			}

			if !tt.cloned {
				return
			}
			_, err = find(t, rs, "git-checkout:"+themeDir).Check(t.Context(), rt)
			if (err == nil) != (tt.wantTheme == "") || err != nil && err.Error() != tt.wantTheme {
				t.Errorf("checking the custom theme: %v, want %q", err, tt.wantTheme)
			}
		})
	}
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, p string) {
	t.Helper()
	mkdirs(t, filepath.Dir(p))
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDependsOn(t *testing.T) {
	if got := (shell.Module{}).DependsOn(); got != nil {
		t.Errorf("DependsOn() = %q, want nothing", got)
	}
}

// TestPlan plans the shell module with the engine, and checks the plan
// shows the init step fish leaves out, and that planning ran nothing as
// root.
func TestPlan(t *testing.T) {
	linuxOnly(t)
	sdkman := contributor{name: "sdkman", env: shells.Env{
		Init: []shells.InitStep{{Name: "sdkman", Bash: "source ~/.sdkman/bin/sdkman-init.sh", Zsh: "source ~/.sdkman/bin/sdkman-init.sh", Last: true}},
	}}
	cfg := &shell.Config{
		Enabled: true,
		Login:   "zsh",
		Zsh: shell.Zsh{
			Settings: shell.Settings{Enabled: true},
			ManageRC: true,
			OhMyZsh:  shell.OhMyZsh{Enabled: true, Ref: "master", Theme: "robbyrussell"},
		},
		Fish: shell.Fish{Settings: shell.Settings{Enabled: true}},
	}
	machine := &config.Machine{Name: "box", OS: config.Fedora, Modules: map[string]config.ModuleConfig{
		"shell":  cfg,
		"sdkman": &config.Toggle{Enabled: true},
	}}
	runner := systemtest.NewFakeRunner(t)
	runner.On("rpm -q", systemtest.Respond(1, "zsh\tx86_64\t5.9-1.fc44\n"))
	runner.On("dnf -q repoquery", systemtest.Respond(0, "fish\n"))
	runner.On("dnf -q check-update", systemtest.Respond(0, ""))
	runner.On("git -c credential.helper= ls-remote", systemtest.Respond(0, omzHead+"\trefs/heads/master\n"))
	runner.On("getent passwd", systemtest.Respond(0, "phil:x:1000:1000::/home/phil:/bin/bash\n"))

	p, err := engine.NewPlan(t.Context(), engine.PlanInput{
		Machine:  machine,
		Facts:    &facts.System{OS: config.Fedora, OSID: "fedora", OSVersion: "44", Arch: config.AMD64},
		Paths:    settings.Paths{Home: t.TempDir()},
		Runner:   runner,
		Modules:  []config.Module{shell.Module{}, sdkman},
		Removers: removers.All(),
		RepoFS:   fstest.MapFS{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	p.Render(&b, false)
	want := `Plan for box (fedora 44, amd64)

shell
  + install fish                                [sudo]
  + oh-my-zsh (master)
  + ~/.config/maisys/shell/env.zsh
  + ~/.zshrc
  + ~/.config/fish/conf.d/maisys.fish
  ! fish: sdkman has no fish integration (skipped)
  ~ login shell /bin/bash → /usr/bin/zsh        [sudo]

6 to change. Needs sudo.
`
	if b.String() != want {
		t.Errorf("plan:\n%s\nwant:\n%s", b.String(), want)
	}
	if root := runner.RootCommands(); len(root) > 0 {
		t.Errorf("planning ran %q as root", root)
	}
}
