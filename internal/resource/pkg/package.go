package pkg

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

const (
	// Kind is the kind of package resources, and of their records.
	Kind = "package"
	// Group is the group package resources are applied in: every
	// module's packages go into one transaction.
	Group = "packages"
)

// validName matches the package names maisys accepts: apt's and dnf's,
// perhaps with an architecture, such as "libc6:i386" or "glibc.i686". On
// Ubuntu, names that end with "-" are rejected too (see Manager.checkName).
var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:~-]*$`)

// Package is a package a module installs and keeps at its latest version.
// Set makes them.
type Package struct {
	module string
	name   string
	// from are resources of the module the package may come from.
	from []resource.Resource
	// upgrade reports whether Check found it installed, with an upgrade.
	upgrade bool
}

var _ resource.Grouped = (*Package)(nil)

// Set returns the package set of module: a Package for each of names, once
// each, in order. Each one installs its package and keeps it at its latest
// version.
//
// from are the module's resources the packages may come from, such as the
// repo that has them, or a release package that adds it. While one of them
// has a change to make, a name the enabled repos don't have is taken to be
// from it, rather than an error: it's checked when it's installed. The
// packages' checks check them again, so their checks must be safe to run
// at the same time as each other.
func Set(rt *resource.Runtime, module string, names []string, from ...resource.Resource) []resource.Resource {
	rs := make([]resource.Resource, 0, len(names))
	pkgs := make([]*Package, 0, len(names))
	for _, name := range names {
		if !slices.ContainsFunc(pkgs, func(p *Package) bool { return p.name == name }) {
			p := &Package{module: module, name: name, from: from}
			pkgs = append(pkgs, p)
			rs = append(rs, p)
		}
	}
	if m, err := ManagerFor(rt); err == nil {
		m.want(pkgs)
	}
	return rs
}

// Name is the package's name.
func (p *Package) Name() string { return p.name }

// ID is "package:MODULE/NAME": two modules can install the same package.
func (p *Package) ID() string { return Kind + ":" + p.module + "/" + p.name }

// Kind is Kind.
func (p *Package) Kind() string { return Kind }

// Targets is empty: packages own no paths maisys checks.
func (p *Package) Targets() []string { return nil }

// Privileged is true: installing packages needs root.
func (p *Package) Privileged() bool { return true }

// Group is Group.
func (p *Package) Group() string { return Group }

// Check finds whether the package needs installing or upgrading. A name
// the enabled repos don't have is an error, unless the package may come
// from something the same sync adds; on Fedora, if the repos have packages
// that provide it, the error names them. A package held at its version
// (apt-mark hold) isn't upgraded: the plan notes that it's held. If apt's
// lists are stale and the plan couldn't refresh them, the sync's first
// package notes that.
func (p *Package) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	m, err := ManagerFor(rt)
	if err != nil {
		return resource.Change{}, err
	}
	in, err := m.lookup(ctx, p.name)
	if err != nil {
		return resource.Change{}, err
	}
	c, err := p.change(ctx, rt, in)
	if err != nil {
		return resource.Change{}, err
	}
	if in.installed && !p.upgrade {
		m.isCurrent(p)
	}
	if note := m.listsNote(p); note != "" {
		c.Notes = append(c.Notes, note)
	}
	return c, nil
}

// change returns the change the package needs, given what looking it up
// found.
func (p *Package) change(ctx context.Context, rt *resource.Runtime, in info) (resource.Change, error) {
	p.upgrade = in.installed && in.newer != "" && !in.held
	switch {
	case p.upgrade:
		return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("upgrade %s %s → %s", p.name, in.version, in.newer)}, nil
	case in.held && in.newer != "":
		return resource.Change{Notes: []string{fmt.Sprintf("%s is held at %s (not upgraded to %s)", p.name, in.version, in.newer)}}, nil
	case in.installed:
		return resource.Change{}, nil
	case in.known || in.unchecked || p.fromPending(ctx, rt):
		return resource.Change{Action: resource.Create, Summary: "install " + p.name}, nil
	case in.noCandidate:
		return resource.Change{}, fmt.Errorf("unknown package %s: the enabled repos have nothing to install for it (is it a virtual package? List the package that provides it instead)", p.name)
	case len(in.providers) > 0:
		return resource.Change{}, providedError(p.name, in.providers)
	default:
		return resource.Change{}, fmt.Errorf("unknown package %s: the enabled repos don't have it", p.name)
	}
}

// providedError is the error for name, which isn't a package name, but
// which providers provide, such as vim, which vim-enhanced provides. It
// names the first few.
func providedError(name string, providers []string) error {
	const shown = 3
	if len(providers) == 1 {
		return fmt.Errorf("%s isn't a package name: %s provides it, so list that", name, providers[0])
	}
	list := strings.Join(providers[:len(providers)-1], ", ") + " and " + providers[len(providers)-1]
	if len(providers) > shown {
		list = fmt.Sprintf("%s and %d others", strings.Join(providers[:shown], ", "), len(providers)-shown)
	}
	return fmt.Errorf("%s isn't a package name: %s provide it, so list one of those", name, list)
}

// fromPending reports whether something the package may come from has a
// change to make, or can't be checked.
func (p *Package) fromPending(ctx context.Context, rt *resource.Runtime) bool {
	for _, r := range p.from {
		c, err := r.Check(ctx, rt)
		if err != nil || c.Action.Applies() {
			return true
		}
	}
	return false
}

// Apply installs or upgrades the package on its own.
func (p *Package) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	out := p.ApplyGroup(ctx, rt, []resource.Member{{Module: p.module, Resource: p}})
	return out[0].Record, out[0].Err
}

// ApplyGroup installs and upgrades the packages of members, from every
// module, in one transaction: one apt-get install, or on Fedora one dnf
// install and one dnf upgrade. If that fails, it tries again module by
// module, so that only the modules whose packages can't be installed fail,
// each with its own error. On Ubuntu, it refreshes the package lists first
// if they're out of date. The plan may not have seen the upgrades the
// refreshed lists have, as when sudo needed a password to refresh them
// then: the members' modules' other packages get those upgrades too, so
// that they're all at their latest version.
func (p *Package) ApplyGroup(ctx context.Context, rt *resource.Runtime, members []resource.Member) []resource.Outcome {
	out := make([]resource.Outcome, len(members))
	pkgs := make([]*Package, len(members))
	var modules []string
	for i, mem := range members {
		pp, ok := mem.Resource.(*Package)
		if !ok {
			out[i].Err = fmt.Errorf("%s isn't a package", mem.Resource.ID())
			continue
		}
		pkgs[i] = pp
		if !slices.Contains(modules, mem.Module) {
			modules = append(modules, mem.Module)
		}
	}

	every := func(int) bool { return true }
	m, err := ManagerFor(rt)
	if err != nil {
		settle(out, pkgs, every, err)
		return out
	}
	err = m.applyPackages(ctx, pkgs)
	if err == nil || len(modules) < 2 || retryless(ctx, err) {
		settle(out, pkgs, every, err)
		return out
	}

	m.notify("installing the packages of %s together failed, so maisys is trying again module by module: %v", strings.Join(modules, ", "), err)
	for _, module := range modules {
		mine := func(i int) bool { return members[i].Module == module }
		some := make([]*Package, len(pkgs))
		for i, pp := range pkgs {
			if pp != nil && mine(i) {
				some[i] = pp
			}
		}
		settle(out, pkgs, mine, m.applyPackages(ctx, some))
	}
	return out
}

// settle records the outcome of the packages in pkgs that pick picks:
// failed with err, or installed.
func settle(out []resource.Outcome, pkgs []*Package, pick func(i int) bool, err error) {
	for i, pp := range pkgs {
		if pp == nil || !pick(i) {
			continue
		}
		if err != nil {
			out[i].Err = err
		} else {
			out[i].Record = state.Record{Details: map[string]string{"name": pp.name}}
		}
	}
}

// retryless reports whether trying module by module would fail the same
// way: maisys was stopped, or another program held the lock, or sudo
// needs a password.
func retryless(ctx context.Context, err error) bool {
	if _, ok := errors.AsType[*lockError](err); ok {
		return true
	}
	return ctx.Err() != nil || errors.Is(err, system.ErrNeedsPassword)
}

// applyPackages installs or upgrades the packages in pkgs that aren't nil,
// as their checks found they need. Names in several modules are installed
// once. If apt's lists were refreshed after the plan looked the packages
// up, their modules' packages that were up to date then get the upgrades
// the refreshed lists have too.
func (m *Manager) applyPackages(ctx context.Context, pkgs []*Package) error {
	var install, upgrade, modules []string
	for _, pp := range pkgs {
		if pp == nil {
			continue
		}
		if !slices.Contains(modules, pp.module) {
			modules = append(modules, pp.module)
		}
		switch {
		case pp.upgrade:
			if !slices.Contains(upgrade, pp.name) {
				upgrade = append(upgrade, pp.name)
			}
		case !slices.Contains(install, pp.name):
			install = append(install, pp.name)
		}
	}
	if len(install)+len(upgrade) == 0 {
		return nil
	}
	if err := m.refresh(ctx); err != nil {
		return err
	}
	more, err := m.missedUpgrades(ctx, modules, slices.Concat(install, upgrade))
	if err != nil {
		return err
	}
	if len(more) > 0 {
		m.notify("the refreshed package lists have upgrades the plan couldn't show, so they're made too: %s", strings.Join(more, ", "))
	}
	return m.backend.install(ctx, m, install, append(upgrade, more...))
}

// Remover handles the records of packages taken out of the config. maisys
// never removes packages: one that's still installed is reported as no
// longer used, and its record kept. The record of one that's gone, or that
// another module of the sync installs, is forgotten.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind is Kind.
func (Remover) Kind() string { return Kind }

// CheckRemove reports the package as no longer used, if it's installed.
func (Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	name := rec.Details["name"]
	if name == "" {
		_, name, _ = strings.Cut(rec.ID, "/")
	}
	m, err := ManagerFor(rt)
	if err != nil {
		return resource.Change{}, err
	}
	if m.wants(name) {
		return resource.Change{}, nil
	}
	installed, err := m.Installed(ctx, name)
	if err != nil || !installed {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Orphan, Summary: "package " + name}, nil
}

// Remove fails: maisys never removes packages.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error {
	return errors.New("maisys never removes packages")
}
