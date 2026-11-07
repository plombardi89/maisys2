package windowsterminal_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/windowsterminal"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/terminal"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
)

func TestResources(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(settings, []byte("{\n    // Terminal's settings.\n    \"defaultProfile\": \"{61c54bbd-c2c6-5271-96e7-009a87ff44bf}\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(terminal.SettingsEnv, settings)
	repo := &config.Repo{FS: fstest.MapFS{"machines/host.toml": {Data: []byte(`os = "windows"

[windows_terminal]
enabled = true

[wsl_host]
default_distro = "fedora-dev"
distros.fedora-dev = { image = "FedoraLinux-44", machine = "host", user = "phil" }
`)}}, Modules: modules.All()}
	m, err := repo.LoadMachine("host")
	if err != nil {
		t.Fatal(err)
	}

	// It's applied after wsl_host, but doesn't depend on all of it.
	if i, j := moduleIndex("wsl_host"), moduleIndex("windows_terminal"); i < 0 || j < i {
		t.Errorf("windows_terminal comes before wsl_host in modules.All()")
	}
	rt := &resource.Runtime{Machine: m}
	rt.SetShared(wsl.RegistryKey{}, registry{{ID: "{0a1b2c3d-4e5f-4071-8293-a4b5c6d7e8f9}", Name: "fedora-dev", Modern: true}})
	rs, err := windowsterminal.Module{}.Resources(m.Modules["windows_terminal"], rt)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 {
		t.Fatalf("resources = %v, want the one settings file", rs)
	}
	p, ok := rs[0].(*terminal.DefaultProfile)
	if !ok || p.Settings.Path != settings || p.Distro != "fedora-dev" {
		t.Fatalf("resource = %+v, want the default profile of %s set to fedora-dev's", rs[0], settings)
	}

	c, err := p.Check(t.Context(), rt)
	if err != nil || c.Action != resource.Update || c.Summary != "default profile → fedora-dev" {
		t.Errorf("Check() = %+v, %v; want ~ default profile → fedora-dev", c, err)
	}
	if _, err := p.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n    // Terminal's settings.\n    \"defaultProfile\": \"{2d71752b-afa5-5775-8684-445c330bed79}\"\n}\n"
	if string(data) != want {
		t.Errorf("settings =\n%s\nwant\n%s", data, want)
	}
}

// moduleIndex returns where modules.All() lists the module called name.
func moduleIndex(name string) int {
	return slices.IndexFunc(modules.All(), func(m config.Module) bool { return m.Info().Name == name })
}

// registry is a wsl.Registry with distros.
type registry []wsl.Registered

func (r registry) Distros() ([]wsl.Registered, error) { return r, nil }
func (registry) DefaultDistro() (string, error)       { return "", nil }
func (registry) Version() (string, error)             { return "2.6.1.0", nil }
func (registry) RebootPending() (bool, error)         { return false, nil }
func (registry) GitDir() (string, error)              { return "", nil }
