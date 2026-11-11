package pkg

import (
	"context"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/system"
)

// dnf is Fedora's package manager.
type dnf struct{}

func (dnf) String() string { return "dnf" }

// rpmFormat is how rpm -q describes each installed package. The version
// has the epoch, if any, as dnf check-update shows it.
const rpmFormat = `%{NAME}\t%{ARCH}\t%|EPOCH?{%{EPOCH}:}:{}|%{VERSION}-%{RELEASE}\n`

// rpmArches are the architectures a package name can end with, as in
// "glibc.i686".
var rpmArches = []string{"x86_64", "aarch64", "i686", "i386", "noarch"}

// query looks names up with rpm -q, for what's installed; dnf repoquery,
// for whether the repos have the rest; and dnf check-update, for the
// upgrades the repos have.
func (dnf) query(ctx context.Context, m *Manager, names []string) (map[string]info, error) {
	// rpm -q's exit code is how many names aren't installed.
	notFound := make([]int, 0, min(len(names), 255))
	for i := range cap(notFound) {
		notFound = append(notFound, i+1)
	}
	res, err := m.runner.Run(ctx, system.Cmd{
		Name:      "rpm",
		Args:      append([]string{"-q", "--qf", rpmFormat}, names...),
		AllowExit: notFound,
		Label:     "rpm -q",
	})
	if err != nil {
		return nil, err
	}
	rows := parseRPMQuery(res.Stdout)
	infos := make(map[string]info, len(names))
	var missing, installed []string
	for _, name := range names {
		in := rpmInfo(rows, name)
		infos[name] = in
		if in.installed {
			installed = append(installed, name)
		} else {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		res, err := m.runner.Run(ctx, system.Cmd{
			Name:  "dnf",
			Args:  append([]string{"-q", "repoquery", "--qf", `%{name}\n`}, missing...),
			Label: "dnf repoquery",
		})
		if err != nil {
			return nil, err
		}
		// dnf 4 doesn't turn \n into a new line.
		have := strings.Fields(strings.ReplaceAll(string(res.Stdout), `\n`, "\n"))
		for _, name := range missing {
			base, _ := splitRPMArch(name)
			in := infos[name]
			in.known = slices.Contains(have, base)
			infos[name] = in
		}
	}

	if len(installed) > 0 {
		upgrades, err := m.upgradeList(ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range installed {
			in := infos[name]
			in.newer = upgrades[name]
			infos[name] = in
		}
	}
	return infos, nil
}

// upgradeList returns the upgrades dnf check-update lists, by package name
// and by name.arch, looking them up the first time. m.lookupMu must be
// held. dnf leaves out the upgrades that dnf versionlock or an exclude
// forbids, as dnf upgrade would, so a locked package is never planned as
// an upgrade.
func (m *Manager) upgradeList(ctx context.Context) (map[string]string, error) {
	if m.upgrades == nil {
		res, err := m.runner.Run(ctx, system.Cmd{
			Name:      "dnf",
			Args:      []string{"-q", "check-update"},
			AllowExit: []int{100}, // there are upgrades
			Label:     "dnf check-update",
		})
		m.upgrades = &upgradeList{err: err}
		if err == nil {
			m.upgrades.versions = parseCheckUpdate(res.Stdout)
		}
	}
	return m.upgrades.versions, m.upgrades.err
}

// install installs packages with dnf install, and upgrades others with dnf
// upgrade: dnf install leaves installed packages as they are.
func (dnf) install(ctx context.Context, m *Manager, install, upgrade []string) error {
	if len(install) > 0 {
		if _, err := m.Run(ctx, system.Cmd{Name: "dnf", Args: append([]string{"install", "-y"}, install...), Root: true, Label: "dnf install"}); err != nil {
			return err
		}
	}
	if len(upgrade) > 0 {
		if _, err := m.Run(ctx, system.Cmd{Name: "dnf", Args: append([]string{"upgrade", "-y"}, upgrade...), Root: true, Label: "dnf upgrade"}); err != nil {
			return err
		}
	}
	return nil
}

// rpmRow is an installed package rpm -q describes.
type rpmRow struct {
	name, arch, version string
}

// parseRPMQuery reads what rpm -q --qf rpmFormat writes, leaving out the
// names that aren't installed.
func parseRPMQuery(out []byte) []rpmRow {
	var rows []rpmRow
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimRight(line, "\r\n"), "\t")
		if len(f) == 3 {
			rows = append(rows, rpmRow{name: f[0], arch: f[1], version: f[2]})
		}
	}
	return rows
}

// rpmInfo returns whether name, perhaps with an architecture such as
// "glibc.i686", is installed according to rows, and which version.
func rpmInfo(rows []rpmRow, name string) info {
	base, arch := splitRPMArch(name)
	for _, r := range rows {
		if r.name == base && (arch == "" || r.arch == arch) {
			return info{installed: true, version: r.version, known: true}
		}
	}
	return info{}
}

// splitRPMArch splits a package name such as "glibc.i686" into the name and
// the architecture, if it has one.
func splitRPMArch(name string) (base, arch string) {
	i := strings.LastIndexByte(name, '.')
	if i > 0 && slices.Contains(rpmArches, name[i+1:]) {
		return name[:i], name[i+1:]
	}
	return name, ""
}

// parseCheckUpdate reads the upgrades dnf -q check-update lists, as lines
// of name.arch, version and repo, and returns their versions by name and by
// name.arch. Obsoleted packages, listed after them, are left out.
func parseCheckUpdate(out []byte) map[string]string {
	versions := map[string]string{}
	for line := range strings.Lines(string(out)) {
		if strings.HasPrefix(line, "Obsoleting") {
			break
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		name, arch := splitRPMArch(f[0])
		if arch == "" {
			continue
		}
		versions[f[0]] = f[1]
		if _, ok := versions[name]; !ok {
			versions[name] = f[1]
		}
	}
	return versions
}
