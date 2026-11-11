package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// FormatVersion is the settings file format this maisys reads and writes.
const FormatVersion = 1

// header starts every settings file maisys writes.
const header = `# Local settings for maisys on this machine: the config repo it syncs from
# and the machine file it uses. maisys writes this file.

`

// ErrNoSettings is wrapped by Load's error when the settings file doesn't
// exist. It wraps fs.ErrNotExist.
var ErrNoSettings error = noSettingsError{}

type noSettingsError struct{}

func (noSettingsError) Error() string { return "no local settings" }
func (noSettingsError) Unwrap() error { return fs.ErrNotExist }

// Settings are a machine's local settings: which config repo it syncs from,
// and which machine file in that repo describes it.
type Settings struct {
	// FormatVersion is the version of the file's format. Save sets it to
	// the current FormatVersion.
	FormatVersion int `toml:"format_version"`
	// RepoDir is the folder the config repo is cloned into.
	RepoDir string `toml:"repo_dir"`
	// RepoURL is the URL the config repo is pulled from, or "" for a clone
	// without a remote.
	RepoURL string `toml:"repo_url"`
	// Machine is the name of the machine file (machines/<name>.toml) that
	// describes this machine.
	Machine string `toml:"machine"`
}

// migration upgrades a settings table from one format version to the next.
type migration func(table map[string]any) error

// migrations[n] upgrades a settings table from format version n to n+1.
var migrations = map[int]migration{}

// Load reads the local settings from path. If the file is in an older
// format, Load migrates it and saves it in the current format, after
// keeping a copy of the old file next to it as <name>.v<N>.bak.
func Load(path string) (*Settings, error) {
	return load(path, FormatVersion, migrations, true)
}

// Read reads the local settings from path as Load does, but never writes
// anything: a file in an older format is only migrated in memory. It's for
// commands that only report, such as status, which don't take the lock and
// so mustn't replace a file a sync may be saving.
func Read(path string) (*Settings, error) {
	return load(path, FormatVersion, migrations, false)
}

// load is Load, or Read unless save is set, for format version current and
// the migrations in steps.
func load(path string, current int, steps map[int]migration, save bool) (*Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s doesn't exist", ErrNoSettings, path)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the local settings: %w", err)
	}

	var head struct {
		FormatVersion *int `toml:"format_version"`
	}
	if err := toml.Unmarshal(data, &head); err != nil {
		return nil, decodeError(path, err, true)
	}
	switch {
	case head.FormatVersion == nil:
		return nil, fmt.Errorf("%s: format_version is missing", path)
	case *head.FormatVersion > current:
		return nil, fmt.Errorf("%s was written by a newer version of maisys (format_version %d; this version reads up to %d): update maisys and try again",
			path, *head.FormatVersion, current)
	case *head.FormatVersion < current:
		return migrate(path, data, *head.FormatVersion, current, steps, save)
	}

	s := &Settings{}
	if err := decodeStrict(data, s); err != nil {
		return nil, decodeError(path, err, true)
	}
	return s, nil
}

// migrate upgrades data, the contents of path, from format version from to
// current. With save, it saves the result after keeping a copy of the old
// file.
func migrate(path string, data []byte, from, current int, steps map[int]migration, save bool) (*Settings, error) {
	var table map[string]any
	if err := toml.Unmarshal(data, &table); err != nil {
		return nil, decodeError(path, err, true)
	}
	for v := from; v < current; v++ {
		m, ok := steps[v]
		if !ok {
			return nil, fmt.Errorf("%s: format_version %d isn't a format maisys knows", path, from)
		}
		if err := m(table); err != nil {
			return nil, fmt.Errorf("%s: migrating from format_version %d to %d: %w", path, v, v+1, err)
		}
		table["format_version"] = int64(v + 1)
	}
	migrated, err := toml.Marshal(table)
	if err != nil {
		return nil, fmt.Errorf("%s: migrating to format_version %d: %w", path, current, err)
	}
	s := &Settings{}
	if err := decodeStrict(migrated, s); err != nil {
		// Line numbers would refer to the migrated file, which isn't saved.
		return nil, decodeError(fmt.Sprintf("%s, migrated to format_version %d", path, current), err, false)
	}
	if !save {
		return s, nil
	}

	if err := backup(path, data, from); err != nil {
		return nil, err
	}
	if err := s.write(path); err != nil {
		return nil, err
	}
	return s, nil
}

// backup keeps a copy of data, the contents of path in format version
// version, unless there already is one.
func backup(path string, data []byte, version int) error {
	name := fmt.Sprintf("%s.v%d.bak", path, version)
	if _, err := os.Lstat(name); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("keeping a copy of the old local settings: %w", err)
	}
	if err := writeFile(name, data); err != nil {
		return fmt.Errorf("keeping a copy of the old local settings: %w", err)
	}
	return nil
}

// Save writes the settings to path in the current format, creating its
// folder if needed. The file is replaced in one step, so it's never left
// half written, and only you can read it.
func (s *Settings) Save(path string) error {
	s.FormatVersion = FormatVersion
	return s.write(path)
}

func (s *Settings) write(path string) error {
	data, err := toml.Marshal(s)
	if err != nil {
		return fmt.Errorf("saving the local settings: %w", err)
	}
	if err := writeFile(path, append([]byte(header), data...)); err != nil {
		return fmt.Errorf("saving the local settings: %w", err)
	}
	return nil
}

// writeFile writes data to a temporary file next to name, then renames it
// over name. It creates name's folder (mode 0700) if needed. The file has
// mode 0600.
func writeFile(name string, data []byte) (err error) {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(name)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}

func decodeStrict(data []byte, s *Settings) error {
	return toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(s)
}

// typeMismatch matches go-toml's message for a value of the wrong type.
var typeMismatch = regexp.MustCompile(`(?s)^cannot decode TOML (\w+) into `)

// decodeError turns a TOML error into errors that start with
// file:line:column, or with just file if positions is false.
func decodeError(file string, err error, positions bool) error {
	at := func(row, col int) string {
		if !positions {
			return file
		}
		return fmt.Sprintf("%s:%d:%d", file, row, col)
	}
	if strict, ok := errors.AsType[*toml.StrictMissingError](err); ok {
		errs := make([]error, 0, len(strict.Errors))
		for i := range strict.Errors {
			e := &strict.Errors[i]
			errs = append(errs, fmt.Errorf("%s: unknown key %s", at(e.Position()), strings.Join(e.Key(), ".")))
		}
		return errors.Join(errs...)
	}
	if de, ok := errors.AsType[*toml.DecodeError](err); ok {
		msg := strings.TrimPrefix(de.Error(), "toml: ")
		// go-toml describes type mismatches in Go terms; describe them in
		// settings terms instead.
		if m := typeMismatch.FindStringSubmatch(msg); m != nil {
			key := strings.Join(de.Key(), ".")
			if want := describeKey(key); want != "" {
				msg = fmt.Sprintf("%s must be %s, not a TOML %s", key, want, m[1])
			}
		}
		return fmt.Errorf("%s: %s", at(de.Position()), msg)
	}
	return fmt.Errorf("%s: %w", file, err)
}

// describeKey describes what a settings key holds, or returns "" if there's
// no such key.
func describeKey(key string) string {
	for f := range reflect.TypeFor[Settings]().Fields() {
		if f.Tag.Get("toml") != key {
			continue
		}
		switch f.Type.Kind() {
		case reflect.Int:
			return "a whole number"
		case reflect.String:
			return "a string in quotes"
		default:
			return ""
		}
	}
	return ""
}
