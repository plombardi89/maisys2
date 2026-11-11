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
// for whether the repos have the rest, and what provides those they don't
// have; and dnf check-update, for the upgrades the repos have.
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
		var unknown []string
		for _, name := range missing {
			base, _ := splitRPMArch(name)
			in := infos[name]
			in.known = slices.Contains(have, base)
			infos[name] = in
			if !in.known {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			providers := whatProvides(ctx, m, unknown)
			for _, name := range unknown {
				in := infos[name]
				in.providers = providers[name]
				infos[name] = in
			}
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

// whatProvides returns, for each of names that the enabled repos don't
// have as a package, the packages they have that provide it, with names'
// architecture if it has one: dnf install accepts such a name, but rpm -q
// never finds it installed, so it would be installed on every sync. It
// looks them all up with one dnf repoquery, printing each provider's name
// and then its provides, a line each, with a blank line after them. If
// that fails, it returns nothing: the providers only make an error
// clearer.
func whatProvides(ctx context.Context, m *Manager, names []string) map[string][]string {
	var bases []string
	for _, name := range names {
		if base, _ := splitRPMArch(name); !slices.Contains(bases, base) {
			bases = append(bases, base)
		}
	}
	res, err := m.runner.Run(ctx, system.Cmd{
		Name:  "dnf",
		Args:  []string{"-q", "repoquery", "--whatprovides", strings.Join(bases, ","), "--qf", `%{name}\n%{provides}\n`},
		Label: "dnf repoquery --whatprovides",
	})
	if err != nil {
		return nil
	}
	byBase := map[string][]string{}
	out := strings.ReplaceAll(string(res.Stdout), `\n`, "\n")
	for block := range strings.SplitSeq(out, "\n\n") {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		provider := strings.TrimSpace(lines[0])
		if provider == "" {
			continue
		}
		for _, line := range lines[1:] {
			f := strings.Fields(line)
			if len(f) > 0 && slices.Contains(bases, f[0]) && !slices.Contains(byBase[f[0]], provider) {
				byBase[f[0]] = append(byBase[f[0]], provider)
			}
		}
	}
	providers := map[string][]string{}
	for _, name := range names {
		base, arch := splitRPMArch(name)
		for _, p := range byBase[base] {
			if arch != "" {
				p += "." + arch
			}
			providers[name] = append(providers[name], p)
		}
	}
	return providers
}

// install installs and upgrades packages in one dnf transaction: with dnf
// install for packages to install, which leaves installed packages as they
// are; dnf upgrade for packages to upgrade; and, if there are both, dnf do,
// which takes several actions at once (dnf 5.2.14 and later, which every
// supported Fedora has). Package names never start with "-", so none is
// read as another action.
func (dnf) install(ctx context.Context, m *Manager, install, upgrade []string) error {
	var args []string
	label := "dnf do"
	switch {
	case len(install) > 0 && len(upgrade) > 0:
		args = slices.Concat([]string{"do", "-y", "--action=install"}, install, []string{"--action=upgrade"}, upgrade)
	case len(install) > 0:
		args, label = append([]string{"install", "-y"}, install...), "dnf install"
	case len(upgrade) > 0:
		args, label = append([]string{"upgrade", "-y"}, upgrade...), "dnf upgrade"
	default:
		return nil
	}
	_, err := m.Run(ctx, system.Cmd{Name: "dnf", Args: args, Root: true, Label: label})
	return err
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
