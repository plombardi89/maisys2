// Package wslhost is the wsl_host module, which sets up WSL on Windows.
package wslhost

import "github.com/plombardi89/maisys2/internal/config"

// Module is the wsl_host module.
type Module struct{}

// Info describes the wsl_host module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "wsl_host",
		Summary: "Installs and updates WSL, writes .wslconfig, and creates and sets up distros.",
		OS:      []config.OS{config.Windows},
	}
}

// NewConfig returns the wsl_host module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{}
}

// Config is the wsl_host module's config.
type Config struct {
	config.Toggle
	// DefaultDistro is the distro WSL starts by default: one of distros.
	DefaultDistro string `toml:"default_distro"`
	// ConfigURL is the HTTPS URL new distros clone the config repo from.
	// Without it, they use this machine's own config URL, which must then
	// be HTTPS.
	ConfigURL string `toml:"config_url"`
	// WSLConfig sets keys in %USERPROFILE%\.wslconfig. Keys left out aren't
	// written, so WSL's defaults apply. Changes take effect after
	// wsl --shutdown.
	WSLConfig WSLConfig `toml:"wslconfig"`
	// Distros are the distros to create, by distro name. Each is created
	// and set up once, then kept up to date by running maisys sync inside
	// it. Distros are never deleted or recreated.
	Distros map[string]Distro `toml:"distros"`
}

// WSLConfig is the settings maisys manages in .wslconfig.
type WSLConfig struct {
	// Memory is the most memory WSL can use, such as "16GB".
	Memory string `toml:"memory"`
	// Processors is the number of processors WSL can use.
	Processors int `toml:"processors"`
	// Swap is the size of WSL's swap file, such as "8GB", or "0" for none.
	Swap string `toml:"swap"`
	// NetworkingMode is "nat" or "mirrored".
	NetworkingMode string `toml:"networking_mode"`
	// AutoMemoryReclaim is how WSL gives unused memory back to Windows:
	// "disabled", "gradual" or "dropcache".
	AutoMemoryReclaim string `toml:"auto_memory_reclaim"`
	// DNSTunneling answers WSL's DNS requests through Windows.
	DNSTunneling bool `toml:"dns_tunneling"`
}

// Distro is a WSL distro, set up from a Linux machine file.
type Distro struct {
	// Image is what to install, by its name in wsl --list --online, such
	// as "FedoraLinux-44". It must match the machine's os, and os_version
	// if set. It's only used when the distro is created.
	Image string `toml:"image"`
	// Machine is the Linux machine file maisys applies inside the distro.
	Machine string `toml:"machine"`
	// User is the Linux user to create, in the wheel or sudo group. It gets
	// a random password, shown once when the distro is created, which you
	// should change with passwd. For the first sync inside the distro only,
	// maisys adds a temporary rule that lets it use sudo without the
	// password; set sudo.nopasswd in the machine file to keep that.
	User string `toml:"user"`
}
