package modules_test

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/files"
	"github.com/plombardi89/maisys2/internal/modules/golang"
	"github.com/plombardi89/maisys2/internal/modules/golangcilint"
	"github.com/plombardi89/maisys2/internal/modules/hashicorp"
	"github.com/plombardi89/maisys2/internal/modules/intellij"
	"github.com/plombardi89/maisys2/internal/modules/kubectl"
	"github.com/plombardi89/maisys2/internal/modules/packages"
	"github.com/plombardi89/maisys2/internal/modules/pythontools"
	"github.com/plombardi89/maisys2/internal/modules/shell"
	"github.com/plombardi89/maisys2/internal/modules/steam"
	"github.com/plombardi89/maisys2/internal/modules/sudo"
	"github.com/plombardi89/maisys2/internal/modules/windowsterminal"
	"github.com/plombardi89/maisys2/internal/modules/winget"
	"github.com/plombardi89/maisys2/internal/modules/wsl"
	"github.com/plombardi89/maisys2/internal/modules/wslhost"
)

// TestModules checks every module against the module tables in issue #1:
// the order, supported systems, and which modules install a versioned
// tool.
func TestModules(t *testing.T) {
	linux := config.LinuxOS()
	windows := []config.OS{config.Windows}
	want := []struct {
		name      string
		os        []config.OS
		arch      []config.Arch
		versioned bool
		hidden    bool
	}{
		{name: "sudo", os: linux},
		{name: "packages", os: linux},
		{name: "files", os: config.AllOS()},
		{name: "shell", os: linux},
		{name: "golang", os: linux, versioned: true},
		{name: "mage", os: linux, versioned: true},
		{name: "golangci_lint", os: linux, versioned: true},
		{name: "kubectl", os: linux, versioned: true},
		{name: "stern", os: linux, versioned: true},
		{name: "yq", os: linux, versioned: true},
		{name: "lnav", os: linux, versioned: true},
		{name: "protoc", os: linux, versioned: true},
		{name: "mutagen", os: linux, versioned: true},
		{name: "hashicorp", os: linux},
		{name: "podman", os: linux},
		{name: "sdkman", os: linux},
		{name: "opencode", os: linux},
		{name: "python_tools", os: linux},
		{name: "steam", os: linux, arch: []config.Arch{config.AMD64}},
		{name: "intellij", os: linux, versioned: true},
		{name: "wsl", os: linux},
		{name: "legacy", os: linux, hidden: true},
		{name: "winget", os: windows},
		{name: "wsl_host", os: windows},
		{name: "windows_terminal", os: windows},
	}

	all := modules.All()
	var names []string
	for _, mod := range all {
		names = append(names, mod.Name)
	}
	var wantNames []string
	for _, w := range want {
		wantNames = append(wantNames, w.name)
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("modules.All() = %v, want %v", names, wantNames)
	}

	for i, w := range want {
		mod := all[i]
		t.Run(w.name, func(t *testing.T) {
			if !slices.Equal(mod.OS, w.os) {
				t.Errorf("OS = %v, want %v", mod.OS, w.os)
			}
			if !slices.Equal(mod.Arch, w.arch) {
				t.Errorf("Arch = %v, want %v", mod.Arch, w.arch)
			}
			if mod.Hidden != w.hidden {
				t.Errorf("Hidden = %v, want %v", mod.Hidden, w.hidden)
			}
			if !strings.HasSuffix(mod.Summary, ".") {
				t.Errorf("Summary = %q, want a sentence", mod.Summary)
			}

			cfg := mod.NewConfig()
			if cfg.IsEnabled() {
				t.Error("the module is on by default")
			}
			if cfg == mod.NewConfig() {
				t.Error("NewConfig returns the same config every time")
			}
			version, versioned := versionOf(cfg)
			switch {
			case versioned != w.versioned:
				t.Errorf("embeds config.Versioned: %v, want %v", versioned, w.versioned)
			case versioned && version != config.Latest:
				t.Errorf("version = %q by default, want %q", version, config.Latest)
			}
			// Later layers can only add to these lists, so a default entry
			// could never be removed.
			for _, path := range unionDefaults(reflect.ValueOf(cfg), w.name) {
				t.Errorf("%s has default entries; lists that combine across layers must start empty", path)
			}
		})
	}
}

func versionOf(cfg config.ModuleConfig) (string, bool) {
	f, ok := reflect.TypeOf(cfg).Elem().FieldByName("Versioned")
	if !ok || !f.Anonymous || f.Type != reflect.TypeFor[config.Versioned]() {
		return "", false
	}
	return reflect.ValueOf(cfg).Elem().FieldByIndex(f.Index).FieldByName("Version").String(), true
}

