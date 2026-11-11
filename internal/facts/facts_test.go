package facts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/plombardi89/maisys2/internal/config"
)

// fakeFiles returns a readFile function that serves files from a map.
func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		data, ok := files[name]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		return []byte(data), nil
	}
}

// fakeEnv returns a getenv function that serves variables from a map.
func fakeEnv(env map[string]string) func(string) string {
	return func(key string) string { return env[key] }
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "os-release", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDetectLinuxDistributions(t *testing.T) {
	tests := []struct {
		fixture string
		want    System
	}{
		{"fedora-43", System{OS: config.Fedora, OSID: "fedora", OSVersion: "43", PrettyName: "Fedora Linux 43 (WSL)"}},
		{"fedora-44", System{OS: config.Fedora, OSID: "fedora", OSVersion: "44", PrettyName: "Fedora Linux 44 (Container Image)"}},
		{"fedora-45", System{OS: config.Fedora, OSID: "fedora", OSVersion: "45", PrettyName: "Fedora Linux 45 (Workstation Edition)"}},
		{"ubuntu-24.04", System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "24.04", PrettyName: "Ubuntu 24.04.3 LTS"}},
		{"ubuntu-26.04", System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", PrettyName: "Ubuntu 26.04.1 LTS"}},
		{"debian-13", System{OSID: "debian", OSVersion: "13", PrettyName: "Debian GNU/Linux 13 (trixie)"}},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			files := map[string]string{
				"/etc/os-release":            fixture(t, tt.fixture),
				"/usr/lib/os-release":        "ID=ignored-when-etc-exists\n",
				"/proc/sys/kernel/osrelease": "6.17.7-300.fc43.x86_64\n",
			}
			got, err := DetectLinux(fakeFiles(files), fakeEnv(nil))
			if err != nil {
				t.Fatalf("DetectLinux: %v", err)
			}
			if *got != tt.want {
				t.Errorf("DetectLinux = %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestDetectLinuxOSRelease(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		want    System
		wantErr string
	}{
		{
			name:  "falls back to /usr/lib/os-release",
			files: map[string]string{"/usr/lib/os-release": "ID=ubuntu\nVERSION_ID=\"26.04\"\nPRETTY_NAME=\"Ubuntu 26.04 LTS\"\n"},
			want:  System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", PrettyName: "Ubuntu 26.04 LTS"},
		},
		{
			name:  "defaults from os-release(5)",
			files: map[string]string{"/etc/os-release": "VERSION_ID=1\n"},
			want:  System{OSID: "linux", OSVersion: "1", PrettyName: "Linux 1"},
		},
		{
			name:  "name when there's no pretty name",
			files: map[string]string{"/etc/os-release": "NAME=Fedora\nID=fedora\nVERSION_ID=44\n"},
			want:  System{OS: config.Fedora, OSID: "fedora", OSVersion: "44", PrettyName: "Fedora 44"},
		},
		{
			name:  "IDs are case-sensitive",
			files: map[string]string{"/etc/os-release": "ID=Fedora\n"},
			want:  System{OSID: "Fedora", PrettyName: "Linux"},
		},
		{
			name:    "no os-release",
			files:   map[string]string{},
			wantErr: "neither /etc/os-release nor /usr/lib/os-release exists",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DetectLinux(fakeFiles(tt.files), fakeEnv(nil))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("DetectLinux error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectLinux: %v", err)
			}
			if *got != tt.want {
				t.Errorf("DetectLinux = %+v, want %+v", *got, tt.want)
			}
		})
	}
}

func TestDetectLinuxReadError(t *testing.T) {
	denied := errors.New("permission denied")
	readFile := func(name string) ([]byte, error) {
		if name == "/etc/os-release" {
			return nil, &fs.PathError{Op: "open", Path: name, Err: denied}
		}
		return []byte("ID=fedora\n"), nil
	}
	_, err := DetectLinux(readFile, fakeEnv(nil))
	if !errors.Is(err, denied) {
		t.Fatalf("DetectLinux error = %v, want %v", err, denied)
	}
	if !strings.Contains(err.Error(), "/etc/os-release") {
		t.Errorf("error %q doesn't name the file", err)
	}
}

func TestDetectLinuxWSL(t *testing.T) {
	tests := []struct {
		name       string
		kernel     string // "" for no kernel release file
		env        map[string]string
		wantWSL    bool
		wantDistro string
	}{
		{name: "WSL 2", kernel: "6.6.87.2-microsoft-standard-WSL2\n", wantWSL: true},
		{name: "WSL 1", kernel: "4.4.0-26100-Microsoft\n", wantWSL: true},
		{
			name:       "WSL with a distro name",
			kernel:     "6.6.87.2-microsoft-standard-WSL2\n",
			env:        map[string]string{"WSL_DISTRO_NAME": "fedora-dev"},
			wantWSL:    true,
			wantDistro: "fedora-dev",
		},
		{
			name:       "distro name only",
			env:        map[string]string{"WSL_DISTRO_NAME": "Ubuntu-26.04"},
			wantWSL:    true,
			wantDistro: "Ubuntu-26.04",
		},
		{name: "not WSL", kernel: "6.17.7-300.fc43.x86_64\n"},
		{name: "no kernel release"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"/etc/os-release": fixture(t, "fedora-43")}
			if tt.kernel != "" {
				files["/proc/sys/kernel/osrelease"] = tt.kernel
			}
			got, err := DetectLinux(fakeFiles(files), fakeEnv(tt.env))
			if err != nil {
				t.Fatalf("DetectLinux: %v", err)
			}
			if got.WSL != tt.wantWSL || got.WSLDistro != tt.wantDistro {
				t.Errorf("WSL, WSLDistro = %v, %q, want %v, %q", got.WSL, got.WSLDistro, tt.wantWSL, tt.wantDistro)
			}
		})
	}
}

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "comments and blank lines",
			input: "# a comment\n\nID=fedora\n   # indented comment\n\t\nVERSION_ID=44\n",
			want:  map[string]string{"ID": "fedora", "VERSION_ID": "44"},
		},
		{
			name:  "quotes",
			input: "A=plain\nB=\"double quoted\"\nC='single quoted'\nD=\nE=\"\"\nF=''\n",
			want:  map[string]string{"A": "plain", "B": "double quoted", "C": "single quoted", "D": "", "E": "", "F": ""},
		},
		{
			name:  "escapes in double quotes",
			input: `A="say \"hi\" \$HOME \\ \` + "`" + `"` + "\n" + `B="C:\path\to"` + "\n",
			want:  map[string]string{"A": "say \"hi\" $HOME \\ `", "B": `C:\path\to`},
		},
		{
			name:  "no escapes in single quotes",
			input: `A='a\b \"c\"'` + "\n",
			want:  map[string]string{"A": `a\b \"c\"`},
		},
		{
			name:  "escapes outside quotes",
			input: `A=two\ words` + "\n" + `B=\"x\"` + "\n" + `C=trailing\` + "\n",
			want:  map[string]string{"A": "two words", "B": `"x"`, "C": `trailing\`},
		},
		{
			name:  "quoted parts run together",
			input: `A="a b"'c d'e` + "\n",
			want:  map[string]string{"A": "a bc de"},
		},
		{
			name:  "quotes inside the other kind",
			input: `A="it's"` + "\n" + `B='say "hi"'` + "\n",
			want:  map[string]string{"A": "it's", "B": `say "hi"`},
		},
		{
			name:  "unterminated quote",
			input: "A=\"no end\nB=ok\n",
			want:  map[string]string{"A": "no end", "B": "ok"},
		},
		{
			name:  "whitespace and CRLF",
			input: "  ID=fedora  \r\nNAME = \"Fedora Linux\"\r\n",
			want:  map[string]string{"ID": "fedora", "NAME": "Fedora Linux"},
		},
		{
			name:  "later lines win",
			input: "ID=one\nID=two\n",
			want:  map[string]string{"ID": "two"},
		},
		{
			name:  "lines that aren't assignments",
			input: "just words\n=value\n1ID=x\nMY ID=x\nexport ID=x\nID=ok\n",
			want:  map[string]string{"ID": "ok"},
		},
		{
			name:  "equals signs in the value",
			input: "A=b=c\n",
			want:  map[string]string{"A": "b=c"},
		},
		{
			name:  "no final newline",
			input: "ID=fedora",
			want:  map[string]string{"ID": "fedora"},
		},
		{
			name:  "empty",
			input: "",
			want:  map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOSRelease(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("ParseOSRelease: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseOSRelease = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseOSReleaseReadError(t *testing.T) {
	broken := errors.New("broken")
	if _, err := ParseOSRelease(iotest.ErrReader(broken)); !errors.Is(err, broken) {
		t.Errorf("ParseOSRelease error = %v, want %v", err, broken)
	}
}

func TestArchOf(t *testing.T) {
	tests := map[string]config.Arch{
		"amd64":   config.AMD64,
		"arm64":   config.ARM64,
		"386":     "",
		"arm":     "",
		"riscv64": "",
		"":        "",
	}
	for goarch, want := range tests {
		if got := archOf(goarch); got != want {
			t.Errorf("archOf(%q) = %q, want %q", goarch, got, want)
		}
	}
}

func TestWindowsName(t *testing.T) {
	tests := map[uint32]string{
		19045: "Windows 10 (build 19045)",
		21999: "Windows 10 (build 21999)",
		22000: "Windows 11 (build 22000)",
		26100: "Windows 11 (build 26100)",
	}
	for build, want := range tests {
		if got := windowsName(build); got != want {
			t.Errorf("windowsName(%d) = %q, want %q", build, got, want)
		}
	}
}

// TestDetect checks detection on the system running the tests.
func TestDetect(t *testing.T) {
	s, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if s.RawArch != runtime.GOARCH || s.Arch != archOf(runtime.GOARCH) {
		t.Errorf("Arch, RawArch = %q, %q, want %q, %q", s.Arch, s.RawArch, archOf(runtime.GOARCH), runtime.GOARCH)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if s.Hostname != host {
		t.Errorf("Hostname = %q, want %q", s.Hostname, host)
	}
	if s.OSID == "" || s.PrettyName == "" {
		t.Errorf("OSID, PrettyName = %q, %q, want both set", s.OSID, s.PrettyName)
	}
	if runtime.GOOS == "windows" {
		if s.OS != config.Windows || s.OSID != "windows" {
			t.Errorf("OS, OSID = %q, %q, want windows", s.OS, s.OSID)
		}
		if build, err := strconv.Atoi(s.OSVersion); err != nil || build <= 0 {
			t.Errorf("OSVersion = %q, want a build number", s.OSVersion)
		}
		if s.WSL {
			t.Error("WSL = true on Windows")
		}
	}
}
