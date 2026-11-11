package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// fakeModule is a module whose resources a test scripts.
type fakeModule struct {
	name      string
	deps      []string
	resources []*fakeResource
	// newConfig, if set, returns the module's config struct instead of a
	// plain Toggle.
	newConfig func() config.ModuleConfig
}

func (m *fakeModule) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: m.name, Summary: "A test module.", OS: config.AllOS()}
}

func (m *fakeModule) NewConfig() config.ModuleConfig {
	if m.newConfig != nil {
		return m.newConfig()
	}
	return &config.Toggle{}
}

func (m *fakeModule) DependsOn() []string { return m.deps }

func (m *fakeModule) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	rs := make([]resource.Resource, len(m.resources))
	for i, r := range m.resources {
		rs[i] = r
	}
	return rs, nil
}

// fakeResource is a resource that's missing until it's applied: until then
// its check finds something to create, and afterwards nothing to change.
type fakeResource struct {
	id         string
	privileged bool
	applyErr   error
	// onApply, if set, runs first when the resource is applied, with the
	// step's context. An error it returns makes the step fail.
	onApply func(ctx context.Context) error

	applied atomic.Bool
	checks  atomic.Int32
}

func (r *fakeResource) ID() string        { return r.id }
func (r *fakeResource) Kind() string      { return "fake" }
func (r *fakeResource) Targets() []string { return nil }
func (r *fakeResource) Privileged() bool  { return r.privileged }

func (r *fakeResource) Check(context.Context, *resource.Runtime) (resource.Change, error) {
	r.checks.Add(1)
	if r.applied.Load() {
		return resource.Change{}, nil
	}
	return resource.Change{Action: resource.Create, Summary: "create " + r.id}, nil
}

func (r *fakeResource) Apply(ctx context.Context, _ *resource.Runtime) (state.Record, error) {
	if r.onApply != nil {
		if err := r.onApply(ctx); err != nil {
			return state.Record{}, err
		}
	}
	if r.applyErr != nil {
		return state.Record{}, r.applyErr
	}
	r.applied.Store(true)
	return state.Record{}, nil
}

// goneRemover removes records of kind "fake" whose things are already
// gone, so removing them only forgets them.
type goneRemover struct{}

func (goneRemover) Kind() string { return "fake" }

func (goneRemover) CheckRemove(context.Context, *resource.Runtime, state.Record) (resource.Change, error) {
	return resource.Change{}, nil
}

func (goneRemover) Remove(context.Context, *resource.Runtime, state.Record) error {
	return errors.New("there's nothing to remove")
}

// fake returns a fake module called name, depending on deps, with a
// resource for each ID in ids.
func fake(name string, deps []string, ids ...string) *fakeModule {
	m := &fakeModule{name: name, deps: deps}
	for _, id := range ids {
		m.resources = append(m.resources, &fakeResource{id: id})
	}
	return m
}

// boxFiles is a config repo whose one machine, box, is for Fedora and
// turns on mods.
func boxFiles(mods ...*fakeModule) map[string]string {
	machine := "os = \"fedora\"\n"
	for _, m := range mods {
		machine += "\n[" + m.name + "]\nenabled = true\n"
	}
	return map[string]string{"maisys.toml": "config_version = 1\n", "machines/box.toml": machine}
}

// useFakes makes mods the only modules maisys has, and sets maisys up
// with a config repo for them, without git: so syncs need --local. It
// returns the config repo's folder.
func (e *testEnv) useFakes(mods ...*fakeModule) string {
	e.t.Helper()
	e.modules = nil
	for _, m := range mods {
		e.modules = append(e.modules, m)
	}
	dir := writeTemp(e.t, boxFiles(mods...))
	e.saveSettings(settings.Settings{RepoDir: dir, Machine: "box"})
	return dir
}

