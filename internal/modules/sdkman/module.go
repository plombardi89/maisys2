// Package sdkman is the sdkman module.
package sdkman

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the sdkman module.
var Module = config.Module{
	Name:      "sdkman",
	Summary:   "Installs SDKMAN, and repairs installs that didn't finish.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the sdkman module's config.
type Config struct {
	config.Toggle
}
