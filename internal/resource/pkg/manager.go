// Package pkg installs and upgrades OS packages with apt (Ubuntu) or dnf
// (Fedora). Each module that lists packages gets a package set (Set): a
// resource for each package, which installs it and keeps it at its latest
// version. Package names are checked against the enabled repos during the
// plan, and the installs of every module go into one apt or dnf
// transaction (see Package.ApplyGroup). maisys never removes packages.
//
// Every package resource of a sync shares one Manager (ManagerFor), which
// looks packages up once for all of them, waits while another program
// holds the package manager's lock, and refreshes apt's package lists
// before installing when they're out of date.
package pkg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

const (
	// DefaultLockTimeout is how long a Manager waits for another program,
	// such as Ubuntu's automatic updates or PackageKit, to let go of the
	// package manager's lock.
	DefaultLockTimeout = 5 * time.Minute
	// lockRetry is how often a Manager tries again while it waits.
	lockRetry = 5 * time.Second
	// listsMaxAge is how old apt's package lists may get before they're
	// refreshed.
	listsMaxAge = time.Hour
)

// Manager runs the machine's package manager: apt on Ubuntu, dnf on
// Fedora. It's safe for concurrent use.
type Manager struct {
	// Notify receives messages for the user, such as that maisys is
	// waiting for another program to finish with the package manager. It
	// may be nil.
	Notify io.Writer

	runner  system.Runner
	backend backend
	// root is where the file system starts: "/" except in tests.
	root  string
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	// lockTimeout and lockRetry are how long to wait for the lock, and
	// how often to try again meanwhile.
	lockTimeout, lockRetry time.Duration

	mu sync.Mutex
	// wanted holds the names package sets want, so they're looked up
	// together.
	wanted []string
	// first is the sync's first package, which the plan's note about
	// apt's lists goes on.
	first *Package
	// current holds, by module, the names checks found installed with no
	// upgrade to make, which are looked up again once apply refreshes
	// apt's lists (see missedUpgrades).
	current map[string][]string
	// sourcesChanged reports whether a package source changed since apt's
	// lists were last refreshed.
	sourcesChanged bool
	// refreshed is when this Manager last refreshed apt's lists.
	refreshed time.Time

	// lookupMu is held while packages are looked up, so that the
	// lookups of concurrent checks are made once, together.
	lookupMu sync.Mutex
	found    map[string]lookup
	upgrades *upgradeList
	// planStale reports whether the plan couldn't refresh apt's stale
	// lists, so it doesn't try again.
	planStale bool
	// relooked holds what looking names up again found since apply last
	// refreshed apt's lists. It's nil until apply refreshes them.
	relooked map[string]info
}

// lookup is what looking a package up found, or why it failed.
type lookup struct {
	info info
	err  error
}

// info is what the package manager says about a package.
type info struct {
	// installed reports whether it's installed, and version which
	// version.
	installed bool
	version   string
	// newer is the version installing it would upgrade it to, if any.
	newer string
	// held reports whether it's installed and held at its version (apt-mark
	// hold), so apt won't upgrade it.
	held bool
	// known reports whether the enabled repos have it, or it's
	// installed.
	known bool
	// noCandidate reports whether the repos know the name, but have
	// nothing to install for it, as for an apt virtual package.
	noCandidate bool
	// unchecked reports whether the repos couldn't be checked, because
	// apt's lists are stale or have never been downloaded, and the plan
	// couldn't refresh them.
	unchecked bool
}

// upgradeList is the available upgrades, once they're looked up.
type upgradeList struct {
	versions map[string]string // by name, and by name.arch
	err      error
}

// backend is apt or dnf.
type backend interface {
	// String names the package manager, such as "apt".
	String() string
	// query looks names up.
	query(ctx context.Context, m *Manager, names []string) (map[string]info, error)
	// install installs the packages in install, and upgrades those in
	// upgrade, which are installed.
	install(ctx context.Context, m *Manager, install, upgrade []string) error
}

// NewManager returns a Manager for the package manager of the system f,
// which runs commands with r.
func NewManager(r system.Runner, f *facts.System) (*Manager, error) {
	m := &Manager{
		runner:      r,
		root:        string(filepath.Separator),
		now:         time.Now,
		sleep:       sleep,
		lockTimeout: DefaultLockTimeout,
		lockRetry:   lockRetry,
		found:       map[string]lookup{},
		current:     map[string][]string{},
	}
	var osName config.OS
	if f != nil {
		osName = f.OS
	}
	switch osName {
	case config.Ubuntu:
		m.backend = apt{}
	case config.Fedora:
		m.backend = dnf{}
	default:
		return nil, errors.New("maisys only manages packages on Fedora and Ubuntu")
	}
	return m, nil
}

// ManagerFor returns the Manager the resources of rt's sync share, which
// tells the user what it's waiting for through rt.Log.
func ManagerFor(rt *resource.Runtime) (*Manager, error) {
	v, err := rt.Shared(managerKey{}, func() (any, error) {
		m, err := NewManager(rt.Runner, rt.Facts)
		if err != nil {
			return nil, err
		}
		m.Notify = rt.Log
		if rt.Now != nil {
			m.now = rt.Now
		}
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	m, _ := v.(*Manager)
	return m, nil
}

// managerKey is the key of the shared Manager.
type managerKey struct{}

// String names the package manager, "apt" or "dnf".
func (m *Manager) String() string {
	return m.backend.String()
}

// want notes the packages of a package set, so their names are looked up
// together.
func (m *Manager) want(pkgs []*Package) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range pkgs {
		if m.first == nil {
			m.first = p
		}
		if !slices.Contains(m.wanted, p.name) {
			m.wanted = append(m.wanted, p.name)
		}
	}
}

// wants reports whether a package set of this sync wants name.
func (m *Manager) wants(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Contains(m.wanted, name)
}

// isCurrent notes that p's check found it installed, with no upgrade to
// make.
func (m *Manager) isCurrent(p *Package) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !slices.Contains(m.current[p.module], p.name) {
		m.current[p.module] = append(m.current[p.module], p.name)
	}
}

