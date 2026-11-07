package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// Target is a path a module writes, for the check that no two modules, or
// two parts of one, write the same path.
type Target struct {
	// Path is the path in your home folder, such as "~/.zshrc". A path
	// ending in "/" is a directory, and everything inside it belongs to the
	// target.
	Path string
	// Key is the dotted config path that makes the module write it, such as
	// "shell.zsh.manage_rc". Errors about the target point at it.
	Key string
}

// Targeter is implemented by module configs that write files, so validation
// can check that nothing else writes them. Only enabled modules are asked.
type Targeter interface {
	// Targets returns the paths the module writes on machine m.
	Targets(m *Machine) []Target
}

// Checker is implemented by module configs with rules of their own. Only
// enabled modules are checked.
type Checker interface {
	// Check reports the problems it finds with c.Errorf.
	Check(c *Check)
}

// Check is what a Checker checks, and collects the problems it finds.
type Check struct {
	// Machine is the machine being validated.
	Machine *Machine
	// Repo is the config repo the machine comes from.
	Repo *Repo
	// Module is the name of the module being checked.
	Module string

	errs []error
}

// Errorf reports a problem with the value at key, a dotted path inside the
// module's table (see JoinKey), such as "items.nvim.source". The error
// starts with where the value was set, then the module's name and key, then
// the message, so the message should carry on from the key:
//
//	c.Errorf("login", "must be bash, zsh or fish, not %q", login)
//
// reports "profiles/base.toml:3:9: shell.login must be bash, zsh or fish,
// not "sh"".
func (c *Check) Errorf(key, format string, a ...any) {
	full := c.key(key)
	c.errs = append(c.errs, fmt.Errorf("%s: %s %s", c.Machine.locate(full), full, fmt.Sprintf(format, a...)))
}

// ItemErrorf is like Errorf for one item of the list at key, and points at
// where that item was added to the list.
func (c *Check) ItemErrorf(key, item, format string, a ...any) {
	full := c.key(key)
	at := c.Machine.WhereItem(full, item)
	if at == "" {
		at = c.Machine.locate(full)
	}
	c.errs = append(c.errs, fmt.Errorf("%s: %s %s", at, full, fmt.Sprintf(format, a...)))
}

// NoEmptyItems reports an empty string in the list at key.
func (c *Check) NoEmptyItems(key string, items []string) {
	for _, item := range items {
		if strings.TrimSpace(item) == "" {
			c.ItemErrorf(key, item, "has an empty entry")
			return
		}
	}
}

// key returns the full dotted path of key.
func (c *Check) key(key string) string {
	if key == "" {
		return JoinKey(c.Module)
	}
	return JoinKey(c.Module) + "." + key
}

// Validate loads a machine, as LoadMachine does, and checks it against
// every rule that needs no live system: its os, os_version and arch, the
// os its profiles declare, whether its enabled modules support it, their
// versions, that no two of them write the same path, and each module's own
// rules. It returns the machine and every problem it finds, joined. Each
// problem starts with where the value at fault was set, as
// "file:line:column". If the machine doesn't load, Validate returns the
// error from LoadMachine and no machine.
func (r *Repo) Validate(name string) (*Machine, error) {
	m, err := r.LoadMachine(name)
	if err != nil {
		return nil, err
	}
	v := &validator{repo: r, m: m}
	v.machine()
	v.profiles()
	v.modules()
	v.ownership()
	return m, errors.Join(v.errs...)
}

// MachineNames returns the names of the machine files in the repo, sorted.
// Files whose names aren't valid machine names are left out; ValidateAll
// reports them.
func (r *Repo) MachineNames() ([]string, error) {
	names, _, err := r.names(machinesDir)
	return names, err
}

