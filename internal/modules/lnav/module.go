// Package lnav is the lnav module.
package lnav

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the lnav module.
var Module = config.Module{
	Name:    "lnav",
	Summary: "Installs lnav, a log file viewer.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the lnav module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