// unionDefaults returns the paths of lists tagged merge:"union" in v that
// aren't empty.
func unionDefaults(v reflect.Value, path string) []string {
	v = reflect.Indirect(v)
	var found []string
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			fieldPath := path
			if !f.Anonymous {
				fieldPath += "." + strings.Split(f.Tag.Get("toml"), ",")[0]
			}
			if f.Tag.Get("merge") == "union" && v.Field(i).Len() > 0 {
				found = append(found, fieldPath)
			}
			found = append(found, unionDefaults(v.Field(i), fieldPath)...)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			found = append(found, unionDefaults(it.Value(), path+"."+it.Key().String())...)
		}
	default:
	}
	return found
}

// TestEmptyMachine checks that the loader accepts every module's config
// type, and that a machine file that sets nothing gets every module's
// defaults.
func TestEmptyMachine(t *testing.T) {
	for _, osName := range config.AllOS() {
		t.Run(string(osName), func(t *testing.T) {
			repo := &config.Repo{
				FS:      fstest.MapFS{"machines/box.toml": {Data: []byte("os = \"" + osName + "\"\n")}},
				Modules: modules.All(),
			}
			m, err := repo.LoadMachine("box")
			if err != nil {
				t.Fatal(err)
			}
			for _, mod := range modules.All() {
				if got, want := m.Modules[mod.Name], mod.NewConfig(); !reflect.DeepEqual(got, want) {
					t.Errorf("%s = %+v, want the defaults %+v", mod.Name, got, want)
				}
			}
		})
	}
}

// baseShell is the shell config profiles/base.toml sets, with
// zshSnippets added to the zsh snippets.
func baseShell(zshSnippets ...string) *shell.Config {
	return &shell.Config{
		Enabled: true,
		Login:   "zsh",
		Bash: shell.Bash{
			Settings: shell.Settings{Enabled: true, Snippets: []string{"shell/bash/aliases.sh"}},
		},
		Zsh: shell.Zsh{
			Settings: shell.Settings{
				Enabled:     true,
				Variables:   map[string]string{"HIST_STAMPS": "yyyy-mm-dd", "DISABLE_UNTRACKED_FILES_DIRTY": "true"},
				PreSnippets: []string{"shell/zsh/zstyles.zsh"},
				Snippets:    append([]string{"shell/zsh/aliases.zsh"}, zshSnippets...),
			},
			ManageRC: true,
			OhMyZsh: shell.OhMyZsh{
				Enabled: true,
				Ref:     "master",
				Theme:   "aphrodite",
				Plugins: []string{"git", "zsh-autosuggestions", "zsh-syntax-highlighting"},
				CustomThemes: map[string]shell.CustomTheme{
					"aphrodite": {Repo: "https://github.com/win0err/aphrodite-terminal-theme", Ref: "master", File: "aphrodite.zsh-theme"},
				},
				CustomPlugins: map[string]shell.CustomPlugin{
					"zsh-autosuggestions":     {Repo: "https://github.com/zsh-users/zsh-autosuggestions", Ref: "v0.7.1"},
					"zsh-syntax-highlighting": {Repo: "https://github.com/zsh-users/zsh-syntax-highlighting", Ref: "master"},
				},
			},
		},
		Fish: shell.Fish{
			Settings: shell.Settings{Enabled: true, Snippets: []string{"shell/fish/abbr.fish"}},
		},
	}
}

// baseFiles is the files config profiles/base.toml sets.
func baseFiles(nvim bool) *files.Config {
	return &files.Config{
		Enabled: true,
		Vars:    map[string]string{"email": "phil@example.com"},
		Items: map[string]files.Item{
			"gitconfig":  {Enabled: true, Source: "files/gitconfig.tmpl", Target: "~/.gitconfig", Template: true},
			"ssh_config": {Enabled: true, Source: "files/ssh_config", Target: "~/.ssh/config", Mode: "0600"},
			"nvim":       {Enabled: nvim, Source: "files/nvim/", Target: "~/.config/nvim/"},
		},
	}
}