// checkName checks that name is a package name maisys accepts. apt reads
// a "-" at the end of a name it doesn't know as "remove this package", so
// a typo such as "openssh-server-" would remove openssh-server: on Ubuntu,
// such names are rejected. A "+" at the end means "install", which is
// harmless, and real packages such as "g++" end with one.
func (m *Manager) checkName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("%q isn't a valid package name", name)
	}
	if _, ok := m.backend.(apt); ok && strings.HasSuffix(name, "-") {
		return fmt.Errorf("%q isn't a valid package name: a \"-\" at the end tells apt to remove %s", name, strings.TrimSuffix(name, "-"))
	}
	return nil
}

// lookup looks name up, together with every wanted name that hasn't been
// looked up yet. Each name is looked up once.
func (m *Manager) lookup(ctx context.Context, name string) (info, error) {
	if err := m.checkName(name); err != nil {
		return info{}, err
	}
	m.lookupMu.Lock()
	defer m.lookupMu.Unlock()
	if l, ok := m.found[name]; ok {
		return l.info, l.err
	}
	names := []string{name}
	m.mu.Lock()
	for _, n := range m.wanted {
		if _, ok := m.found[n]; !ok && n != name && m.checkName(n) == nil {
			names = append(names, n)
		}
	}
	m.mu.Unlock()

	infos, err := m.backend.query(ctx, m, names)
	for _, n := range names {
		m.found[n] = lookup{info: infos[n], err: err}
	}
	return infos[name], err
}

// Installed reports whether the package name is installed.
func (m *Manager) Installed(ctx context.Context, name string) (bool, error) {
	in, err := m.lookup(ctx, name)
	return in.installed, err
}

// SourcesChanged notes that the package sources changed, such as a repo
// added, so apt's package lists are refreshed before the next install.
func (m *Manager) SourcesChanged() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sourcesChanged = true
}

// Install installs packages, as root, without asking anything: names, or
// paths of package files. On Ubuntu, it refreshes the package lists first
// if they're out of date.
func (m *Manager) Install(ctx context.Context, names ...string) error {
	if err := m.refresh(ctx); err != nil {
		return err
	}
	return m.backend.install(ctx, m, names, nil)
}

// Run runs a command of the package manager, such as dpkg --add-architecture.
// If another program holds the package manager's lock, it waits for up to
// DefaultLockTimeout, trying again every few seconds, and says it's
// waiting.
func (m *Manager) Run(ctx context.Context, c system.Cmd) (system.Result, error) {
	var deadline time.Time
	for {
		res, err := m.runner.Run(ctx, c)
		if err == nil || !lockHeld(res) {
			return res, err
		}
		now := m.now()
		if deadline.IsZero() {
			deadline = now.Add(m.lockTimeout)
			m.notify("waiting for another program to finish with %s (%s)", m.backend, lockMessage(res))
		}
		if !now.Before(deadline) {
			return res, &lockError{tool: m.backend.String(), waited: m.lockTimeout, err: err}
		}
		if err := m.sleep(ctx, m.lockRetry); err != nil {
			return res, err
		}
	}
}

// notify tells the user something.
func (m *Manager) notify(format string, a ...any) {
	if m.Notify != nil {
		_, _ = fmt.Fprintf(m.Notify, "maisys: "+format+"\n", a...)
	}
}

// lockError means another program held the package manager's lock for as
// long as maisys waits.
type lockError struct {
	tool   string
	waited time.Duration
	err    error
}

func (e *lockError) Error() string {
	return fmt.Sprintf("another program kept %s busy for %s, so maisys stopped waiting: %v", e.tool, minutes(e.waited), e.err)
}

func (e *lockError) Unwrap() error { return e.err }

// minutes describes d, such as "5 minutes".
func minutes(d time.Duration) string {
	switch {
	case d == time.Minute:
		return "1 minute"
	case d%time.Minute == 0:
		return fmt.Sprintf("%d minutes", d/time.Minute)
	default:
		return d.String()
	}
}

// lockMessages are what apt, dpkg and dnf say when another program holds
// their lock.
var lockMessages = []string{
	"Could not get lock",
	"Unable to acquire the dpkg frontend lock",
	"Unable to lock the administration directory",
	"Unable to lock directory",
	"locked by another process",
	"Failed to obtain rpm transaction lock",
	"Failed to obtain the transaction lock",
	"Another transaction is in progress",
}

// lockHeld reports whether a command failed because another program held
// the package manager's lock.
func lockHeld(res system.Result) bool {
	out := string(res.Stderr) + string(res.Stdout)
	return slices.ContainsFunc(lockMessages, func(msg string) bool { return strings.Contains(out, msg) })
}

// lockMessage returns the line of a command's output that says the lock is
// held.
func lockMessage(res system.Result) string {
	for line := range strings.Lines(string(res.Stderr) + string(res.Stdout)) {
		line = strings.TrimSpace(line)
		if slices.ContainsFunc(lockMessages, func(msg string) bool { return strings.Contains(line, msg) }) {
			return line
		}
	}
	return "its lock is held"
}

// sleep waits for d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// path returns the path of name, an absolute slash-separated path, under
// the Manager's root.
func (m *Manager) path(name string) string {
	return filepath.Join(m.root, filepath.FromSlash(name))
}

// exists reports whether the file name exists under the Manager's root.
func (m *Manager) exists(name string) bool {
	_, err := os.Stat(m.path(name))
	return err == nil
}