// ValidateAll validates every machine in the repo, and reads every profile,
// including those no machine lists. It returns every problem it finds,
// each reported once, even if it affects several machines.
func (r *Repo) ValidateAll() error {
	schema, err := newSchema(r.Modules)
	if err != nil {
		return err
	}
	machines, badMachines, err := r.names(machinesDir)
	if err != nil {
		return err
	}
	profiles, badProfiles, err := r.names(profilesDir)
	if err != nil {
		return err
	}

	var errs []error
	for _, name := range badMachines {
		errs = append(errs, fmt.Errorf("%s: invalid machine name %q: %s", MachineFile(name), name, nameRule))
	}
	if len(machines) == 0 && len(badMachines) == 0 {
		errs = append(errs, fmt.Errorf("%s/ has no machine files", machinesDir))
	}
	for _, name := range machines {
		_, err := r.Validate(name)
		errs = append(errs, err)
	}
	for _, name := range badProfiles {
		errs = append(errs, fmt.Errorf("%s: invalid profile name %q: %s", profileFile(name), name, nameRule))
	}
	for _, name := range profiles {
		file := profileFile(name)
		l, err := r.readLayer(file, schema.profile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if osName := OS(l.value.FieldByName("OS").String()); osName != "" && !validOS(osName) {
			errs = append(errs, fmt.Errorf("%s: %s", l.locs.where(file, "os"), invalidOS(osName)))
		}
	}
	return errors.Join(unique(errs)...)
}

// names returns the names of the .toml files in dir, sorted, split into
// valid and invalid names.
func (r *Repo) names(dir string) (valid, invalid []string, err error) {
	entries, err := fs.ReadDir(r.FS, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".toml")
		switch {
		case !ok || e.IsDir():
		case namePattern.MatchString(name):
			valid = append(valid, name)
		default:
			invalid = append(invalid, name)
		}
	}
	slices.Sort(valid)
	return valid, invalid, nil
}

// unique flattens joined errors, and drops errors with the same message as
// an earlier one.
func unique(errs []error) []error {
	var out []error
	seen := map[string]bool{}
	var add func(err error)
	add = func(err error) {
		if err == nil {
			return
		}
		// Only split errors that are themselves joined, not errors that
		// wrap one.
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, e := range joined.Unwrap() {
				add(e)
			}
			return
		}
		if !seen[err.Error()] {
			seen[err.Error()] = true
			out = append(out, err)
		}
	}
	for _, err := range errs {
		add(err)
	}
	return out
}

// osVersions describes the os_version each OS takes.
var osVersions = map[OS]struct {
	pattern       *regexp.Regexp
	what, example string
}{
	Fedora:  {regexp.MustCompile(`^[0-9]+$`), "a Fedora release", "44"},
	Ubuntu:  {regexp.MustCompile(`^[0-9]{2}\.[0-9]{2}$`), "an Ubuntu release", "26.04"},
	Windows: {regexp.MustCompile(`^[0-9]+$`), "a Windows build number", "26100"},
}

// validator checks one machine, collecting the problems it finds.
type validator struct {
	repo *Repo
	m    *Machine
	errs []error
}

func (v *validator) errorf(at, format string, a ...any) {
	v.errs = append(v.errs, fmt.Errorf("%s: %s", at, fmt.Sprintf(format, a...)))
}

// machine checks the machine's own keys.
func (v *validator) machine() {
	m := v.m
	switch {
	case m.OS == "":
		v.errorf(m.file(), "os is missing: set it to %s", either(AllOS()))
	case !validOS(m.OS):
		v.errorf(m.locate("os"), "%s", invalidOS(m.OS))
	case m.OSVersion != "":
		if f := osVersions[m.OS]; !f.pattern.MatchString(m.OSVersion) {
			v.errorf(m.locate("os_version"), "os_version must be %s such as %q, not %q", f.what, f.example, m.OSVersion)
		}
	default:
	}
	switch {
	case m.Arch == "":
	case m.OS == Windows:
		v.errorf(m.locate("arch"), "arch can only be set on Linux machines: Windows hosts are always %s", AMD64)
	case !slices.Contains(AllArch(), m.Arch):
		v.errorf(m.locate("arch"), "arch must be %s, not %q", either(AllArch()), m.Arch)
	default:
	}
}

// profiles checks the os each profile declares.
func (v *validator) profiles() {
	m := v.m
	for _, p := range m.Profiles {
		profileOS, ok := m.ProfileOS[p]
		if !ok {
			continue
		}
		at := m.whereIn(profileFile(p), "os")
		switch {
		case !validOS(profileOS):
			v.errorf(at, "%s", invalidOS(profileOS))
		case validOS(m.OS) && profileOS != m.OS:
			v.errorf(m.WhereItem("profiles", p), "profile %q sets os = %q (%s), so a %s machine can't use it", p, profileOS, at, m.OS)
		default:
		}
	}
}

