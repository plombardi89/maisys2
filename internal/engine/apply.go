package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// ApplyInput is what Apply needs besides the plan.
type ApplyInput struct {
	// Store is the state file, saved after every step. Its State is the
	// one the plan was made from.
	Store *state.Store
	// Abort, if set, is done when the step in progress must stop too, such
	// as on a second Ctrl-C. Cancelling Apply's ctx only stops it from
	// starting another step.
	Abort context.Context
	// Progress, if set, receives a line for each step as it starts.
	Progress io.Writer
	// Out, if set, is the terminal, which steps show things on as they
	// run (see resource.Runtime.Out). It's never the log.
	Out io.Writer
}

// Result is what applying a plan did.
type Result struct {
	// Modules are the modules applied, failed or skipped, in the order
	// they were applied. After an interruption, the modules not reached
	// are left out.
	Modules []ModuleResult
	// Removals are the removals carried out, failed or skipped.
	Removals []RemovalResult
	// Interrupted reports whether Apply stopped early because its context
	// was cancelled, as on Ctrl-C.
	Interrupted bool
	// Err is why Apply stopped early otherwise: the state file couldn't be
	// saved.
	Err error
}

// ModuleResult is what applying one module did.
type ModuleResult struct {
	// Name is the module's name.
	Name string
	// Applied is how many of its changes were applied.
	Applied int
	// Err is why the module failed, in the plan or while it was applied.
	Err error
	// SkippedBecause names the failed module this one depends on, if it
	// was skipped because of it, or the module waiting for a reboot that
	// it depends on.
	SkippedBecause string
	// Reboot says why the module stopped for a reboot, such as "WSL was
	// installed", if a change it applied needs one (see
	// resource.RebootNeeder). Its later changes, and the modules that
	// depend on it, wait for the next sync.
	Reboot string
}

// RemovalResult is what removing one thing did.
type RemovalResult struct {
	// Summary describes what was removed.
	Summary string
	// Record is its record in the state file, which is deleted once it's
	// removed.
	Record state.Record
	// Removed reports whether it was removed.
	Removed bool
	// Err is why it couldn't be removed, if it couldn't.
	Err error
	// SkippedBecause names the failed module the record belongs to, or
	// that this module depends on, if the removal was skipped; or the
	// module waiting for a reboot.
	SkippedBecause string
}

// Apply applies the plan p: the modules' changes, one module at a time in
// dependency order and each module's in the order it lists them, then the
// removals. Changes with Action Warn and Unknown are applied like Create
// and Update. If any change needs root, the sudo session (the only thing
// that may ask for a password) is started before the first step and
// stopped at the end. On Windows, that's the elevated helper, which runs
// the steps that need admin rights, with one UAC prompt.
//
// The state file is saved after every step. A step that fails makes its
// module fail: the module's other changes and every module that depends on
// it, directly or not, are skipped, as are the removals of their records,
// and everything else carries on. A resource.Independent resource that
// fails makes its module fail too, but the module's other changes are
// still applied. Modules that failed in the plan count as failed too. A
// step that needs a reboot (see resource.RebootNeeder) stops its module in
// the same way, but without failing: what it stops waits for the next
// sync, after the reboot.
//
// Changes of resources in a group (resource.Grouped), such as package
// installs, are applied in one step, with one call to ApplyGroup, when the
// first of them is reached. That step takes in every change of the group
// that's ready by then, from any module: one whose module will be applied,
// whose module's dependencies have been applied, and whose module has no
// other change before it still to make, such as adding the repo a package
// comes from. The rest are applied in another step, when the first of them
// is reached. A change of the group that fails makes its own module fail,
// at that change.
//
// Applying a resource may change what's inside the folders it owns, as
// replacing a checkout replaces the checkouts inside it, after their
// checks ran. So a resource whose targets are inside a target of one
// applied before it, in its module or an earlier one, is checked again
// just before it's reached, and applied if it needs it then, as the plan's
// changes are. A check that fails then makes its module fail.
//
// Cancelling ctx (on Ctrl-C) lets the step in progress finish, with a
// context that isn't cancelled, and then stops Apply before the next step;
// in.Abort stops the step in progress too. At the end, Apply records when
// the sync ended and how in the state file. A plan without changes can be
// applied too: that only brings the state file up to date, deleting the
// records of things that are already gone and saving those that checks
// found out of date (see resource.Change.Record), and records the sync.
func Apply(ctx context.Context, p *Plan, in ApplyInput) *Result {
	a := &applier{plan: p, in: in, ctx: ctx, res: &Result{}, stopped: map[string]string{}, finished: map[string]bool{}, grouped: map[*Item]error{}}
	var cancel context.CancelFunc
	a.step, cancel = context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	if in.Abort != nil {
		defer context.AfterFunc(in.Abort, cancel)()
	}

	in.Store.Machine = p.Machine
	p.rt.Out = in.Out
	if !a.done() && p.NeedsPrivileges() {
		if sudo, ok := p.rt.Runner.(system.SudoRunner); ok {
			if a.sudoErr = sudo.StartSudo(a.step); a.sudoErr == nil {
				defer sudo.StopSudo()
			}
		}
	}

	a.modules()
	if !a.done() {
		a.removals()
	}

	result := state.ResultOK
	switch {
	case a.res.Interrupted:
		result = state.ResultInterrupted
	case a.res.failed():
		result = state.ResultFailed
	case a.res.RebootNeeded():
		// It stopped partway, for the reboot.
		result = state.ResultInterrupted
	}
	in.Store.LastSync = p.rt.Now()
	in.Store.LastSyncResult = result
	if err := in.Store.Save(); err != nil && a.res.Err == nil {
		a.res.Err = err
	}
	return a.res
}

