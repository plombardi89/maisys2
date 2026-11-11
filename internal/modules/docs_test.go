package modules_test

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pelletier/go-toml/v2"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
)

// TestDocs checks that every key of every module, including the keys of
// embedded types such as config.Toggle, has a doc for users.
func TestDocs(t *testing.T) {
	docs := modules.Docs()
	var check func(typ reflect.Type, path string)
	check = func(typ reflect.Type, path string) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			check(typ.Elem(), path)
		case reflect.Struct:
			for f := range typ.Fields() {
				if !f.IsExported() {
					continue
				}
				key, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
				if f.Anonymous && key == "" {
					check(f.Type, path)
					continue
				}
				if docs.Field(typ, f) == "" {
					t.Errorf("%s (%s.%s) has no doc comment; add one and run go generate ./...", path+"."+key, typ, f.Name)
				}
				check(f.Type, path+"."+key)
			}
		default:
		}
	}
	for _, mod := range modules.All() {
		check(reflect.TypeOf(mod.NewConfig()), mod.Info().Name)
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

// TestExampleTOML checks that each module's example for config init,
// uncommented, loads to the module's defaults plus the example entries.
func TestExampleTOML(t *testing.T) {
	for _, mod := range modules.All() {
		info := mod.Info()
		t.Run(info.Name, func(t *testing.T) {
			machine := "os = \"" + string(info.OS[0]) + "\"\n" + uncomment(config.ExampleTOML(mod, modules.Docs()))
			repo := &config.Repo{FS: fstest.MapFS{"machines/box.toml": {Data: []byte(machine)}}, Modules: modules.All(), Strict: true}
			m, err := repo.LoadMachine("box")
			if err != nil {
				t.Fatalf("loading the example:\n%s\n%v", machine, err)
			}
			for _, other := range modules.All() {
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

// TestExamplesTogether checks that the examples of every module an OS
// supports, the way config init writes them, load together.
func TestExamplesTogether(t *testing.T) {
	for _, osName := range config.AllOS() {
		t.Run(string(osName), func(t *testing.T) {
			examples := []string{"os = \"" + string(osName) + "\"\n"}
			for _, mod := range modules.All() {
				if info := mod.Info(); !info.Hidden && slices.Contains(info.OS, osName) {
					examples = append(examples, uncomment(config.ExampleTOML(mod, modules.Docs())))
				}
			}
			machine := strings.Join(examples, "\n")
			repo := &config.Repo{FS: fstest.MapFS{"machines/box.toml": {Data: []byte(machine)}}, Modules: modules.All(), Strict: true}
			if _, err := repo.LoadMachine("box"); err != nil {
				t.Fatalf("loading the examples:\n%s\n%v", machine, err)
			}
		})
	}
}

// TestRenderExample renders the example machines, and checks that the
// output is valid TOML holding each enabled module's merged config.
func TestRenderExample(t *testing.T) {
	repo := &config.Repo{FS: os.DirFS("testdata/example"), Modules: modules.All(), Strict: true}
	for _, name := range []string{"phil-workp14s", "workbox-camwin-fc44", "workbox-camwin"} {
		t.Run(name, func(t *testing.T) {
			m, err := repo.LoadMachine(name)
			if err != nil {
				t.Fatal(err)
			}
			for _, explain := range []bool{false, true} {
				var out strings.Builder
				if err := config.Render(&out, m, modules.All(), modules.Docs(), explain); err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := toml.Unmarshal([]byte(out.String()), &doc); err != nil {
					t.Fatalf("the output (explain: %v) isn't valid TOML: %v\n%s", explain, err, out.String())
				}
				if doc["os"] != string(m.OS) {
					t.Errorf("os = %v, want %s", doc["os"], m.OS)
				}
				for _, mod := range modules.All() {
					name := mod.Info().Name
					cfg := m.Modules[name]
					table, rendered := doc[name]
					if rendered != cfg.IsEnabled() {
						t.Errorf("%s is in the output: %v, want %v", name, rendered, cfg.IsEnabled())
					}
					if !rendered || !cfg.IsEnabled() {
						continue
					}
					data, err := toml.Marshal(table)
					if err != nil {
						t.Fatal(err)
					}
					got := reflect.New(reflect.TypeOf(mod.NewConfig()).Elem()).Interface()
					if err := toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields().Decode(got); err != nil {
						t.Fatalf("%s: %v", name, err)
					}
					if !reflect.DeepEqual(normalized(got), normalized(any(cfg))) {
						t.Errorf("%s in the output = %+v, want %+v", name, got, cfg)
					}
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
