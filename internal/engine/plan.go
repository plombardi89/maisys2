// Package engine plans and applies a sync. A plan checks every resource of
// the enabled modules against the live system, in parallel and without
// ever asking for a password, and finds the records in the state file that
// are no longer wanted. Applying it makes the changes module by module, in
// dependency order, saving the state file after every step: a module that
// fails only stops the modules that depend on it. Then it removes what was
// taken out of the config, following the removal rules.
package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// DefaultParallel is how many checks a plan runs at once, unless
// PlanInput.Parallel says otherwise.
const DefaultParallel = 8

// Module is a module's behaviour side: the resources it manages, and the
// modules it must be applied after. Modules that don't implement it have no
// resources.
type Module interface {
	config.Module
	// DependsOn names the modules this one is applied after, when they're
	// part of the same sync. If one of them fails, this one is skipped.
	DependsOn() []string
	// Resources returns the module's resources for its merged config cfg,
	// in the order they're applied. It only describes them: checking the
	// live system is up to each resource's Check.
	Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error)
}

// PlanInput is what NewPlan plans from.
type PlanInput struct {
	// Machine is the machine's merged, validated config.
	Machine *config.Machine
	// Facts is the live system.
	Facts *facts.System
	// Paths are where maisys keeps its files.
	Paths settings.Paths
	// Runner runs the checks' commands. It must not have a sudo session
	// started, so that no check can ask for a password.
	Runner system.Runner
	// State is what maisys manages, from the state file. Nil means
	// nothing.
	State *state.State
	// Modules are every module maisys has (modules.All()). They're applied
	// in this order, except that each comes after the modules it depends
	// on.
	Modules []config.Module
	// Removers remove what's taken out of the config, by kind.
	Removers resource.Removers
	// Only limits the sync to these modules, if it isn't empty. Records
	// of other modules are left alone.
	Only []string
	// Parallel is how many checks run at once. Zero means
	// DefaultParallel.
	Parallel int
	// Header holds extra lines for the top of the plan, such as
	// "config repo: OLD → NEW" or "machine: X (was Y)".
	Header []string
	// Warnings are shown before the changes, such as deprecated keys or an
	// untested OS version.
	Warnings []string
	// Log receives notes for the run's log. It may be nil.
	Log io.Writer
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Plan is what a sync would change, module by module, and what it would
// remove.
type Plan struct {
	// Machine is the machine's name.
	Machine string
	// OS, OSVersion and Arch describe the live system.
	OS        config.OS
	OSVersion string
	Arch      string
	// Header holds extra lines for the top of the plan.
	Header []string
	// Warnings are shown before the changes.
	Warnings []string
	// Modules are the enabled modules that have resources, in the order
	// they're applied.
	Modules []*ModulePlan
	// Removals are the records in the state file that are no longer
	// wanted, in the order they're removed.
	Removals []*Removal

	rt *resource.Runtime
}

// ModulePlan is the plan for one module.
type ModulePlan struct {
	// Name is the module's name.
	Name string
	// DependsOn names the modules in the plan this one depends on.
	DependsOn []string
	// Items are the module's resources and their changes, in the order
	// they're applied.
	Items []*Item
	// Err is why the module couldn't list its resources, if it couldn't.
	Err error
	// SkippedBecause names the failed module this one depends on, directly
	// or not, if there is one. A module that's skipped isn't applied.
	SkippedBecause string
}

// Item is a resource and the change its check found.
type Item struct {
	Resource resource.Resource
	Change   resource.Change
	// Err is why the check failed, if it did.
	Err error
}

// Removal is a record in the state file that's no longer wanted.
type Removal struct {
	// Record is the state file's record.
	Record state.Record
	// Remover removes things of the record's kind, or is nil if maisys
	// never removes them: then the plan says it's no longer used, and the
	// record stays.
	Remover resource.Remover
	// Change is what removing it would do.
	Change resource.Change
	// Err is why checking the removal failed, if it did.
	Err error
}

// NewPlan checks every resource of the enabled modules (only those in
// in.Only, if it's set) against the live system, and finds the records in
// the state file that are no longer wanted. Checks run in parallel and never
// ask for a password: a check that needs one is checked during apply. A
// check that fails makes its module fail, which the plan shows. NewPlan
// returns an error if the modules can't be ordered, if two resources have
// the same ID, if in.Only names a module that doesn't exist, or if a change
// needs sudo and sudo is missing or the user may not use it.
func NewPlan(ctx context.Context, in PlanInput) (*Plan, error) {
	if in.Machine == nil {
		return nil, errors.New("planning needs the machine's config")
	}
	st := in.State
	if st == nil {
		st = &state.State{}
	}
	now := in.Now
	if now == nil {
		now = time.Now
	}
	p := &Plan{
		Machine:  in.Machine.Name,
		OS:       in.Machine.OS,
		Arch:     string(in.Machine.Arch),
		Header:   slices.Clone(in.Header),
		Warnings: slices.Clone(in.Warnings),
		rt: &resource.Runtime{
			Runner:   in.Runner,
			Facts:    in.Facts,
			Paths:    in.Paths,
			Machine:  in.Machine,
			Previous: st.Get,
			Log:      in.Log,
			Now:      now,
		},
	}
	if f := in.Facts; f != nil {
		p.OS, p.OSVersion, p.Arch = cmp.Or(f.OS, config.OS(f.OSID)), f.OSVersion, cmp.Or(string(f.Arch), f.RawArch)
	}

	selected, err := selection(in.Modules, in.Only)
	if err != nil {
		return nil, err
	}
	if err := checkModules(in.Modules); err != nil {
		return nil, err
	}
	mods, err := order(enabled(in, selected))
	if err != nil {
		return nil, err
	}

	owner := map[string]string{}
	for _, mod := range mods {
		name := mod.Info().Name
		mp := &ModulePlan{Name: name}
		for _, dep := range mod.DependsOn() {
			if slices.ContainsFunc(mods, func(m Module) bool { return m.Info().Name == dep }) {
				mp.DependsOn = append(mp.DependsOn, dep)
			}
		}
		p.Modules = append(p.Modules, mp)
		resources, err := mod.Resources(in.Machine.Modules[name], p.rt)
		if err != nil {
			mp.Err = err
			continue
		}
		for _, r := range resources {
			id := r.ID()
			switch {
			case id == "":
				return nil, fmt.Errorf("module %s has a resource without an ID", name)
			case r.Kind() == "":
				return nil, fmt.Errorf("module %s's resource %s has no kind", name, id)
			case owner[id] == name:
				return nil, fmt.Errorf("module %s has two resources with the ID %s", name, id)
			case owner[id] != "":
				return nil, fmt.Errorf("modules %s and %s both have a resource with the ID %s", owner[id], name, id)
			}
			owner[id] = name
			mp.Items = append(mp.Items, &Item{Resource: r})
		}
	}

	p.Removals = removals(in, st, owner, selected)
	p.check(ctx, cmp.Or(in.Parallel, DefaultParallel))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.skip()

	if p.NeedsPrivileges() && in.Machine.OS != config.Windows {
		if sudo, ok := in.Runner.(system.SudoRunner); ok {
			if _, err := sudo.CheckSudo(ctx); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

// selection returns whether each module is part of the sync: every module,
// or only those in only if it isn't empty.
func selection(mods []config.Module, only []string) (func(name string) bool, error) {
	if len(only) == 0 {
		return func(string) bool { return true }, nil
	}
	for _, name := range only {
		if !slices.ContainsFunc(mods, func(m config.Module) bool { return m.Info().Name == name }) {
			return nil, fmt.Errorf("can't limit the sync to module %q: there's no module by that name", name)
		}
	}
	return func(name string) bool { return slices.Contains(only, name) }, nil
}

// checkModules checks that no two modules have the same name, and that
// every module depends only on modules maisys has.
func checkModules(mods []config.Module) error {
	seen := map[string]bool{}
	for _, mod := range mods {
		name := mod.Info().Name
		if seen[name] {
			return fmt.Errorf("there are two modules called %s", name)
		}
		seen[name] = true
	}
	for _, mod := range mods {
		m, ok := mod.(Module)
		if !ok {
			continue
		}
		for _, dep := range m.DependsOn() {
			if !seen[dep] {
				return fmt.Errorf("module %s depends on module %q, which maisys doesn't have", m.Info().Name, dep)
			}
		}
	}
	return nil
}

// enabled returns the modules that are turned on, part of the sync, and
// have resources, in the order in.Modules lists them.
func enabled(in PlanInput, selected func(string) bool) []Module {
	var mods []Module
	for _, mod := range in.Modules {
		name := mod.Info().Name
		cfg := in.Machine.Modules[name]
		m, ok := mod.(Module)
		if ok && cfg != nil && cfg.IsEnabled() && selected(name) {
			mods = append(mods, m)
		}
	}
	return mods
}

// order sorts mods so that every module comes after the ones it depends on,
// keeping their order otherwise. Dependencies that aren't in mods are
// ignored.
func order(mods []Module) ([]Module, error) {
	deps := make(map[string][]string, len(mods))
	for _, m := range mods {
		deps[m.Info().Name] = nil
	}
	for _, m := range mods {
		name := m.Info().Name
		for _, dep := range m.DependsOn() {
			if _, ok := deps[dep]; ok && !slices.Contains(deps[name], dep) {
				deps[name] = append(deps[name], dep)
			}
		}
	}

	done := make(map[string]bool, len(mods))
	sorted := make([]Module, 0, len(mods))
	for len(sorted) < len(mods) {
		i := slices.IndexFunc(mods, func(m Module) bool {
			name := m.Info().Name
			return !done[name] && !slices.ContainsFunc(deps[name], func(dep string) bool { return !done[dep] })
		})
		if i < 0 {
			return nil, cycle(mods, deps, done)
		}
		done[mods[i].Info().Name] = true
		sorted = append(sorted, mods[i])
	}
	return sorted, nil
}

// cycle describes a cycle of dependencies among the modules that aren't
// done, each of which depends on another that isn't.
func cycle(mods []Module, deps map[string][]string, done map[string]bool) error {
	i := slices.IndexFunc(mods, func(m Module) bool { return !done[m.Info().Name] })
	path := []string{mods[i].Info().Name}
	for {
		last := path[len(path)-1]
		j := slices.IndexFunc(deps[last], func(dep string) bool { return !done[dep] })
		next := deps[last][j]
		if k := slices.Index(path, next); k >= 0 {
			path = append(path[k:], next)
			return fmt.Errorf("modules depend on each other in a cycle: %s", strings.Join(path, " → "))
		}
		path = append(path, next)
	}
}

// removals returns the records in st that no resource in owner has, and
// that belong to the sync: those of the selected modules (all of them
// without Only). They're in the order they're removed: modules in the
// reverse of their order in in.Modules, after modules maisys doesn't have,
// and records by ID.
func removals(in PlanInput, st *state.State, owner map[string]string, selected func(string) bool) []*Removal {
	rank := map[string]int{}
	for i, mod := range in.Modules {
		rank[mod.Info().Name] = len(in.Modules) - i
	}
	var rs []*Removal
	for _, id := range st.IDs() {
		rec, ok := st.Get(id)
		if !ok || owner[id] != "" || !selected(rec.Module) {
			continue
		}
		rs = append(rs, &Removal{Record: rec, Remover: in.Removers[rec.Kind]})
	}
	slices.SortStableFunc(rs, func(a, b *Removal) int {
		return cmp.Or(cmp.Compare(rank[a.Record.Module], rank[b.Record.Module]), cmp.Compare(a.Record.ID, b.Record.ID))
	})
	return rs
}

// check runs every resource's check, and every removal's, at most limit at
// a time.
func (p *Plan) check(ctx context.Context, limit int) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, max(1, limit))
	run := func(check func()) {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			check()
		})
	}
	for _, m := range p.Modules {
		for _, item := range m.Items {
			run(func() {
				item.Change, item.Err = needsPassword(item.Resource.Check(ctx, p.rt))
			})
		}
	}
	for _, r := range p.Removals {
		if r.Remover == nil {
			r.Change = resource.Change{Action: resource.Orphan}
			continue
		}
		run(func() {
			r.Change, r.Err = needsPassword(r.Remover.CheckRemove(ctx, p.rt, r.Record))
		})
	}
	wg.Wait()
}

