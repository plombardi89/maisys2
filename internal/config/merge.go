package config

import (
	"reflect"
	"strings"
)

// merger copies the values one layer sets into the merged configuration,
// recording where they came from.
type merger struct {
	file    string
	origins map[string][]string
}

// mergeStruct merges the fields of src that the layer sets (the keys in
// present) into dst, which must be addressable.
func (m *merger) mergeStruct(dst, src reflect.Value, present map[string]any, prefix string) {
	t := dst.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := tomlName(f)
		if name == "-" {
			continue
		}
		// Embedded structs without a key of their own (like Toggle) add
		// their fields to this table.
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			m.mergeStruct(dst.Field(i), src.Field(i), present, prefix)
			continue
		}
		if name == "" {
			name = f.Name
		}
		value, ok := present[name]
		if !ok {
			continue
		}
		m.mergeField(dst.Field(i), src.Field(i), f, value, joinPath(prefix, name))
	}
}

func (m *merger) mergeField(dst, src reflect.Value, f reflect.StructField, present any, path string) {
	switch dst.Kind() {
	case reflect.Struct:
		sub, _ := present.(map[string]any)
		m.mergeStruct(dst, src, sub, path)
	case reflect.Map:
		sub, _ := present.(map[string]any)
		m.mergeMap(dst, src, sub, path)
	case reflect.Slice:
		if f.Tag.Get("merge") == "union" {
			m.union(dst, src, path)
			return
		}
		m.replace(dst, src, path)
	default:
		m.replace(dst, src, path)
	}
}

func (m *merger) replace(dst, src reflect.Value, path string) {
	dst.Set(src)
	m.origins[path] = []string{m.file}
}

// mergeMap merges a table keyed by name entry by entry. Entries that are
// tables themselves are merged key by key, starting from their defaults if
// they're new.
func (m *merger) mergeMap(dst, src reflect.Value, present map[string]any, path string) {
	if dst.IsNil() {
		dst.Set(reflect.MakeMap(dst.Type()))
	}
	elemType := dst.Type().Elem()
	for _, key := range src.MapKeys() {
		entryPath := joinPath(path, key.String())
		srcEntry := src.MapIndex(key)
		if elemType.Kind() != reflect.Struct {
			dst.SetMapIndex(key, srcEntry)
			m.origins[entryPath] = []string{m.file}
			continue
		}

		entry := reflect.New(elemType).Elem()
		if cur := dst.MapIndex(key); cur.IsValid() {
			entry.Set(cur)
		} else if d, ok := reflect.TypeAssert[Defaulter](entry.Addr()); ok {
			d.SetDefaults()
		}
		sub, _ := present[key.String()].(map[string]any)
		m.mergeStruct(entry, srcEntry, sub, entryPath)
		dst.SetMapIndex(key, entry)
	}
}

// union appends the items of src that dst doesn't have yet.
func (m *merger) union(dst, src reflect.Value, path string) {
	if src.Len() == 0 {
		return
	}
	for i := range src.Len() {
		item := src.Index(i)
		if !containsValue(dst, item) {
			dst.Set(reflect.Append(dst, item))
		}
	}
	m.origins[path] = append(m.origins[path], m.file)
}

func containsValue(list, item reflect.Value) bool {
	for i := range list.Len() {
		if reflect.DeepEqual(list.Index(i).Interface(), item.Interface()) {
			return true
		}
	}
	return false
}

// tomlName returns the key name from a field's toml tag.
func tomlName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
	return name
}

// joinPath appends key to a dotted path, quoting it if it contains a dot.
func joinPath(prefix, key string) string {
	if strings.ContainsAny(key, ". ") {
		key = `"` + key + `"`
	}
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
