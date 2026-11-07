package resource

import (
	"context"

	"github.com/plombardi89/maisys2/internal/state"
)

// Grouped is a resource whose changes are applied together with those of
// the other resources in its group, from every module: package installs,
// for example, go into one apt or dnf transaction. The engine applies a
// group's changes with one call to ApplyGroup, when it reaches the first of
// them, taking in every other one that's ready by then (see engine.Apply).
// Its Apply still applies it on its own, as a group of one.
type Grouped interface {
	Resource
	// Group names the resource's group.
	Group() string
	// ApplyGroup applies the changes of members, the resources of the
	// group to apply now, which start with the resource it's called on. It
	// returns an Outcome for each member, in the same order: the record to
	// keep in the state file, or why applying that member failed, which
	// makes its module fail. The engine fills in the records as it does
	// for Apply.
	ApplyGroup(ctx context.Context, rt *Runtime, members []Member) []Outcome
}

// Member is a resource applied with the rest of its group.
type Member struct {
	// Module is the module the resource belongs to.
	Module string
	// Resource is the resource, which was checked on this value.
	Resource Grouped
}

// Outcome is what applying one member of a group did.
type Outcome struct {
	// Record is what to keep in the state file, if it was applied.
	Record state.Record
	// Err is why it couldn't be applied, if it couldn't.
	Err error
}
