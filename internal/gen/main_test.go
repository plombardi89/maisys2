package main

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestGeneratedFilesAreUpToDate checks the committed generated files
// against what go generate would write now.
func TestGeneratedFilesAreUpToDate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	files, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("%v; run go generate ./...", err)
			continue
		}
		if !bytes.Equal(got, files[name]) {
			t.Errorf("%s is out of date; run go generate ./...", name)
		}
	}
}

func TestParseDir(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", `package a

// Config is the config.
type Config struct {
	Toggle
	// Name is the name,
	// over two lines.
	Name string
	// A and B share a doc.
	A, B int
	// hidden isn't a key.
	hidden string
	Undocumented string
}

type (
	// Grouped is declared in a group.
	Grouped struct {
		// Field is a field.
		Field string
	}
	// NotAStruct isn't a struct.
	NotAStruct string
)
`)
	write("a_test.go", `package a

// Ignored is in a test.
type Ignored struct {
	// Field is a field.
	Field string
}
`)

	types := map[string]typeDocs{}
	if err := parseDir(dir, "example.com/a", types); err != nil {
		t.Fatal(err)
	}
	want := map[string]typeDocs{
		"example.com/a.Config": {doc: "Config is the config.", fields: map[string]string{
			"Name": "Name is the name,\nover two lines.",
			"A":    "A and B share a doc.",
			"B":    "A and B share a doc.",
		}},
		"example.com/a.Grouped": {doc: "Grouped is declared in a group.", fields: map[string]string{
			"Field": "Field is a field.",
		}},
	}
	if len(types) != len(want) {
		t.Errorf("found types %v, want %v", slices.Sorted(maps.Keys(types)), slices.Sorted(maps.Keys(want)))
	}
	for key, w := range want {
		got := types[key]
		if got.doc != w.doc || !maps.Equal(got.fields, w.fields) {
			t.Errorf("%s = %+v, want %+v", key, got, w)
		}
	}
}
