// Package wsl is the wsl module, for Linux distros running in WSL.
package wsl

import "github.com/plombardi89/maisys2/internal/config"

// Module is the wsl module.
type Module struct{}

// Info describes the wsl module.
func (Module) Info() config.ModuleInfo {
	return config.ModuleInfo{
		Name:    "wsl",
		Summary: "Sets up a Linux distro running in WSL: /etc/wsl.conf, opening links in Windows, and optionally the Windows Git Credential Manager.",
		OS:      config.LinuxOS(),
	}
}

// NewConfig returns the wsl module's defaults.
func (Module) NewConfig() config.ModuleConfig {
	return &Config{Systemd: true, WSLG: true, AppendWindowsPath: true}
}

// Config is the wsl module's config. Changes to /etc/wsl.conf take effect
// after the distro restarts.
type Config struct {
	config.Toggle
	// Systemd runs systemd in the distro.
	Systemd bool `toml:"systemd"`
	// WSLG shows Linux GUI apps with WSLg. Set it to false to use X410
	// instead: maisys then sets up x410.service, a systemd user service
	// that forwards display :0 to X410 over VSOCK with socat, and sets
	// DISPLAY to :0. That needs systemd, X410 listening on VSOCK, and WSLg
	// turned off in Windows (guiApplications=false in .wslconfig).
	WSLG bool `toml:"wslg"`
	// AppendWindowsPath adds the Windows PATH to the end of the distro's
	// PATH.
	AppendWindowsPath bool `toml:"append_windows_path"`
	// GitCredentialManager makes git use the Windows Git Credential
	// Manager, so the distro shares the sign-ins Windows has. It needs Git
	// for Windows, in C:\Program Files\Git.
	GitCredentialManager bool `toml:"git_credential_manager"`
}
