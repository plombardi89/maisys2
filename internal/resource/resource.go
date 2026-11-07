// Package resource defines resources, the building blocks modules are made
// of: a package set, a tool, a file, a git checkout and so on. Each one
// checks the live system against what the config wants, and applies the
// difference. The engine plans and applies them, and removes the ones
// taken out of the config with a Remover for their kind.
package resource

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// Action is what applying a change does, as the plan shows it.
type Action int

// The actions a change can have.
const (
	// NoChange means the resource is as the config wants it. The plan
	// leaves it out, and it isn't applied.
	NoChange Action = iota
	// Create adds something that isn't there ("+").
	Create
	// Update changes something that's there ("~").
	Update
	// Remove deletes something ("-").
	Remove
	// Warn is a change that needs your attention ("!"), such as a file
	// changed outside maisys that applying backs up and overwrites. It's
	// applied like Create and Update.
	Warn
	// Unknown means the check needs root and sudo needs a password, which
	// a plan never asks for. The plan says it's checked during apply, and
	// applying it checks again.
	Unknown
	// Orphan is something no longer in the config that maisys never
	// removes, such as an OS package or a package repo ("?"). The plan says
	// it's no longer used, and it's left in place.
	Orphan
)

// Symbol returns the action's symbol in the plan, or "" for NoChange.
func (a Action) Symbol() string {
	switch a {
	case Create:
		return "+"
	case Update:
		return "~"
	case Remove:
		return "-"
	case Warn:
		return "!"
	case Unknown, Orphan:
		return "?"
	default:
		return ""
	}
}

// String names the action.
func (a Action) String() string {
	switch a {
	case NoChange:
		return "no change"
	case Create:
		return "create"
	case Update:
		return "update"
	case Remove:
		return "remove"
	case Warn:
		return "warn"
	case Unknown:
		return "unknown"
	case Orphan:
		return "orphan"
	default:
		return fmt.Sprintf("Action(%d)", int(a))
	}
}

// Applies reports whether a change with the action is applied: Create,
// Update, Remove, Warn and Unknown are.
func (a Action) Applies() bool {
	switch a {
	case Create, Update, Remove, Warn, Unknown:
		return true
	default:
		return false
	}
}

// Change is what applying a resource would do, as Check found it.
type Change struct {
	// Action is what kind of change it is.
	Action Action
	// Summary says what changes, in a few words, such as "install jq" or
	// "toolchain 1.27.3 → 1.27.4 (latest)". Empty means the resource's ID.
	Summary string
	// Notes are warnings the plan shows under the change, even if Action is
	// NoChange, such as "fish: sdkman has no fish integration (skipped)".
	Notes []string
	// Privileged reports whether applying the change needs root on Linux,
	// or admin on Windows. A resource's changes need it anyway if the
	// resource is Privileged; a removal's need it only if this is set.
	Privileged bool
	// MayReboot reports whether applying the change may need a reboot
	// before the rest of the sync can be applied, such as installing WSL
	// for the first time. The plan says so. Whether it does is up to the
	// resource, once it's applied (see RebootNeeder).
	MayReboot bool
}

// Runtime is what resources check and apply with. It's shared by every
// resource of a sync, and must not be changed while they use it.
type Runtime struct {
	// Runner runs commands. During the plan, a command run as root fails
	// with system.ErrNeedsPassword instead of asking for a password.
	Runner system.Runner
	// Facts is the live system.
	Facts *facts.System
	// Paths are where maisys keeps its files.
	Paths settings.Paths
	// Machine is the machine's merged config.
	Machine *config.Machine
	// Modules are every module of the sync, enabled or not, so a module
	// can ask the others (the shell module collects their environment).
	Modules []config.Module
	// RepoFS is the config repo's files, such as the files module's
	// sources and the shells' snippets. It may be nil in tests.
	RepoFS fs.FS
	// Previous returns the state file's record of the resource with the
	// given ID, as the last sync that applied it left it, and whether
	// there is one.
	Previous func(id string) (state.Record, bool)
	// Log receives notes for the run's log. It may be nil.
	Log io.Writer
	// Now returns the current time.
	Now func() time.Time

	sharedMu sync.Mutex
	shared   map[any]any
}

// Shared returns the value the resources of one sync share under key, such
// as the package manager or the download client, making it with create the
// first time it's asked for. A failed create isn't remembered.
func (rt *Runtime) Shared(key any, create func() (any, error)) (any, error) {
	rt.sharedMu.Lock()
	defer rt.sharedMu.Unlock()
	if v, ok := rt.shared[key]; ok {
		return v, nil
	}
	v, err := create()
	if err != nil {
		return nil, err
	}
	rt.SetShared(key, v)
	return v, nil
}

