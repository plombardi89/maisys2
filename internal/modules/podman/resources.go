package podman

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
)

var _ engine.Module = Module{}

// DependsOn returns nothing.
func (Module) DependsOn() []string { return nil }

// Resources returns the podman package, and on Fedora iptables-nft, which
// podman's networking needs there.
func (Module) Resources(_ config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	names := []string{"podman"}
	if rt.Facts != nil && rt.Facts.OS == config.Fedora {
		names = append(names, "iptables-nft")
	}
	return pkg.Set(rt, "podman", names), nil
}
