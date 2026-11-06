// Package winget is the winget module.
package winget

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the winget module.
var Module = config.Module{
	Name:      "winget",
	Summary:   "Installs Windows packages with winget and keeps them at their latest version.",
	OS:        []config.OS{config.Windows},
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the winget module's config.
type Config struct {
	config.Toggle
	// Install lists winget package IDs, such as
	// "Microsoft.WindowsTerminal". Packages taken off the list aren't
	// uninstalled. Git.Git is always installed, because maisys needs it.
	Install []string `toml:"install" merge:"union"`
}
