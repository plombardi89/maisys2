package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"regexp"

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

// NameRule describes the names machines and profiles can have.
const NameRule = "use lower-case letters, digits, '.', '-' and '_'"

// ValidName reports whether name can name a machine or a profile: it's
// made of lower-case letters, digits, '.', '-' and '_', and starts with a
// letter or a digit.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// Repo is a config repo.
type Repo struct {
	// FS is the repo's files, rooted at the top of the repo.
	FS fs.FS
	// Modules are the modules whose tables config files may contain.
	Modules []Module
	// URL is the repo's own URL, the one this machine clones it from, if
	// known. Validation uses it to check that new WSL distros can clone
	// the repo.
	URL string
}

// machineKeys are the keys a machine file has besides module tables.
var machineKeys = []string{"os", "os_version", "arch", "profiles"}

// LoadMachine loads machines/<name>.toml and the profiles it lists, and
// merges them on top of each module's defaults: the profiles in the order
// listed, then the machine file. It reports every problem it finds in every
// file, not just the first.
func (r *Repo) LoadMachine(name string) (*Machine, error) {
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid machine name %q: %s", name, NameRule)
	}
	schema, err := newSchema(r.Modules)
	if err != nil {
		return nil, err
	}

	machineFile, err := r.readLayer(MachineFile(name), schema.machine)
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
		locs:      map[string]*locations{},
	}
	for _, key := range machineKeys {
		if _, ok := machineFile.present[key]; ok {
			m.Origins[key] = []string{machineFile.path}
		}
	}

	var layers []*layer
	var errs []error
	seen := map[string]bool{}
	for _, p := range m.Profiles {
		at := machineFile.locs.whereItem(machineFile.path, "profiles", p)
		switch {
		case !namePattern.MatchString(p):
			errs = append(errs, fmt.Errorf("%s: invalid profile name %q: %s", at, p, NameRule))
			continue
		case seen[p]:
			errs = append(errs, fmt.Errorf("%s: profile %q is listed more than once", at, p))
			continue
		}
		seen[p] = true
		l, err := r.readLayer(profileFile(p), schema.profile)
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

	for i, mod := range r.Modules {
		m.Modules[schema.names[i]] = freshDefaults(mod)
	}
	for _, l := range layers {
		m.layers = append(m.layers, l.path)
		m.locs[l.path] = l.locs
		mg := &merger{file: l.path, origins: m.Origins}
		for i, name := range schema.names {
			present, ok := l.present[name].(map[string]any)
			if !ok {
				continue
			}
			dst := reflect.ValueOf(m.Modules[name]).Elem()
			src := l.value.FieldByName(moduleField(i)).Elem()
			mg.mergeStruct(dst, src, present, name)
		}
	}
	return m, nil
}

// freshDefaults returns a deep copy of the module's defaults, so merging
// never writes into a struct, map or slice that NewConfig might share.
func freshDefaults(mod Module) ModuleConfig {
	defaults := reflect.ValueOf(mod.NewConfig())
	fresh := reflect.New(defaults.Type().Elem())
	fresh.Elem().Set(deepCopy(defaults.Elem()))
	cfg, _ := reflect.TypeAssert[ModuleConfig](fresh)
	return cfg
}

// layer is one decoded profile or machine file.
type layer struct {
	path    string
	value   reflect.Value  // the decoded struct
	present map[string]any // the keys the file sets
	locs    *locations     // where it sets them
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
	present, err := decodeStrict(file, data, v)
	if err != nil {
		return nil, err
	}
	return &layer{path: file, value: v.Elem(), present: present, locs: locate(data)}, nil
}

// decodeStrict decodes data, read from file, into ptr, a pointer to a
// struct. Keys the struct has no field for are errors, including keys that
// only differ from a field's in case. It returns the keys data sets.
func decodeStrict(file string, data []byte, ptr reflect.Value) (map[string]any, error) {
	typ := ptr.Type().Elem()
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(ptr.Interface()); err != nil {
		return nil, decodeError(file, typ, err)
	}
	var present map[string]any
	if err := toml.Unmarshal(data, &present); err != nil {
		return nil, decodeError(file, typ, err)
	}
	if unknown := unknownKeys(typ, present, nil); len(unknown) > 0 {
		return nil, unknownKeyErrors(file, data, unknown)
	}
	return present, nil
}
