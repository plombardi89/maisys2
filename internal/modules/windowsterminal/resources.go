package windowsterminal

import (
	"fmt"
	"os"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/wslhost"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/terminal"
)

var _ engine.Module = Module{}

// DependsOn returns nothing. windows_terminal is applied after wsl_host,
// which creates the default distro, because modules.All() lists it after
// it; but it doesn't depend on all of wsl_host working, only on the
// default distro existing, which it checks itself, so that another distro
// that can't be set up doesn't stop it.
func (Module) DependsOn() []string { return nil }

// Resources returns the default profile of each Windows Terminal settings
// file there is (see terminal.Find): the profile of wsl_host's
// default_distro.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	if _, ok := cfg.(*Config); !ok {
		return nil, fmt.Errorf("the windows_terminal module can't use a %T config", cfg)
	}
	var distro string
	if rt.Machine != nil {
		if host, ok := rt.Machine.Modules["wsl_host"].(*wslhost.Config); ok {
			distro = host.DefaultDistro
		}
	}
	files := terminal.Find(os.Getenv("LOCALAPPDATA"), os.Getenv)
	if len(files) == 0 {
		return []resource.Resource{&terminal.DefaultProfile{Distro: distro}}, nil
	}
	rs := make([]resource.Resource, 0, len(files))
	for _, f := range files {
		rs = append(rs, &terminal.DefaultProfile{Settings: f, Distro: distro})
	}
	return rs, nil
}
