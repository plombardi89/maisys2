// Package yq is the yq module.
package yq

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the yq module.
var Module = config.Module{
	Name:    "yq",
	Summary: "Installs yq, a YAML, JSON and XML processor.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the yq module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
