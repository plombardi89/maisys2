package wsl

import (
	"context"
	"fmt"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
)

// DefaultKind is the kind of resource a Default is.
const DefaultKind = "wsl-default"

// Default makes Name the distro WSL starts by default, as wsl --set-default
// does. Its check reports "~ default distro → NAME" if it isn't. A distro
// created by the same sync is checked again when it's applied.
type Default struct {
	// Name is the distro's name.
	Name string
}

var _ resource.Resource = (*Default)(nil)

// ID returns "wsl-default".
func (d *Default) ID() string { return DefaultKind }

// Kind returns "wsl-default".
func (d *Default) Kind() string { return DefaultKind }

// Targets returns nothing.
func (d *Default) Targets() []string { return nil }

// Privileged returns false.
func (d *Default) Privileged() bool { return false }

// Check compares WSL's default distro with Name.
func (d *Default) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	ok, _, err := d.inspect(rt)
	if err != nil || ok {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Update, Summary: "default distro → " + d.Name}, nil
}

// Apply makes Name WSL's default distro.
func (d *Default) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	ok, exists, err := d.inspect(rt)
	switch {
	case err != nil:
		return state.Record{}, err
	case ok:
	case !exists:
		return state.Record{}, fmt.Errorf("there's no distro %s to start by default", d.Name)
	default:
		if _, err := rt.Runner.Run(ctx, command("making "+d.Name+" the default distro", "--set-default", d.Name)); err != nil {
			return state.Record{}, err
		}
	}
	return state.Record{Details: map[string]string{"distro": d.Name}}, nil
}

// inspect reports whether Name is the default distro, and whether it
// exists.
func (d *Default) inspect(rt *resource.Runtime) (isDefault, exists bool, err error) {
	reg, err := RegistryFor(rt)
	if err != nil {
		return false, false, err
	}
	distros, err := reg.Distros()
	if err != nil {
		return false, false, err
	}
	distro, exists := Find(distros, d.Name)
	if !exists {
		return false, false, nil
	}
	id, err := reg.DefaultDistro()
	if err != nil {
		return false, true, err
	}
	return strings.EqualFold(id, distro.ID), true, nil
}

// DefaultRemover handles the records of the default distro once it's
// taken out of the config: WSL's default stays as it is, and the record is
// dropped without a line in the plan.
type DefaultRemover struct{}

var _ resource.Remover = DefaultRemover{}

// Kind returns "wsl-default".
func (DefaultRemover) Kind() string { return DefaultKind }

// CheckRemove returns no change: the default distro stays as it is.
func (DefaultRemover) CheckRemove(context.Context, *resource.Runtime, state.Record) (resource.Change, error) {
	return resource.Change{}, nil
}

// Remove does nothing.
func (DefaultRemover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }
