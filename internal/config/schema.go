package config

import (
	"fmt"
	"reflect"
)

// schema holds the struct types machine and profile files decode into: a
// few top-level keys of their own, plus one table per module, in fields
// named by moduleField.
type schema struct {
	machine, profile reflect.Type
	// names holds each module's name, in the order modules were given.
	names []string
}

func moduleField(i int) string {
	return fmt.Sprintf("Module%d", i)
}

func newSchema(modules []Module) (*schema, error) {
	machineFields := []reflect.StructField{
		{Name: "Profiles", Type: reflect.TypeFor[[]string](), Tag: `toml:"profiles"`},
		{Name: "OS", Type: reflect.TypeFor[OS](), Tag: `toml:"os"`},
		{Name: "OSVersion", Type: reflect.TypeFor[string](), Tag: `toml:"os_version"`},
		{Name: "Arch", Type: reflect.TypeFor[Arch](), Tag: `toml:"arch"`},
	}
	profileFields := []reflect.StructField{
		{Name: "OS", Type: reflect.TypeFor[OS](), Tag: `toml:"os"`},
	}
	reserved := map[string]bool{"profiles": true, "os": true, "os_version": true, "arch": true}

	names := make([]string, 0, len(modules))
	seen := map[string]bool{}
	for i, mod := range modules {
		if mod == nil {
			return nil, fmt.Errorf("module %d is nil", i)
		}
		info := mod.Info()
		name := info.Name
		switch {
		case name == "":
			return nil, fmt.Errorf("module %d has no name", i)
		case reserved[name]:
			return nil, fmt.Errorf("module name %q is a reserved key", name)
		case seen[name]:
			return nil, fmt.Errorf("module %q is registered more than once", name)
		case len(info.OS) == 0:
			return nil, fmt.Errorf("module %q supports no operating system", name)
		}
		seen[name] = true
		names = append(names, name)

		t := reflect.TypeOf(mod.NewConfig())
		if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
			return nil, fmt.Errorf("module %q: NewConfig must return a pointer to a struct, not %v", name, t)
		}
		if err := checkStruct(t.Elem(), name); err != nil {
			return nil, fmt.Errorf("module %q: %w", name, err)
		}
		field := reflect.StructField{
			Name: moduleField(i),
			Type: t,
			Tag:  reflect.StructTag(fmt.Sprintf("toml:%q", name)),
		}
		machineFields = append(machineFields, field)
		profileFields = append(profileFields, field)
	}
	return &schema{
		machine: reflect.StructOf(machineFields),
		profile: reflect.StructOf(profileFields),
		names:   names,
	}, nil
}

// checkStruct rejects config types the loader can't read and merge
// reliably. Every key needs a toml tag, because go-toml matches untagged
// fields by name without regard to case while merging compares names
// exactly. Pointers and interfaces aren't allowed, because merging would
// replace them whole instead of merging their keys.
func checkStruct(t reflect.Type, path string) error {
	return checkFields(t, path, map[string]bool{})
}

// checkFields checks the fields of t, including those of embedded structs,
// which share t's table and so its keys.
func checkFields(t reflect.Type, path string, keys map[string]bool) error {
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name := tomlName(f)
		if name == "-" {
			continue
		}
		if f.Anonymous && name == "" {
			if f.Type.Kind() != reflect.Struct {
				return fmt.Errorf("%s: embedded field %s must be a struct", path, f.Name)
			}
			if err := checkFields(f.Type, path, keys); err != nil {
				return err
			}
			continue
		}
		if name == "" {
			return fmt.Errorf("%s: field %s has no toml tag", path, f.Name)
		}
		if keys[name] {
			return fmt.Errorf("%s: more than one field has the key %q", path, name)
		}
		keys[name] = true

		fieldPath := joinPath(path, name)
		switch rule := f.Tag.Get("merge"); rule {
		case "":
		case "union":
			if f.Type.Kind() != reflect.Slice {
				return fmt.Errorf("%s: merge:\"union\" only applies to lists, not %s", fieldPath, f.Type)
			}
		default:
			return fmt.Errorf("%s: unknown merge rule %q", fieldPath, rule)
		}
		if err := checkValueType(f.Type, fieldPath); err != nil {
			return err
		}
	}
	return nil
}

func checkValueType(t reflect.Type, path string) error {
	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Struct:
		return checkStruct(t, path)
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return fmt.Errorf("%s: tables keyed by name need string keys, not %s", path, t.Key())
		}
		return checkValueType(t.Elem(), joinPath(path, "<name>"))
	case reflect.Slice:
		return checkValueType(t.Elem(), path+"[]")
	default:
		return fmt.Errorf("%s: %s values aren't supported", path, t)
	}
}

// fieldByKey returns the field of struct t whose toml tag is exactly key,
// looking inside embedded structs too.
func fieldByKey(t reflect.Type, key string) (reflect.StructField, bool) {
	for f := range t.Fields() {
		name := tomlName(f)
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			if inner, ok := fieldByKey(f.Type, key); ok {
				return inner, true
			}
			continue
		}
		if name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// keyNames returns the keys of struct t's fields, including those of
// embedded structs.
func keyNames(t reflect.Type) []string {
	var names []string
	for f := range t.Fields() {
		name := tomlName(f)
		switch {
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			names = append(names, keyNames(f.Type)...)
		case name != "" && name != "-":
			names = append(names, name)
		}
	}
	return names
}
