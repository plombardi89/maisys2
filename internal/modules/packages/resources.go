package packages

import (
	"fmt"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
)

var _ engine.Module = Module{}

// DependsOn returns nothing.
func (Module) DependsOn() []string { return nil }

// Resources returns a package for each name in install, which the
// machine's profiles give for its OS. They're installed together with
// every other module's packages.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the packages module can't use a %T config", cfg)
	}
	return pkg.Set(rt, "packages", c.Install), nil
}
