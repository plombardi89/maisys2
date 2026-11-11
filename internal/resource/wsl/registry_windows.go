//go:build windows

package wsl

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Where WSL and Windows keep what Registry reads.
const (
	// lxssKey holds the user's distros (in HKEY_CURRENT_USER), and in
	// HKEY_LOCAL_MACHINE, WSL's own settings.
	lxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`
	// msiKey is where WSL's installer records its version.
	msiKey = lxssKey + `\MSI`
	// rebootKey exists while Windows has servicing changes, such as newly
	// turned on optional features, that wait for a restart.
	rebootKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`
	// gitKey is where Git for Windows' installer records where it went.
	gitKey = `SOFTWARE\GitForWindows`
)

// Live returns the Registry of the running Windows.
func Live() Registry { return liveRegistry{} }

type liveRegistry struct{}

func (liveRegistry) Distros() ([]Registered, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, lxssKey, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading WSL's distros from the registry: %w", err)
	}
	defer func() { _ = k.Close() }()
	ids, err := k.ReadSubKeyNames(0)
	if err != nil {
		return nil, fmt.Errorf("reading WSL's distros from the registry: %w", err)
	}
	var distros []Registered
	for _, id := range ids {
		d, ok, err := readDistro(k, id)
		if err != nil {
			return nil, err
		}
		if ok {
			distros = append(distros, d)
		}
	}
	return distros, nil
}

// readDistro reads the distro under lxss's subkey id, and whether that's a
// distro at all.
func readDistro(lxss registry.Key, id string) (Registered, bool, error) {
	k, err := registry.OpenKey(lxss, id, registry.QUERY_VALUE)
	if err != nil {
		return Registered{}, false, fmt.Errorf("reading WSL distro %s from the registry: %w", id, err)
	}
	defer func() { _ = k.Close() }()
	name, _, err := k.GetStringValue("DistributionName")
	if errors.Is(err, registry.ErrNotExist) {
		return Registered{}, false, nil
	}
	if err != nil {
		return Registered{}, false, fmt.Errorf("reading WSL distro %s from the registry: %w", id, err)
	}
	d := Registered{ID: id, Name: name}
	if modern, _, err := k.GetIntegerValue("Modern"); err == nil {
		d.Modern = modern != 0
	}
	if profile, _, err := k.GetStringValue("TerminalProfilePath"); err == nil {
		d.TerminalProfile = profile
	}
	return d, true, nil
}

func (liveRegistry) DefaultDistro() (string, error) {
	return stringValue(registry.CURRENT_USER, lxssKey, "DefaultDistribution")
}

func (liveRegistry) Version() (string, error) {
	return stringValue(registry.LOCAL_MACHINE, msiKey, "Version")
}

func (liveRegistry) RebootPending() (bool, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, rebootKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking whether Windows needs a restart: %w", err)
	}
	_ = k.Close()
	return true, nil
}

func (liveRegistry) GitDir() (string, error) {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		dir, err := stringValue(root, gitKey, "InstallPath")
		if err != nil {
			return "", err
		}
		if dir != "" {
			return dir, nil
		}
	}
	// Installs that didn't record where they went use the default folder.
	dir := filepath.Join(cmp.Or(os.Getenv("ProgramFiles"), `C:\Program Files`), "Git")
	info, err := os.Stat(dir)
	switch {
	case err == nil && info.IsDir():
		return dir, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return "", err
	default:
		return "", nil
	}
}

// stringValue returns the string value name of the key at path under
// root, or "" if there's no such key or value.
func stringValue(root registry.Key, path, name string) (string, error) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s from the registry: %w", path, err)
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s\\%s from the registry: %w", path, name, err)
	}
	return strings.TrimSpace(v), nil
}
