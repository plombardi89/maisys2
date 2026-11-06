// Package mutagen is the mutagen module.
package mutagen

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the mutagen module.
var Module = config.Module{
	Name:    "mutagen",
	Summary: "Installs Mutagen, which syncs files and forwards network ports.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the mutagen module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