// paths returns where maisys keeps its files in this environment.
func (e *testEnv) paths() settings.Paths {
	e.t.Helper()
	paths, err := settings.DefaultPaths(runtime.GOOS, func(key string) string { return e.vars[key] })
	if err != nil {
		e.t.Fatal(err)
	}
	return paths
}

// state returns the state file's contents.
func (e *testEnv) state() *state.State {
	e.t.Helper()
	s, err := state.Load(e.paths().StateFile())
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// wantApplied checks which of the fake modules' resources were applied.
func wantApplied(t *testing.T, mods []*fakeModule, want ...string) {
	t.Helper()
	var got []string
	for _, m := range mods {
		for _, r := range m.resources {
			if r.applied.Load() {
				got = append(got, r.id)
			}
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("applied %q, want %q", got, want)
	}
}

// boxPlan is the start of the plan for box on the test system.
const boxPlan = "Plan for box (" + testSystem + ")\n"

func TestSyncRefusesRoot(t *testing.T) {
	e := newTestEnv(t)
	e.system.Root = true
	e.useFakes(fake("alpha", nil, "fake:a"))

	code, stdout, stderr := e.run("sync", "-v")
	if want := "maisys: " + facts.ErrRoot.Error() + "\n"; code != exitError || stdout != "" || stderr != want {
		t.Errorf("exit code = %d, stdout %q, stderr %q; want %d and only %q, before the self-update step", code, stdout, stderr, exitError, want)
	}
}

func TestSyncLocked(t *testing.T) {
	e := newTestEnv(t)
	mods := []*fakeModule{fake("alpha", nil, "fake:a")}
	e.useFakes(mods...)
	lock, err := state.Acquire(e.paths().LockFile())
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := e.run("sync", "--local", "--yes")
	want := fmt.Sprintf("maisys: another maisys sync is running (process %d); wait for it to finish, then try again\n", os.Getpid())
	if code != exitError || stdout != "" || stderr != want {
		t.Errorf("exit code = %d, stdout %q, stderr %q; want %d and only %q", code, stdout, stderr, exitError, want)
	}
	wantApplied(t, mods)

	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := e.run("sync", "--local", "--yes"); code != exitOK {
		t.Errorf("after the lock is released: exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	wantApplied(t, mods, "fake:a")
}

func TestSyncDryRun(t *testing.T) {
	tests := []struct {
		name    string
		applied bool
		code    int
		stdout  string
	}{
		{
			name:   "changes",
			code:   exitChanges,
			stdout: boxPlan + "\nalpha\n  + create fake:a\n\n1 to change.\n",
		},
		{
			name:    "nothing to change",
			applied: true,
			code:    exitOK,
			stdout:  boxPlan + "\nNothing to change.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			alpha := fake("alpha", nil, "fake:a")
			alpha.resources[0].applied.Store(tt.applied)
			e.useFakes(alpha)

			code, stdout, stderr := e.run("sync", "--dry-run", "--local")
			if code != tt.code || stdout != tt.stdout || stderr != "" {
				t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d and stdout:\n%s", code, stdout, stderr, tt.code, tt.stdout)
			}
			if s := e.state(); !s.LastSync.IsZero() || len(s.IDs()) != 0 {
				t.Errorf("state = %+v, records %q; want nothing recorded", s, s.IDs())
			}
		})
	}
}

func TestSyncNothingToChange(t *testing.T) {
	e := newTestEnv(t)
	alpha := fake("alpha", nil, "fake:a")
	alpha.resources[0].applied.Store(true)
	e.useFakes(alpha)
	// A record of something that's gone already is forgotten.
	e.removers = resource.NewRemovers(goneRemover{})
	st := &state.State{}
	st.Put(state.Record{ID: "fake:old", Kind: "fake", Module: "alpha"})
	if err := st.Save(e.paths().StateFile()); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := e.run("sync", "--local")
	if want := boxPlan + "\nNothing to change.\n"; code != exitOK || stdout != want || stderr != "" {
		t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d and stdout:\n%s", code, stdout, stderr, exitOK, want)
	}
	s := e.state()
	if !s.LastSync.Equal(testNow) || s.LastSyncResult != state.ResultOK || s.Machine != "box" {
		t.Errorf("last sync = %v, %q, machine %q; want %v, %q, box", s.LastSync, s.LastSyncResult, s.Machine, testNow, state.ResultOK)
	}
	if ids := s.IDs(); len(ids) != 0 {
		t.Errorf("records = %q, want the one of something gone forgotten", ids)
	}
}

func TestSyncConfirm(t *testing.T) {
	const plan = boxPlan + "\nalpha\n  + create fake:a\n\n1 to change.\n"
	const applied = "\nalpha: + create fake:a\n\nApplied 1 change.\n"
	tests := []struct {
		name     string
		args     []string
		terminal bool
		stdin    string
		code     int
		stdout   string
		stderr   string
		applied  bool
	}{
		{
			name:   "no terminal",
			code:   exitError,
			stdout: plan,
			stderr: "maisys: the plan has changes, and maisys can only ask whether to apply them in a terminal: " +
				"run maisys sync in a terminal, or with --yes to apply them without asking\n",
		},
		{
			name:    "no terminal, with --yes",
			args:    []string{"--yes"},
			stdout:  plan + applied,
			applied: true,
		},
		{
			name:     "yes",
			terminal: true,
			stdin:    "y\n",
			stdout:   plan + "Apply? [y/N] " + applied,
			applied:  true,
		},
		{
			name:     "YES",
			terminal: true,
			stdin:    " YES \n",
			stdout:   plan + "Apply? [y/N] " + applied,
			applied:  true,
		},
		{
			name:     "no",
			terminal: true,
			stdin:    "n\n",
			stdout:   plan + "Apply? [y/N] maisys: nothing was changed\n",
		},
		{
			name:     "nothing typed",
			terminal: true,
			stdin:    "\n",
			stdout:   plan + "Apply? [y/N] maisys: nothing was changed\n",
		},
		{
			name:     "stdin ends",
			terminal: true,
			stdout:   plan + "Apply? [y/N] \nmaisys: nothing was changed\n",
		},
		{
			name:     "a terminal, with -y",
			args:     []string{"-y"},
			terminal: true,
			stdout:   plan + applied,
			applied:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.terminal, e.stdin = tt.terminal, tt.stdin
			e.vars["NO_COLOR"] = "1"
			mods := []*fakeModule{fake("alpha", nil, "fake:a")}
			e.useFakes(mods...)

			code, stdout, stderr := e.run(append([]string{"sync", "--local"}, tt.args...)...)
			if code != tt.code || stdout != tt.stdout || stderr != tt.stderr {
				t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, stdout:\n%s\nstderr:\n%s", code, stdout, stderr, tt.code, tt.stdout, tt.stderr)
			}
			s := e.state()
			if tt.applied {
				wantApplied(t, mods, "fake:a")
				if ids := s.IDs(); !slices.Equal(ids, []string{"fake:a"}) || s.LastSyncResult != state.ResultOK {
					t.Errorf("records = %q, last sync %q; want fake:a and %q", ids, s.LastSyncResult, state.ResultOK)
				}
			} else {
				wantApplied(t, mods)
				if ids := s.IDs(); len(ids) != 0 || !s.LastSync.IsZero() {
					t.Errorf("records = %q, last sync %v; want nothing recorded", ids, s.LastSync)
				}
			}
		})
	}
}

func TestSyncPlanColor(t *testing.T) {
	tests := []struct {
		name     string
		terminal bool
		noColor  string
		color    bool
	}{
		{name: "a terminal", terminal: true, color: true},
		{name: "a terminal, with NO_COLOR", terminal: true, noColor: "1"},
		{name: "no terminal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.terminal = tt.terminal
			e.vars["NO_COLOR"] = tt.noColor
			e.useFakes(fake("alpha", nil, "fake:a"))

			code, stdout, _ := e.run("sync", "--local", "-n")
			if color := strings.Contains(stdout, "\x1b["); code != exitChanges || color != tt.color {
				t.Errorf("exit code = %d, stdout %q; want %d, and colour %v", code, stdout, exitChanges, tt.color)
			}
		})
	}
}

func TestSyncOnly(t *testing.T) {
	tests := []struct {
		name    string
		only    string
		code    int
		applied []string
		stderr  string
	}{
		{name: "one module", only: "beta", applied: []string{"fake:b"}},
		{name: "both modules", only: "alpha,beta", applied: []string{"fake:a", "fake:b"}},
		{
			name:   "a module that doesn't exist",
			only:   "beta,gamma",
			code:   exitError,
			stderr: "maisys: can't limit the sync to module \"gamma\": there's no module by that name\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			mods := []*fakeModule{fake("alpha", nil, "fake:a"), fake("beta", nil, "fake:b")}
			e.useFakes(mods...)

			code, stdout, stderr := e.run("sync", "--local", "--yes", "--only", tt.only)
			if code != tt.code || stderr != tt.stderr {
				t.Errorf("exit code = %d, stderr:\n%s\nwant %d, stderr:\n%s", code, stderr, tt.code, tt.stderr)
			}
			wantApplied(t, mods, tt.applied...)
			if ids := e.state().IDs(); !slices.Equal(ids, tt.applied) {
				t.Errorf("records = %q, want %q", ids, tt.applied)
			}
			if tt.code == exitOK && strings.Contains(stdout, "alpha") != slices.Contains(tt.applied, "fake:a") {
				t.Errorf("stdout:\n%s\nwant alpha in the plan only if it's synced", stdout)
			}
		})
	}
}

