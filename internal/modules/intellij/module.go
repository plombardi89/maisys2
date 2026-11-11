// Package intellij is the intellij module.
package intellij

import (
	"slices"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the intellij module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

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
	// run, which minimal installs and WSL distros may not have: the X11,
	// font and sound libraries its Java runtime uses, and those of its
	// embedded browser, which shows the Markdown preview.
	InstallDeps bool `toml:"install_deps"`
}

// Deps returns the packages install_deps installs on os, which minimal
// installs and WSL distros may not have:
//
//   - the X11 libraries the JetBrains Runtime draws windows with, and the
//     font and sound libraries it uses;
//   - the libraries of JCEF, the embedded browser behind the Markdown
//     preview and other web views (see JetBrains' "JCEF: the missing libs
//     problem on Linux"). Without them it fails to start, without saying
//     which library is missing.
//
// The lists are the same on Fedora and Ubuntu, package for package.
func Deps(os config.OS) []string {
	switch os {
	case config.Fedora:
		return []string{
			"libXext", "libXrender", "libXtst", "libXi", "fontconfig", "freetype", "alsa-lib",
			"nss", "nspr", "atk", "at-spi2-atk", "cups-libs", "libdrm", "mesa-libgbm", "libxkbcommon",
			"libXcomposite", "libXdamage", "libXfixes", "libXrandr", "libXScrnSaver", "pango", "cairo",
		}
	case config.Ubuntu:
		return []string{
			"libxext6", "libxrender1", "libxtst6", "libxi6", "fontconfig", "libfreetype6", "libasound2t64",
			"libnss3", "libnspr4", "libatk1.0-0t64", "libatk-bridge2.0-0t64", "libcups2t64", "libdrm2", "libgbm1", "libxkbcommon0",
			"libxcomposite1", "libxdamage1", "libxfixes3", "libxrandr2", "libxss1", "libpango-1.0-0", "libcairo2",
		}
	default:
		return nil
	}
}

// DependsOn returns nothing: the intellij module needs no other module.
func (Module) DependsOn() []string { return nil }

// Resources returns, with install_deps, the packages IntelliJ needs on the
// machine's OS (see Deps), installed with every other module's packages;
// then IntelliJ itself, at the version the config asks for.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	idea, err := toolmodule.Tool(tool.IntelliJ(), c.Version, rt)
	if err != nil {
		return nil, err
	}
	var rs []resource.Resource
	if c.InstallDeps {
		rs = pkg.Set(rt, "intellij", Deps(machineOS(rt)))
	}
	return slices.Concat(rs, []resource.Resource{idea}), nil
}

// machineOS returns the OS of rt's sync: the live system's, or else the
// machine's.
func machineOS(rt *resource.Runtime) config.OS {
	if rt.Facts != nil && rt.Facts.OS != "" {
		return rt.Facts.OS
	}
	if rt.Machine != nil {
		return rt.Machine.OS
	}
	return ""
}

// ShellEnv adds ~/.local/bin, where the idea command is, to PATH.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return toolmodule.ShellEnv(), nil
}
