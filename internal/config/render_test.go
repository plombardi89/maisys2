package config_test

import (
	"maps"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"

	"github.com/plombardi89/maisys2/internal/config"
)

// kindsConfig has every kind of value a config can hold.
type kindsConfig struct {
	config.Toggle
	Count   int                   `toml:"count"`
	Ratio   float64               `toml:"ratio"`
	Text    string                `toml:"text"`
	Nums    []int                 `toml:"nums"`
	Names   map[string]string     `toml:"names"`
	Entries map[string]kindsEntry `toml:"entries"`
}

type kindsEntry struct {
	On   bool     `toml:"on"`
	Tags []string `toml:"tags" merge:"union"`
}

func (e *kindsEntry) SetDefaults() {
	e.On = true
}

func newToggle() config.ModuleConfig {
	return &config.Toggle{}
}

// renderModules are test modules with summaries, covering hidden modules
// and the systems modules support.
var renderModules = []config.Module{
	fakeModule{
		info: config.ModuleInfo{Name: "golang", Summary: "Installs Go.", OS: []config.OS{config.Fedora}},
		newConfig: func() config.ModuleConfig {
			return &golangConfig{Version: "latest", Tools: []string{"default-tool"}}
		},
	},
	fakeModule{
		info:      config.ModuleInfo{Name: "files", Summary: "Copies files.", OS: []config.OS{config.Fedora}},
		newConfig: func() config.ModuleConfig { return &filesConfig{} },
	},
	fakeModule{
		info:      config.ModuleInfo{Name: "kinds", Summary: "Has every kind of value.", OS: []config.OS{config.Fedora}},
		newConfig: func() config.ModuleConfig { return &kindsConfig{Count: 2, Ratio: 0.5} },
	},
	fakeModule{
		info:      config.ModuleInfo{Name: "secret", Summary: "Is hidden.", OS: []config.OS{config.Fedora}, Hidden: true},
		newConfig: newToggle,
	},
	fakeModule{
		info:      config.ModuleInfo{Name: "wide", Summary: "Works everywhere, on amd64.", OS: config.AllOS(), Arch: []config.Arch{config.AMD64}},
		newConfig: newToggle,
	},
	fakeModule{
		info:      config.ModuleInfo{Name: "winonly", Summary: "Works on Windows.", OS: []config.OS{config.Windows}},
		newConfig: newToggle,
	},
}

// renderFiles is a config repo for the render tests.
var renderFiles = fstest.MapFS{
	"profiles/base.toml": file(`
[golang]
enabled = true
tools = ["a"]

[files]
enabled = false

[files.vars]
editor = "vi"

[secret]
enabled = false
`),
	"machines/box.toml": file(`
os = "fedora"
profiles = ["base"]

[golang]
version = "1.27.3"
tools = ["b"]

[kinds]
enabled = true
count = -3
ratio = 2.0
text = "quote \" backslash \\ tab \t newline \n bell \u0007 accent é"
nums = [1, 2]

[kinds.names]
"dotted.key" = "x"
"with space" = "y"
plain = "z"

[kinds.entries."a b"]
tags = ["t"]

[wide]
enabled = true
`),
}

