package unit_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/unit"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

const service = "[Unit]\nDescription=X410 bridge\n\n[Service]\nExecStart=/usr/bin/socat\n\n[Install]\nWantedBy=multi-user.target\n"

// fakeSystemd answers systemctl commands for one unit, as systemd would.
type fakeSystemd struct {
	mu      sync.Mutex
	enabled string // what is-enabled prints
	active  string // what is-active prints
	// answers, if set, answer the verbs they have instead, as systemctl
	// does when it can't reach systemd.
	answers map[string]system.Result
}

func (s *fakeSystemd) run(_ context.Context, c system.Cmd) (system.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	args := slices.DeleteFunc(slices.Clone(c.Args), func(a string) bool { return a == "--user" })
	if res, ok := s.answers[args[0]]; ok {
		return res, nil
	}
	switch args[0] {
	case "is-enabled":
		if s.enabled == "enabled" || s.enabled == "static" {
			return system.Result{Stdout: []byte(s.enabled + "\n")}, nil
		}
		return system.Result{ExitCode: 1, Stdout: []byte(s.enabled + "\n")}, nil
	case "is-active":
		if s.active == "active" {
			return system.Result{Stdout: []byte("active\n")}, nil
		}
		return system.Result{ExitCode: 3, Stdout: []byte(s.active + "\n")}, nil
	case "enable":
		s.enabled = "enabled"
		if slices.Contains(args, "--now") {
			s.active = "active"
		}
	case "restart":
		s.active = "active"
	case "disable":
		s.enabled, s.active = "disabled", "inactive"
	}
	return system.Result{}, nil
}

// env is a home folder, a folder for system units, and a fake systemd.
type env struct {
	t       *testing.T
	home    string
	dir     string // where system units go
	runner  *systemtest.FakeRunner
	systemd *fakeSystemd
	state   *state.State
	rt      *resource.Runtime
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("systemd is only on Linux")
	}
	e := &env{
		t:       t,
		home:    t.TempDir(),
		dir:     t.TempDir(),
		runner:  systemtest.NewFakeRunner(t),
		systemd: &fakeSystemd{enabled: "disabled", active: "inactive"},
		state:   &state.State{},
	}
	e.runner.On("systemctl", e.systemd.run)
	e.runner.On("install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		mode, _ := strconv.ParseUint(c.Args[1], 8, 32)
		data, err := os.ReadFile(c.Args[6])
		if err == nil {
			err = os.WriteFile(c.Args[7], data, os.FileMode(mode))
		}
		if err != nil {
			return system.Result{ExitCode: -1}, err
		}
		return system.Result{}, nil
	})
	e.runner.On("rm", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if err := os.Remove(c.Args[len(c.Args)-1]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return system.Result{ExitCode: 1}, nil
		}
		return system.Result{}, nil
	})
	e.rt = &resource.Runtime{
		Runner:   e.runner,
		Paths:    settings.Paths{Home: e.home},
		Previous: e.state.Get,
		Now:      func() time.Time { return testNow },
	}
	return e
}

// unit returns the x410.service system unit with content.
func (e *env) unit(content string) *unit.Unit {
	return &unit.Unit{Name: "x410.service", Content: file.Bytes([]byte(content)), Dir: e.dir}
}

// check checks u while sudo needs a password, as a plan does.
func (e *env) check(u *unit.Unit) resource.Change {
	e.t.Helper()
	e.runner.SudoStatus = system.SudoNeedsPassword
	defer func() { e.runner.SudoStatus = "" }()
	c, err := u.Check(e.t.Context(), e.rt)
	if err != nil {
		e.t.Fatalf("Check() error = %v", err)
	}
	return c
}

// apply checks and applies u, keeping its record, and returns the change
// and the commands applying ran.
func (e *env) apply(u *unit.Unit) (resource.Change, state.Record, []string) {
	e.t.Helper()
	c := e.check(u)
	before := len(e.runner.Commands())
	rec, err := u.Apply(e.t.Context(), e.rt)
	if err != nil {
		e.t.Fatalf("Apply() error = %v", err)
	}
	rec.ID, rec.Kind = u.ID(), u.Kind()
	e.state.Put(rec)
	var cmds []string
	for _, cmd := range e.runner.Commands()[before:] {
		if !strings.Contains(cmd, " is-") {
			cmds = append(cmds, cmd)
		}
	}
	return c, rec, cmds
}