// needsPassword turns a check that failed because sudo needs a password
// into one that's checked during apply.
func needsPassword(c resource.Change, err error) (resource.Change, error) {
	if errors.Is(err, system.ErrNeedsPassword) {
		c.Action = resource.Unknown
		return c, nil
	}
	return c, err
}

// skip marks the modules that depend on a failed module, directly or not,
// and leaves out the removals of records of failed and skipped modules.
func (p *Plan) skip() {
	stopped := map[string]string{}
	for _, m := range p.Modules {
		if m.failed() {
			stopped[m.Name] = m.Name
			continue
		}
		for _, dep := range m.DependsOn {
			if cause := stopped[dep]; cause != "" {
				m.SkippedBecause = cause
				stopped[m.Name] = cause
				break
			}
		}
	}
	p.Removals = slices.DeleteFunc(p.Removals, func(r *Removal) bool { return stopped[r.Record.Module] != "" })
}

// failed reports whether the module couldn't list its resources, or one of
// their checks failed.
func (m *ModulePlan) failed() bool {
	return m.Err != nil || slices.ContainsFunc(m.Items, func(item *Item) bool { return item.Err != nil })
}

// failure returns why the module failed in the plan, or nil.
func (m *ModulePlan) failure() error {
	if m.Err != nil {
		return m.Err
	}
	var errs []error
	for _, item := range m.Items {
		if item.Err != nil {
			errs = append(errs, item.checkError())
		}
	}
	return errors.Join(errs...)
}

