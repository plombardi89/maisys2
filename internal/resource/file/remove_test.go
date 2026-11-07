package file_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
)

func TestRemover(t *testing.T) {
	const rel = ".config/nvim/init.lua"
	tests := []struct {
		name string
		// setup applies the file and changes things after.
		setup   func(e *env) state.Record
		change  resource.Change
		removed bool
	}{
		{
			name: "created by maisys",
			setup: func(e *env) state.Record {
				_, rec := e.apply(textFile(rel, "x\n"))
				return rec
			},
			change:  resource.Change{Action: resource.Remove, Summary: "~/.config/nvim/init.lua"},
			removed: true,
		},
		{
			name: "created by maisys, then edited",
			setup: func(e *env) state.Record {
				_, rec := e.apply(textFile(rel, "x\n"))
				e.write(rel, "edited\n", 0o644)
				return rec
			},
			change: resource.Change{Notes: []string{"~/.config/nvim/init.lua changed outside maisys (left in place)"}},
		},
		{
			name: "taken over",
			setup: func(e *env) state.Record {
				e.write(rel, "mine\n", 0o644)
				_, rec := e.apply(textFile(rel, "x\n"))
				return rec
			},
			change: resource.Change{Notes: []string{"~/.config/nvim/init.lua left in place; no longer managed"}},
		},
		{
			name: "already gone",
			setup: func(e *env) state.Record {
				_, rec := e.apply(textFile(rel, "x\n"))
				if err := os.Remove(e.path(rel)); err != nil {
					e.t.Fatal(err)
				}
				return rec
			},
			removed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			rec := tt.setup(e)
			var r file.Remover
			if r.Kind() != "file" {
				t.Errorf("Kind() = %q, want file", r.Kind())
			}
			c, err := r.CheckRemove(t.Context(), e.rt, rec)
			if err != nil {
				t.Fatal(err)
			}
			if c.Action != tt.change.Action || c.Summary != tt.change.Summary || !slices.Equal(c.Notes, tt.change.Notes) || c.Privileged {
				t.Errorf("CheckRemove() = %+v, want %+v", c, tt.change)
			}
			if err := r.Remove(t.Context(), e.rt, rec); err != nil {
				t.Fatal(err)
			}
			_, err = os.Lstat(e.path(rel))
			if gone := errors.Is(err, fs.ErrNotExist); gone != tt.removed {
				t.Errorf("after Remove(), the file is gone: %t, want %t", gone, tt.removed)
			}
		})
	}
}

func TestRemoverEditedAfterCheck(t *testing.T) {
	e := newEnv(t)
	_, rec := e.apply(textFile(".gitconfig", "x\n"))
	var r file.Remover
	if c, err := r.CheckRemove(t.Context(), e.rt, rec); err != nil || c.Action != resource.Remove {
		t.Fatalf("CheckRemove() = %+v, %v; want remove", c, err)
	}
	e.write(".gitconfig", "edited\n", 0o644)
	if err := r.Remove(t.Context(), e.rt, rec); err != nil {
		t.Fatal(err)
	}
	if got := e.read(".gitconfig"); got != "edited\n" {
		t.Errorf("~/.gitconfig holds %q, want the edit left in place", got)
	}
}

func TestRemoverErrors(t *testing.T) {
	e := newEnv(t)
	var r file.Remover
	for _, rec := range []state.Record{
		{ID: "file:x", Kind: "file"},
		{ID: "file:x", Kind: "file", Paths: []string{"relative"}},
	} {
		if _, err := r.CheckRemove(t.Context(), e.rt, rec); err == nil {
			t.Errorf("CheckRemove(%+v): no error", rec)
		}
		if err := r.Remove(t.Context(), e.rt, rec); err == nil {
			t.Errorf("Remove(%+v): no error", rec)
		}
	}
}

func TestRemoverRoot(t *testing.T) {
	skipOnWindows(t, "sudo")
	e := newEnv(t)
	conf := filepath.Join(t.TempDir(), "wsl.conf")
	e.runner.On("install", installFake(t))
	e.runner.On("rm", rmFake(t))
	_, rec := e.apply(&file.File{Path: conf, Content: file.Bytes([]byte("[boot]\n")), Root: true})

	var r file.Remover
	c, err := r.CheckRemove(t.Context(), e.rt, rec)
	if err != nil || c.Action != resource.Remove || !c.Privileged || c.Summary != conf {
		t.Errorf("CheckRemove() = %+v, %v; want a removal that needs root", c, err)
	}
	if err := r.Remove(t.Context(), e.rt, rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(conf); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("after Remove(), %s is still there: %v", conf, err)
	}
	if cmds := e.runner.RootCommands(); len(cmds) != 2 || cmds[1] != "rm -f -- "+conf {
		t.Errorf("commands run as root = %q, want the file installed, then deleted", cmds)
	}
}
