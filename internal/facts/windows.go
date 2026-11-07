package facts

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/system"
)

// Windows describes a Windows host: what WSL 2 needs from it, and the WSL
// distros registered to the user.
type Windows struct {
	// Admin reports whether the user is a local admin: a member of the
	// Administrators group, whether or not maisys is running elevated.
	Admin bool
	// Elevated reports whether maisys is running elevated, with admin
	// rights. Then it runs the steps that need admin itself, without a
	// UAC prompt.
	Elevated bool
	// Virtualization says whether the CPU's virtualization extensions,
	// which WSL 2 needs, are turned on.
	Virtualization Virtualization
	// Winget is winget's version, such as "1.11.430", or "" if winget
	// isn't there. On a fresh Windows install, winget may be installed but
	// not yet registered for the user, which makes it look missing.
	Winget string
	// WSL is the version of WSL, such as "2.6.1.0", or "" if WSL isn't
	// installed.
	WSL string
	// Distros are the WSL distros registered to the user, by name. They're
	// read from the registry, never from wsl --list.
	Distros []Distro
	// Warnings say what couldn't be detected, and why.
	Warnings []string
}

// Virtualization says whether the CPU's virtualization extensions are
// turned on.
type Virtualization string

// The states of virtualization.
const (
	// VirtualizationUnknown means maisys couldn't tell.
	VirtualizationUnknown Virtualization = ""
	// VirtualizationOff means it's turned off in the firmware.
	VirtualizationOff Virtualization = "off"
	// VirtualizationOn means it's turned on in the firmware, or a
	// hypervisor is already running, as it is once WSL 2 or Hyper-V is.
	VirtualizationOn Virtualization = "on"
)

// Distro is a WSL distro registered to the user, as WSL records it under
// HKEY_CURRENT_USER\Software\Microsoft\Windows\CurrentVersion\Lxss.
type Distro struct {
	// Name is the distro's name, as wsl -d takes it.
	Name string
	// ID is the GUID WSL registered it under, such as
	// "{0d5f8a2e-…}".
	ID string
	// BasePath is the folder that holds the distro's files or disk.
	BasePath string
	// Version is the version of WSL it runs in: 2 in WSL 2's VM, 1
	// otherwise, or 0 if the registry doesn't say.
	Version int
	// State is WSL's state of the distro: DistroInstalled once it's
	// ready, other values while it's being installed, removed or
	// converted.
	State uint32
	// DefaultUID is the UID of the user wsl.exe starts the distro as.
	DefaultUID uint32
	// Default reports whether it's the default distro.
	Default bool
}

// DistroInstalled is the State of a distro that's ready to use.
const DistroInstalled = 1

// Installed reports whether the distro is ready to use: not still being
// installed, removed or converted.
func (d Distro) Installed() bool {
	return d.State == DistroInstalled
}

// Distro returns the registered distro called name, and whether there is
// one. Like WSL, it ignores case.
func (w *Windows) Distro(name string) (Distro, bool) {
	for _, d := range w.Distros {
		if strings.EqualFold(d.Name, name) {
			return d, true
		}
	}
	return Distro{}, false
}

// lxssKey is where WSL registers each user's distros, under
// HKEY_CURRENT_USER: one subkey per distro, named by its GUID.
const lxssKey = `Software\Microsoft\Windows\CurrentVersion\Lxss`

// lxssVMMode is the bit of a distro's Flags that's set when it runs in
// WSL 2's VM (LXSS_DISTRO_FLAGS_VM_MODE in WSL's source).
const lxssVMMode = 0x8

// registryReader reads values under HKEY_CURRENT_USER. Tests use a fake one.
type registryReader interface {
	// SubKeys returns the names of key's subkeys. A key that doesn't exist
	// has none.
	SubKeys(key string) ([]string, error)
	// String returns key's string value name (REG_SZ or REG_EXPAND_SZ,
	// unexpanded), and whether it exists.
	String(key, name string) (string, bool, error)
	// Integer returns key's integer value name (REG_DWORD or REG_QWORD),
	// and whether it exists.
	Integer(key, name string) (uint64, bool, error)
}

// windowsProbe is what Windows detection asks: commands through runner,
// the registry, and the process's access token.
type windowsProbe struct {
	runner   system.Runner
	registry registryReader
	// token reports whether the user is a local admin, and whether maisys
	// is running elevated.
	token func() (admin, elevated bool, err error)
}

// detectTimeout is how long each command Windows detection runs may take.
const detectTimeout = time.Minute

// The commands Windows detection runs.
var (
	// virtualizationCmd asks CIM whether a hypervisor is running, and
	// whether virtualization is turned on in the firmware. The firmware
	// setting reads as off while a hypervisor is running, so both count.
	virtualizationCmd = system.Cmd{
		Name: "powershell.exe",
		Args: []string{
			"-NoProfile", "-NonInteractive", "-Command",
			"$cs = Get-CimInstance -ClassName Win32_ComputerSystem; " +
				"$cpu = @(Get-CimInstance -ClassName Win32_Processor)[0]; " +
				"[pscustomobject]@{HypervisorPresent = [bool]$cs.HypervisorPresent; " +
				"VirtualizationFirmwareEnabled = [bool]$cpu.VirtualizationFirmwareEnabled} | ConvertTo-Json -Compress",
		},
		Label: "checking virtualization",
	}
	// wingetVersionCmd prints winget's version, such as "v1.11.430".
	wingetVersionCmd = system.Cmd{Name: "winget", Args: []string{"--version"}, Label: "winget --version"}
	// wslVersionCmd prints the versions of WSL and its parts. WSL_UTF8
	// asks wsl.exe for UTF-8 rather than UTF-16.
	wslVersionCmd = system.Cmd{Name: "wsl.exe", Args: []string{"--version"}, Env: []string{"WSL_UTF8=1"}, Label: "wsl --version"}
)

// detectWindows detects a Windows host's details with p. The registry and
// the access token must be readable; what commands report is only
// best-effort, and Warnings say what they couldn't tell.
func detectWindows(ctx context.Context, p windowsProbe) (*Windows, error) {
	w := &Windows{}
	var err error
	if w.Admin, w.Elevated, err = p.token(); err != nil {
		return nil, fmt.Errorf("checking whether you're an admin: %w", err)
	}
	if w.Distros, err = readDistros(p.registry); err != nil {
		return nil, fmt.Errorf("reading WSL's distros from the registry: %w", err)
	}

	var mu sync.Mutex
	warn := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		w.Warnings = append(w.Warnings, fmt.Sprintf(format, a...))
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		v, err := detectVirtualization(ctx, p.runner)
		if err != nil {
			warn("couldn't tell whether virtualization is turned on: %v", err)
		}
		w.Virtualization = v
	})
	wg.Go(func() {
		v, err := DetectWinget(ctx, p.runner)
		if err != nil {
			warn("couldn't tell whether winget works: %v", err)
		}
		w.Winget = v
	})
	wg.Go(func() {
		v, err := detectWSL(ctx, p.runner)
		if err != nil {
			warn("couldn't tell which version of WSL is installed: %v", err)
		}
		w.WSL = v
	})
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.Sort(w.Warnings)
	return w, nil
}

