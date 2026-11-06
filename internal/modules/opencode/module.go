// Package opencode is the opencode module.
package opencode

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the opencode module.
var Module = config.Module{
	Name:      "opencode",
	Summary:   "Makes sure opencode v2 or newer is installed. From then on, opencode updates itself.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the opencode module's config.
type Config struct {
	config.Toggle
}
