package facts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// windowsOutput returns the recorded Windows command output name, from
// testdata/windows.
func windowsOutput(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "windows", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakeRegistry is a registry of keys under HKEY_CURRENT_USER, each with
// its values: strings, or uint64s for integers.
type fakeRegistry struct {
	keys map[string]map[string]any
	err  error
}

func (r fakeRegistry) SubKeys(key string) ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	var subs []string
	for k := range r.keys {
		if sub, ok := strings.CutPrefix(k, key+`\`); ok && !strings.Contains(sub, `\`) {
			subs = append(subs, sub)
		}
	}
	return subs, nil
}

func (r fakeRegistry) value(key, name string) (any, bool) {
	v, ok := r.keys[key][name]
	return v, ok
}

func (r fakeRegistry) String(key, name string) (string, bool, error) {
	v, ok := r.value(key, name)
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", false, fmt.Errorf("%s\\%s isn't a string", key, name)
	}
	return s, true, nil
}

func (r fakeRegistry) Integer(key, name string) (uint64, bool, error) {
	v, ok := r.value(key, name)
	if !ok {
		return 0, false, nil
	}
	n, ok := v.(uint64)
	if !ok {
		return 0, false, fmt.Errorf("%s\\%s isn't an integer", key, name)
	}
	return n, true, nil
}

// lxssRegistry is a registry with WSL's record of distros, as WSL writes
// it: a WSL 2 distro that's the default, a WSL 1 distro, one still being
// installed, and a subkey that isn't a distro.
var lxssRegistry = fakeRegistry{keys: map[string]map[string]any{
	lxssKey: {
		"DefaultDistribution": "{6c5e3c3b-8f3a-4b7a-9d2e-1f0a2b3c4d5e}",
		"DefaultVersion":      uint64(2),
	},
	lxssKey + `\{6c5e3c3b-8f3a-4b7a-9d2e-1f0a2b3c4d5e}`: {
		"DistributionName": "fedora-dev",
		"BasePath":         `C:\Users\phil\AppData\Local\wsl\{6c5e3c3b-8f3a-4b7a-9d2e-1f0a2b3c4d5e}`,
		"State":            uint64(1),
		"Version":          uint64(2),
		"Flags":            uint64(15),
		"DefaultUid":       uint64(1000),
		"Flavor":           "fedora",
		"OsVersion":        "44",
	},
	lxssKey + `\{0d1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d}`: {
		"DistributionName":  "Ubuntu",
		"BasePath":          `\\?\C:\Users\phil\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState`,
		"State":             uint64(1),
		"Version":           uint64(2),
		"Flags":             uint64(7),
		"DefaultUid":        uint64(1000),
		"PackageFamilyName": "CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc",
	},
	lxssKey + `\{a1b2c3d4-e5f6-4789-abcd-ef0123456789}`: {
		"DistributionName": "ubuntu-scratch",
		"BasePath":         `C:\wsl\ubuntu-scratch`,
		"State":            uint64(3),
		"Version":          uint64(2),
		"Flags":            uint64(15),
	},
	lxssKey + `\AppxInstallerCache`: {},
}}

// lxssDistros are the distros in lxssRegistry.
var lxssDistros = []Distro{
	{
		Name: "fedora-dev", ID: "{6c5e3c3b-8f3a-4b7a-9d2e-1f0a2b3c4d5e}",
		BasePath: `C:\Users\phil\AppData\Local\wsl\{6c5e3c3b-8f3a-4b7a-9d2e-1f0a2b3c4d5e}`,
		Version:  2, State: DistroInstalled, DefaultUID: 1000, Default: true,
	},
	{
		Name: "Ubuntu", ID: "{0d1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d}",
		BasePath: `C:\Users\phil\AppData\Local\Packages\CanonicalGroupLimited.Ubuntu_79rhkp1fndgsc\LocalState`,
		Version:  1, State: DistroInstalled, DefaultUID: 1000,
	},
	{Name: "ubuntu-scratch", ID: "{a1b2c3d4-e5f6-4789-abcd-ef0123456789}", BasePath: `C:\wsl\ubuntu-scratch`, Version: 2, State: 3},
}

// notInstalled is how the runner fails to run a program that isn't
// installed.
func notInstalled(c system.Cmd) (system.Result, error) {
	return system.Result{ExitCode: -1}, fmt.Errorf("%s: %s isn't installed: %w", c.Label, c.Name, exec.ErrNotFound)
}

func TestDetectWindows(t *testing.T) {
	tests := []struct {
		name string
		// setup scripts the commands.
		setup func(t *testing.T, r *systemtest.FakeRunner)
		want  Windows
	}{
		{
			name: "everything",
			setup: func(t *testing.T, r *systemtest.FakeRunner) {
				t.Helper()
				r.On("powershell.exe", systemtest.Respond(0, string(windowsOutput(t, "virtualization-hypervisor.json"))))
				r.On("winget --version", systemtest.Respond(0, string(windowsOutput(t, "winget-version.txt"))))
				r.On("wsl.exe --version", systemtest.Respond(0, string(windowsOutput(t, "wsl-version.txt"))))
			},
			want: Windows{Admin: true, Virtualization: VirtualizationOn, Winget: "1.11.430", WSL: "2.6.1.0", Distros: lxssDistros},
		},
		{
			name: "WSL's output in UTF-16",
			setup: func(t *testing.T, r *systemtest.FakeRunner) {
				t.Helper()
				r.On("powershell.exe", systemtest.Respond(0, string(windowsOutput(t, "virtualization-firmware.json"))))
				r.On("winget --version", systemtest.Respond(0, string(windowsOutput(t, "winget-version.txt"))))
				r.On("wsl.exe --version", systemtest.Respond(0, string(windowsOutput(t, "wsl-version-utf16.txt"))))
			},
			want: Windows{Admin: true, Virtualization: VirtualizationOn, Winget: "1.11.430", WSL: "2.6.1.0", Distros: lxssDistros},
		},
		{
			name: "a fresh install",
			setup: func(t *testing.T, r *systemtest.FakeRunner) {
				t.Helper()
				r.On("powershell.exe", systemtest.Respond(0, string(windowsOutput(t, "virtualization-off.json"))))
				r.On("winget --version", func(_ context.Context, c system.Cmd) (system.Result, error) { return notInstalled(c) })
				r.On("wsl.exe --version", systemtest.Respond(1, string(windowsOutput(t, "wsl-not-installed.txt"))))
			},
			want: Windows{Admin: true, Virtualization: VirtualizationOff, Distros: lxssDistros},
		},
		{
			name: "nothing can be checked",
			setup: func(_ *testing.T, r *systemtest.FakeRunner) {
				r.On("powershell.exe", systemtest.Fail(1, "Get-CimInstance : Access denied\r\n"))
				r.On("winget --version", systemtest.Respond(0, "Failed to initialize\r\n"))
				r.On("wsl.exe --version", func(_ context.Context, c system.Cmd) (system.Result, error) { return notInstalled(c) })
			},
			want: Windows{Admin: true, Distros: lxssDistros, Warnings: []string{
				"couldn't tell whether virtualization is turned on: checking virtualization failed with exit code 1: Get-CimInstance : Access denied",
				`couldn't tell whether winget works: unexpected output from winget --version: "Failed to initialize"`,
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := systemtest.NewFakeRunner(t)
			tt.setup(t, r)
			got, err := detectWindows(t.Context(), windowsProbe{
				runner:   r,
				registry: lxssRegistry,
				token:    func() (bool, bool, error) { return true, false, nil },
			})
			if err != nil {
				t.Fatalf("detectWindows() error = %v", err)
			}
			if !reflect.DeepEqual(*got, tt.want) {
				t.Errorf("detectWindows() =\n%+v\nwant\n%+v", *got, tt.want)
			}
			for _, c := range r.Calls() {
				if c.Cmd.Root {
					t.Errorf("%s ran as admin", c.Cmd)
				}
				if c.Cmd.Name == "wsl.exe" && !reflect.DeepEqual(c.Cmd.Env, []string{"WSL_UTF8=1"}) {
					t.Errorf("%s ran with environment %q, want WSL_UTF8=1", c.Cmd, c.Cmd.Env)
				}
			}
		})
	}
}

