package pkg

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/plombardi89/maisys2/internal/system"
)

// apt is Ubuntu's package manager.
type apt struct{}

func (apt) String() string { return "apt" }

const (
	// listsDir holds apt's package lists.
	listsDir = "/var/lib/apt/lists"
	// updateStamp is touched after every successful apt-get update, on
	// systems with update-notifier-common.
	updateStamp = "/var/lib/apt/periodic/update-success-stamp"
	// dpkgFormat is how dpkg-query describes each package.
	dpkgFormat = `${Package}\t${Architecture}\t${Status}\t${Version}\n`
)

// noninteractive keeps apt and dpkg from asking anything.
var noninteractive = []string{"DEBIAN_FRONTEND=noninteractive"}

// query looks names up with dpkg-query, for what's installed, and
// apt-cache policy, for what the repos have.
func (apt) query(ctx context.Context, m *Manager, names []string) (map[string]info, error) {
	res, err := m.runner.Run(ctx, system.Cmd{
		Name:      "dpkg-query",
		Args:      append([]string{"-W", "-f", dpkgFormat}, names...),
		AllowExit: []int{1}, // some names aren't installed
		Label:     "dpkg-query",
	})
	if err != nil {
		return nil, err
	}
	rows := parseDpkgQuery(res.Stdout)
	infos := make(map[string]info, len(names))
	for _, name := range names {
		infos[name] = dpkgInfo(rows, name)
	}
	// Names are checked against current lists: stale ones are refreshed
	// now if sudo works without a password and no other program is using
	// apt, as the plan never asks for a password or waits. Otherwise names
	// the old lists don't have are checked when the lists are refreshed,
	// before installing.
	stale := m.listsStale()
	if stale {
		fresh, err := m.refreshForPlan(ctx)
		if err != nil {
			return nil, err
		}
		stale = !fresh
	}
	if !m.listsDownloaded() {
		for name, in := range infos {
			in.known, in.unchecked = in.installed, !in.installed
			infos[name] = in
		}
		return infos, nil
	}

	res, err = m.runner.Run(ctx, system.Cmd{Name: "apt-cache", Args: append([]string{"policy"}, names...), Label: "apt-cache policy"})
	if err != nil {
		return nil, err
	}
	candidates := parsePolicy(res.Stdout)
	for _, name := range names {
		in := infos[name]
		candidate, listed := candidates[name]
		switch {
		case !listed:
			in.known, in.unchecked = in.installed, !in.installed && stale
		case candidate == "" || candidate == "(none)":
			in.known, in.noCandidate = in.installed, !in.installed
			in.unchecked = !in.installed && stale
		default:
			in.known = true
			if in.installed && candidate != in.version {
				in.newer = candidate
			}
		}
		infos[name] = in
	}
	return infos, nil
}

// install installs and upgrades packages with one apt-get install, which
// upgrades installed packages to the version the repos have. With
// --no-remove, apt fails rather than remove any package, as dnf does
// without --allowerasing: maisys never removes packages.
func (apt) install(ctx context.Context, m *Manager, install, upgrade []string) error {
	names := append(append([]string(nil), install...), upgrade...)
	if len(names) == 0 {
		return nil
	}
	args := []string{"install", "-y", "--no-remove", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	_, err := m.Run(ctx, system.Cmd{
		Name:  "apt-get",
		Args:  append(args, names...),
		Env:   noninteractive,
		Root:  true,
		Label: "apt-get install",
	})
	return err
}

// dpkgRow is a package dpkg-query describes.
type dpkgRow struct {
	pkg, arch, status, version string
}

// parseDpkgQuery reads what dpkg-query -W -f dpkgFormat writes.
func parseDpkgQuery(out []byte) []dpkgRow {
	var rows []dpkgRow
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(f) == 4 {
			rows = append(rows, dpkgRow{pkg: f[0], arch: f[1], status: f[2], version: f[3]})
		}
	}
	return rows
}

// dpkgInfo returns whether name, perhaps with an architecture such as
// "libc6:i386", is installed according to rows, which version, and whether
// it's held.
func dpkgInfo(rows []dpkgRow, name string) info {
	pkg, arch, _ := strings.Cut(name, ":")
	for _, r := range rows {
		if r.pkg == pkg && (arch == "" || r.arch == arch) && dpkgInstalled(r.status) {
			// The status starts with the package's selection, which
			// apt-mark hold sets to "hold".
			held := strings.HasPrefix(r.status, "hold ")
			return info{installed: true, version: r.version, known: true, held: held}
		}
	}
	return info{}
}

// dpkgInstalled reports whether a package with status (such as "install ok
// installed") is installed.
func dpkgInstalled(status string) bool {
	f := strings.Fields(status)
	if len(f) == 0 {
		return false
	}
	switch f[len(f)-1] {
	case "installed", "triggers-awaiting", "triggers-pending":
		return true
	default:
		return false
	}
}

// parsePolicy reads what apt-cache policy writes about packages, and
// returns each package's candidate version, "(none)" if it has none.
func parsePolicy(out []byte) map[string]string {
	candidates := map[string]string{}
	var current string
	for line := range strings.Lines(string(out)) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
		case !strings.HasPrefix(line, " ") && strings.HasSuffix(line, ":"):
			current = strings.TrimSuffix(line, ":")
			candidates[current] = ""
		case current != "":
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:"); ok {
				candidates[current] = strings.TrimSpace(v)
			}
		}
	}
	return candidates
}

