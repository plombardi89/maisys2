package winget

import (
	"fmt"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	wingetpkg "github.com/plombardi89/maisys2/internal/resource/winget"
)

var _ engine.Module = Module{}

// Git is Git for Windows, which the winget module always installs: maisys
// clones config repos with it, and WSL distros use its Git Credential
// Manager. It's installed system-wide, in C:\Program Files\Git, where the
// wsl module looks for the Credential Manager from inside a distro.
func Git() wingetpkg.Spec {
	return wingetpkg.Spec{ID: "Git.Git", Machine: true}
}

// DependsOn returns nothing.
func (Module) DependsOn() []string { return nil }

// Resources returns Git (see Git), then a package for each ID in install.
// Each is installed if it's missing, and upgraded when winget has a newer
// version.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the winget module can't use a %T config", cfg)
	}
	specs := []wingetpkg.Spec{Git()}
	for _, id := range c.Install {
		specs = append(specs, wingetpkg.Spec{ID: id})
	}
	return wingetpkg.Set(rt, "winget", specs...), nil
}
