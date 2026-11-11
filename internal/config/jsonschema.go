package config

import (
	"bytes"
	"encoding/json"
	"reflect"
)

// fileKey is a key machine and profile files have besides module tables.
type fileKey struct {
	key, doc string
	typ      reflect.Type
	enum     []string
}

// fileKeys returns the keys machine and profile files have besides module
// tables.
func fileKeys() []fileKey {
	var osNames, archNames []string
	for _, o := range AllOS() {
		osNames = append(osNames, string(o))
	}
	for _, a := range []Arch{AMD64, ARM64} {
		archNames = append(archNames, string(a))
	}
	return []fileKey{
		{
			key: "profiles",
			typ: reflect.TypeFor[[]string](),
			doc: "Machine files only. The profiles to apply before the machine file, in order, by their file names in profiles/ without .toml.",
		},
		{
			key:  "os",
			typ:  reflect.TypeFor[string](),
			enum: osNames,
			doc:  `The operating system: "fedora", "ubuntu" or "windows". Every machine file sets it. A profile that sets it can only be used by machines with that OS.`,
		},
		{
			key: "os_version",
			typ: reflect.TypeFor[string](),
			doc: `Machine files only, optional. The OS version, such as "44" or "26.04". maisys refuses to run on any other version.`,
		},
		{
			key:  "arch",
			typ:  reflect.TypeFor[string](),
			enum: archNames,
			doc:  `Linux machine files only, optional. The CPU architecture: "amd64" or "arm64". maisys refuses to run on any other architecture.`,
		},
	}
}

// jsonSchema is a JSON schema, or a schema for one value in it.
type jsonSchema struct {
	Schema               string         `json:"$schema,omitempty"`
	ID                   string         `json:"$id,omitempty"`
	Title                string         `json:"title,omitempty"`
	Description          string         `json:"description,omitempty"`
	Deprecated           bool           `json:"deprecated,omitempty"`
	Type                 string         `json:"type,omitempty"`
	Enum                 []string       `json:"enum,omitempty"`
	Items                *jsonSchema    `json:"items,omitempty"`
	Properties           jsonProperties `json:"properties,omitempty"`
	AdditionalProperties any            `json:"additionalProperties,omitempty"`
	Default              any            `json:"default,omitempty"`
}

// jsonProperties are an object's properties, in order.
type jsonProperties []jsonProperty

type jsonProperty struct {
	name   string
	schema *jsonSchema
}

// MarshalJSON writes the properties as a JSON object, keeping their order.
func (ps jsonProperties) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range ps {
		if i > 0 {
			b.WriteByte(',')
		}
		name, err := marshalJSON(p.name)
		if err != nil {
			return nil, err
		}
		schema, err := marshalJSON(p.schema)
		if err != nil {
			return nil, err
		}
		b.Write(name)
		b.WriteByte(':')
		b.Write(schema)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalJSON is json.Marshal without escaping <, > and &, which docs may
// contain.
func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// Schema returns a JSON schema (draft 2020-12) for machine and profile
// files, which editors use to complete and check keys. id is the URL the
// schema is published at. Every module has a property, including hidden
// ones, since configs may use them.
func Schema(modules []Module, docs Docs, id string) ([]byte, error) {
	if _, err := newSchema(modules); err != nil {
		return nil, err
	}
	root := &jsonSchema{
		Schema:               "https://json-schema.org/draft/2020-12/schema",
		ID:                   id,
		Title:                "maisys config",
		Description:          "A maisys machine file (machines/<name>.toml) or profile (profiles/<name>.toml).",
		Type:                 "object",
		AdditionalProperties: false,
	}
	for _, k := range fileKeys() {
		s := valueSchema(reflect.New(k.typ).Elem(), docs)
		s.Description = k.doc
		s.Enum = k.enum
		s.Default = nil
		root.Properties = append(root.Properties, jsonProperty{k.key, s})
	}
	for _, mod := range modules {
		info := mod.Info()
		s := valueSchema(reflect.Indirect(reflect.ValueOf(mod.NewConfig())), docs)
		s.Description = info.Summary
		root.Properties = append(root.Properties, jsonProperty{info.Name, s})
	}

	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// valueSchema returns the schema for a config value like v. Single values
// and lists get v as their default.
func valueSchema(v reflect.Value, docs Docs) *jsonSchema {
	t := v.Type()
	switch t.Kind() {
	case reflect.Struct:
		s := &jsonSchema{Type: "object", AdditionalProperties: false}
		for _, k := range configKeys(v) {
			ks := valueSchema(k.value, docs)
			ks.Description = k.doc(docs)
			ks.Deprecated = k.deprecated()
			s.Properties = append(s.Properties, jsonProperty{k.name, ks})
		}
		return s
	case reflect.Map:
		entry := valueSchema(newEntry(t.Elem()), docs)
		entry.Default = nil
		return &jsonSchema{Type: "object", AdditionalProperties: entry}
	case reflect.Slice, reflect.Array:
		items := valueSchema(reflect.New(t.Elem()).Elem(), docs)
		items.Default = nil
		s := &jsonSchema{Type: "array", Items: items, Default: v.Interface()}
		if v.Len() == 0 {
			s.Default = []any{}
		}
		return s
	case reflect.Bool:
		return &jsonSchema{Type: "boolean", Default: v.Interface()}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &jsonSchema{Type: "integer", Default: v.Interface()}
	case reflect.Float32, reflect.Float64:
		return &jsonSchema{Type: "number", Default: v.Interface()}
	default:
		return &jsonSchema{Type: "string", Default: v.Interface()}
	}
}
