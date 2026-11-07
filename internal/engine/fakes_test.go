package engine

import (
	"cmp"
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// testNow is the time tests run at.
var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

// steps records what fake resources and removers did, in order.
type steps struct {
	mu   sync.Mutex
	list []string
}

func (s *steps) add(step string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, step)
}

func (s *steps) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.list)
}

// fakeModule is a module with resources.
type fakeModule struct {
	name      string
	deps      []string
	resources []resource.Resource
	err       error
}

func (m *fakeModule) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: m.name, Summary: "A test module.", OS: config.AllOS()}
}

func (m *fakeModule) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func (m *fakeModule) DependsOn() []string { return m.deps }

func (m *fakeModule) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	return m.resources, m.err
}

// configOnly is a module without resources, as every module is before M5.
type configOnly struct{ name string }

func (m configOnly) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: m.name, Summary: "A module without resources.", OS: config.AllOS()}
}

func (configOnly) NewConfig() config.ModuleConfig { return &config.Toggle{} }

// fakeResource is a resource whose check finds change, or checkErr.
type fakeResource struct {
	id, kind   string
	privileged bool
	targets    []string
	change     resource.Change
	checkErr   error
	applyErr   error
	// onCheck and onApply, if set, replace the check and apply.
	onCheck func(ctx context.Context, rt *resource.Runtime) (resource.Change, error)
	onApply func(ctx context.Context, rt *resource.Runtime) (state.Record, error)
	steps   *steps
}

// res returns a fake resource whose check finds a change with action and
// summary.
func res(id string, action resource.Action, summary string) *fakeResource {
	return &fakeResource{id: id, change: resource.Change{Action: action, Summary: summary}}
}

// sudo makes r need root.
func (r *fakeResource) sudo() *fakeResource {
	r.privileged = true
	return r
}

func (r *fakeResource) ID() string        { return r.id }
func (r *fakeResource) Kind() string      { return cmp.Or(r.kind, "fake") }
func (r *fakeResource) Targets() []string { return r.targets }
func (r *fakeResource) Privileged() bool  { return r.privileged }

func (r *fakeResource) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if r.onCheck != nil {
		return r.onCheck(ctx, rt)
	}
	return r.change, r.checkErr
}

func (r *fakeResource) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	if r.steps != nil {
		r.steps.add("apply " + r.id)
	}
	if r.onApply != nil {
		return r.onApply(ctx, rt)
	}
	if r.applyErr != nil {
		return state.Record{}, r.applyErr
	}
	return state.Record{Details: map[string]string{"applied": r.change.Summary}}, nil
}

// fakeRemover removes records of its kind. Its check finds change, or the
// change for a record's ID in changes.
type fakeRemover struct {
	kind      string
	change    resource.Change
	changes   map[string]resource.Change
	checkErr  map[string]error
	removeErr map[string]error
	onRemove  func(ctx context.Context, rec state.Record)
	steps     *steps
}

func (r *fakeRemover) Kind() string { return r.kind }

func (r *fakeRemover) CheckRemove(_ context.Context, _ *resource.Runtime, rec state.Record) (resource.Change, error) {
	if err := r.checkErr[rec.ID]; err != nil {
		return resource.Change{}, err
	}
	if c, ok := r.changes[rec.ID]; ok {
		return c, nil
	}
	return r.change, nil
}

func (r *fakeRemover) Remove(ctx context.Context, _ *resource.Runtime, rec state.Record) error {
	if r.steps != nil {
		r.steps.add("remove " + rec.ID)
	}
	if r.onRemove != nil {
		r.onRemove(ctx, rec)
	}
	return r.removeErr[rec.ID]
}

// testEnv is a machine to plan and apply for, with fake modules.
type testEnv struct {
	t        *testing.T
	runner   *systemtest.FakeRunner
	store    *state.Store
	machine  *config.Machine
	facts    *facts.System
	modules  []config.Module
	removers resource.Removers
	steps    *steps
	only     []string
	header   []string
	warnings []string
	parallel int
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	store, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{
		t:        t,
		runner:   systemtest.NewFakeRunner(t),
		store:    store,
		machine:  &config.Machine{Name: "phil-workp14s", OS: config.Ubuntu, Modules: map[string]config.ModuleConfig{}},
		facts:    &facts.System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", Arch: config.AMD64, RawArch: "amd64"},
		removers: resource.Removers{},
		steps:    &steps{},
	}
}

// windows makes the machine a Windows host.
func (e *testEnv) windows() {
	e.machine.OS = config.Windows
	e.facts = &facts.System{OS: config.Windows, OSID: "windows", OSVersion: "26100", Arch: config.AMD64, RawArch: "amd64"}
}

// add adds an enabled module with resources rs, which record their steps.
// If the module is there already, it adds rs to its resources.
func (e *testEnv) add(name string, deps []string, rs ...*fakeResource) *fakeModule {
	var m *fakeModule
	for _, mod := range e.modules {
		if fm, ok := mod.(*fakeModule); ok && fm.name == name {
			m = fm
		}
	}
	if m == nil {
		m = &fakeModule{name: name, deps: deps}
		e.modules = append(e.modules, m)
	}
	for _, r := range rs {
		r.steps = e.steps
		m.resources = append(m.resources, r)
	}
	e.machine.Modules[name] = &config.Toggle{Enabled: true}
	return m
}

// disable turns module name off.
func (e *testEnv) disable(name string) {
	e.machine.Modules[name] = &config.Toggle{}
}

// remover adds a remover, which records its steps.
func (e *testEnv) remover(r *fakeRemover) {
	r.steps = e.steps
	e.removers[r.kind] = r
}

// record puts a record into the state.
func (e *testEnv) record(id, kind, module string) {
	e.store.Put(state.Record{ID: id, Kind: kind, Module: module})
}

func (e *testEnv) input() PlanInput {
	return PlanInput{
		Machine:  e.machine,
		Facts:    e.facts,
		Runner:   e.runner,
		State:    e.store.State,
		Modules:  e.modules,
		Removers: e.removers,
		Only:     e.only,
		Parallel: e.parallel,
		Header:   e.header,
		Warnings: e.warnings,
		Now:      func() time.Time { return testNow },
	}
}

// plan plans, failing the test if that fails.
func (e *testEnv) plan() *Plan {
	e.t.Helper()
	p, err := NewPlan(e.t.Context(), e.input())
	if err != nil {
		e.t.Fatalf("NewPlan() error = %v", err)
	}
	return p
}

// apply plans and applies.
func (e *testEnv) apply(ctx context.Context) *Result {
	e.t.Helper()
	return Apply(ctx, e.plan(), ApplyInput{Store: e.store})
}

// ids returns the IDs of the records in the state file.
func (e *testEnv) ids() []string {
	e.t.Helper()
	return savedIDs(e.t, e.store.Path())
}

// savedIDs returns the IDs of the records in the state file at path.
func savedIDs(t *testing.T, path string) []string {
	t.Helper()
	s, err := state.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return s.IDs()
}
