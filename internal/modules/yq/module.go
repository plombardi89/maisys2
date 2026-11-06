// Package yq is the yq module.
package yq

import "github.com/plombardi89/maisys2/internal/config"

// Module is the yq module.
type Module struct{}

// Info describes the yq module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "yq",
		Summary: "Installs yq, a YAML, JSON and XML processor.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the yq module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the yq module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
