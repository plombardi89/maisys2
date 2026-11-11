package pkg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// testNow is the time tests run at.
var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

// testSystem is a machine for tests: a runtime on a fake system of os, with
// its Manager, whose file system is a temporary folder and whose clock
// moves only when it sleeps.
type testSystem struct {
	t      *testing.T
	rt     *resource.Runtime
	runner *systemtest.FakeRunner
	m      *Manager
	now    time.Time
	slept  time.Duration
	notes  strings.Builder
}

func newTestSystem(t *testing.T, os config.OS) *testSystem {
	t.Helper()
	s := &testSystem{t: t, runner: systemtest.NewFakeRunner(t), now: testNow}
	s.rt = &resource.Runtime{
		Runner: s.runner,
		Facts:  &facts.System{OS: os, OSID: string(os), Arch: config.AMD64, RawArch: "amd64"},
		Now:    func() time.Time { return s.now },
	}
	m, err := ManagerFor(s.rt)
	if err != nil {
		t.Fatal(err)
	}
	m.root = t.TempDir()
	m.Notify = &s.notes
	m.sleep = func(_ context.Context, d time.Duration) error {
		s.now = s.now.Add(d)
		s.slept += d
		return nil
	}
	s.m = m
	return s
}

// ubuntu returns a test system for Ubuntu whose package lists were
// refreshed 10 minutes ago.
func ubuntu(t *testing.T) *testSystem {
	t.Helper()
	s := newTestSystem(t, config.Ubuntu)
	s.lists(10 * time.Minute)
	return s
}

// fedora returns a test system for Fedora.
func fedora(t *testing.T) *testSystem {
	t.Helper()
	return newTestSystem(t, config.Fedora)
}

// lists makes apt's package lists age old.
func (s *testSystem) lists(age time.Duration) {
	s.t.Helper()
	dir := s.m.path(listsDir)
	s.write(listsDir+"/archive.ubuntu.com_ubuntu_dists_noble_main_binary-amd64_Packages", "Package: jq\n")
	s.touch(dir, age)
}

// write writes a file under the system's root.
func (s *testSystem) write(name, data string) {
	s.t.Helper()
	path := s.m.path(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// touch sets the modification time of path to age before now.
func (s *testSystem) touch(path string, age time.Duration) {
	s.t.Helper()
	when := s.now.Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		s.t.Fatal(err)
	}
}

// on answers command prefix with h.
func (s *testSystem) on(prefix string, h systemtest.Handler) {
	s.runner.On(prefix, h)
}

// result returns a handler for a command that writes stdout and stderr and
// exits with code.
func result(code int, stdout, stderr string) systemtest.Handler {
	return func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{ExitCode: code, Stdout: []byte(stdout), Stderr: []byte(stderr)}, nil
	}
}