func TestDetectWindowsFails(t *testing.T) {
	denied := errors.New("access is denied")
	tests := []struct {
		name     string
		registry registryReader
		token    func() (bool, bool, error)
		want     string
	}{
		{
			name:     "the registry",
			registry: fakeRegistry{err: denied},
			token:    func() (bool, bool, error) { return true, true, nil },
			want:     "reading WSL's distros from the registry: access is denied",
		},
		{
			name: "a value of the wrong type",
			registry: fakeRegistry{keys: map[string]map[string]any{
				lxssKey + `\{x}`: {"DistributionName": "dev", "State": "installed"},
			}},
			token: func() (bool, bool, error) { return true, true, nil },
			want:  `reading WSL's distros from the registry: ` + lxssKey + `\{x}\State isn't an integer`,
		},
		{
			name:     "the token",
			registry: lxssRegistry,
			token:    func() (bool, bool, error) { return false, false, denied },
			want:     "checking whether you're an admin: access is denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := detectWindows(t.Context(), windowsProbe{runner: systemtest.NewFakeRunner(t), registry: tt.registry, token: tt.token})
			if err == nil || err.Error() != tt.want {
				t.Errorf("detectWindows() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDetectWindowsWithoutWSL(t *testing.T) {
	r := systemtest.NewFakeRunner(t)
	r.On("powershell.exe", systemtest.Respond(0, string(windowsOutput(t, "virtualization-firmware.json"))))
	r.On("winget --version", systemtest.Respond(0, "v1.11.430\n"))
	r.On("wsl.exe --version", systemtest.Respond(1, string(windowsOutput(t, "wsl-not-installed.txt"))))
	got, err := detectWindows(t.Context(), windowsProbe{
		runner:   r,
		registry: fakeRegistry{},
		token:    func() (bool, bool, error) { return true, true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Windows{Admin: true, Elevated: true, Virtualization: VirtualizationOn, Winget: "1.11.430"}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("detectWindows() = %+v, want %+v", *got, want)
	}
}

func TestDetectWindowsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := detectWindows(ctx, windowsProbe{
		runner:   systemtest.NewFakeRunner(t),
		registry: lxssRegistry,
		token:    func() (bool, bool, error) { return true, false, nil },
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("detectWindows() error = %v, want context.Canceled", err)
	}
}

func TestWindowsDistro(t *testing.T) {
	w := &Windows{Distros: lxssDistros}
	if d, ok := w.Distro("FEDORA-dev"); !ok || d.ID != lxssDistros[0].ID || !d.Installed() {
		t.Errorf("Distro(FEDORA-dev) = %+v, %t; want fedora-dev, which is installed", d, ok)
	}
	if d, ok := w.Distro("ubuntu-scratch"); !ok || d.Installed() {
		t.Errorf("Distro(ubuntu-scratch) = %+v, %t; want one that's still being installed", d, ok)
	}
	if _, ok := w.Distro("debian"); ok {
		t.Error("Distro(debian) found one")
	}
}

func TestParseWSLVersion(t *testing.T) {
	tests := []struct {
		name string
		out  []byte
		want string
	}{
		{"UTF-8", windowsOutput(t, "wsl-version.txt"), "2.6.1.0"},
		{"UTF-16", windowsOutput(t, "wsl-version-utf16.txt"), "2.6.1.0"},
		{"UTF-16 with a byte order mark, in German", windowsOutput(t, "wsl-version-de-bom.txt"), "2.5.10.0"},
		{"WSL isn't installed", windowsOutput(t, "wsl-not-installed.txt"), ""},
		{"LF line endings", []byte("WSL version: 2.4.13.0\nKernel version: 5.15.167.4-1\n"), "2.4.13.0"},
		{"leading blank lines", []byte("\r\n\r\nWSL version: 2.4.13.0\r\n"), "2.4.13.0"},
		{"nothing", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseWSLVersion(tt.out); got != tt.want {
				t.Errorf("ParseWSLVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDecodeWSLOutput(t *testing.T) {
	const text = "Windows Subsystem for Linux — Ubuntu (Standard)\r\n"
	utf16 := func(s string, bom bool) []byte {
		var b []byte
		if bom {
			b = []byte{0xff, 0xfe}
		}
		for _, r := range s {
			if r > 0xffff {
				panic("not in the BMP")
			}
			b = append(b, byte(r), byte(r>>8))
		}
		return b
	}
	want := "Windows Subsystem for Linux — Ubuntu (Standard)\n"
	tests := []struct {
		name string
		out  []byte
	}{
		{"UTF-8", []byte(text)},
		{"UTF-16", utf16(text, false)},
		{"UTF-16 with a byte order mark", utf16(text, true)},
		{"UTF-16 with an odd byte at the end", append(utf16(text, false), 'x')},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecodeWSLOutput(tt.out); got != want {
				t.Errorf("DecodeWSLOutput() = %q, want %q", got, want)
			}
		})
	}
}

func TestParseVirtualization(t *testing.T) {
	tests := []struct {
		name    string
		out     []byte
		want    Virtualization
		wantErr bool
	}{
		{name: "a hypervisor is running", out: windowsOutput(t, "virtualization-hypervisor.json"), want: VirtualizationOn},
		{name: "turned on in the firmware", out: windowsOutput(t, "virtualization-firmware.json"), want: VirtualizationOn},
		{name: "turned off", out: windowsOutput(t, "virtualization-off.json"), want: VirtualizationOff},
		{name: "missing a field", out: []byte(`{"HypervisorPresent":false}`), wantErr: true},
		{name: "not JSON", out: []byte("Get-CimInstance : Invalid class\r\n"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVirtualization(tt.out)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("ParseVirtualization() = %q, %v; want %q, error: %t", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestParseWingetVersion(t *testing.T) {
	tests := map[string]string{
		"v1.11.430\r\n":         "1.11.430",
		"v1.6.3133":             "1.6.3133",
		"1.12.35-preview\n":     "1.12.35",
		"Failed to initialize":  "",
		"":                      "",
		"v\r\n":                 "",
		"   v1.9.25200  \r\n\n": "1.9.25200",
	}
	for out, want := range tests {
		if got := ParseWingetVersion([]byte(out)); got != want {
			t.Errorf("ParseWingetVersion(%q) = %q, want %q", out, got, want)
		}
	}
}

func TestCheckWindowsHost(t *testing.T) {
	ready := Windows{Admin: true, Virtualization: VirtualizationOn, Winget: "1.11.430"}
	host := func(build string, w Windows) *System {
		return &System{
			OS: config.Windows, OSID: "windows", OSVersion: build, PrettyName: "Windows", Arch: config.AMD64, RawArch: "amd64",
			Windows: &w,
		}
	}
	tests := []struct {
		name   string
		system *System
		want   []string // the start of each problem
	}{
		{name: "ready", system: host("26100", ready)},
		{name: "ready, unelevated, as an admin", system: host("22000", Windows{Admin: true, Virtualization: VirtualizationOn, Winget: "1.6.3133"})},
		{name: "virtualization unknown", system: host("26100", Windows{Admin: true, Winget: "1.11.430"})},
		{name: "Linux", system: &System{OS: config.Fedora, OSVersion: "44"}},
		{name: "Windows 10", system: host("19045", ready), want: []string{"maisys needs Windows 11 (build 22000 or newer), but this is Windows 10 (build 19045): upgrade"}},
		{name: "an unknown build", system: host("", ready), want: []string{"maisys needs Windows 11 (build 22000 or newer), but this is Windows: upgrade"}},
		{
			name:   "nothing's ready",
			system: host("26100", Windows{Virtualization: VirtualizationOff}),
			want: []string{
				"virtualization is turned off in this PC's firmware, and WSL 2 needs it: restart into the BIOS or UEFI settings",
				"winget isn't installed, and maisys installs Windows software with it: install App Installer from the Microsoft Store",
				"you aren't a local admin (a member of the Administrators group), and installing WSL needs admin rights",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.system.CheckWindowsHost()
			var got []string
			if err != nil {
				got = strings.Split(err.Error(), "\n")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("CheckWindowsHost() = %v, want %d problems", err, len(tt.want))
			}
			for i, want := range tt.want {
				if !strings.HasPrefix(got[i], want) || !strings.HasSuffix(got[i], "run maisys sync again") {
					t.Errorf("problem %d = %q, want it to start with %q and say to run maisys sync again", i+1, got[i], want)
				}
			}
		})
	}

	if err := (&System{OS: config.Windows, OSVersion: "26100"}).CheckWindowsHost(); err == nil {
		t.Error("CheckWindowsHost() before the details are detected: no error")
	}
}
