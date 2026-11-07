package pythontools_test

import (
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules/pythontools"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/resource/uvtool"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

func TestResources(t *testing.T) {
	home := t.TempDir()
	rt := &resource.Runtime{
		Runner: systemtest.NewFakeRunner(t),
		Paths:  settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
	}
	cfg := &pythontools.Config{Enabled: true, Tools: []string{"azure-cli", "codespell", "ftfy"}}
	rs, err := pythontools.Module{}.Resources(cfg, rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	if want := []string{"tool:uv", "uv-tool:azure-cli", "uv-tool:codespell", "uv-tool:ftfy"}; !slices.Equal(ids, want) {
		t.Fatalf("Resources() = %q, want %q", ids, want)
	}

	uv, ok := rs[0].(*tool.Tool)
	if !ok || uv.Spec.Name != "uv" || uv.Version != config.Latest {
		t.Errorf("Resources()[0] = %#v, want uv at its latest version", rs[0])
	}
	var shared *uvtool.UV
	for _, r := range rs[1:] {
		tl, ok := r.(*uvtool.Tool)
		if !ok {
			t.Fatalf("%s is a %T, want a *uvtool.Tool", r.ID(), r)
		}
		if want := filepath.Join(home, ".local", "bin", "uv"); tl.UV.Path != want {
			t.Errorf("%s is installed with %q, want %q", tl.ID(), tl.UV.Path, want)
		}
		if shared != nil && tl.UV != shared {
			t.Errorf("%s has a uv of its own, want one for every tool", tl.ID())
		}
		shared = tl.UV
	}

	if deps := (pythontools.Module{}).DependsOn(); len(deps) > 0 {
		t.Errorf("DependsOn() = %q, want nothing", deps)
	}
}

func TestResourcesNoTools(t *testing.T) {
	home := t.TempDir()
	rt := &resource.Runtime{Paths: settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache")}}
	rs, err := pythontools.Module{}.Resources(&pythontools.Config{}, rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].ID() != "tool:uv" {
		t.Errorf("Resources() = %v, want only uv", rs)
	}
}

func TestResourcesSamePackage(t *testing.T) {
	home := t.TempDir()
	rt := &resource.Runtime{Paths: settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache")}}
	rs, err := pythontools.Module{}.Resources(&pythontools.Config{Tools: []string{"azure-cli", "codespell", "Azure_CLI"}}, rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	if want := []string{"tool:uv", "uv-tool:azure-cli", "uv-tool:codespell"}; !slices.Equal(ids, want) {
		t.Errorf("Resources() = %q, want %q", ids, want)
	}
}

func TestShellEnv(t *testing.T) {
	got, err := pythontools.Module{}.ShellEnv(&pythontools.Config{}, &resource.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if want := (shells.Env{Path: []string{"~/.local/bin"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("ShellEnv() = %+v, want %+v", got, want)
	}
}
