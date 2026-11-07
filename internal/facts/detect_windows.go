//go:build windows

package facts

import (
	"context"
	"errors"
	"strconv"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/system"
)

func detectOS() (*System, error) {
	build := windows.RtlGetVersion().BuildNumber
	return &System{
		OS:         config.Windows,
		OSID:       string(config.Windows),
		OSVersion:  strconv.FormatUint(uint64(build), 10),
		PrettyName: windowsName(build),
	}, nil
}

// DetectWindows detects the details of the Windows host maisys is running
// on: whether you're a local admin and maisys is elevated (from its access
// token), whether virtualization is turned on, winget's and WSL's versions
// (running commands with r), and the WSL distros registered to you (from
// the registry). What the commands can't tell is in Warnings.
func DetectWindows(ctx context.Context, r system.Runner) (*Windows, error) {
	return detectWindows(ctx, windowsProbe{runner: r, registry: liveRegistry{}, token: tokenInfo})
}

// liveRegistry reads the registry of the user maisys runs as.
type liveRegistry struct{}

func (liveRegistry) SubKeys(path string) ([]string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = k.Close() }()
	return k.ReadSubKeyNames(-1)
}

func (liveRegistry) String(path, name string) (string, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetStringValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (liveRegistry) Integer(path, name string) (uint64, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = k.Close() }()
	v, _, err := k.GetIntegerValue(name)
	if errors.Is(err, registry.ErrNotExist) {
		return 0, false, nil
	}
	return v, err == nil, err
}

// tokenInfo reports whether the user is a local admin, and whether maisys
// is running elevated, from its access token. Unelevated, under UAC, the
// token still lists the Administrators group, but only to deny access; the
// elevated token linked to it is checked too.
func tokenInfo() (admin, elevated bool, err error) {
	token := windows.GetCurrentProcessToken()
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, false, err
	}
	admin, err = hasGroup(token, admins)
	if err != nil {
		return false, false, err
	}
	if !admin {
		// Only a split token under UAC has a linked token.
		if linked, err := token.GetLinkedToken(); err == nil {
			admin, _ = hasGroup(linked, admins)
			_ = linked.Close()
		}
	}
	return admin, token.IsElevated(), nil
}

// hasGroup reports whether token lists group, enabled or only for denying
// access.
func hasGroup(token windows.Token, group *windows.SID) (bool, error) {
	groups, err := token.GetTokenGroups()
	if err != nil {
		return false, err
	}
	for _, g := range groups.AllGroups() {
		if g.Sid.Equals(group) {
			return true, nil
		}
	}
	return false, nil
}