func TestSyncWrongSystem(t *testing.T) {
	e := newTestEnv(t)
	alpha := fake("alpha", nil, "fake:a")
	dir := e.useFakes(alpha)
	writeFiles(t, dir, map[string]string{"machines/box.toml": "os = \"fedora\"\nos_version = \"43\"\n\n[alpha]\nenabled = true\n"})

	code, stdout, stderr := e.run("sync", "--local", "--yes")
	want := "machines/box.toml:2:14: machine \"box\" is for Fedora 43, but it's running on Fedora 44\nmaisys: 1 problem found\n"
	if code != exitError || stdout != "" || stderr != want {
		t.Errorf("exit code = %d, stdout %q, stderr:\n%s\nwant %d, and stderr:\n%s", code, stdout, stderr, exitError, want)
	}
	if n := alpha.resources[0].checks.Load(); n != 0 {
		t.Errorf("checked fake:a %d times, want the sync to stop before planning", n)
	}
	if exists(e.paths().LogsDir()) {
		t.Errorf("%s exists, want the sync to stop before planning", e.paths().LogsDir())
	}
}

// deprecatingConfig is a config with a deprecated key.
type deprecatingConfig struct {
	config.Toggle
	Old string `toml:"old" deprecated:"use new instead."`
}

func TestSyncPlanWarnings(t *testing.T) {
	e := newTestEnv(t)
	e.system.OSVersion, e.system.PrettyName = "41", "Fedora Linux 41 (Workstation Edition)"
	alpha := fake("alpha", nil, "fake:a")
	alpha.newConfig = func() config.ModuleConfig { return &deprecatingConfig{} }
	dir := e.useFakes(alpha)
	writeFiles(t, dir, map[string]string{"machines/box.toml": "os = \"fedora\"\n\n[alpha]\nenabled = true\nold = \"x\"\n"})

	code, stdout, stderr := e.run("sync", "--local", "-n")
	want := "Plan for box (fedora 41, amd64)\n\n" +
		"warning: untested: Fedora 41 isn't one of the releases maisys tests on (43, 44 and 45); maisys may still work\n" +
		"warning: machines/box.toml:5:7: alpha.old is deprecated and will be removed in a later config_version: use new instead.\n" +
		"\nalpha\n  + create fake:a\n\n1 to change.\n"
	if code != exitChanges || stdout != want || stderr != "" {
		t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, and stdout:\n%s", code, stdout, stderr, exitChanges, want)
	}
}

