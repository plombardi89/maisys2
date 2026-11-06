// Package windowsterminal is the windows_terminal module.
package windowsterminal

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the windows_terminal module.
var Module = config.Module{
	Name:      "windows_terminal",
	Summary:   "Makes wsl_host's default distro the default profile in Windows Terminal, keeping the comments and formatting in its settings.",
	OS:        []config.OS{config.Windows},
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the windows_terminal module's config.
type Config struct {
	config.Toggle
}
