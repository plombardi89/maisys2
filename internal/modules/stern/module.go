// Package stern is the stern module.
package stern

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the stern module.
var Module = config.Module{
	Name:    "stern",
	Summary: "Installs stern, which tails logs from Kubernetes pods.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the stern module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