// commands returns the command lines that ran, leaving out those that
// start with any of skip.
func (s *testSystem) commands(skip ...string) []string {
	var cmds []string
	for _, c := range s.runner.Commands() {
		if !slices.ContainsFunc(skip, func(p string) bool { return strings.HasPrefix(c, p) }) {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// check checks every resource, in order, and returns their changes and
// errors.
func (s *testSystem) check(rs []resource.Resource) ([]resource.Change, []error) {
	s.t.Helper()
	changes := make([]resource.Change, len(rs))
	errs := make([]error, len(rs))
	for i, r := range rs {
		changes[i], errs[i] = r.Check(s.t.Context(), s.rt)
	}
	return changes, errs
}

// Realistic outputs of Ubuntu 24.04's dpkg-query and apt-cache, for jq (an
// upgrade), tmux (up to date), htop (removed, its config files left),
// netcat (a virtual package) and jqq (a typo).
const (
	ubuntuDpkgQuery = "jq\tamd64\tinstall ok installed\t1.7.1-3build1\n" +
		"tmux\tamd64\tinstall ok installed\t3.4-1build1\n" +
		"htop\tamd64\tdeinstall ok config-files\t3.3.0-4build1\n"
	ubuntuDpkgQueryErr = "dpkg-query: no packages found matching netcat\n" +
		"dpkg-query: no packages found matching jqq\n"
	ubuntuPolicy = `jq:
  Installed: 1.7.1-3build1
  Candidate: 1.7.1-3ubuntu0.24.04.1
  Version table:
     1.7.1-3ubuntu0.24.04.1 500
        500 http://archive.ubuntu.com/ubuntu noble-updates/main amd64 Packages
        500 http://security.ubuntu.com/ubuntu noble-security/main amd64 Packages
 *** 1.7.1-3build1 500
        500 http://archive.ubuntu.com/ubuntu noble/main amd64 Packages
        100 /var/lib/dpkg/status
tmux:
  Installed: 3.4-1build1
  Candidate: 3.4-1build1
  Version table:
 *** 3.4-1build1 500
        500 http://archive.ubuntu.com/ubuntu noble/main amd64 Packages
        100 /var/lib/dpkg/status
htop:
  Installed: (none)
  Candidate: 3.3.0-4build1
  Version table:
     3.3.0-4build1 500
        500 http://archive.ubuntu.com/ubuntu noble/main amd64 Packages
        100 /var/lib/dpkg/status
netcat:
  Installed: (none)
  Candidate: (none)
  Version table:
`
	ubuntuPolicyErr = "N: Unable to locate package jqq\n"
)

// Realistic outputs of Fedora 44's rpm and dnf, for jq (up to date), tmux
// (an upgrade), htop (not installed) and jqq (a typo).
const (
	fedoraRPM = "jq\tx86_64\t1.7.1-11.fc44\n" +
		"tmux\tx86_64\t3.5a-3.fc44\n" +
		"package htop is not installed\n" +
		"package jqq is not installed\n"
	fedoraRepoquery   = "htop\n"
	fedoraCheckUpdate = `
kernel-core.x86_64                 6.17.4-300.fc44                 updates
tmux.x86_64                        3.5a-4.fc44                     updates
vim-enhanced.x86_64                2:9.1.1623-1.fc44               updates
Obsoleting Packages
grubby.x86_64                      8.40-80.fc44                    updates
    jq.x86_64                      1.7.1-11.fc44                   @fedora
`
)

func TestCheckUbuntu(t *testing.T) {
	s := ubuntu(t)
	s.on("dpkg-query -W", result(1, ubuntuDpkgQuery, ubuntuDpkgQueryErr))
	s.on("apt-cache policy", result(0, ubuntuPolicy, ubuntuPolicyErr))

	rs := Set(s.rt, "packages", []string{"jq", "tmux", "htop", "netcat", "jqq", "jq"})
	changes, errs := s.check(rs)
	want := []resource.Change{
		{Action: resource.Update, Summary: "upgrade jq 1.7.1-3build1 → 1.7.1-3ubuntu0.24.04.1"},
		{},
		{Action: resource.Create, Summary: "install htop"},
		{},
		{},
	}
	wantErrs := []string{
		"",
		"",
		"",
		"unknown package netcat: the enabled repos have nothing to install for it (is it a virtual package? List the package that provides it instead)",
		"unknown package jqq: the enabled repos don't have it",
	}
	wantResults(t, rs, changes, errs, want, wantErrs)

	wantCmds := []string{
		`dpkg-query -W -f '${Package}\t${Architecture}\t${Status}\t${Version}\n' jq tmux htop netcat jqq`,
		"apt-cache policy jq tmux htop netcat jqq",
	}
	if got := s.commands(); !slices.Equal(got, wantCmds) {
		t.Errorf("commands =\n%q\nwant one lookup for every package:\n%q", got, wantCmds)
	}
	for _, c := range s.runner.Calls() {
		if c.Cmd.Root {
			t.Errorf("%s ran as root: checks never need root", c.Cmd)
		}
	}
}

// wantResults compares the checks of rs with what's wanted.
func wantResults(t *testing.T, rs []resource.Resource, changes []resource.Change, errs []error, want []resource.Change, wantErrs []string) {
	t.Helper()
	if len(rs) != len(want) {
		t.Fatalf("%d resources, want %d", len(rs), len(want))
	}
	for i, r := range rs {
		var got string
		if errs[i] != nil {
			got = errs[i].Error()
		}
		if got != wantErrs[i] {
			t.Errorf("%s: error = %q, want %q", r.ID(), got, wantErrs[i])
		}
		if changes[i].Action != want[i].Action || changes[i].Summary != want[i].Summary {
			t.Errorf("%s: change = %+v, want %+v", r.ID(), changes[i], want[i])
		}
	}
}

func TestCheckFedora(t *testing.T) {
	s := fedora(t)
	s.on("rpm -q", result(2, fedoraRPM, ""))
	s.on("dnf -q repoquery", result(0, fedoraRepoquery, ""))
	s.on("dnf -q check-update", result(100, fedoraCheckUpdate, ""))

	rs := Set(s.rt, "packages", []string{"jq", "tmux", "htop", "jqq"})
	changes, errs := s.check(rs)
	want := []resource.Change{
		{},
		{Action: resource.Update, Summary: "upgrade tmux 3.5a-3.fc44 → 3.5a-4.fc44"},
		{Action: resource.Create, Summary: "install htop"},
		{},
	}
	wantErrs := []string{"", "", "", "unknown package jqq: the enabled repos don't have it"}
	wantResults(t, rs, changes, errs, want, wantErrs)

	wantCmds := []string{
		`rpm -q --qf '%{NAME}\t%{ARCH}\t%|EPOCH?{%{EPOCH}:}:{}|%{VERSION}-%{RELEASE}\n' jq tmux htop jqq`,
		`dnf -q repoquery --qf '%{name}\n' htop jqq`,
		"dnf -q check-update",
	}
	if got := s.commands(); !slices.Equal(got, wantCmds) {
		t.Errorf("commands =\n%q\nwant\n%q", got, wantCmds)
	}
}

func TestCheckUpToDate(t *testing.T) {
	s := fedora(t)
	s.on("rpm -q", result(0, "jq\tx86_64\t1.7.1-11.fc44\n", ""))
	s.on("dnf -q check-update", result(0, "", ""))

	rs := Set(s.rt, "packages", []string{"jq"})
	changes, errs := s.check(rs)
	wantResults(t, rs, changes, errs, []resource.Change{{}}, []string{""})
	if got := s.commands(); len(got) != 2 {
		t.Errorf("commands = %q, want rpm -q and dnf check-update only", got)
	}
}

func TestCheckArchitectures(t *testing.T) {
	t.Run("ubuntu", func(t *testing.T) {
		s := ubuntu(t)
		s.on("dpkg-query -W", result(0, "libc6\tamd64\tinstall ok installed\t2.39-0ubuntu8\nlibc6\ti386\tinstall ok installed\t2.39-0ubuntu8\n", ""))
		s.on("apt-cache policy", result(0, "libc6:i386:\n  Installed: 2.39-0ubuntu8\n  Candidate: 2.39-0ubuntu8.4\n", ""))
		rs := Set(s.rt, "steam", []string{"libc6:i386"})
		changes, errs := s.check(rs)
		wantResults(t, rs, changes, errs, []resource.Change{{Action: resource.Update, Summary: "upgrade libc6:i386 2.39-0ubuntu8 → 2.39-0ubuntu8.4"}}, []string{""})
	})
	t.Run("fedora", func(t *testing.T) {
		s := fedora(t)
		s.on("rpm -q", result(0, "glibc\tx86_64\t2.42-4.fc44\nglibc\ti686\t2.42-4.fc44\n", ""))
		s.on("dnf -q check-update", result(100, "glibc.i686    2.42-5.fc44    updates\n", ""))
		rs := Set(s.rt, "steam", []string{"glibc", "glibc.i686"})
		changes, errs := s.check(rs)
		want := []resource.Change{
			{Action: resource.Update, Summary: "upgrade glibc 2.42-4.fc44 → 2.42-5.fc44"},
			{Action: resource.Update, Summary: "upgrade glibc.i686 2.42-4.fc44 → 2.42-5.fc44"},
		}
		wantResults(t, rs, changes, errs, want, []string{"", ""})
	})
}

// TestCheckWithoutLists checks a fresh Ubuntu, which has never downloaded
// its package lists: names can't be checked during the plan.
// TestCheckWithoutLists checks a fresh Ubuntu whose package lists were
// never downloaded, where sudo needs a password: the plan can't refresh
// them, so names are checked once they're refreshed, before installing.
func TestCheckWithoutLists(t *testing.T) {
	s := newTestSystem(t, config.Ubuntu)
	s.runner.SudoStatus = system.SudoNeedsPassword
	s.on("dpkg-query -W", result(1, "", "dpkg-query: no packages found matching jq\n"))

	rs := Set(s.rt, "packages", []string{"jq"})
	changes, errs := s.check(rs)
	wantResults(t, rs, changes, errs, []resource.Change{{Action: resource.Create, Summary: "install jq"}}, []string{""})
	if got := s.runner.RootCommands(); len(got) != 0 {
		t.Errorf("ran %q as root during the plan, though sudo needs a password", got)
	}
}

// TestCheckStaleLists checks package lists more than an hour old, such as
// a WSL image's: names they don't have aren't typos until the lists are
// refreshed. The plan refreshes them if sudo works without a password,
// and otherwise leaves the names to be checked before installing.
func TestCheckStaleLists(t *testing.T) {
	tests := []struct {
		name    string
		sudo    system.SudoStatus
		refresh bool
		want    resource.Change
		wantErr string
	}{
		{
			name:    "sudo without a password: refreshed, and a typo is caught",
			sudo:    system.SudoNoPassword,
			refresh: true,
			wantErr: "unknown package fsh: the enabled repos don't have it",
		},
		{
			name: "sudo needs a password: checked before installing",
			sudo: system.SudoNeedsPassword,
			want: resource.Change{Action: resource.Create, Summary: "install fsh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSystem(t, config.Ubuntu)
			s.lists(3 * time.Hour)
			s.runner.SudoStatus = tt.sudo
			s.on("dpkg-query -W", result(1, "", "dpkg-query: no packages found matching fsh\n"))
			s.on("apt-get update", result(0, "", ""))
			s.on("apt-cache policy", result(0, "", ""))

			rs := Set(s.rt, "packages", []string{"fsh"})
			changes, errs := s.check(rs)
			wantResults(t, rs, changes, errs, []resource.Change{tt.want}, []string{tt.wantErr})
			refreshed := slices.Contains(s.runner.RootCommands(), "apt-get update")
			if refreshed != tt.refresh {
				t.Errorf("refreshed the lists during the plan: %t, want %t", refreshed, tt.refresh)
			}
		})
	}
}

// source is a resource packages come from, such as a repo, whose check
// finds change.
type source struct {
	change resource.Change
	err    error
}

func (source) ID() string        { return "repo:hashicorp" }
func (source) Kind() string      { return "repo" }
func (source) Targets() []string { return nil }
func (source) Privileged() bool  { return true }

func (r source) Check(context.Context, *resource.Runtime) (resource.Change, error) {
	return r.change, r.err
}

func (source) Apply(context.Context, *resource.Runtime) (state.Record, error) {
	return state.Record{}, nil
}

// TestCheckFromPendingSource checks packages from a repo the same sync
// adds: until it's there, the repos can't have them.
func TestCheckFromPendingSource(t *testing.T) {
	tests := []struct {
		name    string
		from    source
		want    resource.Change
		wantErr string
	}{
		{name: "the repo is added", from: source{change: resource.Change{Action: resource.Create}}, want: resource.Change{Action: resource.Create, Summary: "install packer"}},
		{name: "the repo is updated", from: source{change: resource.Change{Action: resource.Update}}, want: resource.Change{Action: resource.Create, Summary: "install packer"}},
		{name: "the repo can't be checked", from: source{err: errors.New("permission denied")}, want: resource.Change{Action: resource.Create, Summary: "install packer"}},
		{name: "the repo is there", from: source{}, wantErr: "unknown package packer: the enabled repos don't have it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ubuntu(t)
			s.on("dpkg-query -W", result(1, "", "dpkg-query: no packages found matching packer\n"))
			s.on("apt-cache policy", result(0, "", "N: Unable to locate package packer\n"))
			rs := Set(s.rt, "hashicorp", []string{"packer"}, tt.from)
			changes, errs := s.check(rs)
			wantResults(t, rs, changes, errs, []resource.Change{tt.want}, []string{tt.wantErr})
		})
	}
}

func TestCheckErrors(t *testing.T) {
	t.Run("an invalid name", func(t *testing.T) {
		s := ubuntu(t)
		s.fakeApt(nil, map[string]string{"jq": "1.7.1-3build1"})
		rs := Set(s.rt, "packages", []string{"-o=APT::Get::Assume-Yes", "jq"})
		changes, errs := s.check(rs)
		if want := `"-o=APT::Get::Assume-Yes" isn't a valid package name`; errs[0] == nil || errs[0].Error() != want {
			t.Errorf("error = %v, want %q", errs[0], want)
		}
		if errs[1] != nil || changes[1].Summary != "install jq" {
			t.Errorf("jq: change = %+v, %v; want it installed, without the invalid name getting in the way", changes[1], errs[1])
		}
		for _, c := range s.commands() {
			if strings.Contains(c, "-o=") {
				t.Errorf("ran %s, want the invalid name left out", c)
			}
		}
	})
	t.Run("a name apt reads as a removal", func(t *testing.T) {
		// Stale lists and sudo with a password: names the lists lack are
		// checked only when they're installed.
		s := newTestSystem(t, config.Ubuntu)
		s.lists(3 * time.Hour)
		s.runner.SudoStatus = system.SudoNeedsPassword
		s.fakeApt(nil, map[string]string{"g++": "4:13.2.0-7ubuntu1", "jq": "1.7.1-3build1"})
		rs := Set(s.rt, "packages", []string{"openssh-server-", "g++", "jq"})
		changes, errs := s.check(rs)
		want := []resource.Change{{}, {Action: resource.Create, Summary: "install g++"}, {Action: resource.Create, Summary: "install jq"}}
		wantErrs := []string{`"openssh-server-" isn't a valid package name: a "-" at the end tells apt to remove openssh-server`, "", ""}
		wantResults(t, rs, changes, errs, want, wantErrs)
		for _, c := range s.commands() {
			if strings.Contains(c, "openssh-server") {
				t.Errorf("ran %s, want the invalid name left out", c)
			}
		}
	})
	t.Run("another OS", func(t *testing.T) {
		rt := &resource.Runtime{Runner: systemtest.NewFakeRunner(t), Facts: &facts.System{OSID: "debian"}}
		rs := Set(rt, "packages", []string{"jq"})
		if _, err := rs[0].Check(t.Context(), rt); err == nil || err.Error() != "maisys only manages packages on Fedora and Ubuntu" {
			t.Errorf("error = %v, want one saying maisys only manages packages on Fedora and Ubuntu", err)
		}
	})
	t.Run("dnf check-update fails", func(t *testing.T) {
		s := fedora(t)
		s.on("rpm -q", result(0, "jq\tx86_64\t1.7.1-11.fc44\ntmux\tx86_64\t3.5a-3.fc44\n", ""))
		s.on("dnf -q check-update", result(1, "", "Curl error (6): Couldn't resolve host name for https://mirrors.fedoraproject.org/metalink?repo=fedora-44&arch=x86_64\n"))
		rs := Set(s.rt, "packages", []string{"jq", "tmux"})
		_, errs := s.check(rs)
		for i, err := range errs {
			if got := errString(err); !strings.HasPrefix(got, "dnf check-update failed with exit code 1: Curl error (6)") {
				t.Errorf("%s: error = %v, want dnf check-update's", rs[i].ID(), err)
			}
		}
		if n := strings.Count(strings.Join(s.commands(), "\n"), "check-update"); n != 1 {
			t.Errorf("dnf check-update ran %d times, want once", n)
		}
	})
}

// errString returns err's message, or "" for nil.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestSetIDs(t *testing.T) {
	s := ubuntu(t)
	var ids []string
	for _, r := range Set(s.rt, "shell", []string{"zsh", "fish", "zsh"}) {
		ids = append(ids, r.ID())
		if p, ok := r.(*Package); !ok || r.Kind() != Kind || !r.Privileged() || p.Group() != Group {
			t.Errorf("%s: kind %q, privileged %t; want a privileged %q in group %q", r.ID(), r.Kind(), r.Privileged(), Kind, Group)
		}
	}
	if want := []string{"package:shell/zsh", "package:shell/fish"}; !slices.Equal(ids, want) {
		t.Errorf("IDs = %q, want %q", ids, want)
	}
}