func TestSyncNeedsSudo(t *testing.T) {
	e := newTestEnv(t)
	alpha := fake("alpha", nil, "fake:a")
	alpha.resources[0].privileged = true
	e.useFakes(alpha)
	e.runner.SudoStatus, e.runner.OS, e.runner.User = system.SudoMissing, config.Fedora, "phil"

	code, stdout, stderr := e.run("sync", "--local", "--yes")
	want := "maisys: sudo isn't installed, and some steps need to run as root.\n" +
		"To fix this, run these commands as root, then log out and back in and run maisys sync again:\n" +
		"  dnf install -y sudo\n  usermod -aG wheel phil\n"
	if code != exitError || stdout != "" || !strings.HasPrefix(stderr, want) {
		t.Errorf("exit code = %d, stdout %q, stderr:\n%s\nwant %d, and stderr to start with:\n%s", code, stdout, stderr, exitError, want)
	}
	if e.runner.SudoStarts() != 0 {
		t.Errorf("started sudo %d times, want never", e.runner.SudoStarts())
	}
	wantApplied(t, []*fakeModule{alpha})
}

func TestSyncFailure(t *testing.T) {
	e := newTestEnv(t)
	alpha := fake("alpha", nil, "fake:a")
	alpha.resources[0].applyErr = errors.New("boom")
	mods := []*fakeModule{alpha, fake("beta", []string{"alpha"}, "fake:b"), fake("gamma", nil, "fake:c")}
	e.useFakes(mods...)

	code, stdout, stderr := e.run("sync", "--local", "--yes")
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	wantApplied(t, mods, "fake:c")
	wantOut := "\nalpha: + create fake:a\ngamma: + create fake:c\n\n" +
		"Applied 1 change.\nalpha failed: create fake:a: boom\nbeta skipped because alpha failed.\n"
	if !strings.HasSuffix(stdout, wantOut) {
		t.Errorf("stdout:\n%s\nwant it to end with:\n%s", stdout, wantOut)
	}

	logs, err := os.ReadDir(e.paths().LogsDir())
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs = %v, %v; want one", logs, err)
	}
	log := filepath.Join(e.paths().LogsDir(), logs[0].Name())
	if want := "maisys: the full output is in " + log + "\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"maisys sync\nversion: ", "\nmachine: box\n", "\nstarted: 2026-11-06T09:30:00Z\n", boxPlan, "beta skipped because alpha failed.\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the log doesn't contain %q:\n%s", want, data)
		}
	}

	s := e.state()
	if ids := s.IDs(); !slices.Equal(ids, []string{"fake:c"}) || s.LastSyncResult != state.ResultFailed {
		t.Errorf("records = %q, last sync %q; want fake:c and %q", ids, s.LastSyncResult, state.ResultFailed)
	}
}

