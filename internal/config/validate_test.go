package config_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
)

// Test modules for the validation rules.

type toolConfig struct {
	config.Toggle
	config.Versioned
}

// nopassConfig has no enabled key: like sudo's, its one setting turns it
// on.
type nopassConfig struct {
	NoPasswd bool `toml:"nopasswd"`
}

func (c *nopassConfig) IsEnabled() bool { return c.NoPasswd }

// writerConfig writes the paths it lists.
type writerConfig struct {
	config.Toggle
	Paths map[string]string `toml:"paths"`
	name  string
}

func (c *writerConfig) Targets(*config.Machine) []config.Target {
	var targets []config.Target
	for _, k := range slices.Sorted(maps.Keys(c.Paths)) {
		targets = append(targets, config.Target{Path: c.Paths[k], Key: config.JoinKey(c.name, "paths", k)})
	}
	return targets
}

// checkedConfig has rules of its own.
type checkedConfig struct {
	config.Toggle
	Value string   `toml:"value"`
	List  []string `toml:"list" merge:"union"`
}

func (c *checkedConfig) Check(check *config.Check) {
	switch c.Value {
	case "":
		check.Errorf("value", "is required")
	case "bad":
		check.Errorf("value", "mustn't be %q on machine %s", c.Value, check.Machine.Name)
	default:
	}
	for _, item := range c.List {
		if item == "bad" {
			check.ItemErrorf("list", item, "mustn't list %q", item)
		}
	}
	check.NoEmptyItems("list", c.List)
}

func validationModules() []config.Module {
	linux := config.LinuxOS()
	all := config.AllOS()
	return []config.Module{
		fakeModule{
			info:      config.ModuleInfo{Name: "tool", OS: linux},
			newConfig: func() config.ModuleConfig { return &toolConfig{Version: config.Latest} },
		},
		fakeModule{
			info:      config.ModuleInfo{Name: "game", OS: linux, Arch: []config.Arch{config.AMD64}},
			newConfig: func() config.ModuleConfig { return &config.Toggle{} },
		},
		fakeModule{
			info:      config.ModuleInfo{Name: "nopass", OS: linux},
			newConfig: func() config.ModuleConfig { return &nopassConfig{} },
		},
		fakeModule{
			info:      config.ModuleInfo{Name: "writer", OS: all},
			newConfig: func() config.ModuleConfig { return &writerConfig{name: "writer"} },
		},
		fakeModule{
			info:      config.ModuleInfo{Name: "other", OS: all},
			newConfig: func() config.ModuleConfig { return &writerConfig{name: "other"} },
		},
		fakeModule{
			info:      config.ModuleInfo{Name: "checked", OS: all},
			newConfig: func() config.ModuleConfig { return &checkedConfig{} },
		},
	}
}

