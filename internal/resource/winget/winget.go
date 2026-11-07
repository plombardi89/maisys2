// Package winget installs Windows packages with winget and keeps them at
// their latest version. Each module that lists packages gets a package set
// (Set): a resource for each package, which installs it and keeps it up to
// date. maisys never removes winget packages.
//
// maisys never reads winget's tables, whose columns change with winget's
// version and the language Windows uses. Instead:
//
//   - What's installed, and at which version, comes from the JSON file
//     winget export writes, once for every package of a sync.
//   - Whether an installed package has an upgrade comes from the JSON of
//     winget dscv3 package --get, WinGet's DSC v3 resource for packages:
//     its "useLatest" is false while an upgrade is available. winget list
//     --upgrade-available can't tell: it exits with 0 whether or not it
//     finds an upgrade, and only its table says which. With a winget too
//     old to have dscv3, maisys installs packages but can't upgrade them,
//     and the plan says so.
//   - Whether a package that isn't installed exists comes from the exit
//     code of winget show.
//   - How an install or upgrade went comes from winget's exit codes (see
//     exitcodes.go).
//
// Packages come from the winget source (the community repository) only,
// by their exact ID, which is case-sensitive.
//
// # Admin rights
//
// winget can't say beforehand, in a form maisys can read, whether a
// package's installer is per-user or system-wide. So packages are
// installed as you, with the installer winget picks: winget, or the
// installer itself, asks for admin rights (a UAC prompt) when an installer
// needs them, and per-user installers install for you. Only the packages a
// module wants system-wide (Spec.Machine), such as Git.Git, are installed
// with --scope machine, which needs admin rights: they're Privileged, so
// they're applied with the sync's other admin steps, in the elevated
// helper. Upgrades keep the scope a package was installed with.
//
// # Reboots
//
// An install or upgrade that needs Windows to restart, before it can be
// installed or to finish, fails with a *RebootError, whose RebootRequired
// method reports true. The sync looks for that method with errors.As to
// stop and ask for a restart (exit code 3) rather than report a failure.
// Running maisys sync again after the restart picks up where it stopped.
package winget

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
)

// Kind is the kind of winget package resources, and of their records.
const Kind = "winget"

// validID matches winget package IDs, as winget's manifest schema
// describes them: two to eight parts separated by dots, such as
// "Microsoft.WindowsTerminal".
var validID = regexp.MustCompile(`^[^.\s\\/:*?"<>|\x00-\x1f]{1,32}(\.[^.\s\\/:*?"<>|\x00-\x1f]{1,32}){1,7}$`)

// ValidID reports whether id is a valid winget package ID, such as
// "Microsoft.WindowsTerminal".
func ValidID(id string) bool {
	return validID.MatchString(id) && !strings.HasPrefix(id, "-")
}

// Spec is a winget package a module wants.
type Spec struct {
	// ID is the package's ID in the winget source, such as "Git.Git".
	ID string
	// Machine installs the package for every user of the machine (winget
	// install --scope machine), which needs admin rights. Without it,
	// winget picks the installer, preferring a per-user one.
	Machine bool
}

// Package is a winget package a module installs and keeps at its latest
// version. Set makes them.
type Package struct {
	module string
	spec   Spec
	// checked reports whether Check ran on this value, and upgrade
	// whether it found the package installed, with an upgrade.
	checked, upgrade bool
	// reboot says why Windows must restart after the last Apply, if it
	// must.
	reboot string
}

var _ resource.RebootNeeder = (*Package)(nil)

// RebootNeeded reports whether winget said Windows must restart, before or
// after the last Apply's install. The next sync, after the restart, checks
// the package again and finishes the install if it has to.
func (p *Package) RebootNeeded() (why string, needed bool) {
	return p.reboot, p.reboot != ""
}

var _ resource.Resource = (*Package)(nil)

