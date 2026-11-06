// Package protoc is the protoc module.
package protoc

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the protoc module.
var Module = config.Module{
	Name:    "protoc",
	Summary: "Installs protoc, the Protocol Buffers compiler.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Version: config.Latest}
	},
}

// Config is the protoc module's config.
type Config struct {
	config.Toggle
	config.Versioned
}
