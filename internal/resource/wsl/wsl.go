// Package wsl manages WSL on a Windows host: WSL itself, installed and
// kept up to date (kind "wsl"); the distros the config lists, each created
// and set up once (kind "wsl-distro"); and the distro WSL starts by default
// (kind "wsl-default"). Distros are never deleted or recreated: once a
// distro exists, it's kept up to date by running maisys sync inside it.
//
// What WSL and Windows know is read from the registry (see Registry), not
// from wsl.exe's output, which is meant for people and translated. wsl.exe
// runs with WSL_UTF8=1, so what it prints in the log is UTF-8, and commands
// inside a distro run with --exec, so their arguments reach Linux as they
// are, without a shell in between.
package wsl

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

// Exe is the WSL command.
const Exe = "wsl.exe"

// utf8Env makes wsl.exe print UTF-8 rather than UTF-16.
const utf8Env = "WSL_UTF8=1"

// command returns a wsl.exe command, labelled for errors.
func command(label string, args ...string) system.Cmd {
	return system.Cmd{Name: Exe, Args: args, Env: []string{utf8Env}, Label: label}
}

// inDistro returns a command that runs argv in distro as user, in the
// user's home folder, without a shell.
func inDistro(distro, user, label string, argv ...string) system.Cmd {
	return command(label, append([]string{"-d", distro, "-u", user, "--cd", "~", "--exec"}, argv...)...)
}

// Registry reads what WSL and Windows record in the registry.
type Registry interface {
	// Distros returns the WSL distros registered to the user, from
	// HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss.
	Distros() ([]Registered, error)
	// DefaultDistro returns the ID of the distro WSL starts by default,
	// or "" if there isn't one.
	DefaultDistro() (string, error)
	// Version returns the version of WSL that's installed, such as
	// "2.6.1.0", or "" if WSL isn't installed.
	Version() (string, error)
	// RebootPending reports whether Windows has changes that only take
	// effect once it restarts, such as the optional features WSL needs.
	RebootPending() (bool, error)
	// GitDir returns the folder Git for Windows is installed in, or "" if
	// it isn't.
	GitDir() (string, error)
}

// Registered is a distro registered to the user.
type Registered struct {
	// ID is the distro's GUID, such as
	// "{0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9}": its key in the registry.
	ID string
	// Name is the distro's name.
	Name string
	// Modern reports whether the distro was installed from a .wsl image,
	// as wsl --install installs them now. WSL gives those a Windows
	// Terminal profile of its own.
	Modern bool
	// TerminalProfile is the Windows Terminal fragment WSL wrote for the
	// distro, if it wrote one.
	TerminalProfile string
}

// Find returns the distro called name in distros. Distro names are
// compared without regard to case, as WSL compares them.
func Find(distros []Registered, name string) (Registered, bool) {
	for _, d := range distros {
		if strings.EqualFold(d.Name, name) {
			return d, true
		}
	}
	return Registered{}, false
}

// RegistryKey is the key the Registry of a sync is shared under (see
// resource.Runtime.Shared). Tests share a fake one under it.
type RegistryKey struct{}

// RegistryFor returns the Registry the resources of rt's sync share: the
// live one, unless the sync shares another.
func RegistryFor(rt *resource.Runtime) (Registry, error) {
	v, err := rt.Shared(RegistryKey{}, func() (any, error) { return Live(), nil })
	if err != nil {
		return nil, err
	}
	reg, ok := v.(Registry)
	if !ok {
		return nil, fmt.Errorf("the shared registry is a %T", v)
	}
	return reg, nil
}

// LinuxPath returns the path WSL mounts the Windows path p at inside a
// distro, such as /mnt/c/Users/phil for C:\Users\phil: drives are under
// /mnt, as they are unless the distro's wsl.conf moves them, which the
// wsl.conf maisys writes doesn't. p must be an absolute path on a drive.
func LinuxPath(p string) (string, error) {
	if len(p) < 3 || p[1] != ':' || (p[2] != '\\' && p[2] != '/') || !isLetter(p[0]) {
		return "", fmt.Errorf("can't find %s inside WSL: only paths on a drive, such as C:\\Users, are", p)
	}
	rest := strings.Trim(strings.ReplaceAll(p[3:], `\`, "/"), "/")
	linux := "/mnt/" + strings.ToLower(p[:1])
	if rest != "" {
		linux += "/" + rest
	}
	return linux, nil
}

// LinuxURL returns the URL a distro clones a git repo from that Windows
// clones from rawURL: the same URL, except that a local path, or a file
// URL, is the path inside WSL (see LinuxPath).
func LinuxURL(rawURL string) (string, error) {
	if u, err := url.Parse(rawURL); err == nil && strings.EqualFold(u.Scheme, "file") {
		return LinuxPath(strings.TrimPrefix(u.Path, "/"))
	}
	if IsLocal(rawURL) {
		return LinuxPath(rawURL)
	}
	return rawURL, nil
}

// IsLocal reports whether rawURL is a path on a Windows drive, or a file
// URL, rather than the URL of a repo on a server.
func IsLocal(rawURL string) bool {
	if len(rawURL) >= 3 && rawURL[1] == ':' && isLetter(rawURL[0]) && (rawURL[2] == '\\' || rawURL[2] == '/') {
		return true
	}
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(u.Scheme, "file")
}

func isLetter(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}
