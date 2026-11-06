package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	machinesDir = "machines"
	profilesDir = "profiles"
)

// ErrUnknownKey is wrapped by errors about keys no module defines.
var ErrUnknownKey = errors.New("unknown key")

// namePattern is what machine and profile names (their file names without
// .toml) must match.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Repo is a config repo.
type Repo struct {
	// FS is the repo's files, rooted at the top of the repo.
	FS fs.FS
	// Modules are the modules whose tables config files may contain.
	Modules []Module
}

// LoadMachine loads machines/<name>.toml and the profiles it lists, and
// merges them on top of each module's defaults: the profiles in the order
// listed, then the machine file. It reports every problem it finds in every
// file, not just the first.
func (r *Repo) LoadMachine(name string) (*Machine, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid machine name %q: use lower-case letters, digits, '.', '-' and '_'", name)
	}
	schema, err := newSchema(r.Modules)
	if err != nil {
		return nil, err
	}

	machineFile, err := r.readLayer(path.Join(machinesDir, name+".toml"), schema.machine)
	if err != nil {
		return nil, err
	}
	mv := machineFile.value
	m := &Machine{
		Name:      name,
		OS:        OS(mv.FieldByName("OS").String()),
		OSVersion: mv.FieldByName("OSVersion").String(),
		Arch:      Arch(mv.FieldByName("Arch").String()),
		Profiles:  stringSlice(mv.FieldByName("Profiles")),
		ProfileOS: map[string]OS{},
		Modules:   map[string]ModuleConfig{},
		Origins:   map[string][]string{},
	}

	var layers []*layer
	var errs []error
	seen := map[string]bool{}
	for _, p := range m.Profiles {
		switch {
		case !namePattern.MatchString(p):
			errs = append(errs, fmt.Errorf("%s: invalid profile name %q", machineFile.path, p))
			continue
		case seen[p]:
			errs = append(errs, fmt.Errorf("%s: profile %q is listed more than once", machineFile.path, p))
			continue
		}
		seen[p] = true
		l, err := r.readLayer(path.Join(profilesDir, p+".toml"), schema.profile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if osName := OS(l.value.FieldByName("OS").String()); osName != "" {
			m.ProfileOS[p] = osName
		}
		layers = append(layers, l)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	layers = append(layers, machineFile)

	for _, mod := range r.Modules {
		m.Modules[mod.Name()] = mod.NewConfig()
	}
	for _, l := range layers {
		mg := &merger{file: l.path, origins: m.Origins}
		for i, mod := range r.Modules {
			present, ok := l.present[mod.Name()].(map[string]any)
			if !ok {
				continue
			}
			dst := reflect.ValueOf(m.Modules[mod.Name()]).Elem()
			src := l.value.FieldByName(moduleField(i)).Elem()
			mg.mergeStruct(dst, src, present, mod.Name())
		}
	}
	return m, nil
}

// schema holds the struct types machine and profile files decode into: a
// few top-level keys of their own, plus one table per module, in fields
// named by moduleField.
type schema struct {
	machine, profile reflect.Type
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

	seen := map[string]bool{}
	for i, mod := range modules {
		name := mod.Name()
		switch {
		case reserved[name]:
			return nil, fmt.Errorf("module name %q is a reserved key", name)
		case seen[name]:
			return nil, fmt.Errorf("module %q is registered more than once", name)
		}
		seen[name] = true

		t := reflect.TypeOf(mod.NewConfig())
		if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
			return nil, fmt.Errorf("module %q: NewConfig must return a pointer to a struct, not %v", name, t)
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
	}, nil
}

// layer is one decoded profile or machine file.
type layer struct {
	path    string
	value   reflect.Value  // the decoded struct
	present map[string]any // the keys the file sets
}

func stringSlice(v reflect.Value) []string {
	s, _ := reflect.TypeAssert[[]string](v)
	return s
}

func (r *Repo) readLayer(file string, typ reflect.Type) (*layer, error) {
	data, err := fs.ReadFile(r.FS, file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s doesn't exist", file)
	}
	if err != nil {
		return nil, err
	}

	v := reflect.New(typ)
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(v.Interface()); err != nil {
		return nil, decodeError(file, typ, err)
	}
	var present map[string]any
	if err := toml.Unmarshal(data, &present); err != nil {
		return nil, decodeError(file, typ, err)
	}
	return &layer{path: file, value: v.Elem(), present: present}, nil
}

// typeMismatch matches go-toml's message for a value of the wrong type.
var typeMismatch = regexp.MustCompile(`(?s)^cannot decode TOML (\w+) into `)

// decodeError turns a TOML error into errors that start with
// file:line:column. typ is the struct the file was decoded into.
func decodeError(file string, typ reflect.Type, err error) error {
	if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		errs := make([]error, 0, len(strict.Errors))
		for i := range strict.Errors {
			e := &strict.Errors[i]
			row, col := e.Position()
			errs = append(errs, fmt.Errorf("%s:%d:%d: %w %s", file, row, col, ErrUnknownKey, strings.Join(e.Key(), ".")))
		}
		return errors.Join(errs...)
	}
	if de, ok := errors.AsType[*toml.DecodeError](err); ok {
		row, col := de.Position()
		msg := strings.TrimPrefix(de.Error(), "toml: ")
		// go-toml describes type mismatches in Go terms; describe them in
		// config terms instead.
		if m := typeMismatch.FindStringSubmatch(msg); m != nil {
			if t := fieldTypeAt(typ, de.Key()); t != nil {
				msg = fmt.Sprintf("%s must be %s, not a TOML %s", strings.Join(de.Key(), "."), describeType(t), m[1])
			}
		}
		return fmt.Errorf("%s:%d:%d: %s", file, row, col, msg)
	}
	return fmt.Errorf("%s: %w", file, err)
}

// fieldTypeAt returns the type of the value at key in typ, following toml
// tags, embedded structs and tables keyed by name. It returns nil if key
// doesn't lead anywhere.
func fieldTypeAt(typ reflect.Type, key []string) reflect.Type {
	t := typ
	for _, k := range key {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Map:
			t = t.Elem()
		case reflect.Struct:
			f, ok := fieldByKey(t, k)
			if !ok {
				return nil
			}
			t = f.Type
		default:
			return nil
		}
	}
	return t
}

func fieldByKey(t reflect.Type, key string) (reflect.StructField, bool) {
	for f := range t.Fields() {
		name := tomlName(f)
		if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
			if inner, ok := fieldByKey(f.Type, key); ok {
				return inner, true
			}
			continue
		}
		if name == key || (name == "" && f.Name == key) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// describeType describes what a config value of type t looks like.
func describeType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string in quotes"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.String {
			return "a list of strings"
		}
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a table"
	default:
		return "a " + t.String()
	}
}
