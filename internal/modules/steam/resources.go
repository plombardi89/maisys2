package steam

import (
	"errors"
	"fmt"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/repo"
)

var _ engine.Module = Module{}

// DependsOn returns nothing: the module adds the repos Steam comes from
// itself, before its packages.
func (Module) DependsOn() []string { return nil }

// Resources returns, on Ubuntu, dpkg's i386 architecture, Valve's
// steam-launcher .deb (which adds Valve's apt repo), and Steam's packages
// from that repo; on Fedora, RPM Fusion's free and nonfree release
// packages (which add RPM Fusion's repos), and steam from them.
//
// Valve's .deb and RPM Fusion's release packages change with each release,
// so their sha256 can't be pinned: each one is looked up when it's
// installed, with its sha256, in the vendor's own index of the repo, over
// HTTPS: Valve's apt Packages file, and the repodata of RPM Fusion's
// release tree for the Fedora version. The Ansible setup installed them
// without checking them at all.
func (Module) Resources(_ config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	if rt.Facts == nil {
		return nil, errors.New("the steam module needs to know the system")
	}
	if rt.Facts.Arch != config.AMD64 {
		return nil, fmt.Errorf("steam only works on amd64, because Valve has no Steam client for %s", rt.Facts.RawArch)
	}
	switch rt.Facts.OS {
	case config.Ubuntu:
		i386 := &repo.Architecture{Name: "i386"}
		launcher := &repo.ReleasePackage{Name: "steam", Package: "steam-launcher", Find: findValve}
		pkgs := pkg.Set(rt, "steam", []string{"steam-launcher", "steam-libs-amd64", "steam-libs-i386:i386"}, i386, launcher)
		return append([]resource.Resource{i386, launcher}, pkgs...), nil
	case config.Fedora:
		free := &repo.ReleasePackage{Name: "rpmfusion-free", Package: "rpmfusion-free-release", Find: findRPMFusion("free")}
		nonfree := &repo.ReleasePackage{Name: "rpmfusion-nonfree", Package: "rpmfusion-nonfree-release", Find: findRPMFusion("nonfree")}
		return append([]resource.Resource{free, nonfree}, pkg.Set(rt, "steam", []string{"steam"}, free, nonfree)...), nil
	default:
		return nil, errors.New("the steam module only works on Fedora and Ubuntu")
	}
}
