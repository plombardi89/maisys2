package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
)

// Test modules covering each merge rule.

type golangConfig struct {
	config.Toggle
	Version string   `toml:"version"`
	Tools   []string `toml:"tools" merge:"union"`
	Flags   []string `toml:"flags"`
}

type filesConfig struct {
	config.Toggle
	Vars   map[string]string   `toml:"vars"`
	Items  map[string]fileItem `toml:"items"`
	Nested struct {
		A string `toml:"a"`
		B string `toml:"b"`
	} `toml:"nested"`
}

type fileItem struct {
	Enabled bool   `toml:"enabled"`
	Source  string `toml:"source"`
	Target  string `toml:"target"`
}

func (i *fileItem) SetDefaults() {
	i.Enabled = true
}

func testModule(name string, newConfig func() config.ModuleConfig) config.Module {
	return config.Module{Name: name, OS: []config.OS{config.Fedora}, NewConfig: newConfig}
}

var testModules = []config.Module{
	testModule("golang", func() config.ModuleConfig {
		return &golangConfig{Version: "latest", Tools: []string{"default-tool"}}
	}),
	testModule("files", func() config.ModuleConfig { return &filesConfig{} }),
}

func load(t *testing.T, files fstest.MapFS, machine string) (*config.Machine, error) {
	t.Helper()
	repo := &config.Repo{FS: files, Modules: testModules}
	return repo.LoadMachine(machine)
}

// mustLoad loads machine "box" from files.
func mustLoad(t *testing.T, files fstest.MapFS) *config.Machine {
	t.Helper()
	m, err := load(t, files, "box")
	if err != nil {
		t.Fatalf("LoadMachine: %v", err)
	}
	return m
}

func file(s string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(strings.TrimPrefix(s, "\n"))}
}

func golang(m *config.Machine) *golangConfig {
	c, _ := m.Modules["golang"].(*golangConfig)
	return c
}

func files(m *config.Machine) *filesConfig {
	c, _ := m.Modules["files"].(*filesConfig)
	return c
}

func TestLoadMachineReadsItsOwnKeys(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{
		"machines/box.toml": file(`
os = "ubuntu"
os_version = "26.04"
arch = "arm64"
profiles = ["base"]
`),
		"profiles/base.toml": file(`os = "ubuntu"`),
	})

	if m.Name != "box" || m.OS != config.Ubuntu || m.OSVersion != "26.04" || m.Arch != config.ARM64 {
		t.Errorf("machine = %+v", m)
	}
	if !reflect.DeepEqual(m.Profiles, []string{"base"}) {
		t.Errorf("Profiles = %v", m.Profiles)
	}
	if m.ProfileOS["base"] != config.Ubuntu {
		t.Errorf("ProfileOS = %v", m.ProfileOS)
	}
}

func TestModulesNoFileMentionsKeepTheirDefaults(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{"machines/box.toml": file(`os = "fedora"`)})

	g := golang(m)
	if g.IsEnabled() || g.Version != "latest" || !reflect.DeepEqual(g.Tools, []string{"default-tool"}) {
		t.Errorf("golang = %+v, want the defaults", g)
	}
	if files(m) == nil {
		t.Error("files has no config")
	}
	if len(m.Origins) != 0 {
		t.Errorf("Origins = %v, want none", m.Origins)
	}
}

func TestLaterLayersWin(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{
		"machines/box.toml": file(`
os = "fedora"
profiles = ["base", "dev"]

[golang]
flags = ["-machine"]
`),
		"profiles/base.toml": file(`
[golang]
enabled = true
version = "1.26.0"
flags = ["-base"]
`),
		"profiles/dev.toml": file(`
[golang]
version = "1.27.3"
`),
	})

	g := golang(m)
	if !g.IsEnabled() {
		t.Error("golang isn't enabled, though base turns it on")
	}
	if g.Version != "1.27.3" {
		t.Errorf("version = %q, want dev's 1.27.3", g.Version)
	}
	if !reflect.DeepEqual(g.Flags, []string{"-machine"}) {
		t.Errorf("flags = %v, want the machine's list to replace base's", g.Flags)
	}
	assertOrigins(t, m, "golang.version", "profiles/dev.toml")
	assertOrigins(t, m, "golang.enabled", "profiles/base.toml")
	assertOrigins(t, m, "golang.flags", "machines/box.toml")
}

func TestUnionListsCombine(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{
		"machines/box.toml": file(`
os = "fedora"
profiles = ["base"]

[golang]
tools = ["task", "gofumpt", "task"]
`),
		"profiles/base.toml": file(`
[golang]
tools = ["goimports", "default-tool"]
`),
	})

	want := []string{"default-tool", "goimports", "task", "gofumpt"}
	if got := golang(m).Tools; !reflect.DeepEqual(got, want) {
		t.Errorf("tools = %v, want %v (defaults, then each layer's new items, without duplicates)", got, want)
	}
	assertOrigins(t, m, "golang.tools", "profiles/base.toml", "machines/box.toml")
}

