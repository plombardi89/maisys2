package file_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// testNow is the time tests run at, and the timestamp of their backups.
var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

// stamp is testNow as backups are named with it.
const stamp = ".maisys-bak.20261106-093000"

// env is a home folder and the runtime resources check and apply with.
type env struct {
	t      *testing.T
	home   string
	runner *systemtest.FakeRunner
	state  *state.State
	rt     *resource.Runtime
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	st := &state.State{}
	runner := systemtest.NewFakeRunner(t)
	return &env{
		t:      t,
		home:   home,
		runner: runner,
		state:  st,
		rt: &resource.Runtime{
			Runner:   runner,
			Facts:    &facts.System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", Arch: config.AMD64, RawArch: "amd64", Hostname: "box"},
			Paths:    settings.Paths{Home: home},
			Machine:  &config.Machine{Name: "box", OS: config.Ubuntu, OSVersion: "26.04"},
			Previous: st.Get,
			Now:      func() time.Time { return testNow },
		},
	}
}

// check checks r, failing the test if that fails.
func (e *env) check(r resource.Resource) resource.Change {
	e.t.Helper()
	c, err := r.Check(e.t.Context(), e.rt)
	if err != nil {
		e.t.Fatalf("Check() error = %v", err)
	}
	return c
}

// apply checks r, and applies it if the check found something to do,
// keeping its record as the engine does: the one Apply returns, or the one
// the check says r should have. It returns the change and r's record.
func (e *env) apply(r resource.Resource) (resource.Change, state.Record) {
	e.t.Helper()
	c := e.check(r)
	var rec state.Record
	switch {
	case c.Action.Applies():
		var err error
		if rec, err = r.Apply(e.t.Context(), e.rt); err != nil {
			e.t.Fatalf("Apply() error = %v", err)
		}
	case c.Record != nil:
		rec = *c.Record
	default:
		rec, _ = e.state.Get(r.ID())
		return c, rec
	}
	rec.ID, rec.Kind, rec.Module = r.ID(), r.Kind(), "files"
	e.state.Put(rec)
	return c, rec
}

// path returns the absolute path of rel, a slash-separated path in the
// home folder.
func (e *env) path(rel string) string {
	return filepath.Join(e.home, filepath.FromSlash(rel))
}

// write writes a file in the home folder.
func (e *env) write(rel, content string, perm os.FileMode) {
	e.t.Helper()
	path := e.path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		e.t.Fatal(err)
	}
}

// read returns what a file in the home folder holds.
func (e *env) read(rel string) string {
	e.t.Helper()
	data, err := os.ReadFile(e.path(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

// perm returns the permissions of a file in the home folder.
func (e *env) perm(rel string) os.FileMode {
	e.t.Helper()
	info, err := os.Stat(e.path(rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return info.Mode().Perm()
}

// skipOnWindows skips a test of something Windows doesn't have.
func skipOnWindows(t *testing.T, what string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no " + what)
	}
}

// installFake answers "install -m MODE -o root -g root SRC DST" as the
// real command would, copying SRC to DST with MODE.
func installFake(t *testing.T) systemtest.Handler {
	t.Helper()
	return func(_ context.Context, c system.Cmd) (system.Result, error) {
		if len(c.Args) != 8 || !c.Root {
			t.Errorf("install ran with %q, root %t", c.Args, c.Root)
			return system.Result{ExitCode: 1}, nil
		}
		mode, err := strconv.ParseUint(c.Args[1], 8, 32)
		if err != nil {
			t.Error(err)
		}
		data, err := os.ReadFile(c.Args[6])
		if err == nil {
			err = os.WriteFile(c.Args[7], data, os.FileMode(mode))
		}
		if err == nil {
			err = os.Chmod(c.Args[7], os.FileMode(mode))
		}
		if err != nil {
			return system.Result{ExitCode: -1}, err
		}
		return system.Result{}, nil
	}
}

// rmFake answers "rm -f -- PATH" as the real command would.
func rmFake(t *testing.T) systemtest.Handler {
	t.Helper()
	return func(_ context.Context, c system.Cmd) (system.Result, error) {
		if len(c.Args) != 3 || c.Args[0] != "-f" || c.Args[1] != "--" {
			t.Errorf("rm ran with %q", c.Args)
		}
		if err := os.Remove(c.Args[len(c.Args)-1]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return system.Result{ExitCode: 1, Stderr: []byte(err.Error())}, nil
		}
		return system.Result{}, nil
	}
}
