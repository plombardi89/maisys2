package link_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/link"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
)

var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

// env is a home folder and the runtime links check and apply with.
type env struct {
	t     *testing.T
	home  string
	state *state.State
	rt    *resource.Runtime
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("maisys only makes symlinks on Linux")
	}
	home := t.TempDir()
	st := &state.State{}
	return &env{t: t, home: home, state: st, rt: &resource.Runtime{
		Paths:    settings.Paths{Home: home},
		Previous: st.Get,
		Now:      func() time.Time { return testNow },
	}}
}

func (e *env) path(rel string) string {
	return filepath.Join(e.home, filepath.FromSlash(rel))
}

func (e *env) symlink(target, rel string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.path(rel)), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Symlink(target, e.path(rel)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) write(rel, content string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.path(rel)), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.path(rel), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) readlink(rel string) string {
	e.t.Helper()
	target, err := os.Readlink(e.path(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return target
}

func TestLink(t *testing.T) {
	const goBin = ".local/share/maisys/tools/go/1.27.4/bin/go"
	tests := []struct {
		name    string
		setup   func(e *env)
		action  resource.Action
		summary string
		backup  string
	}{
		{
			name:    "missing",
			setup:   func(*env) {},
			action:  resource.Create,
			summary: "~/.local/bin/go → ~/" + goBin,
		},
		{
			name: "pointing elsewhere",
			setup: func(e *env) {
				e.symlink(e.path(".local/share/maisys/tools/go/1.27.3/bin/go"), ".local/bin/go")
			},
			action:  resource.Update,
			summary: "~/.local/bin/go → ~/" + goBin + " (was ~/.local/share/maisys/tools/go/1.27.3/bin/go)",
		},
		{
			name: "pointing outside the home folder",
			setup: func(e *env) {
				e.symlink("/usr/local/go/bin/go", ".local/bin/go")
			},
			action:  resource.Update,
			summary: "~/.local/bin/go → ~/" + goBin + " (was /usr/local/go/bin/go)",
		},
		{
			name: "a file",
			setup: func(e *env) {
				e.write(".local/bin/go", "#!/bin/sh\n")
			},
			action:  resource.Warn,
			summary: "~/.local/bin/go is a file (will back up and replace with a link to ~/" + goBin + ")",
			backup:  "#!/bin/sh\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			l := &link.Link{Path: "~/.local/bin/go", Target: "~/" + goBin}
			if l.ID() != "symlink:~/.local/bin/go" || l.Kind() != "symlink" || l.Privileged() || !slices.Equal(l.Targets(), []string{"~/.local/bin/go"}) {
				t.Errorf("ID() = %q, Kind() = %q, Privileged() = %t, Targets() = %q", l.ID(), l.Kind(), l.Privileged(), l.Targets())
			}

			c, err := l.Check(t.Context(), e.rt)
			if err != nil || c.Action != tt.action || c.Summary != tt.summary {
				t.Errorf("Check() = %s %q, %v; want %s %q", c.Action, c.Summary, err, tt.action, tt.summary)
			}
			rec, err := l.Apply(t.Context(), e.rt)
			if err != nil {
				t.Fatal(err)
			}
			if got := e.readlink(".local/bin/go"); got != e.path(goBin) {
				t.Errorf("the link points at %q, want %q", got, e.path(goBin))
			}
			if rec.Details[link.DetailTarget] != e.path(goBin) || !slices.Equal(rec.Paths, []string{"~/.local/bin/go"}) {
				t.Errorf("record = %+v, want the link and its target", rec)
			}
			backup := e.path(".local/bin/go") + ".maisys-bak.20261106-093000"
			data, err := os.ReadFile(backup)
			switch {
			case tt.backup == "" && err == nil:
				t.Errorf("Apply() backed up %s, want no backup", backup)
			case tt.backup != "" && (err != nil || string(data) != tt.backup):
				t.Errorf("the backup holds %q, %v; want %q", data, err, tt.backup)
			}
			entries, err := os.ReadDir(e.path(".local/bin"))
			if err != nil {
				t.Fatal(err)
			}
			if tt.backup == "" && len(entries) != 1 {
				t.Errorf("~/.local/bin has %d entries, want only the link", len(entries))
			}

			if c, err := l.Check(t.Context(), e.rt); err != nil || c.Action != resource.NoChange {
				t.Errorf("Check() after Apply() = %s %q, %v; want no change", c.Action, c.Summary, err)
			}
		})
	}
}

// TestLinkRecord checks the record a check says a link should have when it
// already points where it should: none while the state file's record is
// up to date, and otherwise the one applying would leave. With that
// record, the link is deleted once it's taken out of the config.
func TestLinkRecord(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *env)
		want  bool // whether the check has a record
	}{
		{
			name: "made by maisys",
			setup: func(e *env) {
				e.apply(&link.Link{Path: "~/bin/x", Target: "/opt/b"})
			},
		},
		{
			name: "pointed by hand where the config now says",
			setup: func(e *env) {
				e.apply(&link.Link{Path: "~/bin/x", Target: "/opt/a"})
				if err := os.Remove(e.path("bin/x")); err != nil {
					e.t.Fatal(err)
				}
				e.symlink("/opt/b", "bin/x")
			},
			want: true,
		},
		{
			name:  "there before maisys",
			setup: func(e *env) { e.symlink("/opt/b", "bin/x") },
			want:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			l := &link.Link{Path: "~/bin/x", Target: "/opt/b"}
			c, err := l.Check(t.Context(), e.rt)
			if err != nil || c.Action != resource.NoChange {
				t.Fatalf("Check() = %s %q, %v; want no change", c.Action, c.Summary, err)
			}
			want := state.Record{Paths: []string{"~/bin/x"}, Details: map[string]string{link.DetailTarget: "/opt/b"}}
			switch {
			case !tt.want && c.Record != nil:
				t.Errorf("Check() has the record %+v, want none: the state file's is up to date", *c.Record)
			case tt.want && (c.Record == nil || !reflect.DeepEqual(*c.Record, want)):
				t.Errorf("Check() has the record %v, want %+v", c.Record, want)
			case tt.want:
				e.put(l, *c.Record)
			}

			rec, _ := e.state.Get(l.ID())
			var r link.Remover
			if c, err := r.CheckRemove(t.Context(), e.rt, rec); err != nil || c.Action != resource.Remove {
				t.Errorf("CheckRemove() = %+v, %v; want the link deleted", c, err)
			}
		})
	}
}

