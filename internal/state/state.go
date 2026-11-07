// Package state keeps the state file, which records what maisys manages on
// this machine, and the lock that stops two syncs running at once.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

// FormatVersion is the state file format this maisys reads and writes.
const FormatVersion = 1

// Result is how a sync ended.
type Result string

// The ways a sync can end.
const (
	// ResultOK means the sync finished and every step worked.
	ResultOK Result = "ok"
	// ResultFailed means the sync finished, but some steps failed.
	ResultFailed Result = "failed"
	// ResultInterrupted means the sync stopped partway, for example after
	// Ctrl-C, or for a reboot that a change needs (on Windows).
	ResultInterrupted Result = "interrupted"
)

// Record is what the state file keeps about one thing maisys manages:
// enough to remove it once it's taken out of the config, even after its
// module is turned off or the machine switches to another config repo.
type Record struct {
	// ID is the resource's stable key, such as "tool:kubectl" or
	// "file:~/.gitconfig".
	ID string `json:"id"`
	// Kind is the kind of resource, such as "tool", "file" or
	// "package-set". It decides how the thing is removed.
	Kind string `json:"kind"`
	// Module is the module the resource came from.
	Module string `json:"module"`
	// Paths are the files and folders the resource owns.
	Paths []string `json:"paths,omitempty"`
	// Details are facts that depend on the kind, such as a version, the
	// sha256 of a file maisys wrote, a git ref or a unit name.
	Details map[string]string `json:"details,omitempty"`
	// TookOver is true when the target existed before maisys managed it.
	// A file maisys took over is left in place when it's taken out of the
	// config.
	TookOver bool `json:"took_over,omitempty"`
	// Updated is when maisys last applied the resource.
	Updated time.Time `json:"updated,omitzero"`
}

// clone returns a copy of r that shares no slices or maps with it. Empty
// Paths and Details become nil, as they are after a round trip through the
// file.
func (r Record) clone() Record {
	paths, details := r.Paths, r.Details
	r.Paths, r.Details = nil, nil
	if len(paths) > 0 {
		r.Paths = slices.Clone(paths)
	}
	if len(details) > 0 {
		r.Details = maps.Clone(details)
	}
	return r
}

// State is what maisys manages on this machine, as recorded in the state
// file. Get, Put, Delete, IDs and Save are safe for concurrent use, so
// modules applied in parallel can share a State. The exported fields
// aren't guarded: set them while nothing else is using the State.
type State struct {
	// FormatVersion is the version of the file's format. Save sets it to
	// the current FormatVersion.
	FormatVersion int
	// Machine is the machine file the last sync used.
	Machine string
	// LastSync is when the last sync ended, or the zero time if there
	// hasn't been one.
	LastSync time.Time
	// LastSyncResult is how the last sync ended.
	LastSyncResult Result

	mu      sync.Mutex
	records map[string]Record
}

// file is the state file's layout.
type file struct {
	FormatVersion  int       `json:"format_version"`
	Machine        string    `json:"machine"`
	LastSync       time.Time `json:"last_sync,omitzero"`
	LastSyncResult Result    `json:"last_sync_result,omitempty"`
	Records        []Record  `json:"records"`
}

// Get returns the record with the given ID, and whether there is one.
func (s *State) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.records[id]
	return rec.clone(), ok
}

// Put adds rec, replacing any record with the same ID. It keeps a copy,
// with Updated in UTC.
func (s *State) Put(rec Record) {
	rec = rec.clone()
	rec.Updated = rec.Updated.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]Record{}
	}
	s.records[rec.ID] = rec
}

// Delete removes the record with the given ID, if there is one.
func (s *State) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, id)
}

// IDs returns the ID of every record, sorted.
func (s *State) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.records))
}

// migration upgrades a state file's JSON object from one format version to
// the next.
type migration func(obj map[string]any) error

// migrations[n] upgrades a state file's JSON object from format version n
// to n+1.
var migrations = map[int]migration{}

// Load reads the state file at path. A missing file is an empty state:
// maisys hasn't synced on this machine yet. If the file is in an older
// format, Load migrates it and saves it in the current format, after
// keeping a copy of the old file next to it as <name>.v<N>.bak.
func Load(path string) (*State, error) {
	s, _, err := load(path, FormatVersion, migrations)
	return s, err
}

