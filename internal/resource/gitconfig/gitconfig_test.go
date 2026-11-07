package gitconfig_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/gitconfig"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// fakeGit answers "git config --file FILE ..." from values, as git does.
type fakeGit struct {
	t      *testing.T
	file   string
	mu     sync.Mutex
	values map[string][]string
}

func (g *fakeGit) run(_ context.Context, c system.Cmd) (system.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	args := c.Args
	if len(args) < 5 || args[0] != "config" || args[1] != "--file" || args[2] != g.file || c.Root {
		g.t.Errorf("unexpected git command: %s (root %t)", c, c.Root)
		return system.Result{ExitCode: 129}, nil
	}
	op, key := args[3], args[len(args)-1]
	if _, err := os.Stat(filepath.Dir(g.file)); err != nil && op != "--null" {
		// git can't create the config file's folder.
		return system.Result{ExitCode: 255, Stderr: []byte("error: could not lock config file " + g.file)}, nil
	}
	switch op {
	case "--null":
		values := g.values[key]
		if len(values) == 0 {
			return system.Result{ExitCode: 1}, nil
		}
		return system.Result{Stdout: []byte(strings.Join(values, "\x00") + "\x00")}, nil
	case "--replace-all":
		g.values[args[4]] = []string{args[5]}
	case "--add":
		g.values[args[4]] = append(g.values[args[4]], args[5])
	case "--unset-all":
		if len(g.values[key]) == 0 {
			return system.Result{ExitCode: 5}, nil
		}
		delete(g.values, key)
	default:
		g.t.Errorf("unexpected git command: %s", c)
	}
	return system.Result{}, nil
}

func newEnv(t *testing.T, values map[string][]string) (*resource.Runtime, *systemtest.FakeRunner, *fakeGit) {
	t.Helper()
	home := t.TempDir()
	g := &fakeGit{t: t, file: filepath.Join(home, ".config", "git", "config"), values: values}
	runner := systemtest.NewFakeRunner(t)
	runner.On("git config", g.run)
	return &resource.Runtime{Runner: runner, Paths: settings.Paths{Home: home}}, runner, g
}

const gcm = "/mnt/c/Program Files/Git/mingw64/bin/git-credential-manager.exe"

func TestSetting(t *testing.T) {
	tests := []struct {
		name    string
		before  map[string][]string
		values  []string
		action  resource.Action
		summary string
	}{
		{
			name:    "not set",
			before:  map[string][]string{},
			values:  []string{gcm},
			action:  resource.Create,
			summary: "git credential.helper = " + gcm,
		},
		{
			name:    "set to something else",
			before:  map[string][]string{"credential.helper": {"cache"}},
			values:  []string{gcm},
			action:  resource.Update,
			summary: "git credential.helper cache → " + gcm,
		},
		{
			name:    "several values replaced",
			before:  map[string][]string{"credential.helper": {"cache", "store"}},
			values:  []string{"", gcm},
			action:  resource.Update,
			summary: `git credential.helper "cache", "store" → "", "` + gcm + `"`,
		},
		{
			name:   "already set",
			before: map[string][]string{"credential.helper": {gcm}},
			values: []string{gcm},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, runner, g := newEnv(t, tt.before)
			s := &gitconfig.Setting{Key: "credential.helper", Values: tt.values}
			if s.ID() != "git-setting:credential.helper" || s.Kind() != "git-setting" || s.Privileged() || s.Targets() != nil {
				t.Errorf("ID() = %q, Kind() = %q, Privileged() = %t, Targets() = %q", s.ID(), s.Kind(), s.Privileged(), s.Targets())
			}
			c, err := s.Check(t.Context(), rt)
			if err != nil || c.Action != tt.action || c.Summary != tt.summary {
				t.Errorf("Check() = %s %q, %v; want %s %q", c.Action, c.Summary, err, tt.action, tt.summary)
			}
			rec, err := s.Apply(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if got := g.values["credential.helper"]; !slices.Equal(got, tt.values) {
				t.Errorf("credential.helper = %q after Apply(), want %q", got, tt.values)
			}
			if rec.Details[gitconfig.DetailKey] != "credential.helper" || rec.Details[gitconfig.DetailFile] != "~/.config/git/config" {
				t.Errorf("record = %+v, want the key and file", rec)
			}
			if c, err := s.Check(t.Context(), rt); err != nil || c.Action != resource.NoChange {
				t.Errorf("Check() after Apply() = %s %q, %v; want no change", c.Action, c.Summary, err)
			}
			for _, cmd := range runner.Commands() {
				if strings.Contains(cmd, ".gitconfig") {
					t.Errorf("ran %s, which uses ~/.gitconfig", cmd)
				}
			}
		})
	}
}

