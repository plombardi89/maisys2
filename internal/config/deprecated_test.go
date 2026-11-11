package config_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
)

type deprecatingConfig struct {
	config.Toggle
	Version    string `toml:"version"`
	OldVersion string `toml:"old_version" deprecated:"use version instead."`
}

func TestDeprecatedKeysWarn(t *testing.T) {
	repo := &config.Repo{
		FS: fstest.MapFS{
			"machines/box.toml": file(`
os = "fedora"
profiles = ["base"]

[tool]
old_version = "2"
`),
			"profiles/base.toml": file(`
[tool]
enabled = true
old_version = "1"
`),
		},
		Modules: []config.Module{testModule("tool", func() config.ModuleConfig { return &deprecatingConfig{} })},
	}
	m, err := repo.LoadMachine("box")
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _ := m.Modules["tool"].(*deprecatingConfig); cfg == nil || cfg.OldVersion != "2" {
		t.Errorf("tool = %+v, want old_version = \"2\": the deprecated key keeps working", cfg)
	}
	want := []string{
		"profiles/base.toml:3:15: tool.old_version is deprecated and will be removed in a later config_version: use version instead.",
		"machines/box.toml:5:15: tool.old_version is deprecated and will be removed in a later config_version: use version instead.",
	}
	if !slices.Equal(m.Warnings, want) {
		t.Errorf("Warnings =\n%q\nwant\n%q", m.Warnings, want)
	}

	f, _ := reflect.TypeFor[deprecatingConfig]().FieldByName("OldVersion")
	docs := config.Docs{"github.com/plombardi89/maisys2/internal/config_test.deprecatingConfig.OldVersion": "OldVersion is the version."}
	if got, want := docs.Field(reflect.TypeFor[deprecatingConfig](), f), "`old_version` is the version. Deprecated: use version instead."; got != want {
		t.Errorf("Docs.Field = %q, want %q", got, want)
	}
}

type deprecatingItemsConfig struct {
	config.Toggle
	Version    string                      `toml:"version"`
	OldVersion string                      `toml:"old_version" deprecated:"use version instead."`
	Items      map[string]deprecatingEntry `toml:"items"`
}

type deprecatingEntry struct {
	Path    string `toml:"path"`
	OldPath string `toml:"old_path" deprecated:"use path instead."`
}

// TestDeprecatedKeysInExamplesAndSchema checks that config init's examples
// leave deprecated keys out, so uncommenting one doesn't set them, and
// that the schema marks them.
func TestDeprecatedKeysInExamplesAndSchema(t *testing.T) {
	mod := testModule("tool", func() config.ModuleConfig { return &deprecatingItemsConfig{} })

	example := config.ExampleTOML(mod, nil)
	for _, want := range []string{"\n# version = \"\"\n", "\n# [tool.items.example]\n# path = \"\"\n"} {
		if !strings.Contains(example, want) {
			t.Errorf("the example doesn't contain %q:\n%s", want, example)
		}
	}
	if strings.Contains(example, "old_") {
		t.Errorf("the example has a deprecated key:\n%s", example)
	}
	repo := &config.Repo{
		FS:      fstest.MapFS{"machines/box.toml": file("os = \"fedora\"\n" + uncomment(example))},
		Modules: []config.Module{mod},
		Strict:  true,
	}
	m, err := repo.LoadMachine("box")
	if err != nil {
		t.Fatalf("the uncommented example doesn't load: %v", err)
	}
	if len(m.Warnings) > 0 {
		t.Errorf("the uncommented example sets deprecated keys: %q", m.Warnings)
	}

	data, err := config.Schema([]config.Module{mod}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{
		"properties.tool.properties.old_version":                                    true,
		"properties.tool.properties.version":                                        false,
		"properties.tool.properties.items.additionalProperties.properties.old_path": true,
		"properties.tool.properties.items.additionalProperties.properties.path":     false,
	} {
		prop, _ := lookup(t, schema, strings.Split(path, ".")...).(map[string]any)
		if got, _ := prop["deprecated"].(bool); got != want {
			t.Errorf("%s: deprecated = %v, want %v", path, prop["deprecated"], want)
		}
	}
}
