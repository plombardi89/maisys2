// Package sdkman is the sdkman module.
package sdkman

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/installer"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system"
)

// Module is the sdkman module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the sdkman module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "sdkman",
		Summary: "Installs SDKMAN, and repairs installs that didn't finish.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the sdkman module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the sdkman module's config.
type Config struct {
	config.Toggle
}

// InstallerURL is SDKMAN's installer script. rcupdate=false makes it leave
// the shells' rc files alone: the shell module loads SDKMAN instead. The
// script changes with every SDKMAN release, so its sha256 can't be pinned:
// it's trusted for coming from SDKMAN over HTTPS.
const InstallerURL = "https://get.sdkman.io?rcupdate=false"

// Prerequisites are the packages SDKMAN's installer, and SDKMAN itself,
// need.
var Prerequisites = []string{"curl", "tar", "unzip", "zip"}

// Dir returns where SDKMAN is installed, given the home folder.
func Dir(home string) string {
	return filepath.Join(home, ".sdkman")
}

// DependsOn returns nothing: the sdkman module needs no other module. It
// installs SDKMAN's prerequisites itself.
func (Module) DependsOn() []string { return nil }

// Resources returns SDKMAN's prerequisites (see Prerequisites), installed
// with every other module's packages, then SDKMAN, installed in ~/.sdkman
// with its installer script. An install counts as complete once
// var/version_native exists and the sdk command loads (see Complete). If it
// isn't, ~/.sdkman is deleted before the installer runs: SDKMAN's
// installer does nothing if it finds an earlier install, even one that
// didn't finish.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	if _, err := toolmodule.ConfigOf[Config](cfg); err != nil {
		return nil, err
	}
	dir := Dir(rt.Paths.Home)
	rs := pkg.Set(rt, "sdkman", Prerequisites)
	return append(rs, &installer.Installer{
		Name:      "sdkman",
		URL:       InstallerURL,
		Env:       []string{"SDKMAN_DIR=" + dir},
		Complete:  Complete(dir),
		Leftovers: []string{dir},
		Paths:     []string{dir},
	}), nil
}

// loadSDK loads SDKMAN into bash and runs sdk version, which fails if
// SDKMAN's files are broken or missing.
const loadSDK = `source "$SDKMAN_DIR/bin/sdkman-init.sh" && sdk version`

// Complete returns the check of an SDKMAN install in dir: it's complete
// once its last step, writing var/version_native, is done, and sdk loads
// in bash.
func Complete(dir string) func(ctx context.Context, rt *resource.Runtime) (installer.Status, error) {
	return func(ctx context.Context, rt *resource.Runtime) (installer.Status, error) {
		if _, err := os.Stat(filepath.Join(dir, "var", "version_native")); errors.Is(err, fs.ErrNotExist) {
			return installer.Status{Reason: unfinished(dir)}, nil
		} else if err != nil {
			return installer.Status{}, err
		}
		_, err := rt.Runner.Run(ctx, system.Cmd{
			Name:  "bash",
			Args:  []string{"-c", loadSDK},
			Env:   []string{"SDKMAN_DIR=" + dir},
			Label: "sdk version",
		})
		if _, ok := errors.AsType[*system.ExitError](err); ok {
			return installer.Status{Reason: "sdk doesn't load"}, nil
		}
		if err != nil {
			return installer.Status{}, err
		}
		return installer.Status{Complete: true}, nil
	}
}

// unfinished says why an install in dir without var/version_native isn't
// complete: there's none, or it didn't finish.
func unfinished(dir string) string {
	if _, err := os.Lstat(dir); err != nil {
		return ""
	}
	return "var/version_native is missing"
}

// initStep loads SDKMAN in bash and zsh. SDKMAN has no fish integration.
const initStep = `if [[ -s "$SDKMAN_DIR/bin/sdkman-init.sh" ]]; then source "$SDKMAN_DIR/bin/sdkman-init.sh"; fi`

// ShellEnv sets SDKMAN_DIR, and loads SDKMAN in bash and zsh, last, as
// SDKMAN requires. fish has no version of that step, so the plan warns
// that SDKMAN has no fish integration.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return shells.Env{
		Variables: []shells.Variable{{Name: "SDKMAN_DIR", Value: "~/.sdkman"}},
		Init:      []shells.InitStep{{Name: "sdkman", Bash: initStep, Zsh: initStep, Last: true}},
	}, nil
}
