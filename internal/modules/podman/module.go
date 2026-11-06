// Package podman is the podman module.
package podman

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the podman module.
var Module = config.Module{
	Name:      "podman",
	Summary:   "Installs Podman, plus iptables-nft on Fedora.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the podman module's config.
type Config struct {
	config.Toggle
}
