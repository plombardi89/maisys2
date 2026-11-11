// Package golang is the golang module.
package golang

import (
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
)

// Module is the golang module.
type Module struct{}

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
	// path and a version, such as "mvdan.cc/gofumpt@latest". A program is
	// only reinstalled when a newer version exists.
	Tools []string `toml:"tools" merge:"union"`
}

var _ config.Checker = (*Config)(nil)

// Check checks that an exact version is a full Go version, such as
// "1.26.4", and that tools has no empty entries.
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
}