// applier applies one plan.
type applier struct {
	plan *Plan
	in   ApplyInput
	// ctx is Apply's context, cancelled on Ctrl-C.
	ctx context.Context
	// step is the context steps run with, which only in.Abort cancels.
	step context.Context
	res  *Result
	// stopped maps each module that failed, was skipped or waits for a
	// reboot to the module that caused it: the one that failed, or the one
	// that needs the reboot.
	stopped map[string]string
	// finished holds the modules that have been applied, failed or
	// skipped.
	finished map[string]bool
	// grouped holds the items applied with their group, with why they
	// failed, or nil if they were applied.
	grouped map[*Item]error
	// applied holds the targets of the resources applied so far, as
	// targets returns them.
	applied []string
	// sudoErr is why the sudo session couldn't be started, if it couldn't.
	sudoErr error
}

// done reports whether Apply must stop before the next step, and records
// an interruption.
func (a *applier) done() bool {
	if a.res.Err != nil {
		return true
	}
	if a.ctx.Err() != nil {
		a.res.Interrupted = true
		return true
	}
	return false
}

// modules applies each module's changes.
func (a *applier) modules() {
	for i, m := range a.plan.Modules {
		if a.done() {
			return
		}
		mr := ModuleResult{Name: m.Name}
		switch {
		case m.failed():
			mr.Err = m.failure()
			a.stopped[m.Name] = m.Name
		case m.SkippedBecause != "":
			mr.SkippedBecause = m.SkippedBecause
			a.stopped[m.Name] = m.SkippedBecause
		default:
			if d := slices.IndexFunc(m.DependsOn, func(dep string) bool { return a.stopped[dep] != "" }); d >= 0 {
				mr.SkippedBecause = a.stopped[m.DependsOn[d]]
				a.stopped[m.Name] = mr.SkippedBecause
				break
			}
			a.module(i, &mr)
		}
		a.res.Modules = append(a.res.Modules, mr)
		a.finished[m.Name] = true
	}
}

// module applies the changes of the plan's module i, which hasn't failed,
// recording what happened in mr. The records of its resources with
// nothing to apply are brought up to date (see refresh). A resource inside
// a folder something applied before it owns is checked again first (see
// inApplied).
func (a *applier) module(i int, mr *ModuleResult) {
	m := a.plan.Modules[i]
	for k, item := range m.Items {
		if a.inApplied(item) {
			if a.done() {
				return
			}
			if err := a.recheck(item); err != nil {
				if a.fail(m, k, mr, item, err) {
					continue
				}
				return
			}
		}
		if !item.Change.Action.Applies() {
			if a.refresh(m.Name, item) != nil {
				return
			}
			continue
		}
		if _, ok := a.grouped[item]; !ok {
			if a.done() {
				return
			}
			if g, ok := item.Resource.(resource.Grouped); ok && !a.needsSudo(item) {
				a.group(i, k, g)
				if a.res.Err != nil {
					return
				}
			}
		}
		err, ok := a.grouped[item]
		if !ok {
			a.progress(m.Name, item.Change.Action, item.summary())
			if a.needsSudo(item) {
				err = a.sudoErr
			} else {
				err = a.apply(m.Name, item.Resource)
			}
		}
		if err != nil {
			if a.res.Err != nil {
				return
			}
			if a.fail(m, k, mr, item, err) {
				continue
			}
			return
		}
		mr.Applied++
		if why, ok := rebootNeeded(item.Resource); ok {
			mr.Reboot = why
			a.stop(m, k, mr)
			return
		}
	}
}

