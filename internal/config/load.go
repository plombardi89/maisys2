package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
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

	for i, mod := range r.Modules {
		m.Modules[schema.names[i]] = freshDefaults(mod)
	}
	for _, l := range layers {
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
	if unknown := unknownKeys(typ, present, nil); len(unknown) > 0 {
		return nil, unknownKeyErrors(file, data, unknown)
	}
	return &layer{path: file, value: v.Elem(), present: present}, nil
}