// readDistros reads the distros registered to the user from the registry,
// sorted by name. Subkeys without a DistributionName aren't distros.
func readDistros(reg registryReader) ([]Distro, error) {
	ids, err := reg.SubKeys(lxssKey)
	if err != nil {
		return nil, err
	}
	defaultID, _, err := reg.String(lxssKey, "DefaultDistribution")
	if err != nil {
		return nil, err
	}
	var distros []Distro
	for _, id := range ids {
		key := lxssKey + `\` + id
		name, ok, err := reg.String(key, "DistributionName")
		if err != nil {
			return nil, err
		}
		if !ok || name == "" {
			continue
		}
		d := Distro{Name: name, ID: id, Default: strings.EqualFold(id, defaultID)}
		basePath, _, err := reg.String(key, "BasePath")
		if err != nil {
			return nil, err
		}
		d.BasePath = strings.TrimPrefix(basePath, `\\?\`)
		state, _, err := reg.Integer(key, "State")
		if err != nil {
			return nil, err
		}
		uid, _, err := reg.Integer(key, "DefaultUid")
		if err != nil {
			return nil, err
		}
		flags, ok, err := reg.Integer(key, "Flags")
		if err != nil {
			return nil, err
		}
		d.State, d.DefaultUID = uint32(state), uint32(uid)
		switch {
		case !ok:
		case flags&lxssVMMode != 0:
			d.Version = 2
		default:
			d.Version = 1
		}
		distros = append(distros, d)
	}
	slices.SortFunc(distros, func(a, b Distro) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})
	return distros, nil
}

// detectVirtualization asks CIM whether virtualization is turned on.
func detectVirtualization(ctx context.Context, r system.Runner) (Virtualization, error) {
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	res, err := r.Run(ctx, virtualizationCmd)
	if err != nil {
		return VirtualizationUnknown, err
	}
	return ParseVirtualization(res.Stdout)
}

// ParseVirtualization reads what virtualizationCmd prints: JSON with
// whether a hypervisor is running, and whether virtualization is turned
// on in the firmware.
func ParseVirtualization(out []byte) (Virtualization, error) {
	var v struct {
		HypervisorPresent             *bool
		VirtualizationFirmwareEnabled *bool
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return VirtualizationUnknown, fmt.Errorf("unexpected output %q: %w", bytes.TrimSpace(out), err)
	}
	if v.HypervisorPresent == nil || v.VirtualizationFirmwareEnabled == nil {
		return VirtualizationUnknown, fmt.Errorf("unexpected output %q", bytes.TrimSpace(out))
	}
	if *v.HypervisorPresent || *v.VirtualizationFirmwareEnabled {
		return VirtualizationOn, nil
	}
	return VirtualizationOff, nil
}

// DetectWinget returns winget's version, or "" if it isn't there.
func DetectWinget(ctx context.Context, r system.Runner) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	res, err := r.Run(ctx, wingetVersionCmd)
	switch {
	case missing(err):
		return "", nil
	case err != nil:
		return "", err
	}
	v := ParseWingetVersion(res.Stdout)
	if v == "" {
		return "", fmt.Errorf("unexpected output from winget --version: %q", bytes.TrimSpace(res.Stdout))
	}
	return v, nil
}

// wingetVersion matches the version winget --version prints.
var wingetVersion = regexp.MustCompile(`^v?(\d+(?:\.\d+)+)`)

// ParseWingetVersion reads the version winget --version prints, such as
// "v1.11.430", without the "v". It returns "" if out isn't a version.
func ParseWingetVersion(out []byte) string {
	m := wingetVersion.FindSubmatch(bytes.TrimSpace(out))
	if m == nil {
		return ""
	}
	return string(m[1])
}

// detectWSL returns WSL's version, or "" if WSL isn't installed: then
// wsl.exe, the stub Windows comes with, fails or doesn't know --version.
func detectWSL(ctx context.Context, r system.Runner) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	res, err := r.Run(ctx, wslVersionCmd)
	if _, ok := errors.AsType[*system.ExitError](err); ok || missing(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return ParseWSLVersion(res.Stdout), nil
}

// wslVersion matches a version number in wsl --version's first line, which
// names WSL in the user's language, such as "WSL version: 2.6.1.0" or
// "WSL-Version: 2.6.1.0".
var wslVersion = regexp.MustCompile(`\d+(?:\.\d+)+`)

// ParseWSLVersion reads WSL's version from what wsl --version prints, in
// UTF-8 or UTF-16 (see DecodeWSLOutput). It returns "" if there's none.
func ParseWSLVersion(out []byte) string {
	text := strings.TrimSpace(DecodeWSLOutput(out))
	first, _, _ := strings.Cut(text, "\n")
	return wslVersion.FindString(first)
}

// DecodeWSLOutput returns wsl.exe's output as text. wsl.exe writes UTF-16LE
// unless WSL_UTF8=1 is set, which older versions ignore, so output with a
// byte order mark or NUL bytes, which UTF-8 text never has, is decoded as
// UTF-16LE. It also turns CRLF line endings into LF.
func DecodeWSLOutput(out []byte) string {
	if bytes.HasPrefix(out, []byte{0xff, 0xfe}) {
		out = utf16LE(out[2:])
	} else if bytes.IndexByte(out, 0) >= 0 {
		out = utf16LE(out)
	}
	return strings.ReplaceAll(string(out), "\r\n", "\n")
}

// utf16LE decodes UTF-16LE text into UTF-8. An odd last byte is dropped.
func utf16LE(b []byte) []byte {
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return []byte(string(utf16.Decode(units)))
}

// missing reports whether err means the command isn't installed.
func missing(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// CheckWindowsHost runs the checks of a Windows host's first sync, before
// it changes anything: that it runs Windows 11, that virtualization is
// turned on, that winget works and that you're a local admin. It returns
// every problem it finds, joined, each saying what to do. The host's
// details must have been detected (see DetectWindows).
func (s *System) CheckWindowsHost() error {
	if s.OS != config.Windows {
		return nil
	}
	w := s.Windows
	if w == nil {
		return errors.New("this Windows host's details haven't been detected")
	}
	var errs []error
	if build, err := strconv.ParseUint(s.OSVersion, 10, 32); err != nil || build < windows11Build {
		errs = append(errs, fmt.Errorf("maisys needs Windows 11 (build %d or newer), but this is %s: upgrade to Windows 11, then run maisys sync again",
			windows11Build, s.name()))
	}
	if w.Virtualization == VirtualizationOff {
		errs = append(errs, errors.New("virtualization is turned off in this PC's firmware, and WSL 2 needs it: "+
			"restart into the BIOS or UEFI settings, turn on Intel VT-x or AMD-V (also called SVM, or Virtualization Technology), then run maisys sync again"))
	}
	if w.Winget == "" {
		errs = append(errs, errors.New("winget isn't installed, and maisys installs Windows software with it: "+
			"install App Installer from the Microsoft Store (https://apps.microsoft.com/detail/9NBLGGH4NNS1), then run maisys sync again"))
	}
	if !w.Admin {
		errs = append(errs, errors.New("you aren't a local admin (a member of the Administrators group), and installing WSL needs admin rights: "+
			"sign in as an admin, or ask one to add you to the Administrators group, then sign out and back in and run maisys sync again"))
	}
	return errors.Join(errs...)
}
