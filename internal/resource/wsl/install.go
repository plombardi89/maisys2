package wsl

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/resource/version"
	"github.com/plombardi89/maisys2/internal/state"
)

// Kind is the kind of resource Install is.
const Kind = "wsl"

// DetailVersion is the key of the WSL version in an Install record's
// Details.
const DetailVersion = "version"

// shutdownNote is how the plan says a change takes effect once WSL
// restarts, which maisys leaves to you, so open sessions aren't closed.
const shutdownNote = "run `wsl --shutdown` to apply"

// Upstream is where the latest WSL release comes from: the GitHub releases
// wsl --update --web-download installs.
var Upstream = version.GitHub{Repo: "microsoft/WSL"}

// Install is WSL itself. Its check reports:
//
//   - "+ install WSL" if it isn't installed;
//   - "~ update WSL 2.5.10 → 2.6.1" if a newer release is out;
//
// and nothing otherwise. If the latest release can't be looked up, the
// plan says so, and the sync carries on: WSL keeps working without the
// update.
//
// Installing and updating need admin rights. Installing WSL for the first
// time usually turns on Windows features that only work once Windows
// restarts: then Apply returns a *RebootError, so the distros that need
// WSL aren't created until it has.
type Install struct {
	// Latest returns the latest WSL release, such as "2.6.1". Nil means
	// looking it up in Upstream, cached like tools' latest versions.
	Latest func(ctx context.Context, rt *resource.Runtime) (string, error)
}

var _ resource.Resource = (*Install)(nil)

// ID returns "wsl".
func (i *Install) ID() string { return Kind }

// Kind returns "wsl".
func (i *Install) Kind() string { return Kind }

// Targets returns nothing.
func (i *Install) Targets() []string { return nil }

// Privileged returns true: WSL's installer needs admin rights.
func (i *Install) Privileged() bool { return true }

// Check compares the WSL that's installed with the latest release.
func (i *Install) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	installed, latest, lookupErr, err := i.inspect(ctx, rt)
	if err != nil {
		return resource.Change{}, err
	}
	switch {
	case installed == "":
		return resource.Change{Action: resource.Create, Summary: "install WSL"}, nil
	case lookupErr != nil:
		return resource.Change{Notes: []string{fmt.Sprintf("couldn't check for a newer WSL: %v", lookupErr)}}, nil
	case newer(latest, installed):
		return resource.Change{
			Action:  resource.Update,
			Summary: fmt.Sprintf("update WSL %s → %s", short(installed), latest),
			Notes:   []string{"the WSL update takes effect once WSL restarts: " + shutdownNote},
		}, nil
	default:
		return resource.Change{}, nil
	}
}

// Apply installs WSL, or updates it.
func (i *Install) Apply(ctx context.Context, rt *resource.Runtime) (state.Record, error) {
	reg, err := RegistryFor(rt)
	if err != nil {
		return state.Record{}, err
	}
	installed, latest, _, err := i.inspect(ctx, rt)
	if err != nil {
		return state.Record{}, err
	}
	switch {
	case installed == "":
		if _, err := rt.Runner.Run(ctx, command("installing WSL", "--install", "--no-distribution")); err != nil {
			return state.Record{}, err
		}
		reboot, err := reg.RebootPending()
		if err != nil {
			return state.Record{}, err
		}
		if reboot {
			return state.Record{}, &RebootError{}
		}
		if installed, err = reg.Version(); err != nil {
			return state.Record{}, err
		}
	case latest != "" && newer(latest, installed):
		if _, err := rt.Runner.Run(ctx, command("updating WSL", "--update", "--web-download")); err != nil {
			return state.Record{}, err
		}
		installed = latest
	default:
	}
	return state.Record{Details: map[string]string{DetailVersion: installed}}, nil
}

// inspect returns the version of WSL that's installed, or "" if it isn't,
// and the latest release, or why it couldn't be looked up.
func (i *Install) inspect(ctx context.Context, rt *resource.Runtime) (installed, latest string, lookupErr, err error) {
	reg, err := RegistryFor(rt)
	if err != nil {
		return "", "", nil, err
	}
	if installed, err = reg.Version(); err != nil || installed == "" {
		return installed, "", nil, err
	}
	latest, lookupErr = i.latest(ctx, rt)
	return installed, latest, lookupErr, nil
}

func (i *Install) latest(ctx context.Context, rt *resource.Runtime) (string, error) {
	if i.Latest != nil {
		return i.Latest(ctx, rt)
	}
	env, err := tool.EnvFor(rt)
	if err != nil {
		return "", err
	}
	return env.Latest.Resolve(ctx, env.Client, "WSL", Upstream)
}

// newer reports whether version a is newer than b. Versions are numbers
// separated by dots, and missing numbers count as 0, so "2.6.1.0" is the
// same as "2.6.1". A part that isn't a number counts as 0 too.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for k := range max(len(pa), len(pb)) {
		na, nb := part(pa, k), part(pb, k)
		if na != nb {
			return na > nb
		}
	}
	return false
}

func part(parts []string, k int) int {
	if k >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[k])
	return n
}

// short drops the zeros at the end of a version, as WSL's releases are
// tagged: "2.6.1" for "2.6.1.0".
func short(v string) string {
	for strings.HasSuffix(v, ".0") && strings.Count(v, ".") > 2 {
		v = strings.TrimSuffix(v, ".0")
	}
	return v
}

// RebootError is the error installing WSL ends with when Windows must
// restart before WSL works, as it usually must the first time. It fails the
// wsl_host module, which stops it from creating distros WSL can't run yet.
//
// A sync that ends with one should stop as #1 says: save the state, say
// "reboot, then run maisys sync again", and exit with code 3. The engine
// will find it with errors.As, or through RebootRequired, which marks
// every error a restart fixes; until it does, the sync fails with this
// error's message, which says the same.
type RebootError struct{}

func (*RebootError) Error() string {
	return "WSL is installed, but Windows must restart before it works: restart, then run maisys sync again to finish"
}

// RebootRequired reports that a restart fixes the error.
func (*RebootError) RebootRequired() bool { return true }

// Remover handles the records of WSL installs once wsl_host is turned off:
// WSL stays installed, and the record is dropped without a line in the
// plan.
type Remover struct{}

var _ resource.Remover = Remover{}

// Kind returns "wsl".
func (Remover) Kind() string { return Kind }

// CheckRemove returns no change: WSL stays installed.
func (Remover) CheckRemove(context.Context, *resource.Runtime, state.Record) (resource.Change, error) {
	return resource.Change{}, nil
}

// Remove does nothing.
func (Remover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }
