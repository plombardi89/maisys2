// Package winget is the winget module.
package winget

import "github.com/plombardi89/maisys2/internal/config"

// Module is the winget module.
type Module struct{}

// Info describes the winget module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "winget",
		Summary: "Installs Windows packages with winget and keeps them at their latest version.",
		OS:      []config.OS{config.Windows},
	}
}

// NewConfig returns the winget module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the winget module's config.
type Config struct {
	config.Toggle
	// Install lists winget package IDs, such as
	// "Microsoft.WindowsTerminal". Packages taken off the list aren't
	// uninstalled. Git.Git is always installed, because maisys needs it.
	Install []string `toml:"install" merge:"union"`
}

var _ config.Checker = (*Config)(nil)

// Check checks that install has no empty entries.
func (c *Config) Check(check *config.Check) {
	check.NoEmptyItems("install", c.Install)
}
