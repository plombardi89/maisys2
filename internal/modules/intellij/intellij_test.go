package intellij_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/intellij"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

func TestDeps(t *testing.T) {
	fedora, ubuntu := intellij.Deps(config.Fedora), intellij.Deps(config.Ubuntu)
	if len(fedora) == 0 || len(fedora) != len(ubuntu) {
		t.Fatalf("Deps() has %d packages on Fedora and %d on Ubuntu, want the same packages on both", len(fedora), len(ubuntu))
	}
	for _, list := range [][]string{fedora, ubuntu} {
		if len(slices.Compact(slices.Sorted(slices.Values(list)))) != len(list) {
			t.Errorf("Deps() = %q, which lists a package twice", list)
		}
		for _, name := range list {
			if strings.HasSuffix(name, "-dev") || strings.HasSuffix(name, "-devel") {
				t.Errorf("Deps() has %s, a development package", name)
			}
		}
	}
	for _, want := range []string{"libXext", "libXrender", "libXtst", "libXi", "fontconfig", "freetype", "nss"} {
		if !slices.Contains(fedora, want) {
			t.Errorf("Deps(fedora) has no %s", want)
		}
	}
	for _, want := range []string{"libxext6", "libxrender1", "libxtst6", "libxi6", "fontconfig", "libfreetype6", "libnss3"} {
		if !slices.Contains(ubuntu, want) {
			t.Errorf("Deps(ubuntu) has no %s", want)
		}
	}
	if got := intellij.Deps(config.Windows); got != nil {
		t.Errorf("Deps(windows) = %q, want nothing", got)
	}
}

func TestResources(t *testing.T) {
	tests := []struct {
		name string
		os   config.OS
		deps bool
	}{
		{"fedora with deps", config.Fedora, true},
		{"ubuntu with deps", config.Ubuntu, true},
		{"without deps", config.Ubuntu, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			rt := &resource.Runtime{
				Runner: systemtest.NewFakeRunner(t),
				Facts:  &facts.System{OS: tt.os, Arch: config.AMD64},
				Paths:  settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
			}
			cfg := &intellij.Config{Version: "2026.2.3", InstallDeps: tt.deps}
			rs, err := intellij.Module{}.Resources(cfg, rt)
			if err != nil {
				t.Fatal(err)
			}

			var want []string
			if tt.deps {
				for _, name := range intellij.Deps(tt.os) {
					want = append(want, "package:intellij/"+name)
				}
			}
			want = append(want, "tool:intellij")
			var ids []string
			for _, r := range rs {
				ids = append(ids, r.ID())
				if _, ok := r.(*pkg.Package); !ok && r.ID() != "tool:intellij" {
					t.Errorf("%s is a %T, want a package", r.ID(), r)
				}
			}
			if !slices.Equal(ids, want) {
				t.Errorf("Resources() = %q, want %q", ids, want)
			}
			idea, ok := rs[len(rs)-1].(*tool.Tool)
			if !ok || idea.Version != "2026.2.3" {
				t.Errorf("Resources() ends with %#v, want IntelliJ 2026.2.3", rs[len(rs)-1])
			}
		})
	}
}

// TestResourcesMachineOS checks that without the live system's details,
// the packages are the machine's OS's.
func TestResourcesMachineOS(t *testing.T) {
	home := t.TempDir()
	rt := &resource.Runtime{
		Runner:  systemtest.NewFakeRunner(t),
		Machine: &config.Machine{Name: "box", OS: config.Fedora},
		Paths:   settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
	}
	rs, err := intellij.Module{}.Resources(&intellij.Config{Version: config.Latest, InstallDeps: true}, rt)
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].ID() != "package:intellij/libXext" {
		t.Errorf("Resources()[0] = %s, want Fedora's libXext", rs[0].ID())
	}
}
