// Package sudo is the sudo module.
package sudo

import "github.com/plombardi89/maisys2/internal/config"

// Module is the sudo module.
type Module struct{}

// Info describes the sudo module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "sudo",
		Summary: "Lets your user run sudo without a password.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the sudo module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the sudo module's config.
type Config struct {
	config.Toggle
	// NoPasswd lets your user run sudo without a password, through a rule
	// in /etc/sudoers.d that is checked with visudo before it's installed.
	NoPasswd bool `toml:"nopasswd"`
}
