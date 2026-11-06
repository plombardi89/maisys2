// Package mutagen is the mutagen module.
package mutagen

import "github.com/plombardi89/maisys2/internal/config"

// Module is the mutagen module.
type Module struct{}

// Info describes the mutagen module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "mutagen",
		Summary: "Installs Mutagen, which syncs files and forwards network ports.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the mutagen module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the mutagen module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