// load is Load, for format version current and the migrations in steps. It
// also returns what the file holds after loading, or nil if there's no file.
func load(path string, current int, steps map[int]migration) (*State, []byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{FormatVersion: current}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the state file: %w", err)
	}

	var head struct {
		FormatVersion *int `json:"format_version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, nil, decodeError(path, data, err)
	}
	switch {
	case head.FormatVersion == nil:
		return nil, nil, fmt.Errorf("%s: format_version is missing", path)
	case *head.FormatVersion > current:
		return nil, nil, fmt.Errorf("%s was written by a newer version of maisys (format_version %d; this version reads up to %d): update maisys and try again",
			path, *head.FormatVersion, current)
	case *head.FormatVersion < current:
		return migrate(path, data, *head.FormatVersion, current, steps)
	}

	s, err := decode(data)
	if err != nil {
		return nil, nil, decodeError(path, data, err)
	}
	return s, data, nil
}

// migrate upgrades data, the contents of path, from format version from to
// current, and saves the result after keeping a copy of the old file.
func migrate(path string, data []byte, from, current int, steps map[int]migration) (*State, []byte, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return nil, nil, decodeError(path, data, err)
	}
	for v := from; v < current; v++ {
		m, ok := steps[v]
		if !ok {
			return nil, nil, fmt.Errorf("%s: format_version %d isn't a format maisys knows", path, from)
		}
		if err := m(obj); err != nil {
			return nil, nil, fmt.Errorf("%s: migrating from format_version %d to %d: %w", path, v, v+1, err)
		}
		obj["format_version"] = v + 1
	}
	migrated, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: migrating to format_version %d: %w", path, current, err)
	}
	s, err := decode(migrated)
	if err != nil {
		// Positions would refer to the migrated file, which isn't saved.
		return nil, nil, decodeError(fmt.Sprintf("%s, migrated to format_version %d", path, current), nil, err)
	}

	if err := backup(path, data, from); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	saved, err := s.writeLocked(path, nil)
	if err != nil {
		return nil, nil, err
	}
	return s, saved, nil
}

// backup keeps a copy of data, the contents of path in format version
// version, unless there already is one.
func backup(path string, data []byte, version int) error {
	name := fmt.Sprintf("%s.v%d.bak", path, version)
	if _, err := os.Lstat(name); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("keeping a copy of the old state file: %w", err)
	}
	if err := writeFile(name, data); err != nil {
		return fmt.Errorf("keeping a copy of the old state file: %w", err)
	}
	return nil
}

// decode reads a state file in the current format, rejecting unknown
// fields and records that can't be told apart or removed.
func decode(data []byte) (*State, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	switch f.LastSyncResult {
	case "", ResultOK, ResultFailed, ResultInterrupted:
	default:
		return nil, fmt.Errorf("last_sync_result must be %q, %q or %q, not %q", ResultOK, ResultFailed, ResultInterrupted, f.LastSyncResult)
	}
	s := &State{
		FormatVersion:  f.FormatVersion,
		Machine:        f.Machine,
		LastSync:       f.LastSync,
		LastSyncResult: f.LastSyncResult,
		records:        make(map[string]Record, len(f.Records)),
	}
	for _, rec := range f.Records {
		if err := checkRecord(rec); err != nil {
			return nil, err
		}
		if _, ok := s.records[rec.ID]; ok {
			return nil, fmt.Errorf("record %q appears more than once", rec.ID)
		}
		s.records[rec.ID] = rec.clone()
	}
	return s, nil
}

// checkRecord reports a record maisys couldn't tell apart from others or
// remove.
func checkRecord(rec Record) error {
	switch {
	case rec.ID == "":
		return errors.New("a record has no id")
	case rec.Kind == "":
		return fmt.Errorf("record %q has no kind", rec.ID)
	}
	return nil
}

// decodeError describes an error reading data, the contents of file.
// Syntax errors start with file:line:column, unless data is nil.
func decodeError(file string, data []byte, err error) error {
	if se, ok := errors.AsType[*json.SyntaxError](err); ok {
		if data == nil {
			return fmt.Errorf("%s: %s", file, se.Error())
		}
		line, col := position(data, se.Offset-1)
		return fmt.Errorf("%s:%d:%d: %s", file, line, col, se.Error())
	}
	if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		// json describes type mismatches in Go terms; describe them in JSON
		// terms instead. Value is a JSON type, perhaps followed by the
		// number at fault.
		got := "a JSON " + te.Value
		if _, number, ok := strings.Cut(te.Value, " "); ok {
			got = number
		}
		if te.Field == "" {
			return fmt.Errorf("%s: the state must be a JSON object, not %s", file, got)
		}
		return fmt.Errorf("%s: %s must be %s, not %s", file, te.Field, describeType(te.Type), got)
	}
	return fmt.Errorf("%s: %s", file, strings.TrimPrefix(err.Error(), "json: "))
}

// position returns the line and column (both from 1) of the byte at offset
// in data.
func position(data []byte, offset int64) (line, col int) {
	before := data[:max(0, min(offset, int64(len(data))))]
	line = bytes.Count(before, []byte("\n")) + 1
	col = len(before) - bytes.LastIndexByte(before, '\n')
	return line, col
}

// describeType describes the JSON values that decode into t.
func describeType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Bool:
		return "true or false"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "an object"
	default:
		return "a " + t.String()
	}
}

// Save writes the state to path in the current format, creating its folder
// if needed. The file is replaced in one step, so it's never left half
// written, and only you can read it.
func (s *State) Save(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.FormatVersion = FormatVersion
	_, err := s.writeLocked(path, nil)
	return err
}

// writeLocked writes the state to path, unless it would write last, which
// the file already holds. It returns what the file holds afterwards. The
// caller holds s.mu, so the state can't change while it's written, and
// concurrent saves are written in turn.
func (s *State) writeLocked(path string, last []byte) ([]byte, error) {
	f := file{
		FormatVersion:  s.FormatVersion,
		Machine:        s.Machine,
		LastSync:       s.LastSync.UTC(),
		LastSyncResult: s.LastSyncResult,
		Records:        make([]Record, 0, len(s.records)),
	}
	for _, id := range slices.Sorted(maps.Keys(s.records)) {
		rec := s.records[id]
		if err := checkRecord(rec); err != nil {
			return nil, fmt.Errorf("saving the state file: %w", err)
		}
		f.Records = append(f.Records, rec)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("saving the state file: %w", err)
	}
	data := buf.Bytes()
	if bytes.Equal(data, last) {
		return last, nil
	}
	if err := writeFile(path, data); err != nil {
		return nil, fmt.Errorf("saving the state file: %w", err)
	}
	return data, nil
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
