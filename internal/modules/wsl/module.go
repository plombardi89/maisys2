// Package wsl is the wsl module, for Linux distros running in WSL.
package wsl

import "github.com/plombardi89/maisys2/internal/config"

// Module describes the wsl module.
var Module = config.Module{
	Name:    "wsl",
	Summary: "Sets up a Linux distro running in WSL: /etc/wsl.conf, opening links in Windows, and optionally the Windows Git Credential Manager.",
	OS:      config.LinuxOS(),
	NewConfig: func() config.ModuleConfig {
		return &Config{Systemd: true, WSLG: true, AppendWindowsPath: true}
	},
}

// Config is the wsl module's config. Changes to /etc/wsl.conf take effect
// after the distro restarts.
type Config struct {
	config.Toggle
	// Systemd runs systemd in the distro.
	Systemd bool `toml:"systemd"`
	// WSLG shows Linux GUI apps with WSLg. Set it to false to use X410
	// instead, through a bridge service maisys sets up.
	WSLG bool `toml:"wslg"`
	// AppendWindowsPath adds the Windows PATH to the end of the distro's
	// PATH.
	AppendWindowsPath bool `toml:"append_windows_path"`
	// GitCredentialManager makes git use the Windows Git Credential
	// Manager, so the distro shares the sign-ins Windows has. It needs Git
	// for Windows.
	GitCredentialManager bool `toml:"git_credential_manager"`
}
