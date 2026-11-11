package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// checkRenamed checks that from is gone and to holds content.
func checkRenamed(t *testing.T, from, to, content string) {
	t.Helper()
	if _, err := os.Lstat(from); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is still there: %v", from, err)
	}
	data, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("%s holds %q, want %q", to, data, content)
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "file.tmp"), filepath.Join(dir, "file")
	writeTestFile(t, from, "first")
	if err := Rename(from, to); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	checkRenamed(t, from, to, "first")

	writeTestFile(t, from, "second")
	if err := Rename(from, to); err != nil {
		t.Fatalf("Rename over an existing file: %v", err)
	}
	checkRenamed(t, from, to, "second")
}

func TestRenameMissing(t *testing.T) {
	dir := t.TempDir()
	err := Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "file"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Rename of a missing file: error = %v, want fs.ErrNotExist", err)
	}
}
