// Package pythontools is the python_tools module.
package pythontools

import "github.com/plombardi89/maisys2/internal/config"

// Module is the python_tools module.
type Module struct{}

// Info describes the python_tools module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "python_tools",
		Summary: "Installs uv, and Python command-line tools with uv tool install.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the python_tools module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the python_tools module's config.
type Config struct {
	config.Toggle
	// Tools lists the Python packages to install with uv tool install,
	// such as "codespell". Each one gets an environment of its own.
	Tools []string `toml:"tools" merge:"union"`
}

var _ config.Checker = (*Config)(nil)

// Check checks that tools has no empty entries.
func (c *Config) Check(check *config.Check) {
	check.NoEmptyItems("tools", c.Tools)
}
