// Package mage is the mage module.
package mage

import "github.com/plombardi89/maisys2/internal/config"

// Module is the mage module.
type Module struct{}

// Info describes the mage module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "mage",
		Summary: "Installs Mage, a make-like build tool for Go.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the mage module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the mage module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
