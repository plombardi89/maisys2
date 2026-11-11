package file_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/state"
)

// filesModule is a module whose resources are files.
type filesModule struct{ files []*file.File }

func (m *filesModule) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: "files", Summary: "Files.", OS: config.AllOS()}
}

func (m *filesModule) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func (m *filesModule) DependsOn() []string { return nil }

func (m *filesModule) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	rs := make([]resource.Resource, len(m.files))
	for i, f := range m.files {
		rs[i] = f
	}
	return rs, nil
}

// sync plans and applies the files, as maisys sync does, and returns the
// plan as it's shown.
func sync(t *testing.T, e *env, store *state.Store, files ...*file.File) string {
	t.Helper()
	machine := &config.Machine{Name: "box", OS: config.Ubuntu, Modules: map[string]config.ModuleConfig{"files": &config.Toggle{Enabled: true}}}
	p, err := engine.NewPlan(t.Context(), engine.PlanInput{
		Machine:  machine,
		Facts:    e.rt.Facts,
		Paths:    e.rt.Paths,
		Runner:   e.runner,
		State:    store.State,
		Modules:  []config.Module{&filesModule{files: files}},
		Removers: resource.NewRemovers(file.Remover{}),
		Now:      func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	p.Render(&b, false)
	if res := engine.Apply(t.Context(), p, engine.ApplyInput{Store: store}); !res.OK() {
		var out bytes.Buffer
		res.Render(&out)
		t.Fatalf("applying failed:\n%s", out.String())
	}
	return b.String()
}

func TestFilesSync(t *testing.T) {
	e := newEnv(t)
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	e.write(".gitconfig", "[user]\n\tname = Phil\n", 0o644)
	nvim := textFile(".config/nvim/init.lua", "-- init\n")
	gitconfig := textFile(".gitconfig", "[user]\n\temail = phil@example.com\n")

	got := sync(t, e, store, nvim, gitconfig)
	want := "Plan for box (ubuntu 26.04, amd64)\n\n" +
		"files\n" +
		"  + ~/.config/nvim/init.lua\n" +
		"  ! ~/.gitconfig exists (will back up and take over)\n\n" +
		"2 to change.\n"
	if got != want {
		t.Errorf("first plan:\n%s\nwant:\n%s", got, want)
	}
	if data, err := os.ReadFile(e.path(".gitconfig") + stamp); err != nil || string(data) != "[user]\n\tname = Phil\n" {
		t.Errorf("the backup of ~/.gitconfig holds %q, %v", data, err)
	}

	got = sync(t, e, store, nvim, gitconfig)
	if want := "Plan for box (ubuntu 26.04, amd64)\n\nNothing to change.\n"; got != want {
		t.Errorf("second plan:\n%s\nwant:\n%s", got, want)
	}

	e.write(".gitconfig", "[user]\n\temail = me@example.com\n", 0o644)
	got = sync(t, e, store, nvim, gitconfig)
	want = "Plan for box (ubuntu 26.04, amd64)\n\n" +
		"files\n" +
		"  ! ~/.gitconfig changed outside maisys (will back up and overwrite)\n\n" +
		"1 to change.\n"
	if got != want {
		t.Errorf("plan after an edit:\n%s\nwant:\n%s", got, want)
	}

	// Taking both out of the config deletes the file maisys created, and
	// leaves the one it took over.
	got = sync(t, e, store)
	want = "Plan for box (ubuntu 26.04, amd64)\n\n" +
		"prune\n" +
		"  - ~/.config/nvim/init.lua\n" +
		"  ! ~/.gitconfig left in place; no longer managed\n\n" +
		"1 to remove.\n"
	if got != want {
		t.Errorf("plan after taking the files out:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Lstat(e.path(".config/nvim/init.lua")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.config/nvim/init.lua is still there: %v", err)
	}
	if got := e.read(".gitconfig"); got != "[user]\n\temail = phil@example.com\n" {
		t.Errorf("~/.gitconfig holds %q, want it left in place", got)
	}
	if ids := store.IDs(); len(ids) != 0 {
		t.Errorf("records = %q, want none", ids)
	}

	got = sync(t, e, store)
	if want := "Plan for box (ubuntu 26.04, amd64)\n\nNothing to change.\n"; got != want {
		t.Errorf("plan once they're gone:\n%s\nwant:\n%s", got, want)
	}
	if backups := backups(t, e, ".gitconfig"); !slices.Equal(backups, []string{e.path(".gitconfig") + stamp, e.path(".gitconfig") + stamp + ".1"}) {
		t.Errorf("backups = %q, want one for taking over and one for the edit", backups)
	}
}

// TestFilesSyncAlreadyRight syncs files that already hold what the config
// says: one maisys wrote and you then edited, before committing the edit
// to the config, and one that was there before maisys. The sync has nothing
// to change, but brings their records up to date: the next change of the
// config is an update without a backup, and taking them out deletes the
// one maisys wrote and leaves the other.
func TestFilesSyncAlreadyRight(t *testing.T) {
	e := newEnv(t)
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	sync(t, e, store, textFile(".config/foo", "v1\n"))
	e.write(".config/foo", "v2\n", 0o644)
	e.write(".config/bar", "v2\n", 0o644)

	got := sync(t, e, store, textFile(".config/foo", "v2\n"), textFile(".config/bar", "v2\n"))
	if want := "Plan for box (ubuntu 26.04, amd64)\n\nNothing to change.\n"; got != want {
		t.Errorf("plan once the files hold what the config says:\n%s\nwant:\n%s", got, want)
	}

	got = sync(t, e, store, textFile(".config/foo", "v3\n"), textFile(".config/bar", "v3\n"))
	want := "Plan for box (ubuntu 26.04, amd64)\n\n" +
		"files\n" +
		"  ~ ~/.config/foo\n" +
		"  ~ ~/.config/bar\n\n" +
		"2 to change.\n"
	if got != want {
		t.Errorf("plan after changing the config:\n%s\nwant:\n%s", got, want)
	}
	for _, rel := range []string{".config/foo", ".config/bar"} {
		if b := backups(t, e, rel); len(b) > 0 {
			t.Errorf("backups = %q, want none: maisys wrote or took over what was there", b)
		}
	}

	got = sync(t, e, store)
	want = "Plan for box (ubuntu 26.04, amd64)\n\n" +
		"prune\n" +
		"  ! ~/.config/bar left in place; no longer managed\n" +
		"  - ~/.config/foo\n\n" +
		"1 to remove.\n"
	if got != want {
		t.Errorf("plan after taking the files out:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Lstat(e.path(".config/foo")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("~/.config/foo is still there: %v", err)
	}
	if got := e.read(".config/bar"); got != "v3\n" {
		t.Errorf("~/.config/bar holds %q, want it left in place", got)
	}
}
