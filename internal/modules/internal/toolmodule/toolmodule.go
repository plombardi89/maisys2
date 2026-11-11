// Package toolmodule holds what the modules that install tools share:
// reading their config, the resource for a tool from its upstream's
// releases, and the folder of commands they add to PATH.
package toolmodule

import (
	"fmt"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// BinPath is the folder tools' commands are linked in (tool.BinDir), as
// the shell environment writes it. Every module with commands there adds
// it to PATH, so PATH has it whichever of them are enabled. The shell
// module is to write a folder only once, however many modules add it.
const BinPath = "~/.local/bin"

// ConfigOf returns cfg, a module's merged config, as the module's own
// config type, *T.
func ConfigOf[T any](cfg config.ModuleConfig) (*T, error) {
	c, ok := any(cfg).(*T)
	if !ok || c == nil {
		return nil, fmt.Errorf("a module got a %T config instead of its own, %T", cfg, (*T)(nil))
	}
	return c, nil
}

// Tool returns the resource that installs spec at version v, "latest" or
// an exact version, with the tool environment rt's sync shares.
func Tool(spec tool.Spec, v string, rt *resource.Runtime) (*tool.Tool, error) {
	env, err := tool.EnvFor(rt)
	if err != nil {
		return nil, err
	}
	return tool.New(spec, v, env), nil
}

// Resources returns the resources of a module that installs one tool,
// spec, at version v: just the tool.
func Resources(spec tool.Spec, v string, rt *resource.Runtime) ([]resource.Resource, error) {
	t, err := Tool(spec, v, rt)
	if err != nil {
		return nil, err
	}
	return []resource.Resource{t}, nil
}

// ShellEnv returns the environment of a module whose commands are linked
// in ~/.local/bin: that folder, on PATH.
func ShellEnv() shells.Env {
	return shells.Env{Path: []string{BinPath}}
}
