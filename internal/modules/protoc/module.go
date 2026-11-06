// Package protoc is the protoc module.
package protoc

import "github.com/plombardi89/maisys2/internal/config"

// Module is the protoc module.
type Module struct{}

// Info describes the protoc module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "protoc",
		Summary: "Installs protoc, the Protocol Buffers compiler.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the protoc module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the protoc module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
