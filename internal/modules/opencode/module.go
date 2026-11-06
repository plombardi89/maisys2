// Package opencode is the opencode module.
package opencode

import "github.com/plombardi89/maisys2/internal/config"

// Module is the opencode module.
type Module struct{}

// Info describes the opencode module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "opencode",
		Summary: "Makes sure opencode v2 or newer is installed. From then on, opencode updates itself.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the opencode module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the opencode module's config.
type Config struct {
	config.Toggle
}