func TestSyncInterrupted(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	alpha := fake("alpha", nil, "fake:a1", "fake:a2")
	// Ctrl-C while the first step runs: it still finishes.
	alpha.resources[0].onApply = func(step context.Context) error {
		cancel()
		return step.Err()
	}
	mods := []*fakeModule{alpha}
	e.useFakes(mods...)

	code, stdout, stderr := e.runContext(ctx, "sync", "--local", "--yes")
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if want := "maisys: stopping after the current step… (press Ctrl-C again to stop it now)\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if want := "\nalpha: + create fake:a1\n\nApplied 1 change.\nInterrupted: run maisys sync again to finish.\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout:\n%s\nwant it to end with:\n%s", stdout, want)
	}
	wantApplied(t, mods, "fake:a1")
	s := e.state()
	if ids := s.IDs(); !slices.Equal(ids, []string{"fake:a1"}) || s.LastSyncResult != state.ResultInterrupted {
		t.Errorf("records = %q, last sync %q; want fake:a1 and %q", ids, s.LastSyncResult, state.ResultInterrupted)
	}

	// The next sync carries on.
	alpha.resources[0].onApply = nil
	code, stdout, stderr = e.run("sync", "--local", "--yes")
	if code != exitOK || !strings.Contains(stdout, "\n  + create fake:a2\n\n1 to change.\n") {
		t.Errorf("next sync: exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d and only fake:a2 to create", code, stdout, stderr, exitOK)
	}
	if ids := e.state().IDs(); !slices.Equal(ids, []string{"fake:a1", "fake:a2"}) {
		t.Errorf("records = %q, want both", ids)
	}
}

