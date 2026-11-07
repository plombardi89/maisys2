package pkg

import (
	"context"
	"errors"
	"os"
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
	// now if sudo works without a password, as the plan never asks for
	// one. Otherwise names the old lists don't have are checked when the
	// lists are refreshed, before installing.
	stale := m.listsStale()
	if stale {
		switch err := m.refresh(ctx); {
		case err == nil:
			stale = false
		case !errors.Is(err, system.ErrNeedsPassword):
			return nil, err
		}
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
// upgrades installed packages to the version the repos have.
func (apt) install(ctx context.Context, m *Manager, install, upgrade []string) error {
	names := append(append([]string(nil), install...), upgrade...)
	if len(names) == 0 {
		return nil
	}
	args := []string{"install", "-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
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
// "libc6:i386", is installed according to rows, and which version.
func dpkgInfo(rows []dpkgRow, name string) info {
	pkg, arch, _ := strings.Cut(name, ":")
	for _, r := range rows {
		if r.pkg == pkg && (arch == "" || r.arch == arch) && dpkgInstalled(r.status) {
			return info{installed: true, version: r.version, known: true}
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
	_, err := m.Run(ctx, system.Cmd{Name: "apt-get", Args: []string{"update"}, Env: noninteractive, Root: true, Label: "apt-get update"})
	if err != nil {
		if _, ok := errors.AsType[*lockError](err); ok {
			return err
		}
		if _, ok := errors.AsType[*system.ExitError](err); !ok {
			return err
		}
	}
	m.mu.Lock()
	m.sourcesChanged, m.refreshed = false, m.now()
	m.mu.Unlock()
	if err != nil {
		m.notify("couldn't refresh the package lists, so packages are installed from the lists there are: %v", err)
	}
	return nil
}
