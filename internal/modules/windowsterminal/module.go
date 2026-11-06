// Package windowsterminal is the windows_terminal module.
package windowsterminal

import "github.com/plombardi89/maisys2/internal/config"

// Module is the windows_terminal module.
type Module struct{}

// Info describes the windows_terminal module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "windows_terminal",
		Summary: "Makes wsl_host's default distro the default profile in Windows Terminal, keeping the comments and formatting in its settings.",
		OS:      []config.OS{config.Windows},
	}
}

// NewConfig returns the windows_terminal module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the windows_terminal module's config.
type Config struct {
	config.Toggle
}