// applies reports whether the module will be applied.
func (m *ModulePlan) applies() bool {
	return !m.failed() && m.SkippedBecause == ""
}

// checkError describes why the item's check failed.
func (item *Item) checkError() error {
	return fmt.Errorf("checking %s: %w", resource.Describe(item.Resource.ID()), item.Err)
}

// summary returns how the plan describes the item's change.
func (item *Item) summary() string {
	return cmp.Or(item.Change.Summary, resource.Describe(item.Resource.ID()))
}

// privileged reports whether applying the item needs root or admin. A
// check that needed a password ran a command as root.
func (item *Item) privileged() bool {
	return item.Change.Privileged || item.Resource.Privileged() || item.Change.Action == resource.Unknown
}

// summary returns how the plan describes the removal.
func (r *Removal) summary() string {
	return cmp.Or(r.Change.Summary, resource.Describe(r.Record.ID))
}

// applies reports whether the removal is carried out: whether there's a
// remover, its check worked, and it found something to do.
func (r *Removal) applies() bool {
	return r.Remover != nil && r.Err == nil && r.Change.Action.Applies()
}

// privileged reports whether the removal needs root or admin.
func (r *Removal) privileged() bool {
	return r.Change.Privileged || r.Change.Action == resource.Unknown
}

// HasChanges reports whether applying the plan would change anything.
func (p *Plan) HasChanges() bool {
	for _, m := range p.Modules {
		if m.applies() && slices.ContainsFunc(m.Items, func(item *Item) bool { return item.Change.Action.Applies() }) {
			return true
		}
	}
	return slices.ContainsFunc(p.Removals, (*Removal).applies)
}

// NeedsPrivileges reports whether applying the plan needs root (sudo) on
// Linux, or admin on Windows.
func (p *Plan) NeedsPrivileges() bool {
	for _, m := range p.Modules {
		if m.applies() && slices.ContainsFunc(m.Items, func(item *Item) bool { return item.Change.Action.Applies() && item.privileged() }) {
			return true
		}
	}
	return slices.ContainsFunc(p.Removals, func(r *Removal) bool { return r.applies() && r.privileged() })
}

// Failed reports whether a module failed in the plan, or a removal
// couldn't be checked. Those aren't applied.
func (p *Plan) Failed() bool {
	return slices.ContainsFunc(p.Modules, (*ModulePlan).failed) ||
		slices.ContainsFunc(p.Removals, func(r *Removal) bool { return r.Err != nil })
}