// modules checks every enabled module: that it supports the machine, its
// version, and its own rules.
func (v *validator) modules() {
	m := v.m
	for _, mod := range v.repo.Modules {
		info := mod.Info()
		name := info.Name
		cfg := m.Modules[name]
		if cfg == nil || !cfg.IsEnabled() {
			continue
		}
		if validOS(m.OS) && !slices.Contains(info.OS, m.OS) {
			v.errorf(v.enabledAt(name), "%s only works on %s, but machine %q is %s", name, either(info.OS), m.Name, m.OS)
		}
		if m.OS != Windows && slices.Contains(AllArch(), m.Arch) && len(info.Arch) > 0 && !slices.Contains(info.Arch, m.Arch) {
			v.errorf(v.enabledAt(name), "%s only works on %s, but machine %q is %s", name, either(info.Arch), m.Name, m.Arch)
		}
		if version, ok := versionOf(cfg); ok && version != Latest && !IsExactVersion(version) {
			key := JoinKey(name, "version")
			v.errorf(m.locate(key), "%s must be %q or an exact version such as \"1.2.3\", not %q", key, Latest, version)
		}
		if checker, ok := cfg.(Checker); ok {
			c := &Check{Machine: m, Repo: v.repo, Module: name}
			checker.Check(c)
			v.errs = append(v.errs, c.errs...)
		}
	}
}

// enabledAt returns where a module was turned on: where its enabled key was
// set, or else the first key set in its table.
func (v *validator) enabledAt(name string) string {
	m := v.m
	if at := m.Where(JoinKey(name, "enabled")); at != "" {
		return at
	}
	prefix := JoinKey(name) + "."
	for _, key := range slices.Sorted(maps.Keys(m.Origins)) {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if at := m.Where(key); at != "" {
			return at
		}
	}
	return m.locate(JoinKey(name))
}

// ownership checks that no two targets of the enabled modules are the same
// path, and that none is inside a directory another one writes.
func (v *validator) ownership() {
	m := v.m
	type claim struct {
		Target
		clean string
		dir   bool
	}
	var claims []claim
	for _, mod := range v.repo.Modules {
		cfg := m.Modules[mod.Info().Name]
		targeter, ok := cfg.(Targeter)
		if !ok || !cfg.IsEnabled() {
			continue
		}
		for _, t := range targeter.Targets(m) {
			clean := path.Clean(t.Path)
			if m.OS == Windows {
				// Windows paths ignore case.
				clean = strings.ToLower(clean)
			}
			claims = append(claims, claim{Target: t, clean: clean, dir: strings.HasSuffix(t.Path, "/")})
		}
	}

	for i, b := range claims {
		for _, a := range claims[:i] {
			switch {
			case a.clean == b.clean:
				v.errorf(v.firstWhere(b.Key, a.Key), "%s and %s both write %s on machine %q", a.Key, b.Key, b.Path, m.Name)
			case a.dir && inside(b.clean, a.clean):
				v.errorf(v.firstWhere(b.Key, a.Key), "%s (%s) is inside %s (%s) on machine %q: only one of them can write it", b.Key, b.Path, a.Key, a.Path, m.Name)
			case b.dir && inside(a.clean, b.clean):
				v.errorf(v.firstWhere(a.Key, b.Key), "%s (%s) is inside %s (%s) on machine %q: only one of them can write it", a.Key, a.Path, b.Key, b.Path, m.Name)
			default:
			}
		}
	}
}

// firstWhere returns where the first of keys that a file sets was set, or
// else where to report a problem with the first key.
func (v *validator) firstWhere(keys ...string) string {
	for _, key := range keys {
		if at := v.m.Where(key); at != "" {
			return at
		}
	}
	return v.m.locate(keys[0])
}

// inside reports whether the clean path p is inside the directory dir.
func inside(p, dir string) bool {
	return strings.HasPrefix(p, dir+"/")
}

var versionedType = reflect.TypeFor[Versioned]()

// versionOf returns the version of a module config that embeds Versioned.
func versionOf(cfg ModuleConfig) (string, bool) {
	v := reflect.Indirect(reflect.ValueOf(cfg))
	if v.Kind() != reflect.Struct {
		return "", false
	}
	return embeddedVersion(v)
}

func embeddedVersion(v reflect.Value) (string, bool) {
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if !f.Anonymous || f.Type.Kind() != reflect.Struct {
			continue
		}
		if versioned, ok := reflect.TypeAssert[Versioned](v.Field(i)); ok && f.Type == versionedType {
			return versioned.Version, true
		}
		if version, ok := embeddedVersion(v.Field(i)); ok {
			return version, true
		}
	}
	return "", false
}

func validOS(os OS) bool {
	return slices.Contains(AllOS(), os)
}

func invalidOS(os OS) string {
	return fmt.Sprintf("os must be %s, not %q", either(AllOS()), os)
}

// either lists items as "a, b or c".
func either[T ~string](items []T) string {
	s := make([]string, len(items))
	for i, item := range items {
		s[i] = string(item)
	}
	if len(s) < 2 {
		return strings.Join(s, "")
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}
