package config_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
)

// fieldKey returns the Docs key of a field of t.
func fieldKey(t reflect.Type, field string) string {
	return t.PkgPath() + "." + t.Name() + "." + field
}

var (
	toggleType  = reflect.TypeFor[config.Toggle]()
	golangType  = reflect.TypeFor[golangConfig]()
	filesType   = reflect.TypeFor[filesConfig]()
	fileItemTyp = reflect.TypeFor[fileItem]()
	kindsType   = reflect.TypeFor[kindsConfig]()
)

// testDocs are the docs of the test modules' config types.
var testDocs = config.Docs{
	fieldKey(toggleType, "Enabled"):  "Enabled turns the module on.",
	fieldKey(golangType, "Version"):  "Version to install.",
	fieldKey(golangType, "Tools"):    "Tools lists programs to install with go install, as a package path and a version. A program is only reinstalled when a newer version exists.",
	fieldKey(filesType, "Vars"):      "Vars holds values for templates.",
	fieldKey(filesType, "Items"):     "Items are the files to copy, by name.",
	fieldKey(fileItemTyp, "Enabled"): "Enabled copies the item.",
	fieldKey(fileItemTyp, "Source"):  "Source is the path in the config repo.",
	fieldKey(fileItemTyp, "Target"):  "Target is where it goes, such as ~/.gitconfig | ~/x.",
	fieldKey(kindsType, "Count"):     "Count\n  is   how many.",
	fieldKey(kindsType, "Ratio"):     "The ratio, which doesn't start with the field name.",
	fieldKey(kindsType, "Text"):      "Textual is a longer word than the field name.",
}

func TestDocsField(t *testing.T) {
	tests := []struct {
		name  string
		typ   reflect.Type
		field string
		want  string
	}{
		{"the Go name becomes the key", golangType, "Version", "`version` to install."},
		{"a field of an embedded struct", toggleType, "Enabled", "`enabled` turns the module on."},
		{"whitespace becomes one paragraph", kindsType, "Count", "`count` is how many."},
		{"a doc that doesn't start with the name", kindsType, "Ratio", "The ratio, which doesn't start with the field name."},
		{"a longer word that starts with the name", kindsType, "Text", "Textual is a longer word than the field name."},
		{"no doc", golangType, "Flags", ""},
		{"an anonymous struct type", reflect.TypeFor[struct {
			Version string `toml:"version"`
		}](), "Version", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := tt.typ.FieldByName(tt.field)
			if !ok {
				t.Fatalf("%v has no field %s", tt.typ, tt.field)
			}
			if got := testDocs.Field(tt.typ, f); got != tt.want {
				t.Errorf("Field = %q, want %q", got, tt.want)
			}
		})
	}
}

// lookup returns the value at path in decoded JSON or TOML.
func lookup(t *testing.T, v any, path ...string) any {
	t.Helper()
	for i, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s isn't a table", strings.Join(path[:i], "."))
		}
		if v, ok = m[p]; !ok {
			t.Fatalf("%s is missing", strings.Join(path[:i+1], "."))
		}
	}
	return v
}

