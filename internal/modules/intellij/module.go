// Package intellij is the intellij module.
package intellij

import "github.com/plombardi89/maisys2/internal/config"

// Module is the intellij module.
type Module struct{}

// Info describes the intellij module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "intellij",
		Summary: "Installs IntelliJ IDEA Ultimate, with an idea command to start it.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the intellij module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the intellij module's config.
type Config struct {
	config.Toggle
	config.Versioned
	// InstallDeps also installs the system packages IntelliJ needs to
	// run, which minimal installs and WSL distros may not have.
	InstallDeps bool `toml:"install_deps"`
}
