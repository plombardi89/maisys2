// Package lnav is the lnav module.
package lnav

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the lnav module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the lnav module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "lnav",
		Summary: "Installs lnav, a log file viewer.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the lnav module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the lnav module's config.
type Config struct {
	config.Toggle
	config.Versioned
}

// DependsOn returns nothing: the lnav module needs no other module.
func (Module) DependsOn() []string { return nil }

// Resources returns lnav, at the version the config asks for.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	return toolmodule.Resources(tool.Lnav(), c.Version, rt)
}

// ShellEnv adds ~/.local/bin, where the lnav command is, to PATH.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return toolmodule.ShellEnv(), nil
}
