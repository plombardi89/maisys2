// Package sudo is the sudo module.
package sudo

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the sudo module.
var Module = config.Module{
	Name:      "sudo",
	Summary:   "Lets your user run sudo without a password.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the sudo module's config.
type Config struct {
	config.Toggle
	// NoPasswd lets your user run sudo without a password, through a rule
	// in /etc/sudoers.d that is checked with visudo before it's installed.
	NoPasswd bool `toml:"nopasswd"`
}
