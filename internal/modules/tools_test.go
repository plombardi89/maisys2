package modules_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/golangcilint"
	"github.com/plombardi89/maisys2/internal/modules/intellij"
	"github.com/plombardi89/maisys2/internal/modules/kubectl"
	"github.com/plombardi89/maisys2/internal/modules/lnav"
	"github.com/plombardi89/maisys2/internal/modules/mage"
	"github.com/plombardi89/maisys2/internal/modules/mutagen"
	"github.com/plombardi89/maisys2/internal/modules/protoc"
	"github.com/plombardi89/maisys2/internal/modules/stern"
	"github.com/plombardi89/maisys2/internal/modules/yq"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// toolRuntime returns a runtime for module tests on a Fedora machine, with
// a temporary home folder and a runner that expects no commands.
func toolRuntime(t *testing.T) *resource.Runtime {
	t.Helper()
	home := t.TempDir()
	return &resource.Runtime{
		Runner:  systemtest.NewFakeRunner(t),
		Facts:   &facts.System{OS: config.Fedora, Arch: config.AMD64},
		Paths:   settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
		Machine: &config.Machine{Name: "box", OS: config.Fedora},
		Now:     time.Now,
	}
}

// otherConfig is a config no module has.
type otherConfig struct {
	config.Toggle
}

// TestExampleResources lists the resources of the tool modules each Linux
// machine of the example config repo enables, checking that each module
// gets its own merged config, and what they install.
func TestExampleResources(t *testing.T) {
	tools := []string{"golang", "mage", "golangci_lint", "kubectl", "stern", "yq", "lnav", "protoc", "mutagen", "sdkman", "opencode", "python_tools", "intellij"}
	tests := []struct {
		machine string
		os      config.OS
		want    []string
	}{
		{
			machine: "phil-workp14s",
			os:      config.Ubuntu,
			want: []string{
				"tool:go", "go-install:github.com/go-task/task/v3/cmd/task", "go-install:golang.org/x/tools/cmd/goimports", "go-install:mvdan.cc/gofumpt",
				"tool:golangci-lint", "tool:kubectl",
				"tool:uv", "uv-tool:azure-cli", "uv-tool:codespell", "uv-tool:ftfy",
				"tool:intellij",
			},
		},
		{
			machine: "workbox-camwin-fc44",
			os:      config.Fedora,
			want: []string{
				"tool:go", "go-install:github.com/go-task/task/v3/cmd/task", "go-install:golang.org/x/tools/cmd/goimports", "go-install:mvdan.cc/gofumpt",
				"tool:golangci-lint", "tool:kubectl",
				"tool:uv", "uv-tool:azure-cli", "uv-tool:codespell", "uv-tool:ftfy",
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
			rt := toolRuntime(t)
			rt.Machine, rt.Facts.OS = m, tt.os
			var ids []string
			for _, mod := range modules.All() {
				name := mod.Info().Name
				em, ok := mod.(engine.Module)
				if !ok || !slices.Contains(tools, name) || !m.Modules[name].IsEnabled() {
					continue
				}
				rs, err := em.Resources(m.Modules[name], rt)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				for _, r := range rs {
					ids = append(ids, r.ID())
				}
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("resources = %q, want %q", ids, tt.want)
			}
		})
	}
}

// TestToolModules checks the modules that install one tool from its
// upstream's releases: each has that tool, at the version the config asks
// for, with its command in ~/.local/bin.
func TestToolModules(t *testing.T) {
	tests := []struct {
		mod      engine.Module
		cfg      config.ModuleConfig
		spec     tool.Spec
		version  string
		commands []string
	}{
		{mage.Module{}, &mage.Config{Version: "1.15.0"}, tool.Mage(), "1.15.0", []string{"mage"}},
		{golangcilint.Module{}, &golangcilint.Config{Version: "2.15.0"}, tool.GolangciLint(), "2.15.0", []string{"golangci-lint"}},
		{kubectl.Module{}, &kubectl.Config{Version: config.Latest}, tool.Kubectl(), config.Latest, []string{"kubectl"}},
		{stern.Module{}, &stern.Config{Version: "1.33.0"}, tool.Stern(), "1.33.0", []string{"stern"}},
		{yq.Module{}, &yq.Config{Version: "v4.47.1"}, tool.Yq(), "v4.47.1", []string{"yq"}},
		{lnav.Module{}, &lnav.Config{Version: config.Latest}, tool.Lnav(), config.Latest, []string{"lnav"}},
		{protoc.Module{}, &protoc.Config{Version: "33.0"}, tool.Protoc(), "33.0", []string{"protoc"}},
		{mutagen.Module{}, &mutagen.Config{Version: "0.18.1"}, tool.Mutagen(), "0.18.1", []string{"mutagen"}},
		{intellij.Module{}, &intellij.Config{Version: "2026.2.3"}, tool.IntelliJ(), "2026.2.3", []string{"idea"}},
	}
	for _, tt := range tests {
		name := tt.mod.Info().Name
		t.Run(name, func(t *testing.T) {
			rt := toolRuntime(t)
			if deps := tt.mod.DependsOn(); len(deps) > 0 {
				t.Errorf("DependsOn() = %q, want nothing", deps)
			}
			rs, err := tt.mod.Resources(tt.cfg, rt)
			if err != nil {
				t.Fatal(err)
			}
			if len(rs) != 1 {
				t.Fatalf("Resources() = %d resources, want 1", len(rs))
			}
			got, ok := rs[0].(*tool.Tool)
			if !ok {
				t.Fatalf("Resources() = %T, want a *tool.Tool", rs[0])
			}
			if got.ID() != "tool:"+tt.spec.Name || got.Spec.Name != tt.spec.Name || got.Version != tt.version {
				t.Errorf("Resources() = %s at %q, want tool:%s at %q", got.ID(), got.Version, tt.spec.Name, tt.version)
			}
			var commands []string
			for _, l := range got.Spec.Links {
				commands = append(commands, l.Name)
			}
			if !slices.Equal(commands, tt.commands) {
				t.Errorf("commands = %q, want %q", commands, tt.commands)
			}
			env, err := tool.EnvFor(rt)
			if err != nil {
				t.Fatal(err)
			}
			if got.Env != env || got.Env.BinDir != filepath.Join(rt.Paths.Home, ".local", "bin") {
				t.Errorf("the tool doesn't use the sync's shared tool environment")
			}

			if _, err := tt.mod.Resources(&otherConfig{}, rt); err == nil {
				t.Error("Resources() with another module's config: no error")
			}

			contributor, ok := tt.mod.(shells.Contributor)
			if !ok {
				t.Fatal("the module doesn't implement shells.Contributor")
			}
			shellEnv, err := contributor.ShellEnv(tt.cfg, rt)
			if err != nil {
				t.Fatal(err)
			}
			if want := (shells.Env{Path: []string{"~/.local/bin"}}); !reflect.DeepEqual(shellEnv, want) {
				t.Errorf("ShellEnv() = %+v, want %+v", shellEnv, want)
			}
		})
	}
}
