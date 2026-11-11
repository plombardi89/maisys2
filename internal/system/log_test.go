package system

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOpenLog(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "logs")
	now := time.Date(2026, 11, 6, 18, 30, 5, 0, time.FixedZone("EST", -5*60*60))
	l, err := OpenLog(dir, now, "maisys 1.2.3 sync")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "2026-11-06T23-30-05Z.log"); l.Path() != want {
		t.Errorf("Path() = %s, want %s", l.Path(), want)
	}
	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{dir: 0o700, l.Path(): 0o600} {
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
				t.Errorf("mode of %s = %v, %v; want %v", path, info.Mode().Perm(), err, want)
			}
		}
	}

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			if _, err := fmt.Fprintf(l, "line %d %s\n", i, strings.Repeat("x", 1000)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "maisys 1.2.3 sync\n\nline ") {
		t.Errorf("the log doesn't start with the header and a blank line:\n%.100s", data)
	}
	line := regexp.MustCompile(`^line \d+ x{1000}$`)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")[2:]
	if len(lines) != 20 {
		t.Errorf("the log has %d lines after the header, want 20", len(lines))
	}
	for _, l := range lines {
		if !line.MatchString(l) {
			t.Errorf("a line was mixed up with another: %.50s…", l)
		}
	}
}

func TestOpenLogKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var names []string
	for i := range 25 {
		name := start.Add(time.Duration(i)*time.Hour).Format(logTimeFormat) + ".log"
		names = append(names, name)
	}
	// A second run in the same second as the newest is newer than it.
	names = append(names, strings.TrimSuffix(names[24], ".log")+"-2.log")
	others := []string{"notes.txt", "2026-01-01.log"}
	for _, name := range append(slices.Clone(names), others...) {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	l, err := OpenLog(dir, start.Add(30*time.Hour), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := append(append(slices.Clone(names[7:]), others...), filepath.Base(l.Path()))
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("files left = %q\nwant %q", got, want)
	}
	if info, err := os.Stat(l.Path()); err != nil || info.Size() != 0 {
		t.Errorf("a log without a header isn't empty: %v, %v", info, err)
	}
}

func TestOpenLogSameSecond(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 6, 18, 30, 5, 0, time.UTC)
	var paths []string
	for range 3 {
		l, err := OpenLog(dir, now, "run")
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, filepath.Base(l.Path()))
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"2026-11-06T18-30-05Z.log", "2026-11-06T18-30-05Z-2.log", "2026-11-06T18-30-05Z-3.log"}
	if !slices.Equal(paths, want) {
		t.Errorf("log files = %q, want %q", paths, want)
	}
}

func TestOpenLogErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLog(filepath.Join(file, "logs"), time.Now(), ""); err == nil {
		t.Error("OpenLog() under a file: no error")
	}
}