func TestSettingCreatesFolder(t *testing.T) {
	rt, _, g := newEnv(t, map[string][]string{})
	s := &gitconfig.Setting{Key: "init.defaultBranch", Values: []string{"main"}}
	if c, err := s.Check(t.Context(), rt); err != nil || c.Action != resource.Create {
		t.Errorf("Check() without the folder = %+v, %v; want create", c, err)
	}
	if _, err := s.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	if got := g.values["init.defaultBranch"]; !slices.Equal(got, []string{"main"}) {
		t.Errorf("init.defaultBranch = %q, want main", got)
	}
}

func TestSettingOtherFile(t *testing.T) {
	rt, _, g := newEnv(t, map[string][]string{})
	g.file = filepath.Join(rt.Paths.Home, ".config", "maisys", "gitconfig")
	s := &gitconfig.Setting{Key: "a.b", Values: []string{"c"}, File: "~/.config/maisys/gitconfig"}
	rec, err := s.Apply(t.Context(), rt)
	if err != nil || rec.Details[gitconfig.DetailFile] != "~/.config/maisys/gitconfig" {
		t.Errorf("Apply() = %+v, %v; want the file recorded", rec, err)
	}
}

func TestSettingErrors(t *testing.T) {
	rt, runner, _ := newEnv(t, map[string][]string{})
	for _, s := range []*gitconfig.Setting{
		{Key: "a.b"},
		{Values: []string{"x"}},
		{Key: "a.b", Values: []string{"x"}, File: "relative"},
	} {
		if _, err := s.Check(t.Context(), rt); err == nil {
			t.Errorf("Check() of %+v: no error", s)
		}
		if _, err := s.Apply(t.Context(), rt); err == nil {
			t.Errorf("Apply() of %+v: no error", s)
		}
	}

	runner.On("git config", systemtest.Fail(128, "fatal: bad config line 1 in file ~/.config/git/config"))
	s := &gitconfig.Setting{Key: "a.b", Values: []string{"x"}}
	if _, err := s.Check(t.Context(), rt); err == nil || !strings.Contains(err.Error(), "bad config line") {
		t.Errorf("Check() of a broken file: error = %v, want git's", err)
	}
}

func TestRemover(t *testing.T) {
	rt, _, g := newEnv(t, map[string][]string{"credential.helper": {"", gcm}, "user.name": {"Phil"}})
	if err := os.MkdirAll(filepath.Dir(g.file), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := state.Record{ID: "git-setting:credential.helper", Kind: "git-setting", Details: map[string]string{
		gitconfig.DetailKey: "credential.helper", gitconfig.DetailFile: "~/.config/git/config",
	}}

	var r gitconfig.Remover
	if r.Kind() != "git-setting" {
		t.Errorf("Kind() = %q, want git-setting", r.Kind())
	}
	c, err := r.CheckRemove(t.Context(), rt, rec)
	if err != nil || c.Action != resource.Remove || c.Summary != "git credential.helper" || c.Privileged {
		t.Errorf("CheckRemove() = %+v, %v; want to unset credential.helper", c, err)
	}
	if err := r.Remove(t.Context(), rt, rec); err != nil {
		t.Fatal(err)
	}
	if _, ok := g.values["credential.helper"]; ok || len(g.values["user.name"]) != 1 {
		t.Errorf("settings after Remove() = %q, want only credential.helper unset", g.values)
	}

	// Once it's unset, there's nothing left to do.
	if c, err := r.CheckRemove(t.Context(), rt, rec); err != nil || c.Action != resource.NoChange {
		t.Errorf("CheckRemove() of an unset setting = %+v, %v; want no change", c, err)
	}
	if err := r.Remove(t.Context(), rt, rec); err != nil {
		t.Errorf("Remove() of an unset setting: %v", err)
	}

	// Records without a file use ~/.config/git/config.
	delete(rec.Details, gitconfig.DetailFile)
	g.values["credential.helper"] = []string{gcm}
	if c, err := r.CheckRemove(t.Context(), rt, rec); err != nil || c.Action != resource.Remove {
		t.Errorf("CheckRemove() of a record without a file = %+v, %v; want remove", c, err)
	}

	if _, err := r.CheckRemove(t.Context(), rt, state.Record{ID: "git-setting:x", Kind: "git-setting"}); err == nil {
		t.Error("CheckRemove() of a record without a key: no error")
	}
}
