// Package pythontools is the python_tools module.
package pythontools

import (
	"path/filepath"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/resource/uvtool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the python_tools module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

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
	// such as "codespell". Each one gets an environment of its own, and is
	// kept at its latest version.
	Tools []string `toml:"tools" merge:"union"`
}

var _ config.Checker = (*Config)(nil)

// Check checks that tools has no empty entries.
func (c *Config) Check(check *config.Check) {
	check.NoEmptyItems("tools", c.Tools)
}

// DependsOn returns nothing: the python_tools module needs no other
// module. It installs uv itself, before the tools.
func (Module) DependsOn() []string { return nil }

// Resources returns uv, at its latest version, then each of tools,
// installed with that uv (~/.local/bin/uv), so that if uv fails, they're
// skipped. Names uv takes for the same package, such as "Azure_CLI" and
// "azure-cli", are installed once.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	uv, err := toolmodule.Tool(tool.UV(), config.Latest, rt)
	if err != nil {
		return nil, err
	}
	installer := &uvtool.UV{Path: filepath.Join(uv.Env.BinDir, "uv")}
	rs := []resource.Resource{uv}
	seen := map[string]bool{}
	for _, name := range c.Tools {
		t := installer.Tool(name)
		if !seen[t.ID()] {
			seen[t.ID()] = true
			rs = append(rs, t)
		}
	}
	return rs, nil
}

// ShellEnv adds ~/.local/bin to PATH: uv's commands are there, and uv puts
// the tools' commands there too, unless UV_TOOL_BIN_DIR, XDG_BIN_HOME or
// XDG_DATA_HOME says otherwise.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return toolmodule.ShellEnv(), nil
}
