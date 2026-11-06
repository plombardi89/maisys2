package config_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
)

// Config types the loader must reject, because they can't be read and
// merged reliably.

type untaggedConfig struct {
	config.Toggle
	Version string
}

type pointerConfig struct {
	config.Toggle
	Item *fileItem `toml:"item"`
}

type pointerEntriesConfig struct {
	config.Toggle
	Items map[string]*fileItem `toml:"items"`
}

type interfaceConfig struct {
	config.Toggle
	Value any `toml:"value"`
}

type intKeysConfig struct {
	config.Toggle
	Names map[int]string `toml:"names"`
}

type unionScalarConfig struct {
	config.Toggle
	Version string `toml:"version" merge:"union"`
}

type unknownRuleConfig struct {
	config.Toggle
	Tools []string `toml:"tools" merge:"append"`
}

type duplicateKeyConfig struct {
	config.Toggle
	On bool `toml:"enabled"`
}

type nestedUntaggedConfig struct {
	config.Toggle
	Nested struct {
		Path string
	} `toml:"nested"`
}

func TestSchemaRejectsConfigsItCantMerge(t *testing.T) {
	tests := []struct {
		name   string
		config config.ModuleConfig
		want   string
	}{
		{"untagged field", &untaggedConfig{}, `module "bad": bad: field Version has no toml tag`},
		{"untagged nested field", &nestedUntaggedConfig{}, `bad.nested: field Path has no toml tag`},
		{"pointer field", &pointerConfig{}, `bad.item: *config_test.fileItem values aren't supported`},
		{"pointer entries", &pointerEntriesConfig{}, `bad.items.<name>: *config_test.fileItem values aren't supported`},
		{"interface field", &interfaceConfig{}, `bad.value: interface {} values aren't supported`},
		{"non-string keys", &intKeysConfig{}, `bad.names: tables keyed by name need string keys, not int`},
		{"union on a single value", &unionScalarConfig{}, `bad.version: merge:"union" only applies to lists, not string`},
		{"unknown merge rule", &unknownRuleConfig{}, `bad.tools: unknown merge rule "append"`},
		{"two fields with one key", &duplicateKeyConfig{}, `bad: more than one field has the key "enabled"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.config
			repo := &config.Repo{
				FS:      fstest.MapFS{"machines/box.toml": file(``)},
				Modules: []config.Module{testModule("bad", func() config.ModuleConfig { return cfg })},
			}
			_, err := repo.LoadMachine("box")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// sharedConfig's module returns the same struct, map and slice from every
// NewConfig call, which merging must never write into.
type sharedConfig struct {
	config.Toggle
	Vars  map[string]string   `toml:"vars"`
	Tools []string            `toml:"tools" merge:"union"`
	Items map[string]fileItem `toml:"items"`
}

func TestDefaultsAreNotShared(t *testing.T) {
	tools := make([]string, 1, 10) // spare capacity an append could write into
	tools[0] = "default-tool"
	shared := &sharedConfig{
		Vars:  map[string]string{"editor": "vi"},
		Tools: tools,
		Items: map[string]fileItem{"base": {Enabled: true, Source: "base"}},
	}
	repo := &config.Repo{
		FS: fstest.MapFS{
			"machines/a.toml": file(`
[shared]
enabled = true
tools = ["a-tool"]

[shared.vars]
email = "a@example.com"

[shared.items.base]
target = "~/base"

[shared.items.extra]
source = "extra"
`),
			"machines/b.toml": file(``),
		},
		Modules: []config.Module{testModule("shared", func() config.ModuleConfig { return shared })},
	}

	a, err := repo.LoadMachine("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.LoadMachine("b")
	if err != nil {
		t.Fatal(err)
	}

	gotA, _ := a.Modules["shared"].(*sharedConfig)
	if gotA == shared {
		t.Fatal("machine a's config is the struct NewConfig returned")
	}
	if !reflect.DeepEqual(gotA.Tools, []string{"default-tool", "a-tool"}) || gotA.Vars["email"] != "a@example.com" || len(gotA.Items) != 2 {
		t.Errorf("machine a = %+v, want its own settings merged", gotA)
	}

	// Machine b, and the struct NewConfig returns, still have only the
	// defaults.
	gotB, _ := b.Modules["shared"].(*sharedConfig)
	for name, c := range map[string]*sharedConfig{"machine b": gotB, "NewConfig's struct": shared} {
		if c.Enabled || !reflect.DeepEqual(c.Tools, []string{"default-tool"}) ||
			!reflect.DeepEqual(c.Vars, map[string]string{"editor": "vi"}) ||
			!reflect.DeepEqual(c.Items, map[string]fileItem{"base": {Enabled: true, Source: "base"}}) {
			t.Errorf("%s = %+v, want only the defaults", name, c)
		}
	}
	if spare := tools[:2][1]; spare != "" {
		t.Errorf("the defaults' spare slice capacity was written to: %q", spare)
	}
}
