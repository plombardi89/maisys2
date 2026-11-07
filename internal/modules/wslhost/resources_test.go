package wslhost_test

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/wslhost"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
	"github.com/plombardi89/maisys2/internal/settings"
)

// hostRepo is a config repo with a Windows host and its distros'
// machines.
var hostRepo = fstest.MapFS{
	"profiles/windows.toml": {Data: []byte(`os = "windows"

[wsl_host.wslconfig]
memory = "16GB"
`)},
	"machines/fedora-dev.toml": {Data: []byte(`os = "fedora"
os_version = "44"

[wsl]
enabled = true
append_windows_path = false
git_credential_manager = true
`)},
	"machines/ubuntu-dev.toml": {Data: []byte("os = \"ubuntu\"\n")},
}

// loadHost loads machines/host.toml, which is machine, from hostRepo.
func loadHost(t *testing.T, machine string) (*config.Machine, fstest.MapFS) {
	t.Helper()
	files := maps.Clone(hostRepo)
	files["machines/host.toml"] = &fstest.MapFile{Data: []byte(machine)}
	m, err := (&config.Repo{FS: files, Modules: modules.All()}).LoadMachine("host")
	if err != nil {
		t.Fatal(err)
	}
	return m, files
}

func TestWSLConfigFile(t *testing.T) {
	const header = "# Written by maisys from wsl_host.wslconfig, which replaces any changes made here.\n" +
		"# Changes take effect once WSL restarts: run `wsl --shutdown`.\n"
	tests := []struct {
		name, machine, want string
	}{
		{
			name:    "no keys",
			machine: "os = \"windows\"\n\n[wsl_host]\nenabled = true\n",
		},
		{
			name:    "keys from a profile and the machine",
			machine: "os = \"windows\"\nprofiles = [\"windows\"]\n\n[wsl_host.wslconfig]\nnetworking_mode = \"mirrored\"\n",
			want:    header + "\n[wsl2]\nmemory=16GB\nnetworkingMode=mirrored\n",
		},
		{
			name: "every key, set to WSL's defaults",
			machine: `os = "windows"

[wsl_host.wslconfig]
dns_tunneling = false
auto_memory_reclaim = "dropcache"
swap = "0"
processors = 2
networking_mode = "nat"
memory = "8GB"
`,
			want: header + "\n[wsl2]\nmemory=8GB\nprocessors=2\nswap=0\nnetworkingMode=nat\ndnsTunneling=false\n" +
				"\n[experimental]\nautoMemoryReclaim=dropCache\n",
		},
		{
			name:    "only experimental",
			machine: "os = \"windows\"\n\n[wsl_host]\nwslconfig.auto_memory_reclaim = \"gradual\"\n",
			want:    header + "\n[experimental]\nautoMemoryReclaim=gradual\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := loadHost(t, tt.machine)
			cfg, ok := m.Modules["wsl_host"].(*wslhost.Config)
			if !ok {
				t.Fatalf("wsl_host's config is a %T", m.Modules["wsl_host"])
			}
			got := wslhost.WSLConfigFile(cfg, m)
			if string(got) != tt.want {
				t.Errorf("WSLConfigFile() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestDistroURL(t *testing.T) {
	tests := []struct {
		name, configURL, repoURL, repoDir string
		want, wantErr                     string
	}{
		{name: "config_url", configURL: "https://github.com/me/config.git", repoURL: "git@github.com:me/config.git", want: "https://github.com/me/config.git"},
		{name: "this machine's HTTPS URL", repoURL: "https://github.com/me/config.git", repoDir: `C:\Users\me\maisys-config`, want: "https://github.com/me/config.git"},
		{name: "a folder", repoURL: `D:\a\_temp\fixture`, repoDir: `C:\Users\me\maisys-config`, want: "/mnt/d/a/_temp/fixture"},
		{name: "a file URL", repoURL: "file:///D:/a/_temp/fixture", want: "/mnt/d/a/_temp/fixture"},
		{name: "no remote", repoDir: `C:\Users\me\maisys-config`, want: "/mnt/c/Users/me/maisys-config"},
		{name: "SSH", repoURL: "git@github.com:me/config.git", wantErr: "set wsl_host.config_url"},
		{name: "nothing", wantErr: "no remote and no folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := wslhost.DistroURL(&wslhost.Config{ConfigURL: tt.configURL}, tt.repoURL, tt.repoDir)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("DistroURL() = %q, %v; want an error containing %q", got, err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("DistroURL() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestResources(t *testing.T) {
	t.Setenv(wsl.ReleaseURLEnv, "file:///D:/a/maisys2/dist")
	m, files := loadHost(t, `os = "windows"
profiles = ["windows"]

[wsl_host]
enabled = true
default_distro = "fedora-dev"

[wsl_host.distros.ubuntu-dev]
image = "Ubuntu-26.04"
machine = "ubuntu-dev"
user = "tester"

[wsl_host.distros.fedora-dev]
image = "FedoraLinux-44"
machine = "fedora-dev"
user = "phil"
`)
	rt := &resource.Runtime{
		Machine: m,
		RepoFS:  files,
		Modules: modules.All(),
		RepoURL: "https://github.com/me/config.git",
		RepoDir: `C:\Users\phil\maisys-config`,
		Paths:   settings.Paths{Home: t.TempDir(), CacheDir: t.TempDir()},
	}
	rs, err := wslhost.Module{}.Resources(m.Modules["wsl_host"], rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	want := []string{"wsl", "file:~/.wslconfig", "wsl-distro:fedora-dev", "wsl-distro:ubuntu-dev", "wsl-default"}
	if !slices.Equal(ids, want) {
		t.Fatalf("resources = %q, want %q", ids, want)
	}

	fedora, ok1 := rs[2].(*wsl.Distro)
	ubuntu, ok2 := rs[3].(*wsl.Distro)
	if !ok1 || !ok2 {
		t.Fatalf("the distros are a %T and a %T", rs[2], rs[3])
	}
	if fedora.OS != config.Fedora || fedora.User != "phil" || fedora.Image != "FedoraLinux-44" || fedora.Machine != "fedora-dev" {
		t.Errorf("fedora-dev = %+v", fedora)
	}
	if fedora.ConfigURL != "https://github.com/me/config.git" || fedora.ReleaseURL != "file:///mnt/d/a/maisys2/dist" || fedora.Binary == nil {
		t.Errorf("fedora-dev clones %q, with release %q and binary %v", fedora.ConfigURL, fedora.ReleaseURL, fedora.Binary)
	}
	wantConf := "systemd=true\n\n[user]\ndefault=phil\n\n[interop]\nappendWindowsPath=false\n"
	if !strings.HasSuffix(string(fedora.WSLConf), wantConf) {
		t.Errorf("fedora-dev's wsl.conf =\n%s\nwant it to end with\n%s", fedora.WSLConf, wantConf)
	}
	// ubuntu-dev's machine doesn't turn the wsl module on: its defaults
	// apply.
	if !strings.Contains(string(ubuntu.WSLConf), "systemd=true\n\n[user]\ndefault=tester\n\n[interop]\nappendWindowsPath=true\n") || ubuntu.OS != config.Ubuntu {
		t.Errorf("ubuntu-dev's wsl.conf =\n%s", ubuntu.WSLConf)
	}
	if got := fedora.HelperValue("/mnt/c/Program Files/Git/ucrt64/bin/git-credential-manager.exe"); got != `/mnt/c/Program\ Files/Git/ucrt64/bin/git-credential-manager.exe` {
		t.Errorf("HelperValue() = %q, want the wsl module's", got)
	}

	// .wslconfig, with Windows' line endings, says how to apply it.
	wslconfig := rs[1]
	c, err := wslconfig.Check(t.Context(), rt)
	if err != nil || c.Action != resource.Create || c.Summary != "write ~\\.wslconfig (run `wsl --shutdown` to apply)" {
		t.Errorf("Check() of .wslconfig = %+v, %v", c, err)
	}
	if _, err := wslconfig.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(rt.Paths.Home, ".wslconfig"))
	if err != nil || !strings.HasSuffix(string(data), "\r\n[wsl2]\r\nmemory=16GB\r\n") {
		t.Errorf(".wslconfig = %q, %v", data, err)
	}
	if err := os.WriteFile(filepath.Join(rt.Paths.Home, ".wslconfig"), []byte("[wsl2]\nmemory=4GB\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = wslconfig.Check(t.Context(), rt)
	wantNote := "~\\.wslconfig takes effect once WSL restarts: run `wsl --shutdown` to apply it"
	if err != nil || c.Action != resource.Warn || !slices.Equal(c.Notes, []string{wantNote}) {
		t.Errorf("Check() of a .wslconfig changed by hand = %+v, %v; want a warning with the note %q", c, err, wantNote)
	}
}

// TestResourcesWithoutDistros checks that a host without .wslconfig keys
// or distros only installs WSL.
func TestResourcesWithoutDistros(t *testing.T) {
	m, files := loadHost(t, "os = \"windows\"\n\n[wsl_host]\nenabled = true\n")
	rt := &resource.Runtime{Machine: m, RepoFS: files, Modules: modules.All()}
	rs, err := wslhost.Module{}.Resources(m.Modules["wsl_host"], rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].ID() != "wsl" {
		t.Errorf("resources = %v, want only WSL", rs)
	}
}