func TestUnit(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(e *env)
		action  resource.Action
		summary string
		cmds    []string // the commands applying runs, besides checks
	}{
		{
			name:    "new",
			setup:   func(*env) {},
			action:  resource.Create,
			summary: "systemd unit x410.service",
			cmds:    []string{"install", "systemctl daemon-reload", "systemctl enable x410.service", "systemctl restart x410.service"},
		},
		{
			name: "disabled and stopped",
			setup: func(e *env) {
				e.apply(e.unit(service))
				e.systemd.enabled, e.systemd.active = "disabled", "inactive"
			},
			action:  resource.Update,
			summary: "systemd unit x410.service (enable and start)",
			cmds:    []string{"systemctl enable --now x410.service"},
		},
		{
			name: "stopped",
			setup: func(e *env) {
				e.apply(e.unit(service))
				e.systemd.active = "failed"
			},
			action:  resource.Update,
			summary: "systemd unit x410.service (start)",
			cmds:    []string{"systemctl enable --now x410.service"},
		},
		{
			name: "disabled",
			setup: func(e *env) {
				e.apply(e.unit(service))
				e.systemd.enabled = "disabled"
			},
			action:  resource.Update,
			summary: "systemd unit x410.service (enable)",
			cmds:    []string{"systemctl enable --now x410.service"},
		},
		{
			name: "a new unit file",
			setup: func(e *env) {
				e.apply(e.unit("[Unit]\n"))
			},
			action:  resource.Update,
			summary: "systemd unit x410.service (update the unit file)",
			cmds:    []string{"install", "systemctl daemon-reload", "systemctl enable x410.service", "systemctl restart x410.service"},
		},
		{
			name: "a unit file changed outside maisys",
			setup: func(e *env) {
				e.apply(e.unit("[Unit]\n"))
				if err := os.WriteFile(filepath.Join(e.dir, "x410.service"), []byte("[Unit]\n# mine\n"), 0o644); err != nil {
					e.t.Fatal(err)
				}
			},
			action:  resource.Warn,
			summary: "x410.service changed outside maisys (will back up and overwrite)",
			cmds:    []string{"install", "install", "systemctl daemon-reload", "systemctl enable x410.service", "systemctl restart x410.service"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			u := e.unit(service)
			c, rec, cmds := e.apply(u)
			if c.Action != tt.action || !strings.HasSuffix(c.Summary, tt.summary) {
				t.Errorf("Check() = %s %q, want %s %q", c.Action, c.Summary, tt.action, tt.summary)
			}
			for i := range cmds {
				if strings.HasPrefix(cmds[i], "install ") {
					cmds[i] = "install"
				}
			}
			if !slices.Equal(cmds, tt.cmds) {
				t.Errorf("Apply() ran %q, want %q", cmds, tt.cmds)
			}
			for _, call := range e.runner.Calls() {
				if strings.Contains(call.Cmd.String(), " is-") == call.Cmd.Root {
					t.Errorf("%s ran as root: %t, want only changes to run as root", call.Cmd, call.Cmd.Root)
				}
			}
			if data, err := os.ReadFile(filepath.Join(e.dir, "x410.service")); err != nil || string(data) != service {
				t.Errorf("the unit file holds %q, %v; want the unit", data, err)
			}
			if rec.Details[unit.DetailName] != "x410.service" || rec.Details[unit.DetailScope] != "system" || rec.Details[file.DetailRoot] != "true" ||
				rec.Details[file.DetailHash] == "" || !slices.Equal(rec.Paths, []string{filepath.Join(e.dir, "x410.service")}) {
				t.Errorf("record = %+v, want the unit, its scope and its file", rec)
			}
			if c := e.check(u); c.Action != resource.NoChange {
				t.Errorf("Check() after Apply() = %s %q, want no change", c.Action, c.Summary)
			}
		})
	}
}

func TestUserUnit(t *testing.T) {
	e := newEnv(t)
	u := &unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(service))}
	if u.ID() != "systemd-unit:user/x410.service" || u.Privileged() || !slices.Equal(u.Targets(), []string{"~/.config/systemd/user/x410.service"}) {
		t.Errorf("ID() = %q, Privileged() = %t, Targets() = %q", u.ID(), u.Privileged(), u.Targets())
	}
	c, rec, cmds := e.apply(u)
	if c.Action != resource.Create || c.Summary != "systemd user unit x410.service" {
		t.Errorf("Check() = %s %q, want a new user unit", c.Action, c.Summary)
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable x410.service", "systemctl --user restart x410.service"}
	if !slices.Equal(cmds, want) {
		t.Errorf("Apply() ran %q, want %q", cmds, want)
	}
	if got := e.runner.RootCommands(); len(got) != 0 {
		t.Errorf("commands run as root = %q, want none", got)
	}
	if data, err := os.ReadFile(filepath.Join(e.home, ".config", "systemd", "user", "x410.service")); err != nil || string(data) != service {
		t.Errorf("the unit file holds %q, %v; want the unit", data, err)
	}
	if rec.Details[unit.DetailScope] != "user" || rec.Details[file.DetailRoot] != "" || !slices.Equal(rec.Paths, []string{"~/.config/systemd/user/x410.service"}) {
		t.Errorf("record = %+v, want a user unit in the home folder", rec)
	}
	if c := e.check(u); c.Action != resource.NoChange {
		t.Errorf("Check() after Apply() = %s %q, want no change", c.Action, c.Summary)
	}
}

