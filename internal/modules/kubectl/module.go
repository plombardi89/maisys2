// Package kubectl is the kubectl module.
package kubectl

import "github.com/plombardi89/maisys2/internal/config"

// Module is the kubectl module.
type Module struct{}

// Info describes the kubectl module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "kubectl",
		Summary: "Installs kubectl, the Kubernetes command-line tool.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the kubectl module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the kubectl module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