// apply applies l, and keeps its record.
func (e *env) apply(l *link.Link) {
	e.t.Helper()
	rec, err := l.Apply(e.t.Context(), e.rt)
	if err != nil {
		e.t.Fatal(err)
	}
	e.put(l, rec)
}

// put keeps rec as l's record, as the engine does.
func (e *env) put(l *link.Link, rec state.Record) {
	rec.ID, rec.Kind, rec.Module = l.ID(), l.Kind(), "golang"
	e.state.Put(rec)
}

func TestLinkTargets(t *testing.T) {
	e := newEnv(t)
	for _, tt := range []struct{ target, want string }{
		{"../share/x", "../share/x"},
		{"/opt/x", "/opt/x"},
		{"~/x", e.path("x")},
	} {
		l := &link.Link{Path: "~/bin/x", Target: tt.target}
		if _, err := l.Apply(t.Context(), e.rt); err != nil {
			t.Fatal(err)
		}
		if got := e.readlink("bin/x"); got != tt.want {
			t.Errorf("a link to %q points at %q, want %q", tt.target, got, tt.want)
		}
	}
}

func TestLinkErrors(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(e.path("bin/dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*link.Link{
		{Path: "~/bin/dir", Target: "/x"},
		{Path: "~/bin/x"},
		{Path: "bin/x", Target: "/x"},
	} {
		if _, err := l.Check(t.Context(), e.rt); err == nil {
			t.Errorf("Check() of %+v: no error", l)
		}
		if _, err := l.Apply(t.Context(), e.rt); err == nil {
			t.Errorf("Apply() of %+v: no error", l)
		}
	}
}

func TestRemover(t *testing.T) {
	tests := []struct {
		name    string
		after   func(e *env) // changes made after the link
		action  resource.Action
		notes   []string
		removed bool
	}{
		{name: "unchanged", after: func(*env) {}, action: resource.Remove, removed: true},
		{name: "already gone", after: func(e *env) {
			if err := os.Remove(e.path("bin/x")); err != nil {
				e.t.Fatal(err)
			}
		}, removed: true},
		{name: "pointing elsewhere", after: func(e *env) {
			if err := os.Remove(e.path("bin/x")); err != nil {
				e.t.Fatal(err)
			}
			e.symlink("/elsewhere", "bin/x")
		}, notes: []string{"~/bin/x changed outside maisys (left in place)"}},
		{name: "a file", after: func(e *env) {
			if err := os.Remove(e.path("bin/x")); err != nil {
				e.t.Fatal(err)
			}
			e.write("bin/x", "mine")
		}, notes: []string{"~/bin/x changed outside maisys (left in place)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			l := &link.Link{Path: "~/bin/x", Target: "/opt/x"}
			rec, err := l.Apply(t.Context(), e.rt)
			if err != nil {
				t.Fatal(err)
			}
			tt.after(e)

			var r link.Remover
			c, err := r.CheckRemove(t.Context(), e.rt, rec)
			if err != nil || c.Action != tt.action || !slices.Equal(c.Notes, tt.notes) {
				t.Errorf("CheckRemove() = %+v, %v; want %s with notes %q", c, err, tt.action, tt.notes)
			}
			if c.Action == resource.Remove && c.Summary != "~/bin/x" {
				t.Errorf("CheckRemove() summary = %q, want ~/bin/x", c.Summary)
			}
			if err := r.Remove(t.Context(), e.rt, rec); err != nil {
				t.Fatal(err)
			}
			_, err = os.Lstat(e.path("bin/x"))
			if gone := errors.Is(err, fs.ErrNotExist); gone != tt.removed {
				t.Errorf("after Remove(), the link is gone: %t, want %t", gone, tt.removed)
			}
		})
	}

	e := newEnv(t)
	var r link.Remover
	if _, err := r.CheckRemove(t.Context(), e.rt, state.Record{ID: "symlink:x", Kind: "symlink"}); err == nil || r.Kind() != "symlink" {
		t.Errorf("CheckRemove() of a record without a path: error = %v; Kind() = %q", err, r.Kind())
	}
}
