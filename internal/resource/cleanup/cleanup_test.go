package cleanup_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	dir := t.TempDir()
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