// fail makes module m fail at its item k, item, with err. It reports
// whether the module's other changes are still applied, as they are when
// item is a resource.Independent resource; otherwise the module stops.
func (a *applier) fail(m *ModulePlan, k int, mr *ModuleResult, item *Item, err error) bool {
	err = fmt.Errorf("%s: %w", item.summary(), err)
	if mr.Err == nil {
		mr.Err = err
	} else {
		mr.Err = errors.Join(mr.Err, err)
	}
	if resource.IsIndependent(item.Resource) {
		a.stopped[m.Name] = m.Name
		return true
	}
	a.stop(m, k, mr)
	return false
}

// stop stops module m at its item k, which failed or needs a reboot,
// recording in mr how many of its later changes were applied all the same,
// with their group.
func (a *applier) stop(m *ModulePlan, k int, mr *ModuleResult) {
	a.stopped[m.Name] = m.Name
	for _, later := range m.Items[k+1:] {
		if err, ok := a.grouped[later]; ok && err == nil {
			mr.Applied++
		}
	}
}

// inApplied reports whether one of item's targets is inside a folder that
// a resource applied in this sync owns, which may have changed what the
// item's check found: replacing a checkout, say, moves the checkouts
// inside it aside too. Items already applied with their group aren't.
func (a *applier) inApplied(item *Item) bool {
	if _, ok := a.grouped[item]; ok || len(a.applied) == 0 {
		return false
	}
	for _, target := range a.targets(item.Resource) {
		for _, dir := range a.applied {
			if rel, err := filepath.Rel(dir, target); err == nil && rel != "." && filepath.IsLocal(rel) {
				return true
			}
		}
	}
	return false
}

// recheck checks item again, and keeps the change it finds now.
func (a *applier) recheck(item *Item) error {
	c, err := item.Resource.Check(a.step, a.plan.rt)
	if err != nil {
		return fmt.Errorf("checking it again: %w", err)
	}
	item.Change = c
	return nil
}

// targets returns r's targets as clean absolute paths, with "~/" expanded
// to the home folder, leaving out those that aren't absolute paths.
func (a *applier) targets(r resource.Resource) []string {
	var paths []string
	for _, t := range r.Targets() {
		if rest, ok := strings.CutPrefix(t, "~/"); ok {
			t = filepath.Join(a.plan.rt.Paths.Home, filepath.FromSlash(rest))
		}
		if t = filepath.Clean(t); filepath.IsAbs(t) {
			paths = append(paths, t)
		}
	}
	return paths
}

// rebootNeeded reports whether the resource r, just applied, needs a
// reboot, and why.
func rebootNeeded(r resource.Resource) (string, bool) {
	rn, ok := r.(resource.RebootNeeder)
	if !ok {
		return "", false
	}
	why, needed := rn.RebootNeeded()
	if why == "" {
		why = resource.Describe(r.ID()) + " was applied"
	}
	return why, needed
}

// needsSudo reports whether applying item needs root, which the sudo
// session couldn't be started for.
func (a *applier) needsSudo(item *Item) bool {
	return item.privileged() && a.sudoErr != nil
}

// group applies, in one step, the change of item k of the plan's module i,
// which is in first's group, together with the group's other changes that
// are ready: those of the rest of module i, and of the modules after it,
// up to the first change of each module that isn't in the group or can't
// be applied now. It records how each one went in a.grouped, and saves
// the records of those applied.
func (a *applier) group(i, k int, first resource.Grouped) {
	var items []*Item
	var members []resource.Member
	for j := i; j < len(a.plan.Modules); j++ {
		m, start := a.plan.Modules[j], k
		if j > i {
			if !a.ready(m) {
				continue
			}
			start = 0
		}
		for _, item := range m.Items[start:] {
			if !item.Change.Action.Applies() {
				continue
			}
			if err, ok := a.grouped[item]; ok {
				if err != nil {
					break
				}
				continue
			}
			g, ok := item.Resource.(resource.Grouped)
			if !ok || g.Group() != first.Group() || a.needsSudo(item) {
				break
			}
			items = append(items, item)
			members = append(members, resource.Member{Module: m.Name, Resource: g})
		}
	}

	for n, item := range items {
		a.progress(members[n].Module, item.Change.Action, item.summary())
	}
	outcomes := first.ApplyGroup(a.step, a.plan.rt, members)
	for n, item := range items {
		if n >= len(outcomes) {
			a.grouped[item] = fmt.Errorf("applying group %s gave no outcome for it", first.Group())
			continue
		}
		o := outcomes[n]
		if o.Err == nil {
			a.put(members[n].Module, item.Resource, o.Record)
			a.applied = append(a.applied, a.targets(item.Resource)...)
		}
		a.grouped[item] = o.Err
	}
	_ = a.save()
}

