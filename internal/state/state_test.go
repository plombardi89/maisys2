package state

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var cet = time.FixedZone("CET", 3600)

// exampleRecords are records of a few kinds, in no particular order.
func exampleRecords() []Record {
	return []Record{
		{
			ID:     "tool:kubectl",
			Kind:   "tool",
			Module: "kubernetes",
			Paths:  []string{"/home/phil/.local/share/maisys/tools/kubectl/1.36.0", "/home/phil/.local/bin/kubectl"},
			Details: map[string]string{
				"version": "1.36.0",
				"url":     "https://example.com/kubectl?version=1.36.0&arch=amd64",
			},
			Updated: time.Date(2026, 11, 6, 10, 29, 30, 500, cet),
		},
		{
			ID:       "file:~/.gitconfig",
			Kind:     "file",
			Module:   "files",
			Paths:    []string{"/home/phil/.gitconfig"},
			Details:  map[string]string{"sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"},
			TookOver: true,
			Updated:  time.Date(2026, 11, 6, 9, 29, 0, 0, time.UTC),
		},
		{
			ID:      "package-set:packages",
			Kind:    "package-set",
			Module:  "packages",
			Details: map[string]string{},
		},
	}
}

// exampleFile is the state file holding exampleRecords.
const exampleFile = `{
  "format_version": 1,
  "machine": "phil-workp14s",
  "last_sync": "2026-11-06T09:30:00Z",
  "last_sync_result": "ok",
  "records": [
    {
      "id": "file:~/.gitconfig",
      "kind": "file",
      "module": "files",
      "paths": [
        "/home/phil/.gitconfig"
      ],
      "details": {
        "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
      },
      "took_over": true,
      "updated": "2026-11-06T09:29:00Z"
    },
    {
      "id": "package-set:packages",
      "kind": "package-set",
      "module": "packages"
    },
    {
      "id": "tool:kubectl",
      "kind": "tool",
      "module": "kubernetes",
      "paths": [
        "/home/phil/.local/share/maisys/tools/kubectl/1.36.0",
        "/home/phil/.local/bin/kubectl"
      ],
      "details": {
        "url": "https://example.com/kubectl?version=1.36.0&arch=amd64",
        "version": "1.36.0"
      },
      "updated": "2026-11-06T09:29:30.0000005Z"
    }
  ]
}
`

func exampleState(records []Record) *State {
	s := &State{
		Machine:        "phil-workp14s",
		LastSync:       time.Date(2026, 11, 6, 10, 30, 0, 0, cet),
		LastSyncResult: ResultOK,
	}
	for _, rec := range records {
		s.Put(rec)
	}
	return s
}

// view is a State's contents, for comparing.
type view struct {
	FormatVersion  int
	Machine        string
	LastSync       time.Time
	LastSyncResult Result
	Records        []Record
}

func viewOf(s *State) view {
	v := view{
		FormatVersion:  s.FormatVersion,
		Machine:        s.Machine,
		LastSync:       s.LastSync.UTC(),
		LastSyncResult: s.LastSyncResult,
	}
	for _, id := range s.IDs() {
		rec, _ := s.Get(id)
		v.Records = append(v.Records, rec)
	}
	return v
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func checkMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode() != want {
		t.Errorf("%s has mode %v, want %v", path, info.Mode(), want)
	}
}

func checkOnly(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Errorf("folder holds %v, want %v", got, want)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "maisys")
	path := filepath.Join(dir, "state.json")
	s := exampleState(exampleRecords())
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if s.FormatVersion != FormatVersion {
		t.Errorf("FormatVersion after Save = %d, want %d", s.FormatVersion, FormatVersion)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(viewOf(got), viewOf(s)) {
		t.Errorf("Load = %+v\nwant %+v", viewOf(got), viewOf(s))
	}
	if !got.LastSync.Equal(s.LastSync) || got.LastSync.Location() != time.UTC {
		t.Errorf("LastSync = %v, want %v in UTC", got.LastSync, s.LastSync)
	}

	checkOnly(t, dir, "state.json")
	if runtime.GOOS != "windows" {
		checkMode(t, path, 0o600)
		checkMode(t, dir, 0o700|fs.ModeDir)
	}
}

func TestSaveIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	forward := exampleRecords()
	backward := slices.Clone(forward)
	slices.Reverse(backward)
	for i, records := range [][]Record{forward, backward} {
		path := filepath.Join(dir, fmt.Sprintf("state%d.json", i))
		if err := exampleState(records).Save(path); err != nil {
			t.Fatal(err)
		}
		if got := readTestFile(t, path); got != exampleFile {
			t.Errorf("records in order %d saved as\n%s\nwant\n%s", i, got, exampleFile)
		}
	}
}

func TestSaveEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := (&State{}).Save(path); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"format_version\": 1,\n  \"machine\": \"\",\n  \"records\": []\n}\n"
	if got := readTestFile(t, path); got != want {
		t.Errorf("saved\n%s\nwant\n%s", got, want)
	}
}

func TestSaveRejectsRecordsItCantLoad(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
		want string
	}{
		{name: "no id", rec: Record{Kind: "tool"}, want: "saving the state file: a record has no id"},
		{name: "no kind", rec: Record{ID: "tool:x"}, want: `saving the state file: record "tool:x" has no kind`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := &State{}
			s.Put(tt.rec)
			err := s.Save(filepath.Join(dir, "state.json"))
			if err == nil || err.Error() != tt.want {
				t.Errorf("Save error = %v, want %q", err, tt.want)
			}
			checkOnly(t, dir)
		})
	}
}

func TestRecords(t *testing.T) {
	s := &State{}
	if ids := s.IDs(); len(ids) != 0 {
		t.Errorf("IDs of an empty state = %v", ids)
	}
	if _, ok := s.Get("tool:kubectl"); ok {
		t.Error("Get found a record in an empty state")
	}
	for _, rec := range exampleRecords() {
		s.Put(rec)
	}
	want := []string{"file:~/.gitconfig", "package-set:packages", "tool:kubectl"}
	if got := s.IDs(); !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}

	rec, ok := s.Get("tool:kubectl")
	if !ok {
		t.Fatal("Get didn't find tool:kubectl")
	}
	if rec.Updated.Location() != time.UTC || !rec.Updated.Equal(exampleRecords()[0].Updated) {
		t.Errorf("Updated = %v, want %v in UTC", rec.Updated, exampleRecords()[0].Updated)
	}
	if rec, _ := s.Get("package-set:packages"); rec.Details != nil {
		t.Errorf("empty Details = %#v, want nil", rec.Details)
	}

	// Records are copied in and out.
	rec.Details["version"] = "changed"
	rec.Paths[0] = "changed"
	put := Record{ID: "tool:helm", Kind: "tool", Paths: []string{"/helm"}, Details: map[string]string{"version": "3"}}
	s.Put(put)
	put.Paths[0] = "changed"
	put.Details["version"] = "changed"
	for _, id := range []string{"tool:kubectl", "tool:helm"} {
		rec, _ := s.Get(id)
		if rec.Paths[0] == "changed" || rec.Details["version"] == "changed" {
			t.Errorf("changing a copy of %s changed the state: %+v", id, rec)
		}
	}

	s.Put(Record{ID: "tool:kubectl", Kind: "tool", Details: map[string]string{"version": "1.37.0"}})
	if rec, _ := s.Get("tool:kubectl"); rec.Details["version"] != "1.37.0" || rec.Paths != nil {
		t.Errorf("after replacing tool:kubectl, Get = %+v", rec)
	}

	s.Delete("tool:kubectl")
	s.Delete("tool:missing")
	want = []string{"file:~/.gitconfig", "package-set:packages", "tool:helm"}
	if got := s.IDs(); !slices.Equal(got, want) {
		t.Errorf("IDs after Delete = %v, want %v", got, want)
	}
	if _, ok := s.Get("tool:kubectl"); ok {
		t.Error("Get found a deleted record")
	}
}

