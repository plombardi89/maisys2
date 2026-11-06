// Package hashicorp is the hashicorp module.
package hashicorp

import "github.com/plombardi89/maisys2/internal/config"

// Module is the hashicorp module.
type Module struct{}

// Info describes the hashicorp module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "hashicorp",
		Summary: "Adds HashiCorp's package repository and installs tools from it.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the hashicorp module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the hashicorp module's config.
type Config struct {
	config.Toggle
	// Tools lists the packages to install from HashiCorp's repository,
	// such as "packer" or "vagrant".
	Tools []string `toml:"tools" merge:"union"`
}
