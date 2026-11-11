//go:build windows

package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// holdOpen opens path as a process that doesn't share delete access does,
// such as an antivirus scanning it. It returns a function that closes it,
// which is also called when the test ends.
func holdOpen(t *testing.T, path string) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = windows.CloseHandle(h) }) }
	t.Cleanup(release)
	return release
}

// setRetryDelays sets retryDelays for the rest of the test.
func setRetryDelays(t *testing.T, delays ...time.Duration) {
	t.Helper()
	old := retryDelays
	retryDelays = delays
	t.Cleanup(func() { retryDelays = old })
}

func TestRenameWaitsForAFileInUse(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "state.json.tmp"), filepath.Join(dir, "state.json")
	writeTestFile(t, from, "new")
	writeTestFile(t, to, "old")
	release := holdOpen(t, to)

	if err := os.Rename(from, to); !inUse(err) {
		t.Fatalf("os.Rename over a file open without delete sharing: error = %v, want access denied or a sharing violation", err)
	}
	timer := time.AfterFunc(100*time.Millisecond, release)
	defer timer.Stop()
	if err := Rename(from, to); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	checkRenamed(t, from, to, "new")
}

func TestRenameGivesUp(t *testing.T) {
	setRetryDelays(t, time.Millisecond, time.Millisecond)
	dir := t.TempDir()
	from, to := filepath.Join(dir, "state.json.tmp"), filepath.Join(dir, "state.json")
	writeTestFile(t, from, "new")
	writeTestFile(t, to, "old")
	holdOpen(t, to)

	if err := Rename(from, to); !inUse(err) {
		t.Errorf("Rename over a file that stays open: error = %v, want access denied or a sharing violation", err)
	}
	for path, want := range map[string]string{from: "new", to: "old"} {
		if data, err := os.ReadFile(path); err != nil || string(data) != want {
			t.Errorf("%s holds %q (%v), want %q", path, data, err, want)
		}
	}
}

func TestRenameDoesntRetryOtherErrors(t *testing.T) {
	setRetryDelays(t, time.Minute)
	dir := t.TempDir()
	start := time.Now()
	err := Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "file"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Rename of a missing file: error = %v, want fs.ErrNotExist", err)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("Rename of a missing file took %v: it tried again", took)
	}
}
