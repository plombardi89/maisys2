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
	// oneshot makes the unit a oneshot service, which is inactive again
	// once it's started.
	oneshot bool
	// unmet makes a condition in the unit file not hold, so systemd skips
	// starting it.
	unmet bool
	// conditionsMet is ConditionResult, and conditionsChecked
	// ConditionTimestampMonotonic: when they were last checked, or 0.
	conditionsMet     bool
	conditionsChecked int
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
	case "show":
		want := []string{"show", "-p", "ConditionResult", "-p", "ConditionTimestampMonotonic", "x410.service"}
		if !slices.Equal(args, want) {
			return system.Result{ExitCode: 1, Stderr: []byte("unexpected arguments\n")}, nil
		}
		met := "no"
		if s.conditionsMet {
			met = "yes"
		}
		return system.Result{Stdout: []byte("ConditionResult=" + met + "\nConditionTimestampMonotonic=" + strconv.Itoa(s.conditionsChecked) + "\n")}, nil
	case "enable":
		s.enabled = "enabled"
		if slices.Contains(args, "--now") {
			s.start()
		}
	case "restart":
		s.start()
	case "disable":
		s.enabled, s.active = "disabled", "inactive"
	}
	return system.Result{}, nil
}

// start starts the unit, checking its conditions first. s.mu must be held.
func (s *fakeSystemd) start() {
	s.conditionsChecked += 1000
	s.conditionsMet = !s.unmet
	switch {
	case s.unmet, s.oneshot:
		s.active = "inactive"
	default:
		s.active = "active"
	}
}

// env is a home folder, a folder for system units, and a fake systemd,
// which runs until the test stops it.
type env struct {
	t       *testing.T
	home    string
	dir     string // where system units go
	booted  string // there while systemd runs the system
	runtime string // $XDG_RUNTIME_DIR, with the user manager's socket
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
		booted:  filepath.Join(t.TempDir(), "run", "systemd", "system"),
		runtime: t.TempDir(),
		runner:  systemtest.NewFakeRunner(t),
		systemd: &fakeSystemd{enabled: "disabled", active: "inactive"},
		state:   &state.State{},
	}
	unit.SetBootedDir(t, e.booted)
	e.startSystemd()
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
		if !reads(cmd) {
			cmds = append(cmds, cmd)
		}
	}
	return c, rec, cmds
}

// reads reports whether the command line cmd only reads a unit's state, as
// checks do.
func reads(cmd string) bool {
	return strings.Contains(cmd, " is-") || strings.Contains(cmd, " show ")
}

// sync checks u, as a sync's plan does, then applies it if the change
// applies, or else keeps the record the check says it should have. It
// returns the change, and the commands applying ran besides checks.
func (e *env) sync(u *unit.Unit) (resource.Change, []string) {
	e.t.Helper()
	c := e.check(u)
	if c.Action.Applies() {
		_, _, cmds := e.apply(u)
		return c, cmds
	}
	if c.Record != nil {
		rec := *c.Record
		rec.ID, rec.Kind = u.ID(), u.Kind()
		e.state.Put(rec)
	}
	return c, nil
}

// startSystemd makes systemd run the system, and this session reach your
// user manager.
func (e *env) startSystemd() {
	e.t.Helper()
	if err := os.MkdirAll(e.booted, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(e.runtime, "systemd"), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.runtime, "systemd", "private"), nil, 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.t.Setenv("XDG_RUNTIME_DIR", e.runtime)
}

// stopSystemd stops systemd, as in WSL before /etc/wsl.conf's [boot]
// systemd takes effect.
func (e *env) stopSystemd() {
	e.t.Helper()
	e.stopUserManager()
	if err := os.RemoveAll(e.booted); err != nil {
		e.t.Fatal(err)
	}
}

// stopUserManager stops your user manager, leaving $XDG_RUNTIME_DIR.
func (e *env) stopUserManager() {
	e.t.Helper()
	if err := os.RemoveAll(filepath.Join(e.runtime, "systemd", "private")); err != nil {
		e.t.Fatal(err)
	}
}

// leaveSession leaves the login session, as podman exec runs commands
// without one: there's no $XDG_RUNTIME_DIR.
func (e *env) leaveSession() {
	e.t.Setenv("XDG_RUNTIME_DIR", "")
}

