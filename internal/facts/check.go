package facts

import (
	"errors"
	"fmt"
	"slices"

	"github.com/plombardi89/maisys2/internal/config"
)

// ErrRoot is the error CheckUser returns when maisys runs as root.
var ErrRoot = errors.New("run maisys as your normal user, not root; it uses sudo when it needs to")

// CheckUser returns ErrRoot if maisys is running as root: it runs as your
// user, and uses sudo only for the steps that need it.
func (s *System) CheckUser() error {
	if s.Root {
		return ErrRoot
	}
	return nil
}

// CheckMachine checks the machine m, which has passed validation, against
// the live system s before a sync changes anything: that maisys runs on s
// at all, that s has m's os, and its os_version and arch if it sets them,
// and, if m doesn't set arch, that every module it turns on supports s's
// architecture. modules are the modules of m's config repo (Repo.Modules).
// It returns every problem it finds, joined, each starting with where the
// value at fault was set.
func (s *System) CheckMachine(m *config.Machine, modules []config.Module) error {
	var errs []error
	errorf := func(at, format string, a ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", at, fmt.Sprintf(format, a...)))
	}
	where := func(key string) string {
		if at := m.Where(key); at != "" {
			return at
		}
		return config.MachineFile(m.Name)
	}

	wanted := release(m.OS, m.OSVersion)
	switch {
	case !slices.Contains(config.AllOS(), s.OS):
		errorf(where("os"), "machine %q is for %s, but it's running on %s: %s", m.Name, wanted, s.PrettyName, runsOn)
	case m.OS != s.OS:
		errorf(where("os"), "machine %q is for %s, but it's running on %s", m.Name, wanted, s.name())
	case m.OSVersion != "" && m.OSVersion != s.OSVersion:
		errorf(where("os_version"), "machine %q is for %s, but it's running on %s", m.Name, wanted, s.name())
	default:
	}

	switch {
	case s.Arch == "":
		errorf(where("arch"), "maisys runs on %s, but machine %q is running on %s", list(config.AllArch(), "and"), m.Name, s.RawArch)
	case m.Arch != "" && m.Arch != s.Arch:
		errorf(where("arch"), "machine %q is for %s, but it's running on %s", m.Name, m.Arch, s.Arch)
	case s.OS == config.Windows && s.Arch != config.AMD64:
		errorf(where("os"), "Windows hosts must be %s, but machine %q is running on %s", config.AMD64, m.Name, s.Arch)
	case m.Arch == "":
		// Validation has checked the modules against an arch the machine
		// sets, but without one only the live system can tell.
		for _, mod := range modules {
			info := mod.Info()
			cfg := m.Modules[info.Name]
			if cfg == nil || !cfg.IsEnabled() || len(info.Arch) == 0 || slices.Contains(info.Arch, s.Arch) {
				continue
			}
			errorf(m.EnabledAt(info.Name), "%s only works on %s, but machine %q is running on %s", info.Name, list(info.Arch, "or"), m.Name, s.Arch)
		}
	default:
	}
	return errors.Join(errs...)
}