// Set returns the package set of module: a Package for each of specs, once
// for each ID (the first spec of an ID wins), in order.
func Set(rt *resource.Runtime, module string, specs ...Spec) []resource.Resource {
	rs := make([]resource.Resource, 0, len(specs))
	var ids []string
	for _, s := range specs {
		if !slices.Contains(ids, s.ID) {
			ids = append(ids, s.ID)
			rs = append(rs, &Package{module: module, spec: s})
		}
	}
	if m, err := ManagerFor(rt); err == nil {
		m.want(ids...)
	}
	return rs
}

// Spec returns the package's spec.
func (p *Package) Spec() Spec { return p.spec }

// ID is "winget:MODULE/ID": two modules can install the same package.
func (p *Package) ID() string { return Kind + ":" + p.module + "/" + p.spec.ID }

// Kind is Kind.
func (p *Package) Kind() string { return Kind }

// Targets is empty: packages own no paths maisys checks.
func (p *Package) Targets() []string { return nil }

// Privileged reports whether the package is installed system-wide, which
// needs admin rights.
func (p *Package) Privileged() bool { return p.spec.Machine }

// Check finds whether the package needs installing or upgrading. An ID the
// winget source doesn't have is an error.
func (p *Package) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	id := p.spec.ID
	if !ValidID(id) {
		return resource.Change{}, fmt.Errorf("%q isn't a valid winget package ID, such as \"Microsoft.WindowsTerminal\"", id)
	}
	m, err := ManagerFor(rt)
	if err != nil {
		return resource.Change{}, err
	}
	version, installed, err := m.Installed(ctx, id)
	if err != nil {
		return resource.Change{}, err
	}
	if !installed {
		if err := m.exists(ctx, id); err != nil {
			return resource.Change{}, err
		}
		p.checked, p.upgrade = true, false
		summary := "install " + id
		if p.spec.Machine {
			summary += " (system-wide)"
		}
		return resource.Change{Action: resource.Create, Summary: summary}, nil
	}

	up, err := m.upgradable(ctx, id, version)
	if err != nil {
		return resource.Change{}, err
	}
	p.checked, p.upgrade = true, up.available
	switch {
	case up.available:
		return resource.Change{Action: resource.Update, Summary: fmt.Sprintf("upgrade %s %s → latest", id, version)}, nil
	case up.note != "":
		return resource.Change{Notes: []string{up.note}}, nil
	default:
		return resource.Change{}, nil
	}
}

// Apply installs or upgrades the package, as Check found it needs. On a
// value Check hasn't run on, such as one the elevated helper made, it
// checks first.
func (p *Package) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	rec := state.Record{Details: map[string]string{"id": p.spec.ID}}
	if !p.checked {
		c, err := p.Check(ctx, rt)
		if err != nil {
			return state.Record{}, err
		}
		if !c.Action.Applies() {
			return rec, nil
		}
	}
	m, err := ManagerFor(rt)
	if err != nil {
		return state.Record{}, err
	}
	p.reboot = ""
	if p.upgrade {
		err = m.upgrade(ctx, p.spec)
	} else {
		err = m.install(ctx, p.spec)
	}
	if reboot, ok := errors.AsType[*RebootError](err); ok {
		// Not a failure: the sync stops for a restart (see RebootNeeded).
		p.reboot = reboot.Error()
		return rec, nil
	}
	if err != nil {
		return state.Record{}, err
	}
	return rec, nil
}

// Remover handles the records of winget packages taken out of the config.
// maisys never removes winget packages: one that's still installed is
// reported as no longer used, and its record kept. The record of one
// that's gone, or that a module of the sync still installs, is forgotten.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind is Kind.
func (Remover) Kind() string { return Kind }

// CheckRemove reports the package as no longer used, if it's installed.
func (Remover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	id := rec.Details["id"]
	if id == "" {
		_, id, _ = strings.Cut(rec.ID, "/")
	}
	m, err := ManagerFor(rt)
	if err != nil {
		return resource.Change{}, err
	}
	if m.wants(id) {
		return resource.Change{}, nil
	}
	_, installed, err := m.Installed(ctx, id)
	if err != nil || !installed {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Orphan, Summary: "winget package " + id}, nil
}

// Remove fails: maisys never removes winget packages.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error {
	return errors.New("maisys never removes winget packages")
}
