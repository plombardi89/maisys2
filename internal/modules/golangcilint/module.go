// Package golangcilint is the golangci_lint module.
package golangcilint

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the golangci_lint module.
var Module = config.Module{
	Name:    "golangci_lint",
	Summary: "Installs golangci-lint, a Go linter runner.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the golangci_lint module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
