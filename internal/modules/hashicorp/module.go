// Package hashicorp is the hashicorp module.
package hashicorp

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the hashicorp module.
var Module = config.Module{
	Name:      "hashicorp",
	Summary:   "Adds HashiCorp's package repository and installs tools from it.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the hashicorp module's config.
type Config struct {
	config.Toggle
	// Tools lists the packages to install from HashiCorp's repository,
	// such as "packer" or "vagrant".
	Tools []string `toml:"tools" merge:"union"`
}