// SetShared sets the value shared under key, for callers that make it
// before the sync plans: Shared returns it from then on. The caller must
// not call it while resources may call Shared.
func (rt *Runtime) SetShared(key, value any) {
	if rt.shared == nil {
		rt.shared = map[any]any{}
	}
	rt.shared[key] = value
}

// Resource is one thing a module manages. A module's resources are planned
// and applied in the order it returns them.
type Resource interface {
	// ID is the resource's stable key in the state file, such as
	// "tool:kubectl". The text before the first colon is usually its kind.
	// No two resources of a sync may have the same ID.
	ID() string
	// Kind is the kind of resource, such as "tool" or "file". It's
	// recorded in the state file, and decides how the resource is removed
	// once it's taken out of the config (see Remover).
	Kind() string
	// Targets are the paths the resource owns, used for the ownership
	// check. They're recorded as the record's Paths unless Apply sets them.
	Targets() []string
	// Privileged reports whether applying the resource needs root on
	// Linux (sudo), or admin on Windows.
	Privileged() bool
	// Check compares the live system with what the config wants. It only
	// reads, never asks for anything, and is run in parallel with other
	// resources' checks. If it can't check without root while sudo needs
	// a password, it returns an error wrapping system.ErrNeedsPassword (as
	// the Runner does), or a change with Action Unknown, and the plan says
	// it's checked during apply.
	Check(ctx context.Context, rt *Runtime) (Change, error)
	// Apply makes the change Check found, or, if Check's change was
	// Unknown, checks again and makes whatever change is needed. It's
	// called on the same value Check was, so it may use what Check found,
	// such as a resolved version. It returns the record to keep in the
	// state file; the engine fills in its ID, Kind, Module, and Paths and
	// Updated if they're empty.
	Apply(ctx context.Context, rt *Runtime) (state.Record, error)
}

// Remover removes things of one kind once they're taken out of the config,
// following the removal rules. It works from the state file's record, so
// it doesn't need the module that created the thing. A kind without a
// Remover is never removed: the plan says it's no longer used, and its
// record stays.
type Remover interface {
	// Kind is the kind of resource the Remover removes.
	Kind() string
	// CheckRemove says what removing the thing rec records would do, as
	// Check does for a resource: Remove, Warn (for example "left in
	// place"), NoChange if there's nothing left to remove, or Orphan to
	// leave it and keep its record.
	CheckRemove(ctx context.Context, rt *Runtime, rec state.Record) (Change, error)
	// Remove removes the thing rec records. Its record is deleted once
	// Remove succeeds.
	Remove(ctx context.Context, rt *Runtime, rec state.Record) error
}

// RebootNeeder is implemented by resources whose changes can need a reboot
// before anything that depends on them works, such as installing WSL on
// Windows. Once such a resource is applied, the engine asks it: if it
// needs a reboot, its module's later changes and the modules that depend
// on it wait for the next sync, which the user runs after rebooting, while
// the rest of the sync carries on. Every change is safe to apply again, so
// that sync picks up where this one stopped.
type RebootNeeder interface {
	// RebootNeeded reports whether the change the last Apply (or
	// ApplyGroup) made needs a reboot, and why, in a few words, such as
	// "WSL was installed".
	RebootNeeded() (why string, needed bool)
}

// SyncStopper is implemented by errors that stop the whole sync, rather
// than fail only the module they happen in. A failed lookup of a "latest"
// version is one: issue #1 says the sync stops, naming the tool and the
// upstream.
type SyncStopper interface {
	StopsSync() bool
}

// StopsSync reports whether err, or an error it wraps, stops the whole
// sync.
func StopsSync(err error) bool {
	var s SyncStopper
	return errors.As(err, &s) && s.StopsSync()
}

// Removers holds the Remover for each kind of resource maisys removes,
// keyed by kind.
type Removers map[string]Remover

// NewRemovers returns Removers holding rs. It panics if two of them remove
// the same kind.
func NewRemovers(rs ...Remover) Removers {
	m := make(Removers, len(rs))
	for _, r := range rs {
		if _, ok := m[r.Kind()]; ok {
			panic(fmt.Sprintf("resource: two removers for kind %q", r.Kind()))
		}
		m[r.Kind()] = r
	}
	return m
}

// Describe returns how the plan names a resource by its ID when it has no
// summary: the ID with its first colon turned into a space, such as
// "repo hashicorp" for "repo:hashicorp".
func Describe(id string) string {
	kind, rest, ok := strings.Cut(id, ":")
	if !ok || kind == "" || rest == "" {
		return id
	}
	return kind + " " + rest
}