// TestExample loads the example config repo from issue #1, in
// testdata/example, and checks every module's merged config. Modules a
// test doesn't list must have their defaults.
func TestExample(t *testing.T) {
	devTools := []string{
		"github.com/go-task/task/v3/cmd/task@latest",
		"golang.org/x/tools/cmd/goimports@latest",
		"mvdan.cc/gofumpt@latest",
	}
	tests := []struct {
		machine   string
		os        config.OS
		osVersion string
		arch      config.Arch
		profileOS map[string]config.OS
		modules   map[string]config.ModuleConfig
		origins   map[string][]string
	}{
		{
			machine:   "phil-workp14s",
			os:        config.Ubuntu,
			osVersion: "26.04",
			arch:      config.AMD64,
			profileOS: map[string]config.OS{"ubuntu": config.Ubuntu},
			modules: map[string]config.ModuleConfig{
				"sudo": &sudo.Config{Enabled: true, NoPasswd: true},
				"packages": &packages.Config{Enabled: true, Install: []string{
					"gnupg", "htop", "jq", "netcat-openbsd", "socat", "tmux", "unzip", "zip", "podman-compose",
				}},
				"files":         baseFiles(true),
				"shell":         baseShell(),
				"golang":        &golang.Config{Enabled: true, Version: config.Latest, Tools: devTools},
				"golangci_lint": &golangcilint.Config{Enabled: true, Version: "2.15.0"},
				"kubectl":       &kubectl.Config{Enabled: true, Version: config.Latest},
				"hashicorp":     &hashicorp.Config{Enabled: true, Tools: []string{"packer", "vagrant"}},
				"python_tools":  &pythontools.Config{Enabled: true, Tools: []string{"azure-cli", "codespell", "ftfy"}},
				"steam":         &steam.Config{Enabled: true},
				"intellij":      &intellij.Config{Enabled: true, Version: "2026.2.3"},
			},
			origins: map[string][]string{
				"packages.install": {"profiles/ubuntu.toml", "machines/phil-workp14s.toml"},
				"intellij.version": {"machines/phil-workp14s.toml"},
			},
		},
		{
			machine:   "workbox-camwin-fc44",
			os:        config.Fedora,
			osVersion: "44",
			profileOS: map[string]config.OS{"fedora": config.Fedora},
			modules: map[string]config.ModuleConfig{
				"sudo": &sudo.Config{Enabled: true, NoPasswd: true},
				"packages": &packages.Config{Enabled: true, Install: []string{
					"gnupg2", "htop", "jq", "netcat", "socat", "tmux", "unzip", "zip",
				}},
				"files":         baseFiles(false),
				"shell":         baseShell("shell/zsh/goproxy.zsh"),
				"golang":        &golang.Config{Enabled: true, Version: "1.26.4", Tools: devTools},
				"golangci_lint": &golangcilint.Config{Enabled: true, Version: "2.15.0"},
				"kubectl":       &kubectl.Config{Enabled: true, Version: config.Latest},
				"hashicorp":     &hashicorp.Config{Enabled: true, Tools: []string{"packer", "vagrant"}},
				"python_tools":  &pythontools.Config{Enabled: true, Tools: []string{"azure-cli", "codespell", "ftfy"}},
				"wsl":           &wsl.Config{Enabled: true, Systemd: true, WSLG: true, GitCredentialManager: true},
			},
			origins: map[string][]string{
				"golang.version":           {"machines/workbox-camwin-fc44.toml"},
				"shell.zsh.snippets":       {"profiles/base.toml", "machines/workbox-camwin-fc44.toml"},
				"files.items.nvim.source":  {"profiles/base.toml"},
				"files.items.nvim.enabled": {"machines/workbox-camwin-fc44.toml"},
				"wsl.append_windows_path":  {"profiles/wsl.toml"},
			},
		},
		{
			machine:   "workbox-camwin",
			os:        config.Windows,
			profileOS: map[string]config.OS{"windows": config.Windows},
			modules: map[string]config.ModuleConfig{
				"winget":           &winget.Config{Enabled: true, Install: []string{"Microsoft.WindowsTerminal"}},
				"windows_terminal": &windowsterminal.Config{Enabled: true},
				"wsl_host": &wslhost.Config{
					Enabled:       true,
					DefaultDistro: "fedora-dev",
					ConfigURL:     "https://github.com/plombardi89/maisys-config.git",
					WSLConfig:     wslhost.WSLConfig{NetworkingMode: "mirrored", Memory: "16GB"},
					Distros: map[string]wslhost.Distro{
						"fedora-dev": {Image: "FedoraLinux-44", Machine: "workbox-camwin-fc44", User: "phil"},
					},
				},
			},
			origins: map[string][]string{
				// .wslconfig keys the config sets have an origin, and those
				// it leaves out don't, so they aren't written.
				"wsl_host.wslconfig.memory":        {"machines/workbox-camwin.toml"},
				"wsl_host.wslconfig.dns_tunneling": nil,
			},
		},
	}

	repo := &config.Repo{FS: os.DirFS("testdata/example"), Modules: modules.All()}
	for _, tt := range tests {
		t.Run(tt.machine, func(t *testing.T) {
			m, err := repo.LoadMachine(tt.machine)
			if err != nil {
				t.Fatal(err)
			}
			if m.OS != tt.os || m.OSVersion != tt.osVersion || m.Arch != tt.arch {
				t.Errorf("os, os_version, arch = %q, %q, %q, want %q, %q, %q",
					m.OS, m.OSVersion, m.Arch, tt.os, tt.osVersion, tt.arch)
			}
			if !reflect.DeepEqual(m.ProfileOS, tt.profileOS) {
				t.Errorf("ProfileOS = %v, want %v", m.ProfileOS, tt.profileOS)
			}
			for _, mod := range modules.All() {
				want, ok := tt.modules[mod.Name]
				if !ok {
					want = mod.NewConfig()
				}
				if got := m.Modules[mod.Name]; !reflect.DeepEqual(got, want) {
					t.Errorf("%s:\n got %+v\nwant %+v", mod.Name, got, want)
				}
			}
			for path, want := range tt.origins {
				if got := m.Origins[path]; !slices.Equal(got, want) {
					t.Errorf("Origins[%q] = %v, want %v", path, got, want)
				}
			}
		})
	}
}
