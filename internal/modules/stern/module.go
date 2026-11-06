// Package stern is the stern module.
package stern

import "github.com/plombardi89/maisys2/internal/config"

// Module is the stern module.
type Module struct{}

// Info describes the stern module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "stern",
		Summary: "Installs stern, which tails logs from Kubernetes pods.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the stern module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the stern module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
