// Package kubectl is the kubectl module.
package kubectl

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the kubectl module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the kubectl module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "kubectl",
		Summary: "Installs kubectl, the Kubernetes command-line tool.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the kubectl module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the kubectl module's config.
type Config struct {
	config.Toggle
	config.Versioned
}

// DependsOn returns nothing: the kubectl module needs no other module.
func (Module) DependsOn() []string { return nil }

// Resources returns kubectl, at the version the config asks for.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	return toolmodule.Resources(tool.Kubectl(), c.Version, rt)
}

// ShellEnv adds ~/.local/bin, where the kubectl command is, to PATH.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return toolmodule.ShellEnv(), nil
}
