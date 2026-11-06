// Package pythontools is the python_tools module.
package pythontools

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the python_tools module.
var Module = config.Module{
	Name:      "python_tools",
	Summary:   "Installs uv, and Python command-line tools with uv tool install.",
	OS:        config.LinuxOS(),
	NewConfig: func() config.ModuleConfig { return &Config{} },
}

// Config is the python_tools module's config.
type Config struct {
	config.Toggle
	// Tools lists the Python packages to install with uv tool install,
	// such as "codespell". Each one gets an environment of its own.
	Tools []string `toml:"tools" merge:"union"`
}