func TestLoadMissing(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := (view{FormatVersion: FormatVersion}); !reflect.DeepEqual(viewOf(s), want) {
		t.Errorf("Load = %+v, want %+v", viewOf(s), want)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string // after the file's path
	}{
		{
			name:    "newer format",
			content: `{"format_version": 2, "something_new": true}`,
			want:    " was written by a newer version of maisys (format_version 2; this version reads up to 1): update maisys and try again",
		},
		{
			name:    "missing format_version",
			content: `{"records": []}`,
			want:    ": format_version is missing",
		},
		{
			name:    "format_version of the wrong type",
			content: `{"format_version": "1"}`,
			want:    ": format_version must be a whole number, not a JSON string",
		},
		{
			name:    "format_version that isn't whole",
			content: `{"format_version": 1.5}`,
			want:    ": format_version must be a whole number, not 1.5",
		},
		{
			name:    "unknown older format",
			content: `{"format_version": 0}`,
			want:    ": format_version 0 isn't a format maisys knows",
		},
		{
			name:    "unknown field",
			content: `{"format_version": 1, "colour": "blue"}`,
			want:    `: unknown field "colour"`,
		},
		{
			name:    "unknown record field",
			content: `{"format_version": 1, "records": [{"id": "tool:x", "kind": "tool", "owner": "me"}]}`,
			want:    `: unknown field "owner"`,
		},
		{
			name:    "record field of the wrong type",
			content: `{"format_version": 1, "records": [{"id": "tool:x", "kind": "tool", "paths": "/x"}]}`,
			want:    ": records.0.paths must be a list, not a JSON string",
		},
		{
			name:    "record without an id",
			content: `{"format_version": 1, "records": [{"kind": "tool"}]}`,
			want:    ": a record has no id",
		},
		{
			name:    "record without a kind",
			content: `{"format_version": 1, "records": [{"id": "tool:x"}]}`,
			want:    `: record "tool:x" has no kind`,
		},
		{
			name:    "record twice",
			content: `{"format_version": 1, "records": [{"id": "tool:x", "kind": "tool"}, {"id": "tool:x", "kind": "tool"}]}`,
			want:    `: record "tool:x" appears more than once`,
		},
		{
			name:    "unknown result",
			content: `{"format_version": 1, "last_sync_result": "great"}`,
			want:    `: last_sync_result must be "ok", "failed" or "interrupted", not "great"`,
		},
		{
			name:    "not an object",
			content: `[]`,
			want:    ": the state must be a JSON object, not a JSON array",
		},
		{
			name:    "not JSON",
			content: "{\n  \"format_version\": 1,\n}\n",
			want:    ":3:1: invalid character '}' looking for beginning of object key string",
		},
		{
			name:    "more after the end",
			content: `{"format_version": 1} {}`,
			want:    ":1:23: invalid character '{' after top-level value",
		},
		{
			name:    "empty",
			content: "",
			want:    ":1:1: unexpected end of JSON input",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			writeTestFile(t, path, tt.content)
			_, err := Load(path)
			if want := path + tt.want; err == nil || err.Error() != want {
				t.Errorf("Load error = %v\nwant %s", err, want)
			}
		})
	}
}

// testSteps are fake migrations from format 1 to 2 (host becomes machine)
// and 2 to 3 (each record's owner becomes module). Each records that it ran
// in ran.
func testSteps(ran *[]int) map[int]migration {
	rename := func(obj map[string]any, from, to string) {
		if v, ok := obj[from]; ok {
			obj[to] = v
			delete(obj, from)
		}
	}
	return map[int]migration{
		1: func(obj map[string]any) error {
			*ran = append(*ran, 1)
			rename(obj, "host", "machine")
			return nil
		},
		2: func(obj map[string]any) error {
			*ran = append(*ran, 2)
			records, _ := obj["records"].([]any)
			for _, r := range records {
				rec, ok := r.(map[string]any)
				if !ok {
					return errors.New("a record isn't an object")
				}
				rename(rec, "owner", "module")
			}
			return nil
		},
	}
}

