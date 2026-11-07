// Package golangcilint is the golangci_lint module.
package golangcilint

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the golangci_lint module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the golangci_lint module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "golangci_lint",
		Summary: "Installs golangci-lint, a Go linter runner.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the golangci_lint module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the golangci_lint module's config.
type Config struct {
	config.Toggle
	config.Versioned
}

// DependsOn returns nothing: the golangci_lint module needs no other module.
func (Module) DependsOn() []string { return nil }

// Resources returns golangci-lint, at the version the config asks for.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	return toolmodule.Resources(tool.GolangciLint(), c.Version, rt)
}

// ShellEnv adds ~/.local/bin, where the golangci-lint command is, to PATH.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return toolmodule.ShellEnv(), nil
}