// ready reports whether module m, which hasn't been reached yet, can have
// changes applied now: it will be applied, and every module it depends on
// has been.
func (a *applier) ready(m *ModulePlan) bool {
	if !m.applies() || a.stopped[m.Name] != "" {
		return false
	}
	return !slices.ContainsFunc(m.DependsOn, func(dep string) bool { return !a.finished[dep] || a.stopped[dep] != "" })
}

// apply applies one resource, and saves its record. If the state file
// can't be saved, it sets a.res.Err.
func (a *applier) apply(module string, r resource.Resource) error {
	rec, err := r.Apply(a.step, a.plan.rt)
	if err != nil {
		return err
	}
	a.applied = append(a.applied, a.targets(r)...)
	a.put(module, r, rec)
	return a.save()
}

// refresh brings the record of item, whose change doesn't apply, up to
// date, and saves it: to the record its check says the resource should
// have, if it says one, or else to belong to module, if it belonged to
// another one. If the state file can't be saved, it sets a.res.Err.
func (a *applier) refresh(module string, item *Item) error {
	switch prev, ok := a.in.Store.Get(item.Resource.ID()); {
	case item.Change.Record != nil:
		a.put(module, item.Resource, *item.Change.Record)
	case ok && prev.Module != module:
		prev.Module = module
		a.in.Store.Put(prev)
	default:
		return nil
	}
	return a.save()
}

// put puts the record of resource r of module into the state, filling in
// what Apply leaves to the engine.
func (a *applier) put(module string, r resource.Resource, rec state.Record) {
	rec.ID, rec.Kind, rec.Module = r.ID(), r.Kind(), module
	if len(rec.Paths) == 0 {
		rec.Paths = r.Targets()
	}
	if rec.Updated.IsZero() {
		rec.Updated = a.plan.rt.Now()
	}
	a.in.Store.Put(rec)
}

// save saves the state file, or sets a.res.Err.
func (a *applier) save() error {
	if err := a.in.Store.Save(); err != nil {
		a.res.Err = err
		return err
	}
	return nil
}

// removals carries out the plan's removals.
func (a *applier) removals() {
	for _, rm := range a.plan.Removals {
		if rm.Remover == nil || rm.Change.Action == resource.Orphan {
			continue
		}
		if a.done() {
			return
		}
		rec := rm.Record
		if rm.Err == nil && rm.Change.Action == resource.NoChange {
			// There's nothing left to remove but the record.
			if a.stopped[rec.Module] == "" {
				a.in.Store.Delete(rec.ID)
				if a.save() != nil {
					return
				}
			}
			continue
		}

		rr := RemovalResult{Summary: rm.summary(), Record: rec}
		switch {
		case a.stopped[rec.Module] != "":
			rr.SkippedBecause = a.stopped[rec.Module]
		case rm.Err != nil:
			rr.Err = rm.Err
		case rm.privileged() && a.sudoErr != nil:
			rr.Err = a.sudoErr
		default:
			a.progress("prune", rm.Change.Action, rr.Summary)
			if rr.Err = rm.Remover.Remove(a.step, a.plan.rt, rec); rr.Err == nil {
				a.in.Store.Delete(rec.ID)
				if a.save() != nil {
					return
				}
				rr.Removed = true
			}
		}
		a.res.Removals = append(a.res.Removals, rr)
	}
}

// progress reports a step that's starting.
func (a *applier) progress(module string, action resource.Action, summary string) {
	if a.in.Progress == nil {
		return
	}
	if action == resource.Unknown {
		summary = "checking " + summary
	}
	_, _ = fmt.Fprintf(a.in.Progress, "%s: %s %s\n", module, action.Symbol(), summary)
}

// Applied returns how many changes were applied, and how many things were
// removed.
func (r *Result) Applied() (changes, removals int) {
	for _, m := range r.Modules {
		changes += m.Applied
	}
	for _, rm := range r.Removals {
		if rm.Removed {
			removals++
		}
	}
	return changes, removals
}