func render(t *testing.T, m *config.Machine, explain bool) string {
	t.Helper()
	var out strings.Builder
	if err := config.Render(&out, m, renderModules, testDocs, explain); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func loadRenderBox(t *testing.T) *config.Machine {
	t.Helper()
	m, err := (&config.Repo{FS: renderFiles, Modules: renderModules}).LoadMachine("box")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRender(t *testing.T) {
	m := loadRenderBox(t)
	got := render(t, m, false)

	want := strings.TrimPrefix(`
os = "fedora"
profiles = ["base"]

[golang]
enabled = true
version = "1.27.3"
tools = ["default-tool", "a", "b"]
flags = []

[kinds]
enabled = true
count = -3
ratio = 2.0
text = "quote \" backslash \\ tab \t newline \n bell \u0007 accent é"
nums = [1, 2]

[kinds.names]
"dotted.key" = "x"
plain = "z"
"with space" = "y"

[kinds.entries."a b"]
on = true
tags = ["t"]

[wide]
enabled = true

# off: files
`, "\n")
	if got != want {
		t.Errorf("Render wrote:\n%s\nwant:\n%s", got, want)
	}

	var doc map[string]any
	if err := toml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("the output isn't valid TOML: %v", err)
	}
	for _, tt := range []struct {
		path []string
		want any
	}{
		{[]string{"os"}, "fedora"},
		{[]string{"profiles"}, []any{"base"}},
		{[]string{"golang", "tools"}, []any{"default-tool", "a", "b"}},
		{[]string{"kinds", "count"}, int64(-3)},
		{[]string{"kinds", "ratio"}, 2.0},
		{[]string{"kinds", "text"}, "quote \" backslash \\ tab \t newline \n bell \a accent é"},
		{[]string{"kinds", "nums"}, []any{int64(1), int64(2)}},
		{[]string{"kinds", "names", "dotted.key"}, "x"},
		{[]string{"kinds", "names", "with space"}, "y"},
		{[]string{"kinds", "entries", "a b", "on"}, true},
	} {
		if v := lookup(t, doc, tt.path...); !reflect.DeepEqual(v, tt.want) {
			t.Errorf("%s = %#v, want %#v", strings.Join(tt.path, "."), v, tt.want)
		}
	}

	// Used as the machine file, the output gives the same config for every
	// enabled module.
	files := fstest.MapFS{"machines/again.toml": {Data: []byte(got)}}
	maps.Copy(files, renderFiles)
	again, err := (&config.Repo{FS: files, Modules: renderModules}).LoadMachine("again")
	if err != nil {
		t.Fatalf("loading the output: %v", err)
	}
	for name, cfg := range m.Modules {
		if cfg.IsEnabled() && !reflect.DeepEqual(normalized(again.Modules[name]), normalized(cfg)) {
			t.Errorf("%s after loading the output = %+v, want %+v", name, again.Modules[name], cfg)
		}
	}
}

func TestRenderExplain(t *testing.T) {
	got := render(t, loadRenderBox(t), true)

	want := map[string]string{
		`os = "fedora"`:                      "machines/box.toml",
		`profiles = ["base"]`:                "machines/box.toml",
		`enabled = true`:                     "",
		`version = "1.27.3"`:                 "machines/box.toml",
		`tools = ["default-tool", "a", "b"]`: "profiles/base.toml, machines/box.toml",
		`flags = []`:                         "default",
		`count = -3`:                         "machines/box.toml",
		`"dotted.key" = "x"`:                 "machines/box.toml",
		`on = true`:                          "default",
		`tags = ["t"]`:                       "machines/box.toml",
	}
	// Comments start in the same column within each table, except after
	// values too long to line up with the rest.
	type commented struct{ line, value string }
	var block []commented
	seen := map[string]bool{}
	checkBlock := func() {
		column := -1
		for _, c := range block {
			if n := utf8.RuneCountInString(c.line); column < 0 || n < column {
				column = n
			}
		}
		for _, c := range block {
			if n := utf8.RuneCountInString(c.line); n != column && c.line != c.value {
				t.Errorf("%q: comment at column %d, want %d", c.line, n, column)
			}
		}
		block = nil
	}
	for line := range strings.Lines(got) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" || strings.HasPrefix(line, "[") {
			checkBlock()
			continue
		}
		if strings.HasPrefix(line, "# off: ") {
			continue
		}
		i := strings.Index(line, "  # ")
		if i < 0 {
			t.Errorf("line %q has no origin", line)
			continue
		}
		value, origin := strings.TrimRight(line[:i], " "), line[i+len("  # "):]
		if w, ok := want[value]; ok && w != "" && origin != w {
			t.Errorf("origin of %s = %q, want %q", value, origin, w)
		}
		seen[value] = true
		block = append(block, commented{line[:i], value})
	}
	checkBlock()
	for value := range want {
		if !seen[value] {
			t.Errorf("the output has no line %s", value)
		}
	}
	if !strings.HasSuffix(got, "\n# off: files\n") {
		t.Errorf("the output doesn't end with the modules that are off:\n%s", got)
	}
}

func TestRenderNeedsEveryModule(t *testing.T) {
	m := loadRenderBox(t)
	delete(m.Modules, "kinds")
	err := config.Render(&strings.Builder{}, m, renderModules, testDocs, false)
	if err == nil || !strings.Contains(err.Error(), `no config for module "kinds"`) {
		t.Errorf("error = %v, want one about the missing module", err)
	}
}

// uncomment removes the leading "# " from every line.
func uncomment(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		b.WriteString(strings.TrimPrefix(line, "# "))
	}
	return b.String()
}

