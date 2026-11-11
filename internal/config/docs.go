package config

import (
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Docs holds the doc comments of module config types and their fields,
// which describe each key for users. Doc comments aren't available at run
// time, so go generate extracts them from the source (see modules.Docs).
//
// Keys are "<import path>.<TypeName>" for a type, and
// "<import path>.<TypeName>.<FieldName>" for a field.
type Docs map[string]string

// Field returns the doc of field f of struct type t for users, as one
// paragraph. Field docs start with the field's Go name, which is replaced
// with its TOML key in backticks. Fields without a doc return "".
func (d Docs) Field(t reflect.Type, f reflect.StructField) string {
	doc := d.fieldDoc(t, f)
	if note := f.Tag.Get("deprecated"); note != "" {
		doc = strings.TrimSpace(doc + " Deprecated: " + note)
	}
	return doc
}

func (d Docs) fieldDoc(t reflect.Type, f reflect.StructField) string {
	if t.Name() == "" {
		return ""
	}
	doc := strings.Join(strings.Fields(d[t.PkgPath()+"."+t.Name()+"."+f.Name]), " ")
	rest, ok := strings.CutPrefix(doc, f.Name)
	if !ok {
		return doc
	}
	if r, _ := utf8.DecodeRuneInString(rest); r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
		return doc
	}
	key := tomlName(f)
	if key == "" || key == "-" {
		key = f.Name
	}
	return "`" + key + "`" + rest
}

// unionNote ends the docs of lists that combine across layers.
const unionNote = "Combined across layers: profiles and the machine file add to the list instead of replacing it."

// configKey is a key in a config table.
type configKey struct {
	name string
	// owner is the struct type that declares field, which may be a struct
	// embedded in the table's own type.
	owner reflect.Type
	field reflect.StructField
	value reflect.Value
}

// configKeys returns the keys of v, a config struct, in struct order. The
// keys of embedded structs without a key of their own (like Toggle) are
// included where they're embedded.
func configKeys(v reflect.Value) []configKey {
	t := v.Type()
	var keys []configKey
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := tomlName(f)
		switch {
		case name == "-":
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			keys = append(keys, configKeys(v.Field(i))...)
		case name != "":
			keys = append(keys, configKey{name: name, owner: t, field: f, value: v.Field(i)})
		}
	}
	return keys
}

// doc returns the key's doc for users, noting whether it's a list that
// combines across layers.
func (k configKey) doc(docs Docs) string {
	doc := docs.Field(k.owner, k.field)
	if k.field.Tag.Get("merge") == "union" {
		doc = strings.TrimSpace(doc + " " + unionNote)
	}
	return doc
}

// deprecated reports whether the key is deprecated.
func (k configKey) deprecated() bool {
	return k.field.Tag.Get("deprecated") != ""
}

// newEntry returns a new entry for a table keyed by name, holding its
// type's defaults.
func newEntry(t reflect.Type) reflect.Value {
	v := reflect.New(t)
	if d, ok := reflect.TypeAssert[Defaulter](v); ok {
		d.SetDefaults()
	}
	return v.Elem()
}
