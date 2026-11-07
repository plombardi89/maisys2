// Package protoc is the protoc module.
package protoc

import (
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the protoc module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the protoc module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "protoc",
		Summary: "Installs protoc, the Protocol Buffers compiler.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the protoc module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the protoc module's config.
type Config struct {
	config.Toggle
	config.Versioned
}

// DependsOn returns nothing: the protoc module needs no other module.
func (Module) DependsOn() []string { return nil }

// Resources returns protoc, at the version the config asks for. Its
// include/ folder, with the well-known types such as
// google/protobuf/timestamp.proto, stays in the version's folder next to
// bin/: protoc finds it there, even when it's run through the link in
// ~/.local/bin, so nothing else needs to point at it.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	return toolmodule.Resources(tool.Protoc(), c.Version, rt)
}

// ShellEnv adds ~/.local/bin, where the protoc command is, to PATH.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return toolmodule.ShellEnv(), nil
}
