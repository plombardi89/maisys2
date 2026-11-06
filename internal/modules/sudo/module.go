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

// Config is the sudo module's config. It has no enabled key: nopasswd is
// its only setting, and it turns the module on. maisys never uninstalls
// sudo.
type Config struct {
	// NoPasswd lets your user run sudo without a password, through a rule
	// in /etc/sudoers.d that is checked with visudo before it's installed.
	// Turning it off deletes that rule again, unless your user isn't in the
	// wheel or sudo group, which would leave them without sudo.
	NoPasswd bool `toml:"nopasswd"`
}

// IsEnabled reports whether the module has anything to do: whether
// nopasswd is on.
func (c *Config) IsEnabled() bool {
	return c.NoPasswd
}
