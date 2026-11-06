// Package legacy is the legacy module, which cleans up after the Ansible
// setup maisys replaces. It's hidden from the docs and will be removed once
// every machine has moved over.
package legacy

import "github.com/plombardi89/maisys2/internal/config"

// Module is the legacy module.
type Module struct{}

// Info describes the legacy module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "legacy",
		Summary: "Removes files left behind by the Ansible setup maisys replaces.",
		OS:      config.LinuxOS(),
		Hidden:  true,
	}
}

// NewConfig returns the legacy module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the legacy module's config.
type Config struct {
	config.Toggle
}
