// Package golang is the golang module.
package golang

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/modules/internal/toolmodule"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/goinstall"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/resource/version"
	"github.com/plombardi89/maisys2/internal/shells"
)

// Module is the golang module.
type Module struct{}

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// Info describes the golang module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "golang",
		Summary: "Installs the Go toolchain, and Go programs with go install.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the golang module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Version: config.Latest}
}

// Config is the golang module's config.
type Config struct {
	config.Toggle
	config.Versioned
	// Tools lists Go programs to install with go install, as a package
	// path followed by @ and a version: "latest", or an exact version such
	// as "v0.8.0". For example "mvdan.cc/gofumpt@latest". Without a
	// version, the latest is installed. A program is only reinstalled when
	// the version wanted changes, such as when a newer one is released. If
	// a program is listed more than once, as when a machine file pins one
	// that a profile lists, the last entry wins.
	Tools []string `toml:"tools" merge:"union"`
}

var _ config.Checker = (*Config)(nil)

// Check checks that an exact version is a full Go version, such as
// "1.26.4", and that every entry of tools is a package path with an
// optional version.
func (c *Config) Check(check *config.Check) {
	// Versions that aren't well-formed are reported for every module.
	if v := c.Version; v != config.Latest && config.IsExactVersion(v) {
		release := strings.TrimPrefix(v, "v")
		if i := strings.IndexAny(release, "-+"); i >= 0 {
			release = release[:i]
		}
		if strings.Count(release, ".") != 2 {
			check.Errorf("version", "must be a full Go version, such as \"1.26.4\", not %q: partial versions aren't supported", v)
		}
	}
	check.NoEmptyItems("tools", c.Tools)
	for _, entry := range c.Tools {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if _, _, err := ParseTool(entry); err != nil {
			check.ItemErrorf("tools", entry, "has an invalid entry, %q: %v", entry, err)
		}
	}
}

// ParseTool splits an entry of tools into the program's package path and
// its version: "latest" if the entry has none.
func ParseTool(entry string) (pkg, v string, err error) {
	pkg, v, ok := strings.Cut(entry, "@")
	if !ok {
		v = goinstall.Latest
	}
	switch {
	case pkg == "":
		return "", "", errors.New("it has no package path")
	case module.CheckImportPath(pkg) != nil:
		return "", "", fmt.Errorf("%q isn't a Go package path, such as \"mvdan.cc/gofumpt\"", pkg)
	case v == goinstall.Latest:
	case !semver.IsValid(v) || semver.Canonical(v) != strings.TrimSuffix(v, "+incompatible"):
		return "", "", fmt.Errorf("the version must be %q or an exact version such as \"v1.2.3\", not %q", goinstall.Latest, v)
	}
	return pkg, v, nil
}

// DependsOn returns nothing: the golang module needs no other module. Its
// programs are built with its own toolchain, which it installs first.
func (Module) DependsOn() []string { return nil }

// Resources returns the Go toolchain, then a go install program for each
// entry of tools, in order, at the version the last entry for it asks for.
// The programs are built with the toolchain, as ~/.local/bin/go, so that if
// it fails, they're skipped. "latest" is looked up in the Go module proxy,
// following GOPROXY and GOPRIVATE; a module that has to be fetched
// directly, such as a private one, is looked up with go list. If the
// toolchain isn't installed yet, that waits until it is.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, err := toolmodule.ConfigOf[Config](cfg)
	if err != nil {
		return nil, err
	}
	toolchain, err := toolmodule.Tool(tool.Go(), c.Version, rt)
	if err != nil {
		return nil, err
	}
	var pkgs []string
	versions := map[string]string{}
	for _, entry := range c.Tools {
		pkg, v, err := ParseTool(entry)
		if err != nil {
			return nil, fmt.Errorf("golang.tools has an invalid entry, %q: %w", entry, err)
		}
		if _, ok := versions[pkg]; !ok {
			pkgs = append(pkgs, pkg)
		}
		versions[pkg] = v
	}
	goBin := filepath.Join(toolchain.Env.BinDir, "go")
	rs := []resource.Resource{toolchain}
	for _, pkg := range pkgs {
		rs = append(rs, newProgram(pkg, versions[pkg], goBin, toolchain.Env, rt))
	}
	return rs, nil
}

// errNoGo means a lookup needs go, which the sync hasn't installed yet.
var errNoGo = errors.New("looking it up needs go, which isn't installed yet")

// newProgram returns the program pkg at version v, built with goBin. If
// its latest version can only be looked up with go, and the toolchain
// isn't installed yet, the plan installs it at whatever version that is,
// and Apply looks it up once the toolchain is installed (see
// goinstall.Program.Check).
func newProgram(pkg, v, goBin string, env *tool.Env, rt *resource.Runtime) *goinstall.Program {
	direct := version.GoList(rt.Runner, goBin)
	resolver := version.GoModule{
		Path: pkg,
		Direct: func(ctx context.Context, path string) (string, error) {
			if _, err := os.Stat(goBin); err != nil {
				return "", errNoGo
			}
			return direct(ctx, path)
		},
	}
	return &goinstall.Program{
		Package: pkg,
		Version: v,
		Go:      goBin,
		Resolve: func(ctx context.Context, _ string) (string, error) {
			return env.Latest.Resolve(ctx, env.Client, pkg, resolver)
		},
	}
}

// ShellEnv sets GOPATH to ~/go, Go's default, and adds ~/.local/bin, where
// the go and gofmt commands are, and $GOPATH/bin, where go install puts
// programs, to PATH. GOROOT isn't set: go finds its own.
func (Module) ShellEnv(config.ModuleConfig, *resource.Runtime) (shells.Env, error) {
	return shells.Env{
		Path:      []string{toolmodule.BinPath, "$GOPATH/bin"},
		Variables: []shells.Variable{{Name: "GOPATH", Value: "~/go"}},
	}, nil
}