func TestUnitDefaults(t *testing.T) {
	e := newEnv(t)
	u := &unit.Unit{Name: "x410.service"}
	if u.ID() != "systemd-unit:x410.service" || u.Kind() != "systemd-unit" || !u.Privileged() ||
		!slices.Equal(u.Targets(), []string{"/etc/systemd/system/x410.service"}) {
		t.Errorf("ID() = %q, Kind() = %q, Privileged() = %t, Targets() = %q", u.ID(), u.Kind(), u.Privileged(), u.Targets())
	}

	for _, name := range []string{"", "x410", "../x.service"} {
		u := &unit.Unit{Name: name, Content: file.Bytes(nil), Dir: e.dir}
		if _, err := u.Check(t.Context(), e.rt); err == nil {
			t.Errorf("Check() of unit %q: no error", name)
		}
		if _, err := u.Apply(t.Context(), e.rt); err == nil {
			t.Errorf("Apply() of unit %q: no error", name)
		}
	}
}

func TestUnitStatic(t *testing.T) {
	e := newEnv(t)
	e.apply(e.unit(service))
	e.systemd.enabled = "static"
	if c := e.check(e.unit(service)); c.Action != resource.NoChange {
		t.Errorf("Check() of a static unit = %s %q, want no change", c.Action, c.Summary)
	}
}

// TestUnitState checks what a check makes of what systemctl is-enabled and
// is-active print: a state, a state newer than maisys, which their exit
// code tells, or anything else, such as systemctl's error when it can't
// reach systemd, which fails the check with what systemctl printed rather
// than plan a change that fails.
func TestUnitState(t *testing.T) {
	noBus := system.Result{ExitCode: 1, Stderr: []byte("Failed to connect to bus: No medium found\n")}
	tests := []struct {
		name    string
		user    bool
		answers map[string]system.Result
		summary string // the check's summary, if it doesn't fail
		wantErr string
	}{
		{
			name:    "no user bus",
			user:    true,
			answers: map[string]system.Result{"is-enabled": noBus},
			wantErr: "systemctl --user is-enabled failed with exit code 1: Failed to connect to bus: No medium found",
		},
		{
			name:    "is-active can't reach systemd",
			answers: map[string]system.Result{"is-active": noBus},
			wantErr: "systemctl is-active failed with exit code 1: Failed to connect to bus: No medium found",
		},
		{
			name:    "not a state",
			answers: map[string]system.Result{"is-enabled": {ExitCode: 1, Stdout: []byte("Unit x410.service could not be found.\n")}},
			wantErr: "systemctl is-enabled failed with exit code 1: Unit x410.service could not be found.",
		},
		{
			name:    "states newer than maisys",
			answers: map[string]system.Result{"is-enabled": {Stdout: []byte("enabled-forever\n")}, "is-active": {Stdout: []byte("active-ish\n")}},
		},
		{
			name:    "masked",
			answers: map[string]system.Result{"is-enabled": {ExitCode: 1, Stdout: []byte("masked\n")}},
			summary: "systemd unit x410.service (enable)",
		},
		{
			name:    "reloading",
			answers: map[string]system.Result{"is-active": {Stdout: []byte("reloading\n")}},
		},
		{
			name:    "restarting after a failure",
			answers: map[string]system.Result{"is-active": {ExitCode: 3, Stdout: []byte("activating\n")}},
			summary: "systemd unit x410.service (start)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := e.unit(service)
			if tt.user {
				u = &unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(service))}
			}
			e.apply(u)
			e.systemd.answers = tt.answers
			c, err := u.Check(t.Context(), e.rt)
			switch {
			case tt.wantErr != "":
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Check() = %+v, %v; want the error %q", c, err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("Check() error = %v", err)
			case tt.summary == "" && c.Action != resource.NoChange,
				tt.summary != "" && (c.Action != resource.Update || c.Summary != tt.summary):
				t.Errorf("Check() = %s %q, want %q", c.Action, c.Summary, tt.summary)
			}
		})
	}
}