func TestTablesMergeKeyByKey(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{
		"machines/box.toml": file(`
os = "fedora"
profiles = ["base"]

[files.nested]
b = "machine"

[files.vars]
email = "me@example.com"

[files.items.nvim]
enabled = false

[files.items.gitconfig]
source = "files/gitconfig"
target = "~/.gitconfig"
`),
		"profiles/base.toml": file(`
[files.nested]
a = "base"
b = "base"

[files.vars]
email = "base@example.com"
editor = "nvim"

[files.items.nvim]
source = "files/nvim/"
target = "~/.config/nvim/"
`),
	})

	f := files(m)
	if f.Nested.A != "base" || f.Nested.B != "machine" {
		t.Errorf("nested = %+v, want a from base and b from the machine", f.Nested)
	}
	wantVars := map[string]string{"email": "me@example.com", "editor": "nvim"}
	if !reflect.DeepEqual(f.Vars, wantVars) {
		t.Errorf("vars = %v, want %v", f.Vars, wantVars)
	}
	wantItems := map[string]fileItem{
		// The machine turns off one inherited item without restating it.
		"nvim": {Enabled: false, Source: "files/nvim/", Target: "~/.config/nvim/"},
		// New items start from their defaults (enabled).
		"gitconfig": {Enabled: true, Source: "files/gitconfig", Target: "~/.gitconfig"},
	}
	if !reflect.DeepEqual(f.Items, wantItems) {
		t.Errorf("items = %+v, want %+v", f.Items, wantItems)
	}
	assertOrigins(t, m, "files.nested.a", "profiles/base.toml")
	assertOrigins(t, m, "files.nested.b", "machines/box.toml")
	assertOrigins(t, m, "files.vars.editor", "profiles/base.toml")
	assertOrigins(t, m, "files.items.nvim.enabled", "machines/box.toml")
	assertOrigins(t, m, "files.items.nvim.source", "profiles/base.toml")
}