// systemctl returns the systemctl commands in cmds.
func systemctl(cmds []string) []string {
	return slices.DeleteFunc(slices.Clone(cmds), func(c string) bool { return !strings.HasPrefix(c, "systemctl ") })
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
				if reads(call.Cmd.String()) == call.Cmd.Root {
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

// TestUnitWithoutSystemd checks a unit while maisys can't reach systemd:
// applying only writes the unit file, the plan notes when the unit is
// enabled and started, and a dry run after that has nothing to change.
// The first sync that can reach systemd enables and starts the unit, and
// restarts it if its file changed in the meantime.
func TestUnitWithoutSystemd(t *testing.T) {
	const (
		notRunning = "systemd isn't running: once it is, the next maisys sync enables and starts x410.service"
		noSession  = "this session can't reach your systemd user manager: the next maisys sync run in a login session enables and starts x410.service"
	)
	reload := []string{"systemctl daemon-reload", "systemctl enable x410.service", "systemctl restart x410.service"}
	userReload := []string{"systemctl --user daemon-reload", "systemctl --user enable x410.service", "systemctl --user restart x410.service"}
	tests := []struct {
		name    string
		user    bool
		before  string // the unit file a sync that reached systemd applied, if any
		stop    func(e *env)
		change  resource.Change // what the check finds then
		pending bool            // whether applying leaves systemd to be told about the file
		later   string          // what a check that reaches systemd then finds, if anything
		cmds    []string        // the commands applying that runs
	}{
		{
			name:    "systemd isn't running",
			stop:    (*env).stopSystemd,
			change:  resource.Change{Action: resource.Create, Summary: "systemd unit x410.service", Notes: []string{notRunning}},
			pending: true,
			later:   "systemd unit x410.service (enable and start)",
			cmds:    reload,
		},
		{
			name:    "a user unit while systemd isn't running",
			user:    true,
			stop:    (*env).stopSystemd,
			change:  resource.Change{Action: resource.Create, Summary: "systemd user unit x410.service", Notes: []string{notRunning}},
			pending: true,
			later:   "systemd user unit x410.service (enable and start)",
			cmds:    userReload,
		},
		{
			name:    "a user unit without a login session",
			user:    true,
			stop:    (*env).leaveSession,
			change:  resource.Change{Action: resource.Create, Summary: "systemd user unit x410.service", Notes: []string{noSession}},
			pending: true,
			later:   "systemd user unit x410.service (enable and start)",
			cmds:    userReload,
		},
		{
			name:    "a user unit while your user manager isn't running",
			user:    true,
			stop:    (*env).stopUserManager,
			change:  resource.Change{Action: resource.Create, Summary: "systemd user unit x410.service", Notes: []string{noSession}},
			pending: true,
			later:   "systemd user unit x410.service (enable and start)",
			cmds:    userReload,
		},
		{
			name:    "a new unit file",
			before:  "[Unit]\n",
			stop:    (*env).stopSystemd,
			change:  resource.Change{Action: resource.Update, Summary: "systemd unit x410.service (update the unit file)", Notes: []string{notRunning}},
			pending: true,
			later:   "systemd unit x410.service (restart with its new unit file)",
			cmds:    reload,
		},
		{
			name:    "a new user unit file without a login session",
			user:    true,
			before:  "[Unit]\n",
			stop:    (*env).leaveSession,
			change:  resource.Change{Action: resource.Update, Summary: "systemd user unit x410.service (update the unit file)", Notes: []string{noSession}},
			pending: true,
			later:   "systemd user unit x410.service (restart with its new unit file)",
			cmds:    userReload,
		},
		{
			name:   "applied before",
			before: service,
			stop:   (*env).stopSystemd,
			change: resource.Change{Notes: []string{notRunning}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := func(content string) *unit.Unit {
				if tt.user {
					return &unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(content))}
				}
				return e.unit(content)
			}
			if tt.before != "" {
				e.sync(u(tt.before))
			}
			tt.stop(e)
			before := len(e.runner.Commands())
			if c, _ := e.sync(u(service)); !reflect.DeepEqual(c, tt.change) {
				t.Errorf("Check() = %+v, want %+v", c, tt.change)
			}
			if got := systemctl(e.runner.Commands()[before:]); len(got) > 0 {
				t.Errorf("ran %q while maisys couldn't reach systemd", got)
			}
			p := filepath.Join(e.dir, "x410.service")
			if tt.user {
				p = filepath.Join(e.home, ".config", "systemd", "user", "x410.service")
			}
			if data, err := os.ReadFile(p); err != nil || string(data) != service {
				t.Errorf("the unit file holds %q, %v; want the unit", data, err)
			}
			if rec, _ := e.state.Get(u(service).ID()); (rec.Details[unit.DetailPending] == "true") != tt.pending {
				t.Errorf("record = %+v, want pending %t", rec, tt.pending)
			}
			if c := e.check(u(service)); c.Action != resource.NoChange || !slices.Equal(c.Notes, tt.change.Notes) || c.Record != nil {
				t.Errorf("Check() after applying = %+v, want no change and the note again", c)
			}

			e.startSystemd()
			c, cmds := e.sync(u(service))
			if c.Summary != tt.later || (c.Action == resource.NoChange) != (tt.later == "") || len(c.Notes) > 0 {
				t.Errorf("Check() once maisys can reach systemd = %+v, want %q", c, tt.later)
			}
			if !slices.Equal(cmds, tt.cmds) {
				t.Errorf("Apply() ran %q, want %q", cmds, tt.cmds)
			}
			if rec, _ := e.state.Get(u(service).ID()); rec.Details[unit.DetailPending] != "" {
				t.Errorf("record = %+v, want systemd told about the file", rec)
			}
			if c := e.check(u(service)); c.Action != resource.NoChange || len(c.Notes) > 0 || c.Record != nil {
				t.Errorf("Check() after that = %+v, want no change", c)
			}
		})
	}
}

