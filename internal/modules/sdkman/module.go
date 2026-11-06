// Package sdkman is the sdkman module.
package sdkman

import "github.com/plombardi89/maisys2/internal/config"

// Module is the sdkman module.
type Module struct{}

// Info describes the sdkman module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "sdkman",
		Summary: "Installs SDKMAN, and repairs installs that didn't finish.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the sdkman module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the sdkman module's config.
type Config struct {
	config.Toggle
}