func TestLoadMachineErrors(t *testing.T) {
	tests := []struct {
		name    string
		machine string
		files   fstest.MapFS
		want    []string
	}{
		{
			name:    "no machine file",
			machine: "box",
			files:   fstest.MapFS{},
			want:    []string{"machines/box.toml doesn't exist"},
		},
		{
			name:    "invalid machine name",
			machine: "../box",
			files:   fstest.MapFS{},
			want:    []string{`invalid machine name "../box"`},
		},
		{
			name:    "missing profile",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file(`profiles = ["nope"]`)},
			want:    []string{"profiles/nope.toml doesn't exist"},
		},
		{
			name:    "profile listed twice",
			machine: "box",
			files: fstest.MapFS{
				"machines/box.toml":  file(`profiles = ["base", "base"]`),
				"profiles/base.toml": file(``),
			},
			want: []string{`machines/box.toml: profile "base" is listed more than once`},
		},
		{
			name:    "unknown keys, reported with their positions",
			machine: "box",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"
colour = "blue"

[golang]
versions = "1.27"

[nope]
enabled = true
`)},
			want: []string{
				"machines/box.toml:2:1: unknown key colour",
				"machines/box.toml:5:1: unknown key golang.versions",
				"machines/box.toml:7:2: unknown key nope",
			},
		},
		{
			name:    "machine-only keys in a profile",
			machine: "box",
			files: fstest.MapFS{
				"machines/box.toml":  file(`profiles = ["base"]`),
				"profiles/base.toml": file(`os_version = "44"`),
			},
			want: []string{"profiles/base.toml:1:1: unknown key os_version"},
		},
		{
			name:    "a number where a string is expected",
			machine: "box",
			files: fstest.MapFS{"machines/box.toml": file(`
[golang]
version = 1.20
`)},
			want: []string{"machines/box.toml:2:11: golang.version must be a string in quotes, not a TOML float"},
		},
		{
			name:    "a string where a list is expected",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file("[golang]\ntools = \"task\"\n")},
			want:    []string{"machines/box.toml:2:9: golang.tools must be a list of strings, not a TOML string"},
		},
		{
			name:    "a string where true or false is expected",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file("[golang]\nenabled = \"yes\"\n")},
			want:    []string{"machines/box.toml:2:11: golang.enabled must be true or false, not a TOML string"},
		},
		{
			name:    "the wrong type in a table keyed by name",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file("[files.items.nvim]\nenabled = 3\n")},
			want:    []string{"machines/box.toml:2:11: files.items.nvim.enabled must be true or false, not a TOML integer"},
		},
		{
			name:    "invalid TOML",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file(`os = `)},
			want:    []string{"machines/box.toml:1:"},
		},
		{
			name:    "a module set to a plain value",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file(`golang = "1.27"`)},
			want:    []string{"golang must be a table, not a TOML string"},
		},
		{
			name:    "keys in the wrong case, which go-toml would accept and merging would drop",
			machine: "box",
			files: fstest.MapFS{"machines/box.toml": file(`
OS = "fedora"

[golang]
Enabled = true
Version = "1.27"

[Files]
enabled = true
`)},
			want: []string{
				"machines/box.toml:1:1: unknown key OS (keys are case-sensitive: did you mean os?)",
				"machines/box.toml:4:1: unknown key golang.Enabled (keys are case-sensitive: did you mean golang.enabled?)",
				"machines/box.toml:5:1: unknown key golang.Version (keys are case-sensitive: did you mean golang.version?)",
				"machines/box.toml:7:2: unknown key Files (keys are case-sensitive: did you mean files?)",
			},
		},
		{
			name:    "the wrong case in dotted keys and inline tables",
			machine: "box",
			files: fstest.MapFS{"machines/box.toml": file(`
golang.Version = "1.27"
files = { Enabled = true }
`)},
			want: []string{
				"machines/box.toml:1:8: unknown key golang.Version (keys are case-sensitive: did you mean golang.version?)",
				"machines/box.toml:2:11: unknown key files.Enabled (keys are case-sensitive: did you mean files.enabled?)",
			},
		},
		{
			name:    "the wrong case in a table keyed by name, in a profile",
			machine: "box",
			files: fstest.MapFS{
				"machines/box.toml": file(`profiles = ["base"]`),
				"profiles/base.toml": file(`
[files.items.nvim]
Source = "files/nvim/"
`),
			},
			want: []string{
				"profiles/base.toml:2:1: unknown key files.items.nvim.Source (keys are case-sensitive: did you mean files.items.nvim.source?)",
			},
		},
		{
			name:    "problems in several profiles are all reported",
			machine: "box",
			files: fstest.MapFS{
				"machines/box.toml": file(`profiles = ["a", "b", "missing"]`),
				"profiles/a.toml":   file("[golang]\nversions = \"1\"\n"),
				"profiles/b.toml":   file("[files]\ntypo = true\n"),
			},
			want: []string{
				"profiles/a.toml:2:1: unknown key golang.versions",
				"profiles/b.toml:2:1: unknown key files.typo",
				"profiles/missing.toml doesn't exist",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.files, tt.machine)
			if err == nil {
				t.Fatal("LoadMachine succeeded, want an error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error doesn't contain %q:\n%v", want, err)
				}
			}
		})
	}
}

func TestUnknownKeyErrorsAreMarked(t *testing.T) {
	for _, doc := range []string{
		"[golang]\nversions = \"1\"\n", // unknown to go-toml
		"[golang]\nVersion = \"1\"\n",  // only differs in case
	} {
		_, err := load(t, fstest.MapFS{"machines/box.toml": file(doc)}, "box")
		if !errors.Is(err, config.ErrUnknownKey) {
			t.Errorf("errors.Is(err, ErrUnknownKey) = false for %v", err)
		}
	}
}

func TestInvalidModules(t *testing.T) {
	tests := []struct {
		name    string
		modules []config.Module
		want    string
	}{
		{
			name:    "reserved name",
			modules: []config.Module{testModule("os", func() config.ModuleConfig { return &golangConfig{} })},
			want:    `module name "os" is a reserved key`,
		},
		{
			name: "registered twice",
			modules: []config.Module{
				testModule("golang", func() config.ModuleConfig { return &golangConfig{} }),
				testModule("golang", func() config.ModuleConfig { return &golangConfig{} }),
			},
			want: `module "golang" is registered more than once`,
		},
		{
			name:    "config isn't a pointer to a struct",
			modules: []config.Module{testModule("golang", func() config.ModuleConfig { return config.Toggle{} })},
			want:    "NewConfig must return a pointer to a struct",
		},
		{
			name:    "no NewConfig",
			modules: []config.Module{{Name: "golang", OS: []config.OS{config.Fedora}}},
			want:    `module "golang" has no NewConfig`,
		},
		{
			name: "no operating system",
			modules: []config.Module{{Name: "golang", NewConfig: func() config.ModuleConfig {
				return &golangConfig{}
			}}},
			want: `module "golang" supports no operating system`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &config.Repo{FS: fstest.MapFS{"machines/box.toml": file(``)}, Modules: tt.modules}
			_, err := repo.LoadMachine("box")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func assertOrigins(t *testing.T, m *config.Machine, path string, want ...string) {
	t.Helper()
	if got := m.Origins[path]; !reflect.DeepEqual(got, want) {
		t.Errorf("Origins[%q] = %v, want %v", path, got, want)
	}
}
