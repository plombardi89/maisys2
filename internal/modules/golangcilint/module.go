// Package golangcilint is the golangci_lint module.
package golangcilint

import "github.com/plombardi89/maisys2/internal/config"

// Module is the golangci_lint module.
type Module struct{}

// Info describes the golangci_lint module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "golangci_lint",
		Summary: "Installs golangci-lint, a Go linter runner.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the golangci_lint module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the golangci_lint module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
