// Package steam is the steam module.
package steam

import "github.com/plombardi89/maisys2/internal/config"

// Module is the steam module.
type Module struct{}

// Info describes the steam module. Valve has no arm64 client, so it
// only supports amd64.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "steam",
		Summary: "Installs Steam: Valve's package on Ubuntu, and RPM Fusion's on Fedora.",
		OS:      config.LinuxOS(),
		Arch:    []config.Arch{config.AMD64},
	}
}

// NewConfig returns the steam module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the steam module's config.
type Config struct {
	config.Toggle
}