// TestUnitOneshot checks a oneshot service without RemainAfterExit, which
// is inactive again once it's run: it's done once it's enabled, so a dry
// run after a sync has nothing to change, unless it failed. Its unit file
// is read as systemd reads it.
func TestUnitOneshot(t *testing.T) {
	svc := func(settings string) string {
		return "[Unit]\nDescription=Set up X410\n\n[Service]\n" + settings + "\n[Install]\nWantedBy=multi-user.target\n"
	}
	tests := []struct {
		name    string
		unit    string
		enabled string
		active  string
		summary string // what the check says, if anything
	}{
		{
			name:    "run",
			unit:    svc("Type=oneshot\nExecStart=/usr/bin/true\n"),
			enabled: "enabled",
			active:  "inactive",
		},
		{
			name:    "failed",
			unit:    svc("Type=oneshot\nExecStart=/usr/bin/false\n"),
			enabled: "enabled",
			active:  "failed",
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "disabled",
			unit:    svc("Type=oneshot\nExecStart=/usr/bin/true\n"),
			enabled: "disabled",
			active:  "inactive",
			summary: "systemd unit x410.service (enable and start)",
		},
		{
			name:    "remains after it's run",
			unit:    svc("Type=oneshot\nRemainAfterExit=yes\nExecStart=/usr/bin/true\n"),
			enabled: "enabled",
			active:  "inactive",
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "remains after it's run, spelt differently",
			unit:    svc("Type = oneshot\nRemainAfterExit = On\nExecStart=/usr/bin/true\n"),
			enabled: "enabled",
			active:  "inactive",
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "a oneshot without a type",
			unit:    svc("ExecStop=/usr/bin/true\n"),
			enabled: "enabled",
			active:  "inactive",
		},
		{
			name:    "type set again",
			unit:    svc("Type=oneshot\nType=simple\nExecStart=/usr/bin/sleep infinity\n"),
			enabled: "enabled",
			active:  "inactive",
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "type reset",
			unit:    svc("Type=oneshot\nType=\nExecStart=/usr/bin/sleep infinity\n"),
			enabled: "enabled",
			active:  "inactive",
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "a type outside [Service]",
			unit:    "[Unit]\nType=oneshot\n" + svc("ExecStart=/usr/bin/sleep infinity\n"),
			enabled: "enabled",
			active:  "inactive",
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "comments and continued lines",
			unit:    svc("# Type=simple\nType=oneshot\n; RemainAfterExit=yes\nExecStart=/usr/bin/sh -c \\\n  'echo \\\n# a comment\nType=simple'\n"),
			enabled: "enabled",
			active:  "inactive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.apply(e.unit(tt.unit))
			e.systemd.enabled, e.systemd.active = tt.enabled, tt.active
			c := e.check(e.unit(tt.unit))
			if c.Summary != tt.summary || (c.Action == resource.NoChange) != (tt.summary == "") {
				t.Errorf("Check() = %s %q, want %q", c.Action, c.Summary, tt.summary)
			}
		})
	}

	t.Run("a dry run after a sync", func(t *testing.T) {
		e := newEnv(t)
		e.systemd.oneshot = true
		u := e.unit(svc("Type=oneshot\nExecStart=/usr/bin/true\n"))
		c, cmds := e.sync(u)
		if want := []string{"install", "systemctl daemon-reload", "systemctl enable x410.service", "systemctl restart x410.service"}; c.Action != resource.Create ||
			len(cmds) != 4 || !strings.HasPrefix(cmds[0], "install ") || !slices.Equal(cmds[1:], want[1:]) {
			t.Errorf("sync = %s %q, ran %q; want the unit created, and %q", c.Action, c.Summary, cmds, want)
		}
		if c := e.check(u); c.Action != resource.NoChange {
			t.Errorf("Check() after the sync = %s %q, want no change", c.Action, c.Summary)
		}
	})
}

