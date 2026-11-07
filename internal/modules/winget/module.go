// Package winget is the winget module.
package winget

import (
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	wingetpkg "github.com/plombardi89/maisys2/internal/resource/winget"
)

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
	// Install lists packages from the winget source by their exact,
	// case-sensitive ID, such as "Microsoft.WindowsTerminal" (winget
	// search finds them). Each is kept at its latest version, and installed
	// as you: an installer that needs admin rights asks for them. Packages
	// taken off the list aren't uninstalled. Git.Git is always installed,
	// system-wide, because maisys needs it.
	Install []string `toml:"install" merge:"union"`
}

var _ config.Checker = (*Config)(nil)

// Check checks that install lists only winget package IDs.
func (c *Config) Check(check *config.Check) {
	check.NoEmptyItems("install", c.Install)
	for _, id := range c.Install {
		if strings.TrimSpace(id) != "" && !wingetpkg.ValidID(id) {
			check.ItemErrorf("install", id, "lists %q, which isn't a winget package ID, such as \"Microsoft.WindowsTerminal\"", id)
		}
	}
}