func TestSyncAborted(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	e.abort = make(chan struct{})
	alpha := fake("alpha", nil, "fake:a1", "fake:a2")
	// Ctrl-C twice while the first step runs: it stops too.
	alpha.resources[0].onApply = func(step context.Context) error {
		cancel()
		close(e.abort)
		<-step.Done()
		return step.Err()
	}
	mods := []*fakeModule{alpha}
	e.useFakes(mods...)

	code, stdout, stderr := e.runContext(ctx, "sync", "--local", "--yes")
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	if want := "maisys: stopping after the current step… (press Ctrl-C again to stop it now)\nmaisys: stopping the current step now\n"; !strings.HasPrefix(stderr, want) {
		t.Errorf("stderr = %q, want it to start with %q", stderr, want)
	}
	if want := "alpha failed: create fake:a1: context canceled\nInterrupted: run maisys sync again to finish.\n"; !strings.HasSuffix(stdout, want) {
		t.Errorf("stdout:\n%s\nwant it to end with:\n%s", stdout, want)
	}
	wantApplied(t, mods)
	if s := e.state(); len(s.IDs()) != 0 || s.LastSyncResult != state.ResultInterrupted {
		t.Errorf("records = %q, last sync %q; want none and %q", s.IDs(), s.LastSyncResult, state.ResultInterrupted)
	}
}

func TestSyncInterruptedWhilePlanning(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mods := []*fakeModule{fake("alpha", nil, "fake:a")}
	e.useFakes(mods...)

	code, stdout, stderr := e.runContext(ctx, "sync", "--local", "--yes")
	if want := "maisys: " + errInterrupted.Error() + "\n"; code != exitError || stdout != "" || stderr != want {
		t.Errorf("exit code = %d, stdout %q, stderr %q; want %d and only %q", code, stdout, stderr, exitError, want)
	}
	wantApplied(t, mods)
}

// TestSyncDryRunAfterSync is the check CI makes on every module: right
// after a sync, a dry run finds nothing to change.
func TestSyncDryRunAfterSync(t *testing.T) {
	e := newTestEnv(t)
	mods := []*fakeModule{fake("alpha", nil, "fake:a1", "fake:a2"), fake("beta", []string{"alpha"}, "fake:b")}
	e.useFakes(mods...)

	if code, stdout, stderr := e.run("sync", "--local", "--yes"); code != exitOK {
		t.Fatalf("sync: exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d", code, stdout, stderr, exitOK)
	}
	code, stdout, stderr := e.run("sync", "--dry-run", "--local")
	if want := boxPlan + "\nNothing to change.\n"; code != exitOK || stdout != want || stderr != "" {
		t.Errorf("sync --dry-run --local: exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d and stdout:\n%s", code, stdout, stderr, exitOK, want)
	}
}