// TestUnitRecord checks the record a check says a running unit should have
// when its file already holds what it should: none while the state file's
// record is up to date, and otherwise the one applying would leave.
func TestUnitRecord(t *testing.T) {
	path := func(e *env) string { return filepath.Join(e.dir, "x410.service") }
	tests := []struct {
		name     string
		setup    func(e *env)
		want     bool // whether the check has a record
		tookOver bool
	}{
		{
			name:  "applied before",
			setup: func(e *env) { e.apply(e.unit(service)) },
		},
		{
			name: "edited to what the config now says",
			setup: func(e *env) {
				e.apply(e.unit("[Unit]\n"))
				if err := os.WriteFile(path(e), []byte(service), 0o644); err != nil {
					e.t.Fatal(err)
				}
			},
			want: true,
		},
		{
			name: "there before maisys",
			setup: func(e *env) {
				if err := os.WriteFile(path(e), []byte(service), 0o644); err != nil {
					e.t.Fatal(err)
				}
				e.systemd.enabled, e.systemd.active = "enabled", "active"
			},
			want:     true,
			tookOver: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.setup(e)
			c := e.check(e.unit(service))
			if c.Action != resource.NoChange {
				t.Fatalf("Check() = %s %q, want no change", c.Action, c.Summary)
			}
			want := &state.Record{
				Paths:    []string{path(e)},
				Details:  map[string]string{file.DetailHash: sha(service), file.DetailRoot: "true", unit.DetailName: "x410.service", unit.DetailScope: "system"},
				TookOver: tt.tookOver,
			}
			switch {
			case !tt.want && c.Record != nil:
				t.Errorf("Check() has the record %+v, want none: the state file's is up to date", *c.Record)
			case tt.want && (c.Record == nil || !reflect.DeepEqual(c.Record, want)):
				t.Errorf("Check() has the record %v, want %+v", c.Record, *want)
			case tt.want:
				rec := *c.Record
				rec.ID, rec.Kind = "systemd-unit:x410.service", "systemd-unit"
				e.state.Put(rec)
				if c := e.check(e.unit(service)); c.Action != resource.NoChange || c.Record != nil {
					t.Errorf("Check() after the record was saved = %s %q with record %v, want no change and no record", c.Action, c.Summary, c.Record)
				}
			}
		})
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestRemover(t *testing.T) {
	t.Run("system", func(t *testing.T) {
		e := newEnv(t)
		_, rec, _ := e.apply(e.unit(service))
		before := len(e.runner.Commands())

		var r unit.Remover
		c, err := r.CheckRemove(t.Context(), e.rt, rec)
		if err != nil || c.Action != resource.Remove || c.Summary != "systemd unit x410.service" || !c.Privileged {
			t.Errorf("CheckRemove() = %+v, %v; want removing the unit as root", c, err)
		}
		if err := r.Remove(t.Context(), e.rt, rec); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(e.dir, "x410.service")
		want := []string{"systemctl disable --now x410.service", "rm -f -- " + path, "systemctl daemon-reload"}
		if got := e.runner.RootCommands()[len(e.runner.RootCommands())-3:]; !slices.Equal(got, want) || len(e.runner.Commands())-before != 3 {
			t.Errorf("Remove() ran %q as root, want %q", got, want)
		}
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the unit file is still there: %v", err)
		}
		if c, err := r.CheckRemove(t.Context(), e.rt, rec); err != nil || c.Action != resource.NoChange {
			t.Errorf("CheckRemove() once it's gone = %+v, %v; want no change", c, err)
		}
	})

	t.Run("user", func(t *testing.T) {
		e := newEnv(t)
		_, rec, _ := e.apply(&unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(service))})
		before := len(e.runner.Commands())

		var r unit.Remover
		c, err := r.CheckRemove(t.Context(), e.rt, rec)
		if err != nil || c.Action != resource.Remove || c.Summary != "systemd user unit x410.service" || c.Privileged {
			t.Errorf("CheckRemove() = %+v, %v; want removing the user unit", c, err)
		}
		if err := r.Remove(t.Context(), e.rt, rec); err != nil {
			t.Fatal(err)
		}
		want := []string{"systemctl --user disable --now x410.service", "systemctl --user daemon-reload"}
		if got := e.runner.Commands()[before:]; !slices.Equal(got, want) {
			t.Errorf("Remove() ran %q, want %q", got, want)
		}
		if len(e.runner.RootCommands()) != 0 {
			t.Errorf("commands run as root = %q, want none", e.runner.RootCommands())
		}
		if _, err := os.Lstat(filepath.Join(e.home, ".config", "systemd", "user", "x410.service")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the unit file is still there: %v", err)
		}
	})

	t.Run("errors", func(t *testing.T) {
		e := newEnv(t)
		var r unit.Remover
		if r.Kind() != "systemd-unit" {
			t.Errorf("Kind() = %q, want systemd-unit", r.Kind())
		}
		rec := state.Record{ID: "systemd-unit:x", Kind: "systemd-unit"}
		if _, err := r.CheckRemove(t.Context(), e.rt, rec); err == nil {
			t.Error("CheckRemove() of a record without a unit: no error")
		}
		if err := r.Remove(t.Context(), e.rt, rec); err == nil {
			t.Error("Remove() of a record without a unit: no error")
		}
	})
}