// listsDownloaded reports whether apt's package lists have been
// downloaded at all. Fresh container images don't have them.
func (m *Manager) listsDownloaded() bool {
	entries, err := os.ReadDir(m.path(listsDir))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "_Packages") {
			return true
		}
	}
	return false
}

// listsUpdated returns when apt's package lists were last refreshed, as
// best it can tell, or the zero time if it can't.
func (m *Manager) listsUpdated() time.Time {
	var newest time.Time
	for _, name := range []string{updateStamp, listsDir, listsDir + "/partial"} {
		if info, err := os.Stat(m.path(name)); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// listsStale reports whether apt's package lists need refreshing before
// packages are installed: because the package sources changed, they've
// never been downloaded, or they're more than an hour old.
func (m *Manager) listsStale() bool {
	m.mu.Lock()
	changed, refreshed := m.sourcesChanged, m.refreshed
	m.mu.Unlock()
	switch {
	case changed:
		return true
	case !refreshed.IsZero():
		return m.now().Sub(refreshed) > listsMaxAge
	case !m.listsDownloaded():
		return true
	}
	return m.now().Sub(m.listsUpdated()) > listsMaxAge
}

// refresh refreshes apt's package lists if they're stale. If apt-get update
// fails, it says so and carries on: apt can still install from the lists
// it has, and one broken repo doesn't stop the others from being
// refreshed. On Fedora, it does nothing: dnf refreshes its metadata itself.
func (m *Manager) refresh(ctx context.Context) error {
	if _, ok := m.backend.(apt); !ok || !m.listsStale() {
		return nil
	}
	_, err := m.Run(ctx, aptUpdate())
	if err != nil {
		if _, ok := errors.AsType[*lockError](err); ok {
			return err
		}
		if _, ok := errors.AsType[*system.ExitError](err); !ok {
			return err
		}
	}
	m.listsRefreshed()
	m.lookupMu.Lock()
	m.relooked = map[string]info{}
	m.lookupMu.Unlock()
	if err != nil {
		m.notify("couldn't refresh the package lists, so packages are installed from the lists there are: %v", err)
	}
	return nil
}

// missedUpgrades returns the upgrades the plan may have missed for the
// packages of modules that their checks found up to date, leaving out
// those in skip: if apply has refreshed apt's lists, they're looked up
// again, once each, and those with a newer version that aren't held are
// returned. Otherwise it returns nothing.
func (m *Manager) missedUpgrades(ctx context.Context, modules, skip []string) ([]string, error) {
	var names []string
	m.mu.Lock()
	for _, name := range m.wanted {
		if !slices.Contains(skip, name) && slices.ContainsFunc(modules, func(module string) bool { return slices.Contains(m.current[module], name) }) {
			names = append(names, name)
		}
	}
	m.mu.Unlock()

	m.lookupMu.Lock()
	defer m.lookupMu.Unlock()
	if m.relooked == nil || len(names) == 0 {
		return nil, nil
	}
	var unknown []string
	for _, name := range names {
		if _, ok := m.relooked[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		infos, err := m.backend.query(ctx, m, unknown)
		if err != nil {
			return nil, err
		}
		for _, name := range unknown {
			m.relooked[name] = infos[name]
		}
	}
	var more []string
	for _, name := range names {
		if in := m.relooked[name]; in.installed && in.newer != "" && !in.held {
			more = append(more, name)
		}
	}
	return more, nil
}

// listsNote returns the note the plan shows on the sync's first package, p,
// if apt's lists were stale and the plan couldn't refresh them, and ""
// otherwise.
func (m *Manager) listsNote(p *Package) string {
	m.mu.Lock()
	first := m.first == p
	m.mu.Unlock()
	m.lookupMu.Lock()
	stale := m.planStale
	m.lookupMu.Unlock()
	switch {
	case !first || !stale:
		return ""
	case !m.listsDownloaded():
		return "apt's package lists haven't been downloaded, and the plan couldn't download them (packages are checked when they're downloaded during apply)"
	default:
		return "apt's package lists are more than an hour old, and the plan couldn't refresh them (upgrades are checked when they're refreshed during apply)"
	}
}

// refreshForPlan refreshes apt's stale package lists for the plan, and
// reports whether it did. Unlike refresh, it never waits for apt's lock:
// if sudo needs a password, another program is using apt, or apt-get
// update fails, the lists stay as they are until refresh tries again
// before installing, and the plan doesn't try again. It says why in the
// log, unless sudo needs a password. It only fails if ctx is done.
// m.lookupMu must be held.
func (m *Manager) refreshForPlan(ctx context.Context) (bool, error) {
	if m.planStale {
		return false, nil
	}
	res, err := m.runner.Run(ctx, aptUpdate())
	switch {
	case err == nil:
		m.listsRefreshed()
		return true, nil
	case ctx.Err() != nil:
		return false, err
	case errors.Is(err, system.ErrNeedsPassword):
	case lockHeld(res):
		m.notify("another program is using apt (%s), so the plan didn't refresh the package lists: packages they don't have are checked before installing", lockMessage(res))
	default:
		m.notify("couldn't refresh the package lists for the plan, so packages they don't have are checked before installing: %v", err)
	}
	m.planStale = true
	return false, nil
}

// aptUpdate is apt-get update, which refreshes apt's package lists.
func aptUpdate() system.Cmd {
	return system.Cmd{Name: "apt-get", Args: []string{"update"}, Env: noninteractive, Root: true, Label: "apt-get update"}
}

// listsRefreshed notes that apt's package lists were just refreshed.
func (m *Manager) listsRefreshed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sourcesChanged, m.refreshed = false, m.now()
}