func TestMigrate(t *testing.T) {
	want := view{
		FormatVersion:  3,
		Machine:        "box",
		LastSync:       time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC),
		LastSyncResult: ResultFailed,
		Records: []Record{
			{ID: "file:~/.bashrc", Kind: "file", Module: "shell", Details: map[string]string{"sha256": "abc"}},
			{ID: "tool:go", Kind: "tool", Module: "golang", Paths: []string{"/go"}},
		},
	}
	tests := []struct {
		name       string
		content    string
		oldBackup  string // an existing backup, if not ""
		wantSteps  []int
		wantBackup string
	}{
		{
			name: "from format 1",
			content: `{"format_version": 1, "host": "box", "last_sync": "2026-11-06T10:30:00+01:00", "last_sync_result": "failed",
				"records": [{"id": "tool:go", "kind": "tool", "owner": "golang", "paths": ["/go"]},
				{"id": "file:~/.bashrc", "kind": "file", "owner": "shell", "details": {"sha256": "abc"}}]}`,
			wantSteps:  []int{1, 2},
			wantBackup: "state.json.v1.bak",
		},
		{
			name: "from format 2, keeping an existing backup",
			content: `{"format_version": 2, "machine": "box", "last_sync": "2026-11-06T09:30:00Z", "last_sync_result": "failed",
				"records": [{"id": "file:~/.bashrc", "kind": "file", "owner": "shell", "details": {"sha256": "abc"}},
				{"id": "tool:go", "kind": "tool", "owner": "golang", "paths": ["/go"]}]}`,
			oldBackup:  "an older backup",
			wantSteps:  []int{2},
			wantBackup: "state.json.v2.bak",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			writeTestFile(t, path, tt.content)
			backup := filepath.Join(dir, tt.wantBackup)
			if tt.oldBackup != "" {
				writeTestFile(t, backup, tt.oldBackup)
			}

			var ran []int
			got, saved, err := load(path, 3, testSteps(&ran))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if !reflect.DeepEqual(viewOf(got), want) {
				t.Errorf("load = %+v\nwant %+v", viewOf(got), want)
			}
			if !slices.Equal(ran, tt.wantSteps) {
				t.Errorf("migrations run = %v, want %v", ran, tt.wantSteps)
			}

			wantBackup := tt.content
			if tt.oldBackup != "" {
				wantBackup = tt.oldBackup
			}
			if got := readTestFile(t, backup); got != wantBackup {
				t.Errorf("backup = %q, want %q", got, wantBackup)
			}
			checkOnly(t, dir, "state.json", tt.wantBackup)

			// The migrated file is saved in the new format.
			file := readTestFile(t, path)
			if string(saved) != file {
				t.Errorf("load says the file holds\n%s\nbut it holds\n%s", saved, file)
			}
			if !strings.HasPrefix(file, "{\n  \"format_version\": 3,\n") {
				t.Errorf("migrated file isn't in format 3:\n%s", file)
			}
			ran = nil
			again, _, err := load(path, 3, testSteps(&ran))
			if err != nil {
				t.Fatalf("loading the migrated file: %v", err)
			}
			if !reflect.DeepEqual(viewOf(again), want) || len(ran) > 0 {
				t.Errorf("loading the migrated file = %+v after migrations %v, want %+v and none", viewOf(again), ran, want)
			}
		})
	}
}

