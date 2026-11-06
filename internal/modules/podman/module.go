// Package podman is the podman module.
package podman

import "github.com/plombardi89/maisys2/internal/config"

// Module is the podman module.
type Module struct{}

// Info describes the podman module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "podman",
		Summary: "Installs Podman, plus iptables-nft on Fedora.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the podman module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the podman module's config.
type Config struct {
	config.Toggle
}