func TestSchema(t *testing.T) {
	const id = "https://example.com/config.schema.json"
	data, err := config.Schema(renderModules, testDocs, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "}\n") {
		t.Error("the schema doesn't end with a newline")
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("the schema isn't valid JSON: %v", err)
	}

	tests := []struct {
		path []string
		want any
	}{
		{[]string{"$schema"}, "https://json-schema.org/draft/2020-12/schema"},
		{[]string{"$id"}, id},
		{[]string{"type"}, "object"},
		{[]string{"additionalProperties"}, false},
		{[]string{"properties", "profiles", "type"}, "array"},
		{[]string{"properties", "profiles", "items"}, map[string]any{"type": "string"}},
		{[]string{"properties", "os", "enum"}, []any{"fedora", "ubuntu", "windows"}},
		{[]string{"properties", "os_version", "type"}, "string"},
		{[]string{"properties", "arch", "enum"}, []any{"amd64", "arm64"}},

		{[]string{"properties", "golang", "type"}, "object"},
		{[]string{"properties", "golang", "description"}, "Installs Go."},
		{[]string{"properties", "golang", "additionalProperties"}, false},
		{[]string{"properties", "golang", "properties", "enabled"}, map[string]any{
			"type": "boolean", "default": false, "description": "`enabled` turns the module on.",
		}},
		{[]string{"properties", "golang", "properties", "version", "default"}, "latest"},
		{[]string{"properties", "golang", "properties", "version", "description"}, "`version` to install."},
		{[]string{"properties", "golang", "properties", "tools", "type"}, "array"},
		{[]string{"properties", "golang", "properties", "tools", "items"}, map[string]any{"type": "string"}},
		{[]string{"properties", "golang", "properties", "tools", "default"}, []any{"default-tool"}},
		{[]string{"properties", "golang", "properties", "flags", "default"}, []any{}},

		{[]string{"properties", "files", "properties", "vars", "additionalProperties"}, map[string]any{"type": "string"}},
		{[]string{"properties", "files", "properties", "items", "type"}, "object"},
		{[]string{"properties", "files", "properties", "items", "additionalProperties", "additionalProperties"}, false},
		{[]string{"properties", "files", "properties", "items", "additionalProperties", "properties", "enabled", "default"}, true},
		{[]string{"properties", "files", "properties", "items", "additionalProperties", "properties", "source", "description"}, "`source` is the path in the config repo."},
		{[]string{"properties", "files", "properties", "nested", "additionalProperties"}, false},
		{[]string{"properties", "files", "properties", "nested", "properties", "a", "type"}, "string"},

		{[]string{"properties", "kinds", "properties", "count", "type"}, "integer"},
		{[]string{"properties", "kinds", "properties", "count", "default"}, float64(2)},
		{[]string{"properties", "kinds", "properties", "ratio", "type"}, "number"},
		{[]string{"properties", "kinds", "properties", "nums", "items"}, map[string]any{"type": "integer"}},
		{[]string{"properties", "kinds", "properties", "entries", "additionalProperties", "properties", "tags", "default"}, []any{}},

		// Hidden modules are in the schema, since configs may use them.
		{[]string{"properties", "secret", "properties", "enabled", "type"}, "boolean"},
	}
	for _, tt := range tests {
		if got := lookup(t, schema, tt.path...); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s = %#v, want %#v", strings.Join(tt.path, "."), got, tt.want)
		}
	}

	desc := lookup(t, schema, "properties", "golang", "properties", "tools", "description")
	if s, _ := desc.(string); !strings.HasPrefix(s, "`tools` lists programs") || !strings.Contains(s, "Combined across layers") {
		t.Errorf("golang.tools description = %q, want its doc and a note that it combines across layers", desc)
	}

	// Properties keep their order: the files' own keys, then modules in
	// order, then keys in struct order.
	text := string(data)
	order := []string{`"profiles"`, `"os"`, `"os_version"`, `"arch"`, `"golang"`, `"files"`, `"kinds"`}
	last := -1
	for _, key := range order {
		i := strings.Index(text, key+": {")
		if i < last {
			t.Errorf("%s comes before %v in the schema", order, key)
		}
		last = i
	}
}

func TestSchemaRejectsInvalidModules(t *testing.T) {
	modules := []config.Module{testModule("golang", newGolangConfig), testModule("golang", newGolangConfig)}
	if _, err := config.Schema(modules, testDocs, ""); err == nil || !strings.Contains(err.Error(), "registered more than once") {
		t.Errorf("error = %v, want one about the duplicate module", err)
	}
}

func TestModulesMarkdown(t *testing.T) {
	doc := string(config.ModulesMarkdown(renderModules, testDocs))

	for _, want := range []string{
		"\n# Modules\n",
		"| [golang](#golang) | Installs Go. | Fedora |\n",
		"| [wide](#wide) | Works everywhere, on amd64. | Fedora, Ubuntu and Windows, amd64 only |\n",
		"| `os` | string | ",
		"\n## golang\n\nInstalls Go.\n\nWorks on Fedora.\n\n| Key | Type | Default | Description |\n|---|---|---|---|\n",
		"| `golang.enabled` | boolean | `false` | `enabled` turns the module on. |\n",
		"| `golang.version` | string | `\"latest\"` | `version` to install. |\n",
		"| `golang.tools` | list of strings | `[\"default-tool\"]` | `tools` lists programs to install with go install, as a package path and a version. A program is only reinstalled when a newer version exists. Combined across layers: ",
		"| `golang.flags` | list of strings | `[]` |  |\n",
		"| `files.vars` | table of strings |  | `vars` holds values for templates. |\n",
		"| `files.items` | table of tables |  | `items` are the files to copy, by name. |\n",
		"| `files.items.<name>.enabled` | boolean | `true` | `enabled` copies the item. |\n",
		// Markdown in docs is escaped, so it shows as written.
		"| `files.items.<name>.target` | string | `\"\"` | `target` is where it goes, such as \\~/.gitconfig \\| \\~/x. |\n",
		"| `files.nested` | table |  |  |\n",
		"| `files.nested.a` | string | `\"\"` |  |\n",
		"| `kinds.count` | integer | `2` | `count` is how many. |\n",
		"| `kinds.ratio` | number | `0.5` | The ratio, which doesn't start with the field name. |\n",
		"| `kinds.nums` | list of integers | `[]` |  |\n",
		"| `kinds.entries.<name>.tags` | list of strings | `[]` | Combined across layers: ",
		"\n## wide\n\nWorks everywhere, on amd64.\n\nWorks on Fedora, Ubuntu and Windows, amd64 only.\n",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the module docs don't contain %q", want)
		}
	}
	if strings.Contains(doc, "secret") {
		t.Error("the module docs include the hidden module")
	}
	if !strings.HasSuffix(doc, " |\n") {
		t.Error("the module docs don't end with a table row and a newline")
	}
}