func TestMigrateErrors(t *testing.T) {
	failing := errors.New("broken")
	noop := func(map[string]any) error { return nil }
	tests := []struct {
		name    string
		content string
		steps   map[int]migration
		want    string // after the file's path
	}{
		{
			name:    "a migration fails",
			content: `{"format_version": 1}`,
			steps:   map[int]migration{1: func(map[string]any) error { return failing }},
			want:    ": migrating from format_version 1 to 2: broken",
		},
		{
			name:    "a migration is missing",
			content: `{"format_version": 1}`,
			steps:   map[int]migration{1: noop},
			want:    ": format_version 1 isn't a format maisys knows",
		},
		{
			name:    "unknown fields after migrating",
			content: `{"format_version": 1, "left_over": 1}`,
			steps:   map[int]migration{1: noop, 2: noop},
			want:    `, migrated to format_version 3: unknown field "left_over"`,
		},
		{
			name:    "a record without a kind after migrating",
			content: `{"format_version": 1, "records": [{"id": "tool:x"}]}`,
			steps:   map[int]migration{1: noop, 2: noop},
			want:    `, migrated to format_version 3: record "tool:x" has no kind`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			writeTestFile(t, path, tt.content)

			_, _, err := load(path, 3, tt.steps)
			if want := path + tt.want; err == nil || err.Error() != want {
				t.Errorf("load error = %v\nwant %s", err, want)
			}
			if got := readTestFile(t, path); got != tt.content {
				t.Errorf("file changed to %q", got)
			}
			checkOnly(t, dir, "state.json")
		})
	}
}

func TestEveryOlderFormatHasAMigration(t *testing.T) {
	for v := 1; v < FormatVersion; v++ {
		if migrations[v] == nil {
			t.Errorf("no migration from format_version %d to %d", v, v+1)
		}
	}
}

func TestStoreSavesOnlyChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if store.Path() != path {
		t.Errorf("Path = %q, want %q", store.Path(), path)
	}

	// The first save writes the file, even with nothing in it.
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	empty := readTestFile(t, path)

	// Saving with no changes leaves the file alone.
	writeTestFile(t, path, "untouched")
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readTestFile(t, path); got != "untouched" {
		t.Errorf("Save with no changes wrote %q", got)
	}
	writeTestFile(t, path, empty)

	store.Put(Record{ID: "tool:go", Kind: "tool", Module: "golang"})
	store.Machine = "box"
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := view{
		FormatVersion: FormatVersion,
		Machine:       "box",
		Records:       []Record{{ID: "tool:go", Kind: "tool", Module: "golang"}},
	}
	if !reflect.DeepEqual(viewOf(got), want) {
		t.Errorf("after Put and Save, Load = %+v\nwant %+v", viewOf(got), want)
	}

	// A store opened on a file holding what it would write doesn't write it
	// again.
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	writeTestFile(t, path, "untouched")
	if err := reopened.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := readTestFile(t, path); got != "untouched" {
		t.Errorf("Save with no changes after Open wrote %q", got)
	}
}

func TestStoreConcurrentPutAndSave(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const workers, each = 8, 25
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				id := fmt.Sprintf("tool:w%d-%02d", w, i)
				store.Put(Record{ID: id, Kind: "tool", Module: fmt.Sprint("m", w), Details: map[string]string{"n": fmt.Sprint(i)}})
				if err := store.Save(); err != nil {
					t.Errorf("Save: %v", err)
					return
				}
				if _, ok := store.Get(id); !ok {
					t.Errorf("Get(%q) found nothing", id)
				}
				_ = store.IDs()
				if i%5 == 0 {
					store.Delete(id)
				}
			}
		})
	}
	wg.Wait()
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(viewOf(got), viewOf(store.State)) {
		t.Errorf("Load = %+v\nwant %+v", viewOf(got), viewOf(store.State))
	}
	if n, want := len(got.IDs()), workers*(each-each/5); n != want {
		t.Errorf("Load found %d records, want %d", n, want)
	}
	checkOnly(t, dir, "state.json")
}
