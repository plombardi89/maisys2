// Package mage is the mage module.
package mage

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the mage module.
var Module = config.Module{
	Name:    "mage",
	Summary: "Installs Mage, a make-like build tool for Go.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the mage module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