// TestUnitConditions checks an enabled unit whose last start systemd
// skipped because a condition in its unit file didn't hold: it's left
// inactive, without a note, and the run's log says why, so a dry run after
// a sync has nothing to change. A unit whose conditions were never
// checked, or held when it last started, is started, and one whose file
// changed is restarted all the same.
func TestUnitConditions(t *testing.T) {
	noBus := system.Result{ExitCode: 1, Stderr: []byte("Failed to connect to bus: No medium found\n")}
	userUnit := func(content string) *unit.Unit {
		return &unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(content))}
	}

	t.Run("a dry run after a sync", func(t *testing.T) {
		for _, user := range []bool{false, true} {
			e := newEnv(t)
			var log strings.Builder
			e.rt.Log = &log
			e.systemd.unmet = true
			u, ran, want := e.unit(service), "systemctl", "systemd unit x410.service: systemd skipped its last start because a condition in its unit file didn't hold, so it's left inactive (systemctl status x410.service says which)\n"
			if user {
				u, ran, want = userUnit(service), "systemctl --user", "systemd user unit x410.service: systemd skipped its last start because a condition in its unit file didn't hold, so it's left inactive (systemctl --user status x410.service says which)\n"
			}
			reload := []string{ran + " daemon-reload", ran + " enable x410.service", ran + " restart x410.service"}
			if c, cmds := e.sync(u); c.Action != resource.Create || !slices.Equal(systemctl(cmds), reload) {
				t.Errorf("sync = %s %q, ran %q; want the unit created and started with %q", c.Action, c.Summary, cmds, reload)
			}
			if c := e.check(u); c.Action != resource.NoChange || len(c.Notes) > 0 || c.Record != nil {
				t.Errorf("Check() after the sync = %+v, want no change", c)
			}
			if log.String() != want {
				t.Errorf("the log has %q, want %q", log.String(), want)
			}
		}
	})

	tests := []struct {
		name    string
		unmet   bool         // whether a condition doesn't hold when the unit is synced
		after   func(e *env) // what happens after the sync, if anything
		summary string       // what the check says, if anything
		wantErr string
	}{
		{
			name:  "skipped",
			unmet: true,
		},
		{
			name: "never started",
			after: func(e *env) {
				e.systemd.active, e.systemd.conditionsMet, e.systemd.conditionsChecked = "inactive", false, 0
			},
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "stopped after it started",
			after:   func(e *env) { e.systemd.active = "inactive" },
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "skipped, then disabled",
			unmet:   true,
			after:   func(e *env) { e.systemd.enabled = "disabled" },
			summary: "systemd unit x410.service (enable and start)",
		},
		{
			name:  "systemctl show without the time",
			unmet: true,
			after: func(e *env) {
				e.systemd.answers = map[string]system.Result{"show": {Stdout: []byte("ConditionResult=no\n")}}
			},
			summary: "systemd unit x410.service (start)",
		},
		{
			name:    "systemctl show fails",
			unmet:   true,
			after:   func(e *env) { e.systemd.answers = map[string]system.Result{"show": noBus} },
			wantErr: "systemctl show failed with exit code 1: Failed to connect to bus: No medium found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			u := e.unit(service)
			e.systemd.unmet = tt.unmet
			e.sync(u)
			if tt.after != nil {
				tt.after(e)
			}
			c, err := u.Check(t.Context(), e.rt)
			switch {
			case tt.wantErr != "":
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Check() = %+v, %v; want the error %q", c, err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("Check() error = %v", err)
			case c.Summary != tt.summary || (c.Action == resource.NoChange) != (tt.summary == "") || len(c.Notes) > 0:
				t.Errorf("Check() = %+v, want %q", c, tt.summary)
			}
		})
	}

	t.Run("a new unit file", func(t *testing.T) {
		e := newEnv(t)
		e.systemd.unmet = true
		e.sync(e.unit("[Unit]\n"))
		c, cmds := e.sync(e.unit(service))
		want := []string{"systemctl daemon-reload", "systemctl enable x410.service", "systemctl restart x410.service"}
		if c.Action != resource.Update || c.Summary != "systemd unit x410.service (update the unit file)" || !slices.Equal(systemctl(cmds), want) {
			t.Errorf("sync = %s %q, ran %q; want the unit file updated, and %q", c.Action, c.Summary, cmds, want)
		}
		if c := e.check(e.unit(service)); c.Action != resource.NoChange {
			t.Errorf("Check() after the sync = %s %q, want no change", c.Action, c.Summary)
		}
	})

	t.Run("a new unit file written without a login session", func(t *testing.T) {
		e := newEnv(t)
		e.systemd.unmet = true
		e.sync(userUnit("[Unit]\n"))
		e.leaveSession()
		e.sync(userUnit(service))
		e.startSystemd()
		c, cmds := e.sync(userUnit(service))
		want := []string{"systemctl --user daemon-reload", "systemctl --user enable x410.service", "systemctl --user restart x410.service"}
		if c.Action != resource.Update || c.Summary != "systemd user unit x410.service (restart with its new unit file)" || !slices.Equal(cmds, want) {
			t.Errorf("sync = %s %q, ran %q; want the unit restarted, with %q", c.Action, c.Summary, cmds, want)
		}
		if c := e.check(userUnit(service)); c.Action != resource.NoChange {
			t.Errorf("Check() after the sync = %s %q, want no change", c.Action, c.Summary)
		}
	})
}