// OK reports whether everything was applied: nothing failed, was skipped
// or waits for a reboot, and Apply wasn't interrupted.
func (r *Result) OK() bool {
	return !r.failed() && !r.RebootNeeded()
}

// RebootNeeded reports whether a change that was applied needs a reboot
// before the rest of the plan can be: what waits for it is left for the
// next sync.
func (r *Result) RebootNeeded() bool {
	return slices.ContainsFunc(r.Modules, func(m ModuleResult) bool { return m.Reboot != "" })
}

// waitsForReboot reports whether module stopped for a reboot.
func (r *Result) waitsForReboot(module string) bool {
	return slices.ContainsFunc(r.Modules, func(m ModuleResult) bool { return m.Name == module && m.Reboot != "" })
}

// failed reports whether anything failed or was skipped because something
// failed, or Apply stopped early. Waiting for a reboot isn't failing.
func (r *Result) failed() bool {
	if r.Interrupted || r.Err != nil {
		return true
	}
	for _, m := range r.Modules {
		if m.Err != nil || (m.SkippedBecause != "" && !r.waitsForReboot(m.SkippedBecause)) {
			return true
		}
	}
	for _, rm := range r.Removals {
		if !rm.Removed && (rm.Err != nil || !r.waitsForReboot(rm.SkippedBecause)) {
			return true
		}
	}
	return false
}

// ExitCode returns the exit code for the sync: 1 if anything failed or was
// skipped because something failed, or Apply was interrupted, and 0
// otherwise, even if changes wait for a reboot: sync has an exit code of
// its own for that (see RebootNeeded).
func (r *Result) ExitCode() int {
	if r.failed() {
		return 1
	}
	return 0
}

// Render writes a short summary of what happened, such as "Applied 5
// changes." followed by a line for each module that failed, was skipped or
// waits for a reboot, and each removal that didn't happen; and, if a
// reboot is needed, says to reboot and sync again.
func (r *Result) Render(w io.Writer) {
	var b strings.Builder
	changes, removals := r.Applied()
	switch {
	case changes > 0 && removals > 0:
		fmt.Fprintf(&b, "Applied %s and %s.\n", plural(changes, "change"), plural(removals, "removal"))
	case changes > 0:
		fmt.Fprintf(&b, "Applied %s.\n", plural(changes, "change"))
	case removals > 0:
		fmt.Fprintf(&b, "Applied %s.\n", plural(removals, "removal"))
	default:
		b.WriteString("Nothing changed.\n")
	}
	for _, m := range r.Modules {
		switch {
		case m.Err != nil:
			fmt.Fprintf(&b, "%s failed: %s\n", m.Name, indent(m.Err))
		case m.Reboot != "":
			fmt.Fprintf(&b, "%s needs a reboot before it can carry on: %s.\n", m.Name, m.Reboot)
		case r.waitsForReboot(m.SkippedBecause):
			fmt.Fprintf(&b, "%s waits for the reboot.\n", m.Name)
		case m.SkippedBecause != "":
			fmt.Fprintf(&b, "%s skipped because %s failed.\n", m.Name, m.SkippedBecause)
		default:
		}
	}
	for _, rm := range r.Removals {
		switch {
		case rm.Err != nil:
			fmt.Fprintf(&b, "Couldn't remove %s: %s\n", rm.Summary, indent(rm.Err))
		case r.waitsForReboot(rm.SkippedBecause):
			fmt.Fprintf(&b, "Didn't remove %s yet: it waits for the reboot.\n", rm.Summary)
		case rm.SkippedBecause != "":
			fmt.Fprintf(&b, "Didn't remove %s because %s failed.\n", rm.Summary, rm.SkippedBecause)
		default:
		}
	}
	switch {
	case r.Err != nil:
		fmt.Fprintf(&b, "Stopped: %s\n", indent(r.Err))
	case r.Interrupted:
		b.WriteString("Interrupted: run maisys sync again to finish.\n")
	case r.RebootNeeded():
		b.WriteString(RebootNotice + "\n")
	}
	_, _ = io.WriteString(w, b.String())
}

// RebootNotice is how Render says what to do when a change needs a reboot.
const RebootNotice = "Reboot, then run `maisys sync` again to finish."

// plural returns "1 change" or "2 changes".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// indent returns err's message with the lines after the first indented.
func indent(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", "\n  ")
}