func TestExampleTOML(t *testing.T) {
	got := config.ExampleTOML(renderModules[0], testDocs)
	want := strings.TrimPrefix(`
# # Installs Go.
# [golang]
# # `+"`enabled`"+` turns the module on.
# enabled = false
# # `+"`version`"+` to install.
# version = "latest"
# # `+"`tools`"+` lists programs to install with go install, as a package path and a
# # version. A program is only reinstalled when a newer version exists.
# # Combined across layers: profiles and the machine file add to the list
# # instead of replacing it.
# tools = ["default-tool"]
# flags = []
`, "\n")
	if got != want {
		t.Errorf("ExampleTOML wrote:\n%s\nwant:\n%s", got, want)
	}
}

// TestExampleTOMLLoads checks that each module's example, uncommented,
// loads to the module's defaults plus the example entries.
func TestExampleTOMLLoads(t *testing.T) {
	for _, mod := range renderModules {
		info := mod.Info()
		t.Run(info.Name, func(t *testing.T) {
			example := config.ExampleTOML(mod, testDocs)
			for line := range strings.Lines(example) {
				if !strings.HasPrefix(line, "#") {
					t.Errorf("line %q isn't commented out", line)
				}
				if strings.HasPrefix(line, "# # ") && utf8.RuneCountInString(strings.TrimSuffix(line, "\n")) > 78 {
					t.Errorf("comment %q is longer than 78 characters", line)
				}
			}

			machine := "os = \"" + string(info.OS[0]) + "\"\n" + uncomment(example)
			repo := &config.Repo{FS: fstest.MapFS{"machines/box.toml": {Data: []byte(machine)}}, Modules: renderModules}
			m, err := repo.LoadMachine("box")
			if err != nil {
				t.Fatalf("loading the example:\n%s\n%v", machine, err)
			}
			for _, other := range renderModules {
				name := other.Info().Name
				want := other.NewConfig()
				if name == info.Name {
					want = withExamples(want)
				}
				if got := m.Modules[name]; !reflect.DeepEqual(normalized(got), normalized(want)) {
					t.Errorf("%s = %+v, want %+v", name, got, want)
				}
			}
		})
	}
}

// normalized returns a copy of v in which empty lists and tables are nil,
// since TOML doesn't tell them apart.
func normalized[T any](v T) T {
	n, _ := reflect.TypeAssert[T](normalize(reflect.ValueOf(&v).Elem()))
	return n
}

func normalize(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Type()).Elem()
		c.Set(normalize(v.Elem()))
		return c
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		c := reflect.New(v.Type().Elem())
		c.Elem().Set(normalize(v.Elem()))
		return c
	case reflect.Struct:
		c := reflect.New(v.Type()).Elem()
		c.Set(v)
		for i := range v.NumField() {
			if c.Field(i).CanSet() {
				c.Field(i).Set(normalize(v.Field(i)))
			}
		}
		return c
	case reflect.Slice:
		if v.Len() == 0 {
			return reflect.Zero(v.Type())
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			c.Index(i).Set(normalize(v.Index(i)))
		}
		return c
	case reflect.Map:
		if v.Len() == 0 {
			return reflect.Zero(v.Type())
		}
		c := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			c.SetMapIndex(it.Key(), normalize(it.Value()))
		}
		return c
	default:
		return v
	}
}

// withExamples returns a copy of cfg with the entries ExampleTOML adds: an
// entry named "example" with its defaults in every table keyed by name.
func withExamples(cfg config.ModuleConfig) config.ModuleConfig {
	c := normalized(cfg)
	addExamples(reflect.ValueOf(c).Elem())
	return c
}

func addExamples(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for _, f := range v.Fields() {
			if f.CanSet() {
				addExamples(f)
			}
		}
	case reflect.Map:
		if v.IsNil() {
			v.Set(reflect.MakeMap(v.Type()))
		}
		for _, key := range v.MapKeys() {
			entry := reflect.New(v.Type().Elem()).Elem()
			entry.Set(v.MapIndex(key))
			addExamples(entry)
			v.SetMapIndex(key, entry)
		}
		key := reflect.ValueOf("example").Convert(v.Type().Key())
		if !v.MapIndex(key).IsValid() {
			entry := reflect.New(v.Type().Elem())
			if d, ok := reflect.TypeAssert[config.Defaulter](entry); ok {
				d.SetDefaults()
			}
			addExamples(entry.Elem())
			v.SetMapIndex(key, entry.Elem())
		}
	default:
	}
}
