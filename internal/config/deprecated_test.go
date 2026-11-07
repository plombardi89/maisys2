package config_test

import (
	"reflect"
	"slices"
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