func TestSyncRunner(t *testing.T) {
	e := newTestEnv(t)
	e.useFakes(fake("alpha", nil, "fake:a"))

	if code, _, stderr := e.run("sync", "--local", "--yes", "-v"); code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	opts := e.runnerOpts
	if opts.Log == nil || opts.Verbose == nil || opts.Sudo.OS != config.Fedora || opts.Sudo.Stdin == nil || opts.Sudo.Stdout == nil || opts.Sudo.Stderr == nil {
		t.Errorf("runner options = %+v, want the log, verbose output, and sudo set up for Fedora in the terminal", opts)
	}

	if code, _, stderr := e.run("sync", "--local", "--yes"); code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	if e.runnerOpts.Verbose != nil {
		t.Errorf("runner options = %+v, want no verbose output without -v", e.runnerOpts)
	}
}

// gitOutput runs git in dir the way maisys runs it in e, and returns its
// output.
func (e *testEnv) gitOutput(dir string, args ...string) string {
	e.t.Helper()
	cmd := exec.CommandContext(e.t.Context(), e.gitBin, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, e.gitEnv...)
	out, err := cmd.Output()
	if err != nil {
		e.t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

func TestStatusNotSetUp(t *testing.T) {
	tests := []struct {
		name    string
		version string
		support string
	}{
		{name: "a supported system", version: "44", support: "supported"},
		{name: "an untested system", version: "41", support: "untested: Fedora 41 isn't one of the releases maisys tests on (43, 44 and 45); maisys may still work"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.system.OSVersion = tt.version
			e.system.PrettyName = "Fedora Linux " + tt.version

			code, stdout, stderr := e.run("status")
			want := "machine:   not set up yet: run `maisys sync CONFIG-URL` to set up this machine, or `maisys config init` to start a new config repo\n" +
				"system:    Fedora Linux " + tt.version + ", amd64\n" +
				"support:   " + tt.support + "\n" +
				"last sync: never\n" +
				"managed:   0 items\n"
			if code != exitOK || stdout != want || stderr != "" {
				t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, and stdout:\n%s", code, stdout, stderr, exitOK, want)
			}
		})
	}
}

func TestStatusAfterSync(t *testing.T) {
	e := newTestEnv(t)
	e.useGit()
	e.system.Hostname = "box"
	mods := []*fakeModule{fake("alpha", nil, "fake:a1", "fake:a2")}
	for _, m := range mods {
		e.modules = append(e.modules, m)
	}
	clone := e.newClone(writeTemp(t, boxFiles(mods...)))
	remote := e.remoteOf(clone)
	if code, stdout, stderr := e.run("sync", remote, "--yes"); code != exitOK {
		t.Fatalf("sync: exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d", code, stdout, stderr, exitOK)
	}
	dir := e.defaultRepoDir()
	commit := e.gitOutput(dir, "rev-parse", "--short", "HEAD")

	// status doesn't need the lock.
	lock, err := state.Acquire(e.paths().LockFile())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	want := func(changes string) string {
		return "machine:     box\n" +
			"config repo: " + dir + "\n" +
			"url:         " + remote + "\n" +
			"commit:      " + commit + ", " + changes + "\n" +
			"system:      Fedora Linux 44 (Workstation Edition), amd64\n" +
			"support:     supported\n" +
			"last sync:   " + testNow.Local().Format(lastSyncFormat) + " (ok)\n" +
			"managed:     2 items\n"
	}
	code, stdout, stderr := e.run("status")
	if w := want("no uncommitted changes"); code != exitOK || stdout != w || stderr != "" {
		t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, and stdout:\n%s", code, stdout, stderr, exitOK, w)
	}

	writeFiles(t, dir, map[string]string{"profiles/new.toml": ""})
	code, stdout, _ = e.run("status")
	if w := want("with uncommitted changes"); code != exitOK || stdout != w {
		t.Errorf("with a change: exit code = %d, stdout:\n%s\nwant %d, and stdout:\n%s", code, stdout, exitOK, w)
	}
}

func TestStatusRepoProblems(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *testEnv) settings.Settings
		want  string
	}{
		{
			name: "no machine",
			setup: func(e *testEnv) settings.Settings {
				return settings.Settings{RepoDir: filepath.Join(e.t.TempDir(), "gone")}
			},
			want: "machine:     not chosen yet: run `maisys sync` to choose one\n",
		},
		{
			name: "the clone is gone",
			setup: func(e *testEnv) settings.Settings {
				return settings.Settings{RepoDir: filepath.Join(e.t.TempDir(), "gone"), Machine: "box"}
			},
			want: "config repo: %s (it doesn't exist)\nsystem: ",
		},
		{
			name: "no repo_dir",
			setup: func(*testEnv) settings.Settings {
				return settings.Settings{Machine: "box"}
			},
			want: "config repo: not set: run `maisys sync --dir PATH` to use a clone, or `maisys sync CONFIG-URL` to clone one\n",
		},
		{
			name: "git isn't installed",
			setup: func(e *testEnv) settings.Settings {
				e.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
				return settings.Settings{RepoDir: e.t.TempDir(), Machine: "box"}
			},
			want: "url:         none (the clone has no remote)\ncommit:      unknown: git isn't installed\n             Install it with: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			s := tt.setup(e)
			e.saveSettings(s)

			code, stdout, stderr := e.run("status")
			if want := strings.ReplaceAll(tt.want, "%s", s.RepoDir); code != exitOK || !strings.Contains(stdout, want) || stderr != "" {
				t.Errorf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, and stdout to contain:\n%s", code, stdout, stderr, exitOK, want)
			}
		})
	}
}

