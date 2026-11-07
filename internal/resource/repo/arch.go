package repo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Architecture is an extra architecture dpkg installs packages for
// (Ubuntu), such as i386 for Steam. The plan shows it as "architecture
// NAME".
type Architecture struct {
	// Name is the architecture, as dpkg names it.
	Name string
}

var _ resource.Resource = (*Architecture)(nil)

// ID is "dpkg-arch:NAME".
func (a *Architecture) ID() string { return ArchKind + ":" + a.Name }

// Kind is ArchKind.
func (a *Architecture) Kind() string { return ArchKind }

// Targets is empty.
func (a *Architecture) Targets() []string { return nil }

// Privileged is true: adding an architecture needs root.
func (a *Architecture) Privileged() bool { return true }

// Check finds whether dpkg has the architecture: it's the machine's own,
// or one of dpkg's foreign architectures.
func (a *Architecture) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if rt.Facts == nil || rt.Facts.OS != config.Ubuntu {
		return resource.Change{}, errors.New("extra package architectures only work on Ubuntu")
	}
	if !validName.MatchString(a.Name) {
		return resource.Change{}, fmt.Errorf("%q isn't a valid dpkg architecture", a.Name)
	}
	if a.Name == string(rt.Facts.Arch) {
		return resource.Change{}, nil
	}
	has, err := foreignArch(ctx, rt.Runner, a.Name)
	if err != nil || has {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Create, Summary: "add architecture " + a.Name}, nil
}

// Apply adds the architecture to dpkg's.
func (a *Architecture) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	m, err := pkg.ManagerFor(rt)
	if err != nil {
		return state.Record{}, err
	}
	if _, err := m.Run(ctx, system.Cmd{Name: "dpkg", Args: []string{"--add-architecture", a.Name}, Root: true, Label: "dpkg --add-architecture"}); err != nil {
		return state.Record{}, err
	}
	m.SourcesChanged()
	return state.Record{Details: map[string]string{"architecture": a.Name}}, nil
}

// foreignArch reports whether arch is one of dpkg's foreign
// architectures.
func foreignArch(ctx context.Context, r system.Runner, arch string) (bool, error) {
	res, err := r.Run(ctx, system.Cmd{Name: "dpkg", Args: []string{"--print-foreign-architectures"}, Label: "dpkg --print-foreign-architectures"})
	if err != nil {
		return false, err
	}
	return slices.Contains(strings.Fields(string(res.Stdout)), arch), nil
}

// ArchRemover handles the records of architectures taken out of the
// config. maisys never removes them, since packages may use them: one
// that's still there is reported as no longer used, and its record kept.
type ArchRemover struct{}

var _ resource.Remover = ArchRemover{}

// Kind is ArchKind.
func (ArchRemover) Kind() string { return ArchKind }

// CheckRemove reports the architecture as no longer used, if dpkg still
// has it.
func (ArchRemover) CheckRemove(ctx context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	arch := rec.Details["architecture"]
	if arch == "" {
		_, arch, _ = strings.Cut(rec.ID, ":")
	}
	has, err := foreignArch(ctx, rt.Runner, arch)
	if err != nil || !has {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Orphan, Summary: "architecture " + arch}, nil
}

// Remove fails: maisys never removes architectures.
func (ArchRemover) Remove(context.Context, *resource.Runtime, state.Record) error {
	return errors.New("maisys never removes package architectures")
}
