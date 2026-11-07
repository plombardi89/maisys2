// Package facts detects details of the system maisys is running on: the
// operating system and its version, the CPU architecture, the hostname,
// whether it's a WSL distro, and whether maisys runs as root. It also holds
// the rolling support policy, and checks a machine against the live system.
package facts

import (
	"fmt"
	"os"
	"runtime"

	"github.com/plombardi89/maisys2/internal/config"
)

// System describes the system maisys is running on.
type System struct {
	// OS is the operating system, or "" if maisys doesn't support it.
	OS config.OS
	// OSID identifies the operating system even when maisys doesn't support
	// it: the ID from os-release on Linux (for example "debian"), or
	// "windows".
	OSID string
	// OSVersion is the OS version as machine files' os_version gives it: the
	// VERSION_ID from os-release on Linux (for example "44" or "26.04"), or
	// the build number on Windows (for example "26100").
	OSVersion string
	// PrettyName is the OS's name and version for people to read, for
	// example "Fedora Linux 44 (Workstation Edition)".
	PrettyName string
	// Arch is the CPU architecture, or "" if maisys doesn't support it.
	Arch config.Arch
	// RawArch is the CPU architecture in Go's terms (GOARCH), even when
	// maisys doesn't support it.
	RawArch string
	// Hostname is the machine's hostname, as the OS reports it.
	Hostname string
	// WSL reports whether this is a Linux distro running in WSL.
	WSL bool
	// WSLDistro is the WSL distro's name, if WSL_DISTRO_NAME is set.
	WSLDistro string
	// Root reports whether maisys is running as root (effective user ID
	// 0). It's always false on Windows.
	Root bool
}

// Detect detects the system maisys is running on.
func Detect() (*System, error) {
	s, err := detectOS()
	if err != nil {
		return nil, err
	}
	s.RawArch = runtime.GOARCH
	s.Arch = archOf(runtime.GOARCH)
	// os.Geteuid returns -1 on Windows.
	s.Root = os.Geteuid() == 0
	if s.Hostname, err = os.Hostname(); err != nil {
		return nil, fmt.Errorf("finding the hostname: %w", err)
	}
	return s, nil
}

// archOf returns the architecture for a GOARCH value, or "" if maisys
// doesn't support it.
func archOf(goarch string) config.Arch {
	switch a := config.Arch(goarch); a {
	case config.AMD64, config.ARM64:
		return a
	default:
		return ""
	}
}

// windowsName returns a readable name for a Windows build. Windows 11 still
// reports version 10.0, so the build number is what tells them apart.
func windowsName(build uint32) string {
	name := "Windows 10"
	if build >= windows11Build {
		name = "Windows 11"
	}
	return fmt.Sprintf("%s (build %d)", name, build)
}
