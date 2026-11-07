package facts_test

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules"
)

var (
	fedora44 = facts.System{
		OS: config.Fedora, OSID: "fedora", OSVersion: "44", PrettyName: "Fedora Linux 44 (Workstation Edition)",
		Arch: config.AMD64, RawArch: "amd64",
	}
	ubuntu2604 = facts.System{
		OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", PrettyName: "Ubuntu 26.04.1 LTS",
		Arch: config.AMD64, RawArch: "amd64",
	}
	windows11 = facts.System{
		OS: config.Windows, OSID: "windows", OSVersion: "26100", PrettyName: "Windows 11 (build 26100)",
		Arch: config.AMD64, RawArch: "amd64",
	}
)

// on returns sys on another architecture.
func on(sys facts.System, arch config.Arch, raw string) facts.System {
	sys.Arch, sys.RawArch = arch, raw
	return sys
}

// version returns sys at another OS version.
func version(sys facts.System, v, pretty string) facts.System {
	sys.OSVersion, sys.PrettyName = v, pretty
	return sys
}

func TestCheckMachine(t *testing.T) {
	debian := facts.System{
		OSID: "debian", OSVersion: "13", PrettyName: "Debian GNU/Linux 13 (trixie)",
		Arch: config.AMD64, RawArch: "amd64",
	}
	tests := []struct {
		name    string
		machine string // machines/box.toml
		system  facts.System
		want    []string
	}{
		{
			name:    "the same OS",
			machine: `os = "fedora"`,
			system:  fedora44,
		},
		{
			name:    "the same OS, version and arch",
			machine: "os = \"fedora\"\nos_version = \"44\"\narch = \"arm64\"\n",
			system:  on(fedora44, config.ARM64, "arm64"),
		},
		{
			name:    "a different OS",
			machine: `os = "fedora"`,
			system:  ubuntu2604,
			want:    []string{`machines/box.toml:1:6: machine "box" is for Fedora, but it's running on Ubuntu 26.04`},
		},
		{
			name:    "a different OS and version",
			machine: "os = \"fedora\"\nos_version = \"44\"\n",
			system:  ubuntu2604,
			want:    []string{`machines/box.toml:1:6: machine "box" is for Fedora 44, but it's running on Ubuntu 26.04`},
		},
		{
			name:    "a different version",
			machine: "os = \"fedora\"\nos_version = \"44\"\n",
			system:  version(fedora44, "43", "Fedora Linux 43 (WSL)"),
			want:    []string{`machines/box.toml:2:14: machine "box" is for Fedora 44, but it's running on Fedora 43`},
		},
		{
			name:    "a different Windows build",
			machine: "os = \"windows\"\nos_version = \"26100\"\n",
			system:  version(windows11, "22631", "Windows 11 (build 22631)"),
			want:    []string{`machines/box.toml:2:14: machine "box" is for Windows 11 (build 26100), but it's running on Windows 11 (build 22631)`},
		},
		{
			name:    "Windows 10 is only a warning",
			machine: `os = "windows"`,
			system:  version(windows11, "19045", "Windows 10 (build 19045)"),
		},
		{
			name:    "an OS maisys doesn't run on",
			machine: `os = "ubuntu"`,
			system:  debian,
			want:    []string{`machines/box.toml:1:6: machine "box" is for Ubuntu, but it's running on Debian GNU/Linux 13 (trixie): maisys runs on Fedora, Ubuntu and Windows 11`},
		},
		{
			name:    "a different arch",
			machine: "os = \"fedora\"\narch = \"amd64\"\n",
			system:  on(fedora44, config.ARM64, "arm64"),
			want:    []string{`machines/box.toml:2:8: machine "box" is for amd64, but it's running on arm64`},
		},
		{
			name:    "an arch maisys doesn't run on",
			machine: `os = "fedora"`,
			system:  on(fedora44, "", "riscv64"),
			want:    []string{`machines/box.toml: maisys runs on amd64 and arm64, but machine "box" is running on riscv64`},
		},
		{
			name:    "an arch maisys doesn't run on, when the machine sets one",
			machine: "os = \"fedora\"\narch = \"arm64\"\n",
			system:  on(fedora44, "", "riscv64"),
			want:    []string{`machines/box.toml:2:8: maisys runs on amd64 and arm64, but machine "box" is running on riscv64`},
		},
		{
			name:    "Windows on arm64",
			machine: `os = "windows"`,
			system:  on(windows11, config.ARM64, "arm64"),
			want:    []string{`machines/box.toml:1:6: Windows hosts must be amd64, but machine "box" is running on arm64`},
		},
		{
			name:    "a module that doesn't support the arch",
			machine: "os = \"fedora\"\nprofiles = [\"gaming\"]\n",
			system:  on(fedora44, config.ARM64, "arm64"),
			want:    []string{`profiles/gaming.toml:2:11: steam only works on amd64, but machine "box" is running on arm64`},
		},
		{
			name:    "a module that supports the arch",
			machine: "os = \"fedora\"\nprofiles = [\"gaming\"]\n",
			system:  fedora44,
		},
		{
			name:    "a module that doesn't support the arch, turned off",
			machine: "os = \"fedora\"\nprofiles = [\"gaming\"]\n\n[steam]\nenabled = false\n",
			system:  on(fedora44, config.ARM64, "arm64"),
		},
		{
			// Validation reports modules that don't support the arch the
			// machine sets.
			name:    "a module that doesn't support the arch the machine sets",
			machine: "os = \"fedora\"\narch = \"amd64\"\nprofiles = [\"gaming\"]\n",
			system:  on(fedora44, config.ARM64, "arm64"),
			want:    []string{`machines/box.toml:2:8: machine "box" is for amd64, but it's running on arm64`},
		},
		{
			name:    "several problems",
			machine: "os = \"ubuntu\"\nos_version = \"24.04\"\nprofiles = [\"gaming\"]\n",
			system:  on(fedora44, config.ARM64, "arm64"),
			want: []string{
				`machines/box.toml:1:6: machine "box" is for Ubuntu 24.04, but it's running on Fedora 44`,
				`profiles/gaming.toml:2:11: steam only works on amd64, but machine "box" is running on arm64`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &config.Repo{
				FS: fstest.MapFS{
					"machines/box.toml":    {Data: []byte(tt.machine)},
					"profiles/gaming.toml": {Data: []byte("[steam]\nenabled = true\n")},
				},
				Modules: modules.All(),
			}
			m, err := repo.Validate("box")
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			err = tt.system.CheckMachine(m, repo.Modules)
			var got []string
			if err != nil {
				got = strings.Split(err.Error(), "\n")
			}
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("CheckMachine errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestCheckUser(t *testing.T) {
	user := fedora44
	if err := user.CheckUser(); err != nil {
		t.Errorf("CheckUser as a user = %v, want nil", err)
	}
	root := fedora44
	root.Root = true
	err := root.CheckUser()
	if !errors.Is(err, facts.ErrRoot) {
		t.Fatalf("CheckUser as root = %v, want %v", err, facts.ErrRoot)
	}
	if want := "run maisys as your normal user, not root; it uses sudo when it needs to"; err.Error() != want {
		t.Errorf("CheckUser as root = %q, want %q", err, want)
	}
}
