// Package lnav is the lnav module.
package lnav

import "github.com/plombardi89/maisys2/internal/config"

// Module is the lnav module.
type Module struct{}

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
