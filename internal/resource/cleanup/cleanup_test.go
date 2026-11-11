package cleanup_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/cleanup"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestLeftover(t *testing.T) {
	tests := []struct {
		name string
		// setup creates things in the home folder.
		setup   func(t *testing.T, home string)
		path    string
		sums    []string
		removed bool
	}{
		{
			name: "a file",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, "bin/kubectl", "kubectl")
			},
			path:    "~/bin/kubectl",
			removed: true,
		},
		{
			name: "a folder",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, "bin/mutagen/mutagen", "mutagen")
				write(t, home, "bin/mutagen/agents.tar.gz", "agents")
			},
			path:    "~/bin/mutagen",
			removed: true,
		},
		{
			name:  "missing",
			setup: func(*testing.T, string) {},
			path:  "~/bin/stern",
		},
		{
			name: "a file with a known sha256",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, "readme.txt", "protoc's readme\n")
			},
			path:    "~/readme.txt",
			sums:    []string{sha("old readme\n"), sha("protoc's readme\n")},
			removed: true,
		},
		{
			name: "a file with another sha256",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, "readme.txt", "my notes\n")
			},
			path: "~/readme.txt",
			sums: []string{sha("protoc's readme\n")},
		},
		{
			name: "a folder where a file with a known sha256 should be",
			setup: func(t *testing.T, home string) {
				t.Helper()
				write(t, home, "readme.txt/notes", "my notes\n")
			},
			path: "~/readme.txt",
			sums: []string{sha("protoc's readme\n")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			tt.setup(t, home)
			rt := &resource.Runtime{Paths: settings.Paths{Home: home}}
			l := &cleanup.Leftover{Path: tt.path, SHA256: tt.sums}
			if l.ID() != "cleanup:"+tt.path || l.Kind() != cleanup.Kind || l.Privileged() || len(l.Targets()) > 0 {
				t.Errorf("ID, Kind, Privileged, Targets = %q, %q, %v, %q", l.ID(), l.Kind(), l.Privileged(), l.Targets())
			}
			c, err := l.Check(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			want := resource.Change{}
			if tt.removed {
				want = resource.Change{Action: resource.Remove, Summary: tt.path}
			}
			if c.Action != want.Action || c.Summary != want.Summary {
				t.Errorf("Check() = %+v, want %+v", c, want)
			}
			if !tt.removed {
				return
			}
			rec, err := l.Apply(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(rec.Paths, []string{tt.path}) {
				t.Errorf("record = %+v, want the path", rec)
			}
			if _, err := os.Lstat(filepath.Join(home, tt.path[2:])); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s is still there: %v", tt.path, err)
			}
			if c, _ := l.Check(t.Context(), rt); c.Action != resource.NoChange {
				t.Errorf("Check() after applying = %+v, want no change", c)
			}
		})
	}
}

func TestLeftoverRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("maisys only deletes things as root on Linux")
	}
	dir := realTempDir(t)
	write(t, dir, "go/bin/go", "go")
	r := systemtest.NewFakeRunner(t)
	r.On("rm -rf --", systemtest.Respond(0, ""))
	rt := &resource.Runtime{Runner: r}
	l := &cleanup.Leftover{Path: filepath.Join(dir, "go"), Root: true}
	if !l.Privileged() {
		t.Error("Privileged() = false, want true")
	}
	if c, err := l.Check(t.Context(), rt); err != nil || c.Action != resource.Remove {
		t.Errorf("Check() = %+v, %v, want it removed", c, err)
	}
	if _, err := l.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	if got, want := r.RootCommands(), []string{"rm -rf -- " + filepath.Join(dir, "go")}; !slices.Equal(got, want) {
		t.Errorf("commands run as root = %q, want %q", got, want)
	}
}

