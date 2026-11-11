package config_test

import (
	"errors"
	"reflect"
	"slices"
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

// fakeModule is a module for tests.
type fakeModule struct {
	info      config.ModuleInfo
	newConfig func() config.ModuleConfig
}

func (m fakeModule) Info() config.ModuleInfo        { return m.info }
func (m fakeModule) NewConfig() config.ModuleConfig { return m.newConfig() }

// testModule returns a module for tests, supported on Fedora.
func testModule(name string, newConfig func() config.ModuleConfig) config.Module {
	return fakeModule{info: config.ModuleInfo{Name: name, OS: []config.OS{config.Fedora}}, newConfig: newConfig}
}

func newGolangConfig() config.ModuleConfig {
	return &golangConfig{}
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

// loadStrict is load with unknown keys as errors.
func loadStrict(t *testing.T, files fstest.MapFS, machine string) (*config.Machine, error) {
	t.Helper()
	repo := &config.Repo{FS: files, Modules: testModules, Strict: true}
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
	for _, key := range []string{"os", "os_version", "arch", "profiles"} {
		assertOrigins(t, m, key, "machines/box.toml")
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
	want := map[string][]string{"os": {"machines/box.toml"}}
	if !reflect.DeepEqual(m.Origins, want) {
		t.Errorf("Origins = %v, want only the machine's os, %v", m.Origins, want)
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

func TestWhere(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{
		"machines/box.toml": file(`
os = "fedora"
profiles = ["base"]

[golang]
tools = ["task"]
flags = [
  "-x",
]

[files.items."a.b"]
target = "~/a.b"

[files.vars]
"my.var" = "x"
'quoted' = 'y'
`),
		"profiles/base.toml": file(`
golang.enabled = true
golang.version = "1.27.3"
golang.tools = ["goimports", "task"]

[files]
nested = { a = "base" }

[files.items.nvim]
source = "files/nvim/"
`),
	})

	tests := []struct {
		path, item, want string
	}{
		{path: "os", want: "machines/box.toml:1:6"},
		{path: "profiles", want: "machines/box.toml:2:12"},
		{path: "os_version", want: ""},
		{path: "golang.enabled", want: "profiles/base.toml:1:18"},
		{path: "golang.version", want: "profiles/base.toml:2:18"},
		// A list that combines across layers: the last file that added to it.
		{path: "golang.tools", want: "machines/box.toml:5:9"},
		{path: "golang.flags", want: "machines/box.toml:6:9"},
		{path: "files.nested.a", want: "profiles/base.toml:6:16"},
		{path: `files.items."a.b".target`, want: "machines/box.toml:11:10"},
		{path: `files.vars."my.var"`, want: "machines/box.toml:14:12"},
		{path: "files.vars.quoted", want: "machines/box.toml:15:12"},
		// Tables: where the last file that names them does.
		{path: "golang", want: "machines/box.toml:4:2"},
		{path: "files.nested", want: "profiles/base.toml:6:10"},
		{path: `files.items."a.b"`, want: "machines/box.toml:10:14"},
		{path: "files.items.nvim", want: "profiles/base.toml:8:14"},
		// Values no file sets keep their defaults.
		{path: "files.nested.b", want: ""},
		{path: "files.items.nvim.target", want: ""},
		// Items: the first file that added them.
		{path: "golang.tools", item: "goimports", want: "profiles/base.toml:3:17"},
		{path: "golang.tools", item: "task", want: "profiles/base.toml:3:30"},
		{path: "golang.tools", item: "unknown", want: "machines/box.toml:5:9"},
		{path: "profiles", item: "base", want: "machines/box.toml:2:13"},
	}
	for _, tt := range tests {
		got := m.Where(tt.path)
		if tt.item != "" {
			got = m.WhereItem(tt.path, tt.item)
		}
		if got != tt.want {
			t.Errorf("Where(%q) (item %q) = %q, want %q", tt.path, tt.item, got, tt.want)
		}
	}
}

func TestWhereIgnoresAProfilesOS(t *testing.T) {
	m := mustLoad(t, fstest.MapFS{
		"machines/box.toml":  file(`profiles = ["base"]`),
		"profiles/base.toml": file(`os = "fedora"`),
	})
	if got := m.Where("os"); got != "" {
		t.Errorf(`Where("os") = %q, want "": the machine file doesn't set os`, got)
	}
}

func TestJoinKey(t *testing.T) {
	if got, want := config.JoinKey("files", "items", "a.b", "my item", "target"), `files.items."a.b"."my item".target`; got != want {
		t.Errorf("JoinKey = %s, want %s", got, want)
	}
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
			want: []string{`machines/box.toml:1:13: profile "base" is listed more than once`},
		},
		{
			name:    "invalid profile name",
			machine: "box",
			files:   fstest.MapFS{"machines/box.toml": file(`profiles = ["Base"]`)},
			want:    []string{`machines/box.toml:1:13: invalid profile name "Base"`},
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
			_, err := loadStrict(t, tt.files, tt.machine)
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
		_, err := loadStrict(t, fstest.MapFS{"machines/box.toml": file(doc)}, "box")
		if !errors.Is(err, config.ErrUnknownKey) {
			t.Errorf("errors.Is(err, ErrUnknownKey) = false for %v", err)
		}
	}
}

// TestMachineOnlyKeysInProfiles checks that a profile that sets a machine
// file's own key is told so, whether or not unknown keys are ignored.
func TestMachineOnlyKeysInProfiles(t *testing.T) {
	files := fstest.MapFS{
		"machines/box.toml": file(`profiles = ["p"]`),
		"profiles/p.toml": file(`
os = "fedora"
arch = "amd64"
os_version = "44"
profiles = ["base"]
colour = "blue"
`),
	}
	for _, strict := range []bool{false, true} {
		repo := &config.Repo{FS: files, Modules: testModules, Strict: strict}
		_, err := repo.LoadMachine("box")
		want := []string{
			"profiles/p.toml:2:1: arch can only be set in a machine file",
			"profiles/p.toml:3:1: os_version can only be set in a machine file",
			"profiles/p.toml:4:1: profiles can only be set in a machine file",
		}
		if strict {
			want = append(want, "profiles/p.toml:5:1: unknown key colour")
		}
		if got := errorLines(err); !slices.Equal(got, want) {
			t.Errorf("strict %v: errors:\n%s\nwant:\n%s", strict, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if !strict && errors.Is(err, config.ErrUnknownKey) {
			t.Errorf("strict %v: errors.Is(err, ErrUnknownKey) = true, want false: the keys aren't unknown", strict)
		}

		// Profiles no machine lists are checked too.
		repo.FS = fstest.MapFS{"machines/box.toml": file(`os = "fedora"`), "profiles/p.toml": files["profiles/p.toml"]}
		if got := errorLines(validateAllErr(repo)); !slices.Equal(got, want) {
			t.Errorf("strict %v: ValidateAll errors:\n%s\nwant:\n%s", strict, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	}
}

// listsConfig has a list of tables, and a table of tables keyed by name.
type listsConfig struct {
	config.Toggle
	Steps []step          `toml:"steps"`
	Named map[string]step `toml:"named"`
}

type step struct {
	Name string   `toml:"name"`
	Args []string `toml:"args"`
}

func TestUnknownKeysAreIgnored(t *testing.T) {
	repo := &config.Repo{
		FS: fstest.MapFS{
			// Keys that only differ in case come after the right ones, so
			// they'd replace their values if they weren't left out.
			"machines/box.toml": file(`
os = "fedora"
OS = "ubuntu"
colour = "blue"
profiles = ["base"]

[golang]
enabled = true
version = "1.26.4"
Version = "1.2"
versions = { a = 1 }

[[lists.steps]]
name = "a"
Name = "b"
retries = 3

[[lists.steps]]
args = ["-x"]
ARGS = ["-y"]

[lists.named.one]
name = "one"
NAME = "uno"
extra = { deep = true }

[newer_module]
enabled = true
`),
			"profiles/base.toml": file(`
OS = "ubuntu"
arch_list = ["amd64"]

[files.items.nvim]
source = "files/nvim/"
Source = "files/vim/"
Enabled = false

[files.Items.other]
source = "files/other"
`),
		},
		Modules: append(slices.Clone(testModules), testModule("lists", func() config.ModuleConfig { return &listsConfig{} })),
	}
	m, err := repo.LoadMachine("box")
	if err != nil {
		t.Fatal(err)
	}
	if m.OS != config.Fedora || m.ProfileOS["base"] != "" {
		t.Errorf("os = %q, profile os = %q, want fedora and none", m.OS, m.ProfileOS["base"])
	}
	if g := golang(m); !g.Enabled || g.Version != "1.26.4" {
		t.Errorf("golang = %+v, want it on, at version 1.26.4", g)
	}
	wantItems := map[string]fileItem{"nvim": {Enabled: true, Source: "files/nvim/"}}
	if got := files(m).Items; !reflect.DeepEqual(got, wantItems) {
		t.Errorf("files.items = %+v, want %+v", got, wantItems)
	}
	lists, _ := m.Modules["lists"].(*listsConfig)
	wantSteps := []step{{Name: "a"}, {Args: []string{"-x"}}}
	if lists == nil || !reflect.DeepEqual(lists.Steps, wantSteps) || !reflect.DeepEqual(lists.Named, map[string]step{"one": {Name: "one"}}) {
		t.Errorf("lists = %+v, want steps %+v and named.one with name one", lists, wantSteps)
	}
	for key := range m.Origins {
		if strings.Contains(strings.ToLower(key), "newer") || strings.ContainsAny(key, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			t.Errorf("Origins has %q, which the files only set with an unknown key", key)
		}
	}
	if len(m.Warnings) > 0 {
		t.Errorf("Warnings = %q, want none", m.Warnings)
	}

	// The same files, read strictly.
	repo.Strict = true
	_, err = repo.LoadMachine("box")
	if !errors.Is(err, config.ErrUnknownKey) {
		t.Fatalf("strict LoadMachine = %v, want unknown key errors", err)
	}
	want := []string{
		"machines/box.toml:2:1: unknown key OS (keys are case-sensitive: did you mean os?)",
		"machines/box.toml:3:1: unknown key colour",
		"machines/box.toml:9:1: unknown key golang.Version (keys are case-sensitive: did you mean golang.version?)",
		"machines/box.toml:10:1: unknown key golang.versions",
		"machines/box.toml:14:1: unknown key lists.steps.Name (keys are case-sensitive: did you mean lists.steps.name?)",
		"machines/box.toml:15:1: unknown key lists.steps.retries",
		"machines/box.toml:19:1: unknown key lists.steps.ARGS (keys are case-sensitive: did you mean lists.steps.args?)",
		"machines/box.toml:23:1: unknown key lists.named.one.NAME (keys are case-sensitive: did you mean lists.named.one.name?)",
		"machines/box.toml:24:1: unknown key lists.named.one.extra",
		"machines/box.toml:26:2: unknown key newer_module",
	}
	if got := strings.Split(err.Error(), "\n"); !slices.Equal(got, want) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestIgnoredKeysKeepPositions checks that a problem in a file whose
// unknown keys are ignored points at the file, not at the copy of it the
// loader decodes without them.
func TestIgnoredKeysKeepPositions(t *testing.T) {
	_, err := load(t, fstest.MapFS{"machines/box.toml": file(`
colour = "blue"

[golang]
Version = 1
versions = "1.27"
tools = ["task"]
flags = "-x"
`)}, "box")
	want := "machines/box.toml:7:9: golang.flags must be a list of strings, not a TOML string"
	if err == nil || err.Error() != want {
		t.Errorf("LoadMachine = %v, want %s", err, want)
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
			modules: []config.Module{testModule("os", newGolangConfig)},
			want:    `module name "os" is a reserved key`,
		},
		{
			name: "registered twice",
			modules: []config.Module{
				testModule("golang", newGolangConfig),
				testModule("golang", newGolangConfig),
			},
			want: `module "golang" is registered more than once`,
		},
		{
			name:    "config isn't a pointer to a struct",
			modules: []config.Module{testModule("golang", func() config.ModuleConfig { return config.Toggle{} })},
			want:    "NewConfig must return a pointer to a struct",
		},
		{
			name:    "nil module",
			modules: []config.Module{testModule("golang", newGolangConfig), nil},
			want:    "module 1 is nil",
		},
		{
			name:    "no name",
			modules: []config.Module{testModule("", newGolangConfig)},
			want:    "module 0 has no name",
		},
		{
			name:    "no operating system",
			modules: []config.Module{fakeModule{info: config.ModuleInfo{Name: "golang"}, newConfig: newGolangConfig}},
			want:    `module "golang" supports no operating system`,
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