// TestUnitRecord checks the record a check says a running unit should have
// when its file already holds what it should: none while the state file's
// record is up to date, and otherwise the one applying would leave, which
// still says systemd is to be told about the file maisys wrote while it
// couldn't reach systemd.
func TestUnitRecord(t *testing.T) {
	path := func(e *env) string { return filepath.Join(e.dir, "x410.service") }
	tests := []struct {
		name     string
		setup    func(e *env)
		want     bool // whether the check has a record
		tookOver bool
		pending  bool
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
		{
			name: "there before maisys, while systemd isn't running",
			setup: func(e *env) {
				if err := os.WriteFile(path(e), []byte(service), 0o644); err != nil {
					e.t.Fatal(err)
				}
				e.stopSystemd()
			},
			want:     true,
			tookOver: true,
		},
		{
			name: "edited to what the config now says while systemd isn't running",
			setup: func(e *env) {
				e.apply(e.unit("[Unit]\n"))
				e.stopSystemd()
				e.apply(e.unit("[Unit]\n# 2\n"))
				if err := os.WriteFile(path(e), []byte(service), 0o644); err != nil {
					e.t.Fatal(err)
				}
			},
			want:    true,
			pending: true,
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
			if tt.pending {
				want.Details[unit.DetailPending] = "true"
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

	// While maisys can't reach systemd, the unit and its record are left
	// for the next sync that can, and a dry run has nothing to change.
	t.Run("without systemd", func(t *testing.T) {
		tests := []struct {
			name string
			user bool
			stop func(e *env)
			want resource.Change
		}{
			{
				name: "systemd isn't running",
				stop: (*env).stopSystemd,
				want: resource.Change{
					Action:  resource.Orphan,
					Summary: "systemd unit x410.service",
					Notes:   []string{"systemd isn't running: once it is, the next maisys sync stops, disables and deletes x410.service"},
				},
			},
			{
				name: "a user unit without a login session",
				user: true,
				stop: (*env).leaveSession,
				want: resource.Change{
					Action:  resource.Orphan,
					Summary: "systemd user unit x410.service",
					Notes:   []string{"this session can't reach your systemd user manager: the next maisys sync run in a login session stops, disables and deletes x410.service"},
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				e := newEnv(t)
				u := e.unit(service)
				if tt.user {
					u = &unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(service))}
				}
				_, rec, _ := e.apply(u)
				tt.stop(e)
				before := len(e.runner.Commands())

				var r unit.Remover
				if c, err := r.CheckRemove(t.Context(), e.rt, rec); err != nil || !reflect.DeepEqual(c, tt.want) {
					t.Errorf("CheckRemove() = %+v, %v; want %+v", c, err, tt.want)
				}
				if got := e.runner.Commands()[before:]; len(got) > 0 {
					t.Errorf("CheckRemove() ran %q", got)
				}
				e.startSystemd()
				if c, err := r.CheckRemove(t.Context(), e.rt, rec); err != nil || c.Action != resource.Remove {
					t.Errorf("CheckRemove() once maisys can reach systemd = %+v, %v; want removing the unit", c, err)
				}
			})
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