// errorLines returns the lines of err's message, or nil if err is nil.
func errorLines(err error) []string {
	if err == nil {
		return nil
	}
	return strings.Split(err.Error(), "\n")
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  []string // every line of the error, in order; none if empty
	}{
		{
			name: "a valid machine",
			files: fstest.MapFS{
				"machines/box.toml": file(`
os = "fedora"
os_version = "44"
arch = "amd64"
profiles = ["base"]

[tool]
enabled = true
version = "1.2.3"
`),
				"profiles/base.toml": file(`
os = "fedora"

[game]
enabled = true
`),
			},
		},
		{
			name:  "no os",
			files: fstest.MapFS{"machines/box.toml": file(``)},
			want:  []string{"machines/box.toml: os is missing: set it to fedora, ubuntu or windows"},
		},
		{
			name:  "an unknown os",
			files: fstest.MapFS{"machines/box.toml": file(`os = "debian"`)},
			want:  []string{`machines/box.toml:1:6: os must be fedora, ubuntu or windows, not "debian"`},
		},
		{
			name:  "a Fedora version that isn't a release number",
			files: fstest.MapFS{"machines/box.toml": file("os = \"fedora\"\nos_version = \"f44\"\n")},
			want:  []string{`machines/box.toml:2:14: os_version must be a Fedora release such as "44", not "f44"`},
		},
		{
			name:  "an Ubuntu version without its month",
			files: fstest.MapFS{"machines/box.toml": file("os = \"ubuntu\"\nos_version = \"26\"\n")},
			want:  []string{`machines/box.toml:2:14: os_version must be an Ubuntu release such as "26.04", not "26"`},
		},
		{
			name:  "a Windows version that isn't a build number",
			files: fstest.MapFS{"machines/box.toml": file("os = \"windows\"\nos_version = \"11.0\"\n")},
			want:  []string{`machines/box.toml:2:14: os_version must be a Windows build number such as "26100", not "11.0"`},
		},
		{
			name:  "valid Ubuntu version",
			files: fstest.MapFS{"machines/box.toml": file("os = \"ubuntu\"\nos_version = \"24.04\"\n")},
		},
		{
			name:  "valid Windows version",
			files: fstest.MapFS{"machines/box.toml": file("os = \"windows\"\nos_version = \"26100\"\n")},
		},
		{
			name:  "arch on Windows",
			files: fstest.MapFS{"machines/box.toml": file("os = \"windows\"\narch = \"amd64\"\n")},
			want:  []string{"machines/box.toml:2:8: arch can only be set on Linux machines: Windows hosts are always amd64"},
		},
		{
			name:  "an unknown arch",
			files: fstest.MapFS{"machines/box.toml": file("os = \"fedora\"\narch = \"x86_64\"\n")},
			want:  []string{`machines/box.toml:2:8: arch must be amd64 or arm64, not "x86_64"`},
		},
		{
			name: "a profile for another os",
			files: fstest.MapFS{
				"machines/box.toml":    file("os = \"ubuntu\"\nprofiles = [\"base\", \"fedora\"]\n"),
				"profiles/base.toml":   file(``),
				"profiles/fedora.toml": file(`os = "fedora"`),
			},
			want: []string{`machines/box.toml:2:21: profile "fedora" sets os = "fedora" (profiles/fedora.toml:1:6), so a ubuntu machine can't use it`},
		},
		{
			name: "a profile with an unknown os",
			files: fstest.MapFS{
				"machines/box.toml":  file("os = \"fedora\"\nprofiles = [\"base\"]\n"),
				"profiles/base.toml": file(`os = "debian"`),
			},
			want: []string{`profiles/base.toml:1:6: os must be fedora, ubuntu or windows, not "debian"`},
		},
		{
			name: "a module the os doesn't support, turned on by a profile",
			files: fstest.MapFS{
				"machines/box.toml":  file("os = \"windows\"\nprofiles = [\"base\"]\n"),
				"profiles/base.toml": file("[tool]\nenabled = true\n"),
			},
			want: []string{`profiles/base.toml:2:11: tool only works on fedora or ubuntu, but machine "box" is windows`},
		},
		{
			name: "a module without an enabled key, turned on by its own setting",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "windows"

[nopass]
nopasswd = true
`)},
			want: []string{`machines/box.toml:4:12: nopass only works on fedora or ubuntu, but machine "box" is windows`},
		},
		{
			name: "a module the os doesn't support, turned off",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "windows"

[tool]
version = "1.2.3"
`)},
		},
		{
			name: "a module the arch doesn't support",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"
arch = "arm64"

[game]
enabled = true
`)},
			want: []string{`machines/box.toml:5:11: game only works on amd64, but machine "box" is arm64`},
		},
		{
			name: "a module limited to some architectures, on a machine without arch",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[game]
enabled = true
`)},
		},
		{
			name: "a malformed version",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[tool]
enabled = true
version = "1.x"
`)},
			want: []string{`machines/box.toml:5:11: tool.version must be "latest" or an exact version such as "1.2.3", not "1.x"`},
		},
		{
			name: "an empty version",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[tool]
enabled = true
version = ""
`)},
			want: []string{`machines/box.toml:5:11: tool.version must be "latest" or an exact version such as "1.2.3", not ""`},
		},
		{
			name: "a malformed version in a module that's off",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[tool]
version = "1.x"
`)},
		},
		{
			name: "two modules write the same path",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.zshrc"

[other]
enabled = true
paths.b = "~/.zshrc"
`)},
			want: []string{`machines/box.toml:9:11: writer.paths.a and other.paths.b both write ~/.zshrc on machine "box"`},
		},
		{
			name: "one module writes a path twice, spelled differently",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.config/nvim/"
paths.b = "~/.config/nvim"
`)},
			want: []string{`machines/box.toml:6:11: writer.paths.a and writer.paths.b both write ~/.config/nvim on machine "box"`},
		},
		{
			name: "a path inside a directory another module writes",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.config/nvim/"

[other]
enabled = true
paths.b = "~/.config/nvim/init.lua"
`)},
			want: []string{
				`machines/box.toml:9:11: other.paths.b (~/.config/nvim/init.lua) is inside writer.paths.a (~/.config/nvim/) on machine "box": only one of them can write it`,
			},
		},
		{
			name: "a directory around a path another module writes",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.config/nvim/init.lua"

[other]
enabled = true
paths.b = "~/.config/"
`)},
			want: []string{
				`machines/box.toml:5:11: writer.paths.a (~/.config/nvim/init.lua) is inside other.paths.b (~/.config/) on machine "box": only one of them can write it`,
			},
		},
		{
			name: "paths that only share a prefix, and a file that isn't a directory",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.config/nvim/"
paths.b = "~/.config/nvim2"
paths.c = "~/.ssh"
paths.d = "~/.ssh/config"
`)},
		},
		{
			name: "paths that differ in case, on Windows",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "windows"

[writer]
enabled = true
paths.a = "~/.Gitconfig"
paths.b = "~/.gitconfig"
`)},
			want: []string{`machines/box.toml:6:11: writer.paths.a and writer.paths.b both write ~/.gitconfig on machine "box"`},
		},
		{
			name: "paths that differ in case, on Linux",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.Gitconfig"
paths.b = "~/.gitconfig"
`)},
		},
		{
			name: "a module that's off writes the same path",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[writer]
enabled = true
paths.a = "~/.zshrc"

[other]
paths.b = "~/.zshrc"
`)},
		},
		{
			name: "a module's own rules",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[checked]
enabled = true
value = "bad"
`)},
			want: []string{`machines/box.toml:5:9: checked.value mustn't be "bad" on machine box`},
		},
		{
			name: "a module's own rules, about a value no file sets",
			files: fstest.MapFS{
				"machines/box.toml":  file("os = \"fedora\"\nprofiles = [\"base\"]\n"),
				"profiles/base.toml": file("[checked]\nenabled = true\n"),
			},
			want: []string{"profiles/base.toml:1:2: checked.value is required"},
		},
		{
			name: "a module's own rules, about list items from different files",
			files: fstest.MapFS{
				"machines/box.toml": file(`
os = "fedora"
profiles = ["base"]

[checked]
list = ["more", ""]
`),
				"profiles/base.toml": file(`
[checked]
enabled = true
value = "ok"
list = ["ok", "bad"]
`),
			},
			want: []string{
				`profiles/base.toml:4:15: checked.list mustn't list "bad"`,
				`machines/box.toml:5:17: checked.list has an empty entry`,
			},
		},
		{
			name: "a module's own rules, when it's off",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "fedora"

[checked]
value = "bad"
`)},
		},
		{
			name: "every problem is reported",
			files: fstest.MapFS{"machines/box.toml": file(`
os = "windows"
arch = "arm64"

[tool]
enabled = true
version = "1.x"
`)},
			want: []string{
				"machines/box.toml:2:8: arch can only be set on Linux machines: Windows hosts are always amd64",
				`machines/box.toml:5:11: tool only works on fedora or ubuntu, but machine "box" is windows`,
				`machines/box.toml:6:11: tool.version must be "latest" or an exact version such as "1.2.3", not "1.x"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &config.Repo{FS: tt.files, Modules: validationModules()}
			m, err := repo.Validate("box")
			if m == nil {
				t.Fatalf("Validate returned no machine: %v", err)
			}
			if got := errorLines(err); !slices.Equal(got, tt.want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestValidateReturnsLoadErrors(t *testing.T) {
	repo := &config.Repo{
		FS:      fstest.MapFS{"machines/box.toml": file("os = \"fedora\"\ncolour = \"blue\"\n")},
		Modules: validationModules(),
	}
	m, err := repo.Validate("box")
	if m != nil || err == nil || !strings.Contains(err.Error(), "machines/box.toml:2:1: unknown key colour") {
		t.Errorf("Validate = %v, %v, want no machine and the unknown key", m, err)
	}
}

func TestIsExactVersion(t *testing.T) {
	for v, want := range map[string]bool{
		"1.2.3":      true,
		"v2.15.0":    true,
		"2026.2.3":   true,
		"1.27":       true,
		"1":          true,
		"1.0.0-rc.1": true,
		"1.0.0+meta": true,
		"latest":     false,
		"":           false,
		"1.x":        false,
		"1.2.":       false,
		"=1.2.3":     false,
		"1.2.3-":     false,
	} {
		if got := config.IsExactVersion(v); got != want {
			t.Errorf("IsExactVersion(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestMachineNames(t *testing.T) {
	repo := &config.Repo{FS: fstest.MapFS{
		"machines/a.toml":        file(``),
		"machines/a.b.toml":      file(``),
		"machines/a-b.toml":      file(``),
		"machines/Bad_Name.toml": file(``),
		"machines/README.md":     file(``),
		"machines/old/c.toml":    file(``),
	}}
	names, err := repo.MachineNames()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "a-b", "a.b"}; !slices.Equal(names, want) {
		t.Errorf("MachineNames = %q, want %q", names, want)
	}

	names, err = (&config.Repo{FS: fstest.MapFS{}}).MachineNames()
	if err != nil || names != nil {
		t.Errorf("MachineNames without a machines folder = %q, %v, want none", names, err)
	}
}

func TestValidateAll(t *testing.T) {
	repo := &config.Repo{
		FS: fstest.MapFS{
			"machines/a.toml":        file("os = \"fedora\"\nprofiles = [\"base\"]\n"),
			"machines/b.toml":        file("os = \"ubuntu\"\nprofiles = [\"base\"]\n"),
			"machines/c.toml":        file(`os = "debian"`),
			"machines/Bad_Name.toml": file(``),
			"machines/README.md":     file(`not a machine`),
			// Both a and b use base, but its problems are reported once.
			"profiles/base.toml":   file("[checked]\nenabled = true\nlist = [\"\"]\n"),
			"profiles/fedora.toml": file(`os = "fedora"`),
			// No machine uses these.
			"profiles/unused.toml": file(`os = "debian"`),
			"profiles/broken.toml": file("[tool]\nversions = \"1\"\n"),
			"profiles/Dev.toml":    file(``),
		},
		Modules: validationModules(),
	}
	want := []string{
		`machines/Bad_Name.toml: invalid machine name "Bad_Name": use lower-case letters, digits, '.', '-' and '_'`,
		"profiles/base.toml:1:2: checked.value is required",
		"profiles/base.toml:3:9: checked.list has an empty entry",
		`machines/c.toml:1:6: os must be fedora, ubuntu or windows, not "debian"`,
		`profiles/Dev.toml: invalid profile name "Dev": use lower-case letters, digits, '.', '-' and '_'`,
		"profiles/broken.toml:2:1: unknown key tool.versions",
		`profiles/unused.toml:1:6: os must be fedora, ubuntu or windows, not "debian"`,
	}
	if got := errorLines(validateAllErr(repo)); !slices.Equal(got, want) {
		t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestValidateAllWithoutMachines(t *testing.T) {
	_, err := (&config.Repo{FS: fstest.MapFS{"maisys.toml": file(`config_version = 1`)}, Modules: validationModules()}).ValidateAll()
	if want := "machines/ has no machine files"; err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

// validateAllErr returns ValidateAll's error.
func validateAllErr(r *config.Repo) error {
	_, err := r.ValidateAll()
	return err
}