func TestDescribeLiveSystem(t *testing.T) {
	tests := []struct {
		system facts.System
		want   string
	}{
		{facts.System{PrettyName: "Fedora Linux 44", Arch: config.ARM64, RawArch: "arm64"}, "Fedora Linux 44, arm64"},
		{facts.System{OSID: "debian", OSVersion: "13", RawArch: "riscv64"}, "debian 13, riscv64"},
		{facts.System{PrettyName: "Ubuntu 26.04 LTS", Arch: config.AMD64, WSL: true, WSLDistro: "dev"}, "Ubuntu 26.04 LTS, amd64, WSL distro dev"},
		{facts.System{PrettyName: "Ubuntu 26.04 LTS", Arch: config.AMD64, WSL: true}, "Ubuntu 26.04 LTS, amd64, in WSL"},
	}
	for _, tt := range tests {
		if got := describeLiveSystem(&tt.system); got != tt.want {
			t.Errorf("describeLiveSystem(%+v) = %q, want %q", tt.system, got, tt.want)
		}
	}
}

// TestSyncSwitchSavedWhenApplied checks that a switch of machine is part of
// the plan: it's only saved once the plan is applied.
func TestSyncSwitchSavedWhenApplied(t *testing.T) {
	e := newTestEnv(t)
	alpha := fake("alpha", nil, "fake:a")
	dir := e.useFakes(alpha)
	other := boxFiles(alpha)["machines/box.toml"]
	if err := os.WriteFile(filepath.Join(dir, "machines", "box2.toml"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	wantMachine := func(step, want string) {
		t.Helper()
		if s := e.loadSettings(); s.Machine != want {
			t.Errorf("%s: saved machine = %q, want %q", step, s.Machine, want)
		}
	}

	if code, _, stderr := e.run("sync", "--local", "--machine", "box2", "--dry-run"); code != exitChanges {
		t.Fatalf("dry run: exit code = %d, stderr:\n%s", code, stderr)
	}
	wantMachine("after a dry run", "box")

	e.terminal, e.stdin = true, "n\n"
	if code, _, stderr := e.run("sync", "--local", "--machine", "box2"); code != exitOK {
		t.Fatalf("answering no: exit code = %d, stderr:\n%s", code, stderr)
	}
	wantMachine("after answering no", "box")

	e.terminal, e.stdin = false, ""
	if code, _, stderr := e.run("sync", "--local", "--machine", "box2", "--yes"); code != exitOK {
		t.Fatalf("applying: exit code = %d, stderr:\n%s", code, stderr)
	}
	wantMachine("after applying", "box2")
}
