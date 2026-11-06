// Package packages is the packages module.
package packages

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the packages module.
var Module = config.Module{
	Name:      "packages",
	Summary:   "Installs and upgrades system packages with apt or dnf.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the packages module's config.
type Config struct {
	config.Toggle
	// Install lists the packages to install and keep up to date, by the
	// names the machine's OS uses for them.
	Install []string `toml:"install" merge:"union"`
}
