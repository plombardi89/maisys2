// Package opencode is the opencode module.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/installer"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system"
)

// Module is the opencode module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the opencode module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "opencode",
		Summary: "Makes sure opencode v2 or newer is installed. From then on, opencode updates itself.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the opencode module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the opencode module's config.
type Config struct {
	config.Toggle
}

// InstallerURL is the installer script for opencode v2 and newer. The one
// at https://opencode.ai/install only installs v1. The script changes with
// opencode's releases, so its sha256 can't be pinned: it's trusted for
// coming from opencode.ai over HTTPS.
const InstallerURL = "https://opencode.ai/v2/install"

// MinMajor is the oldest major version of opencode the module accepts.
const MinMajor = 2

// Prerequisites are the packages the installer script needs: it downloads
// opencode with curl, and unpacks it with tar.
var Prerequisites = []string{"curl", "tar"}

// BinDir returns the folder the installer puts opencode in, given the home
// folder.
func BinDir(home string) string {
	return filepath.Join(home, ".opencode", "bin")
}

// DependsOn returns nothing: the opencode module needs no other module. It
// installs the installer's prerequisites itself.
func (Module) DependsOn() []string { return nil }

// Resources returns the installer's prerequisites (see Prerequisites),
// installed with every other module's packages, then opencode, installed
// with its installer script unless ~/.opencode/bin/opencode is version 2 or
// newer. The installer runs with --no-modify-path, so it leaves the shells'
// rc files alone: ShellEnv adds opencode to PATH instead.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	if _, err := toolmodule.ConfigOf[Config](cfg); err != nil {
		return nil, err
	}
	rs := pkg.Set(rt, "opencode", Prerequisites)
	return append(rs, newInstall(rt.Paths.Home)), nil
}

// install installs opencode with its installer script, and calls replacing
// an older version an upgrade in the plan.
type install struct {
	*installer.Installer
	// bin is the opencode the installer installs.
	bin string
	// old is the version the last check found, if it's older than
	// MinMajor.
	old string
}

func newInstall(home string) *install {
	in := &install{bin: filepath.Join(BinDir(home), "opencode")}
	in.Installer = &installer.Installer{
		Name:     "opencode",
		URL:      InstallerURL,
		Args:     []string{"--no-modify-path"},
		Complete: in.complete,
		Paths:    []string{BinDir(home)},
	}
	return in
}

// Check checks the install. If an older opencode is installed, the change
// is an upgrade, such as "upgrade opencode 1.4.2 → 2 (installer)".
func (in *install) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	in.old = ""
	c, err := in.Installer.Check(ctx, rt)
	if err != nil || !c.Action.Applies() || in.old == "" {
		return c, err
	}
	c.Action = resource.Update
	c.Summary = fmt.Sprintf("upgrade opencode %s → %d (installer)", in.old, MinMajor)
	return c, nil
}

// complete checks that opencode is installed, at version MinMajor or newer.
func (in *install) complete(ctx context.Context, rt *resource.Runtime) (installer.Status, error) {
	in.old = ""
	if _, err := os.Stat(in.bin); errors.Is(err, fs.ErrNotExist) {
		return installer.Status{}, nil
	} else if err != nil {
		return installer.Status{}, err
	}
	res, err := rt.Runner.Run(ctx, system.Cmd{Name: in.bin, Args: []string{"--version"}, Label: "opencode --version"})
	if _, ok := errors.AsType[*system.ExitError](err); ok {
		return installer.Status{Reason: "opencode --version failed"}, nil
	}
	if err != nil {
		return installer.Status{}, err
	}
	v, major, ok := ParseVersion(string(res.Stdout))
	switch {
	case !ok:
		return installer.Status{Reason: "opencode --version printed no version"}, nil
	case major < MinMajor:
		in.old = v
		return installer.Status{Reason: fmt.Sprintf("version %s is older than %d", v, MinMajor)}, nil
	}
	return installer.Status{Complete: true}, nil
}

// versionRE finds a version in opencode --version's output.
var versionRE = regexp.MustCompile(`([0-9]+)\.[0-9]+\.[0-9]+`)

// ParseVersion finds the version in what opencode --version printed, and
// its major version: v1 prints just the version, such as "1.4.2", and v2
// prints "opencode v2.0.3".
func ParseVersion(out string) (v string, major int, ok bool) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return "", 0, false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return "", 0, false
	}
	return m[0], major, true
}

// ShellEnv adds ~/.opencode/bin, where the installer puts opencode, to
// PATH.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return shells.Env{Path: []string{"~/.opencode/bin"}}, nil
}
