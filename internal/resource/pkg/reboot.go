package pkg

import (
	"context"
	"fmt"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/system"
)

// rebootRequired is the file Ubuntu creates when an upgrade needs a
// reboot.
const rebootRequired = "/var/run/reboot-required"

// RebootRecommended reports whether the system f recommends a reboot to
// finish installing package upgrades: on Ubuntu, when
// /var/run/reboot-required exists; on Fedora, when dnf needs-restarting -r
// says so. Elsewhere it reports false. It runs commands with r, never as
// root.
func RebootRecommended(ctx context.Context, r system.Runner, f *facts.System) (bool, error) {
	if f == nil || (f.OS != config.Ubuntu && f.OS != config.Fedora) {
		return false, nil
	}
	m, err := NewManager(r, f)
	if err != nil {
		return false, err
	}
	return m.rebootRecommended(ctx)
}

func (m *Manager) rebootRecommended(ctx context.Context) (bool, error) {
	if _, ok := m.backend.(apt); ok {
		return m.exists(rebootRequired), nil
	}
	res, err := m.runner.Run(ctx, system.Cmd{
		Name:      "dnf",
		Args:      []string{"needs-restarting", "-r"},
		AllowExit: []int{1}, // a reboot is recommended
		Label:     "dnf needs-restarting",
	})
	if err != nil {
		return false, err
	}
	if res.ExitCode == 1 {
		// dnf 4 without the plugin exits with 1 too.
		if out := string(res.Stdout) + string(res.Stderr); strings.Contains(out, "No such command") {
			return false, fmt.Errorf("dnf needs-restarting isn't installed: %s", strings.TrimSpace(out))
		}
		return true, nil
	}
	return false, nil
}