// TestLeftoverSymlinks checks that a symlink at a leftover's path is
// deleted, not what it points to, and that a leftover in a folder that's a
// symlink is left in place, with a note.
func TestLeftoverSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the cleanup only runs on Linux")
	}
	tests := []struct {
		name string
		// setup creates things in the home folder, and in other, a folder
		// outside it.
		setup func(t *testing.T, home, other string)
		// linkedHome makes the home folder a symlink to the folder setup
		// gets.
		linkedHome bool
		path       string
		sums       []string
		removed    bool
		note       string
		// kept are files in other that must still be there afterwards.
		kept []string
	}{
		{
			name: "a symlink to a file",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "kubectl", "kubectl")
				symlink(t, filepath.Join(other, "kubectl"), home, "bin/kubectl")
			},
			path:    "~/bin/kubectl",
			removed: true,
			kept:    []string{"kubectl"},
		},
		{
			name: "a symlink to a folder",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "mutagen/mutagen", "mutagen")
				symlink(t, filepath.Join(other, "mutagen"), home, "bin/mutagen")
			},
			path:    "~/bin/mutagen",
			removed: true,
			kept:    []string{"mutagen/mutagen"},
		},
		{
			name: "a symlink where a file with a known sha256 should be",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "readme.txt", "protoc's readme\n")
				symlink(t, filepath.Join(other, "readme.txt"), home, "readme.txt")
			},
			path: "~/readme.txt",
			sums: []string{sha("protoc's readme\n")},
			kept: []string{"readme.txt"},
		},
		{
			name: "in a folder that's a symlink",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "dotfiles/bin/kubectl", "kubectl")
				symlink(t, filepath.Join(other, "dotfiles/bin"), home, "bin")
			},
			path: "~/bin/kubectl",
			note: "~/bin/kubectl is in ~/bin, which is a symlink (left in place)",
			kept: []string{"dotfiles/bin/kubectl"},
		},
		{
			name: "deeper in a folder that's a symlink",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "Apps/idea/bin/idea", "idea")
				symlink(t, filepath.Join(other, "Apps"), home, "Apps")
			},
			path: "~/Apps/idea/bin",
			note: "~/Apps/idea/bin is in ~/Apps, which is a symlink (left in place)",
			kept: []string{"Apps/idea/bin/idea"},
		},
		{
			name: "in a symlink in a folder",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "idea/bin/idea", "idea")
				symlink(t, filepath.Join(other, "idea"), home, "Apps/jetbrains")
			},
			path: "~/Apps/jetbrains/bin",
			note: "~/Apps/jetbrains/bin is in ~/Apps/jetbrains, which is a symlink (left in place)",
			kept: []string{"idea/bin/idea"},
		},
		{
			name: "missing, in a folder that's a symlink",
			setup: func(t *testing.T, home, other string) {
				t.Helper()
				write(t, other, "bin/other", "other")
				symlink(t, filepath.Join(other, "bin"), home, "bin")
			},
			path: "~/bin/kubectl",
			kept: []string{"bin/other"},
		},
		{
			name: "in a home folder that's a symlink",
			setup: func(t *testing.T, home, _ string) {
				t.Helper()
				write(t, home, "bin/kubectl", "kubectl")
			},
			linkedHome: true,
			path:       "~/bin/kubectl",
			removed:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, other := t.TempDir(), t.TempDir()
			tt.setup(t, home, other)
			if tt.linkedHome {
				home = symlink(t, home, t.TempDir(), "home")
			}
			rt := &resource.Runtime{Paths: settings.Paths{Home: home}}
			l := &cleanup.Leftover{Path: tt.path, SHA256: tt.sums}
			c, err := l.Check(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			want := resource.Change{}
			if tt.removed {
				want = resource.Change{Action: resource.Remove, Summary: tt.path}
			}
			if tt.note != "" {
				want.Notes = []string{tt.note}
			}
			if !reflect.DeepEqual(c, want) {
				t.Errorf("Check() = %+v, want %+v", c, want)
			}

			if _, err := l.Apply(t.Context(), rt); err != nil {
				t.Fatal(err)
			}
			_, err = os.Lstat(filepath.Join(home, tt.path[2:]))
			if gone := errors.Is(err, fs.ErrNotExist); tt.removed && !gone {
				t.Errorf("%s is still there: %v", tt.path, err)
			}
			for _, rel := range tt.kept {
				if _, err := os.Stat(filepath.Join(other, rel)); err != nil {
					t.Errorf("%s, outside the home folder, is gone: %v", rel, err)
				}
			}
		})
	}
}

// TestLeftoverRootSymlinks checks that a leftover outside the home folder,
// in a folder that's a symlink, is left in place.
func TestLeftoverRootSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("maisys only deletes things as root on Linux")
	}
	dir := realTempDir(t)
	write(t, dir, "var/usrlocal/go/bin/go", "go")
	local := symlink(t, filepath.Join(dir, "var/usrlocal"), dir, "usr/local")
	r := systemtest.NewFakeRunner(t)
	rt := &resource.Runtime{Runner: r}
	l := &cleanup.Leftover{Path: filepath.Join(local, "go"), Root: true}
	c, err := l.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	want := resource.Change{Notes: []string{filepath.Join(local, "go") + " is in " + local + ", which is a symlink (left in place)"}}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Check() = %+v, want %+v", c, want)
	}
	if _, err := l.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	if got := r.Commands(); len(got) > 0 {
		t.Errorf("Apply() ran %q, want nothing", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "var/usrlocal/go/bin/go")); err != nil {
		t.Errorf("the folder the symlink points to lost go: %v", err)
	}
}

func TestRemover(t *testing.T) {
	var rm cleanup.Remover
	rec := state.Record{ID: "cleanup:/usr/local/go", Kind: cleanup.Kind, Paths: []string{"/usr/local/go"}}
	c, err := rm.CheckRemove(t.Context(), &resource.Runtime{}, rec)
	if err != nil || c.Action != resource.NoChange || len(c.Notes) > 0 {
		t.Errorf("CheckRemove() = %+v, %v, want nothing to do", c, err)
	}
	if err := rm.Remove(t.Context(), &resource.Runtime{}, rec); err != nil {
		t.Errorf("Remove() error = %v", err)
	}
}

func write(t *testing.T, dir, rel, data string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// symlink makes rel in dir a symlink to target, and returns its path.
func symlink(t *testing.T, target, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// realTempDir returns a new temporary folder, with the symlinks in its path
// resolved: the folders a leftover outside the home folder is in are
// checked for symlinks from the root down.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
