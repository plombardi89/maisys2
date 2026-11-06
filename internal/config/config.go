// Package config loads a machine's configuration from a config repo: its
// machine file and the profiles it lists, merged on top of each module's
// defaults.
package config

// OS is an operating system a machine can declare with the os key.
type OS string

// The operating systems maisys supports.
const (
	Fedora  OS = "fedora"
	Ubuntu  OS = "ubuntu"
	Windows OS = "windows"
)

// LinuxOS returns the Linux distributions maisys supports.
func LinuxOS() []OS {
	return []OS{Fedora, Ubuntu}
}

// AllOS returns every operating system maisys supports.
func AllOS() []OS {
	return []OS{Fedora, Ubuntu, Windows}
}

// Arch is a CPU architecture a Linux machine can declare with the arch key.
type Arch string

// The architectures maisys supports.
const (
	AMD64 Arch = "amd64"
	ARM64 Arch = "arm64"
)

// Module is a module's configuration side: what it is, and its config
// struct.
type Module interface {
	// Info describes the module.
	Info() ModuleInfo
	// NewConfig returns a pointer to a new config struct holding the
	// module's defaults.
	NewConfig() ModuleConfig
}

// ModuleInfo describes a module to the loader, and to the generated docs
// and config init.
type ModuleInfo struct {
	// Name is the module's table in config files, for example "golang".
	Name string
	// Summary describes what the module does, in one sentence.
	Summary string
	// OS lists the operating systems the module supports.
	OS []OS
	// Arch lists the CPU architectures the module supports. Empty means
	// every architecture.
	Arch []Arch
	// Hidden modules are left out of the generated docs and config init.
	Hidden bool
}

// ModuleConfig is a module's config struct. Every one embeds Toggle.
//
// Struct fields are read from TOML keys named by their toml tags. When
// layers are merged, a value set by a later layer replaces an earlier one,
// except that:
//   - nested tables are merged key by key;
//   - tables keyed by name (map fields) are merged entry by entry, and new
//     entries start from their type's defaults (see Defaulter);
//   - lists tagged merge:"union" combine across layers, without duplicates.
type ModuleConfig interface {
	IsEnabled() bool
}

// Toggle provides the enabled key every module has. Modules are off unless
// a profile or machine file turns them on.
type Toggle struct {
	// Enabled turns the module on.
	Enabled bool `toml:"enabled"`
}

// IsEnabled reports whether the module is turned on.
func (t Toggle) IsEnabled() bool {
	return t.Enabled
}

// Latest is the version that means the newest stable release.
const Latest = "latest"

// Versioned is embedded by modules that install a tool at a chosen version.
type Versioned struct {
	// Version to install: "latest" (the default) for the newest stable
	// release, or an exact version such as "1.2.3" to pin it.
	Version string `toml:"version"`
}

// Defaulter is implemented by the values of tables keyed by name (for
// example a files item) whose defaults aren't their zero values.
type Defaulter interface {
	SetDefaults()
}

// Machine is a machine's merged configuration.
type Machine struct {
	Name      string
	OS        OS
	OSVersion string
	Arch      Arch
	Profiles  []string

	// ProfileOS records the os each listed profile declares, if any.
	ProfileOS map[string]OS

	// Modules holds the merged config of every module, keyed by module
	// name. Modules no file mentions keep their defaults.
	Modules map[string]ModuleConfig

	// Origins records which files set each value, keyed by its dotted path
	// (for example "golang.version"). For lists that combine across layers,
	// it lists every file that added to them, in order.
	Origins map[string][]string
}
