package engine

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
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
	// was skipped because of it.
	SkippedBecause string
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
	// that this module depends on, if the removal was skipped.
	SkippedBecause string
}

// Apply applies the plan p: the modules' changes, one module at a time in
// dependency order and each module's in the order it lists them, then the
// removals. Changes with Action Warn and Unknown are applied like Create
// and Update. If any change needs root, the sudo session (the only thing
// that may ask for a password) is started before the first step and
// stopped at the end. Windows has no sudo: there, steps that need admin
// rights will run in an elevated helper instead.
//
// The state file is saved after every step. A step that fails makes its
// module fail: the module's other changes and every module that depends on
// it, directly or not, are skipped, as are the removals of their records,
// and everything else carries on. Modules that failed in the plan count as
// failed too.
//
// Cancelling ctx (on Ctrl-C) lets the step in progress finish, with a
// context that isn't cancelled, and then stops Apply before the next step;
// in.Abort stops the step in progress too. At the end, Apply records when
// the sync ended and how in the state file. A plan without changes can be
// applied too: that only deletes the records of things that are already
// gone, and records the sync.
func Apply(ctx context.Context, p *Plan, in ApplyInput) *Result {
	a := &applier{plan: p, in: in, ctx: ctx, res: &Result{}, stopped: map[string]string{}}
	var cancel context.CancelFunc
	a.step, cancel = context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	if in.Abort != nil {
		defer context.AfterFunc(in.Abort, cancel)()
	}

	in.Store.Machine = p.Machine
	if !a.done() && p.NeedsPrivileges() && p.rt.Machine.OS != config.Windows {
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
	case !a.res.OK():
		result = state.ResultFailed
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
	// stopped maps each module that failed or was skipped to the failed
	// module that caused it.
	stopped map[string]string
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
	for _, m := range a.plan.Modules {
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
			if i := slices.IndexFunc(m.DependsOn, func(dep string) bool { return a.stopped[dep] != "" }); i >= 0 {
				mr.SkippedBecause = a.stopped[m.DependsOn[i]]
				a.stopped[m.Name] = mr.SkippedBecause
				break
			}
			a.module(m, &mr)
		}
		a.res.Modules = append(a.res.Modules, mr)
	}
}

// module applies the changes of module m, which hasn't failed, recording
// what happened in mr.
func (a *applier) module(m *ModulePlan, mr *ModuleResult) {
	for _, item := range m.Items {
		if !item.Change.Action.Applies() {
			continue
		}
		if a.done() {
			return
		}
		a.progress(m.Name, item.Change.Action, item.summary())
		var err error
		if item.privileged() && a.sudoErr != nil {
			err = a.sudoErr
		} else {
			err = a.apply(m.Name, item.Resource)
		}
		if err != nil {
			if a.res.Err != nil {
				return
			}
			mr.Err = fmt.Errorf("%s: %w", item.summary(), err)
			a.stopped[m.Name] = m.Name
			return
		}
		mr.Applied++
	}
}

// apply applies one resource, and saves its record. If the state file
// can't be saved, it sets a.res.Err.
func (a *applier) apply(module string, r resource.Resource) error {
	rec, err := r.Apply(a.step, a.plan.rt)
	if err != nil {
		return err
	}
	rec.ID, rec.Kind, rec.Module = r.ID(), r.Kind(), module
	if len(rec.Paths) == 0 {
		rec.Paths = r.Targets()
	}
	if rec.Updated.IsZero() {
		rec.Updated = a.plan.rt.Now()
	}
	a.in.Store.Put(rec)
	return a.save()
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

// OK reports whether everything was applied: nothing failed or was
// skipped, and Apply wasn't interrupted.
func (r *Result) OK() bool {
	if r.Interrupted || r.Err != nil {
		return false
	}
	for _, m := range r.Modules {
		if m.Err != nil || m.SkippedBecause != "" {
			return false
		}
	}
	for _, rm := range r.Removals {
		if !rm.Removed {
			return false
		}
	}
	return true
}

// ExitCode returns the exit code for the sync: 0 if everything was
// applied, and 1 otherwise.
func (r *Result) ExitCode() int {
	if r.OK() {
		return 0
	}
	return 1
}

// Render writes a short summary of what happened, such as "Applied 5
// changes." followed by a line for each module that failed or was skipped
// and each removal that didn't happen.
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
		case m.SkippedBecause != "":
			fmt.Fprintf(&b, "%s skipped because %s failed.\n", m.Name, m.SkippedBecause)
		default:
		}
	}
	for _, rm := range r.Removals {
		switch {
		case rm.Err != nil:
			fmt.Fprintf(&b, "Couldn't remove %s: %s\n", rm.Summary, indent(rm.Err))
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
	}
	_, _ = io.WriteString(w, b.String())
}

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
