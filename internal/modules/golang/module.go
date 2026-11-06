// Package golang is the golang module.
package golang

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the golang module.
var Module = config.Module{
	Name:    "golang",
	Summary: "Installs the Go toolchain, and Go programs with go install.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the golang module's config.
type Config struct {
	config.Toggle
	config.Versioned
	// Tools lists Go programs to install with go install, as a package
	// path and a version, such as "mvdan.cc/gofumpt@latest". A program is
	// only reinstalled when a newer version exists.
	Tools []string `toml:"tools" merge:"union"`
}
