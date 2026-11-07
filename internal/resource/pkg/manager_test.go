package pkg

import (
	"context"
	"errors"
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

// What apt, dpkg and dnf say when another program holds their lock.
const (
	aptLocked = "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 4242 (unattended-upgr)\n" +
		"N: Be aware that removing the lock file is not a solution and may break your system.\n" +
		"E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), is another process using it?\n"
	aptListsLocked = "E: Could not get lock /var/lib/apt/lists/lock. It is held by process 4242 (apt-get)\n" +
		"N: Be aware that removing the lock file is not a solution and may break your system.\n" +
		"E: Unable to lock directory /var/lib/apt/lists/\n"
	dpkgLocked = "dpkg: error: dpkg frontend lock was locked by another process with pid 4242\n" +
		"Note: removing the lock file is always wrong, can damage the locked area\n" +
		"and the entire system. See <https://wiki.debian.org/Teams/Dpkg/FAQ#db-lock>.\n"
	dnfLocked  = "Running transaction\nTransaction failed: Failed to obtain rpm transaction lock. Another transaction is in progress.\n"
	dnf4Locked = "Running transaction\nFailed to obtain the transaction lock (logged in as: root).\nError: Could not run transaction.\n"
)

func TestLockHeld(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		held   bool
	}{
		{"apt", aptLocked, true},
		{"apt-get update", aptListsLocked, true},
		{"dpkg", dpkgLocked, true},
		{"dnf", dnfLocked, true},
		{"dnf 4", dnf4Locked, true},
		{"a missing package", "E: Unable to locate package jqq\n", false},
		{"no network", "Curl error (6): Couldn't resolve host name\n", false},
	}
	for _, tt := range tests {
		if got := lockHeld(system.Result{ExitCode: 100, Stderr: []byte(tt.stderr)}); got != tt.held {
			t.Errorf("%s: lockHeld() = %t, want %t", tt.name, got, tt.held)
		}
	}
}

func TestRunWaitsForTheLock(t *testing.T) {
	s := ubuntu(t)
	tries := 0
	s.on("apt-get install", func(context.Context, system.Cmd) (system.Result, error) {
		tries++
		if tries <= 2 {
			return system.Result{ExitCode: 100, Stderr: []byte(aptLocked)}, nil
		}
		return system.Result{}, nil
	})

	if err := s.m.Install(t.Context(), "jq"); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if tries != 3 || s.slept != 10*time.Second {
		t.Errorf("apt-get install ran %d times, sleeping %s; want 3 times, 5 seconds apart", tries, s.slept)
	}
	want := "maisys: waiting for another program to finish with apt (E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 4242 (unattended-upgr))\n"
	if s.notes.String() != want {
		t.Errorf("notes = %q, want %q", s.notes.String(), want)
	}
}

func TestRunGivesUpOnTheLock(t *testing.T) {
	s := fedora(t)
	s.on("dnf install", result(1, "", dnfLocked))

	err := s.m.Install(t.Context(), "jq")
	want := "another program kept dnf busy for 5 minutes, so maisys stopped waiting: dnf install failed with exit code 1:\n" +
		"  Running transaction\n  Transaction failed: Failed to obtain rpm transaction lock. Another transaction is in progress."
	if errString(err) != want {
		t.Errorf("Install() error = %v, want %q", err, want)
	}
	if _, ok := errors.AsType[*system.ExitError](err); !ok {
		t.Errorf("Install() error = %v, want it to wrap dnf's *system.ExitError", err)
	}
	if n := len(s.runner.Commands()); n != 61 || s.slept != DefaultLockTimeout {
		t.Errorf("dnf install ran %d times in %s, want 61 times in %s", n, s.slept, DefaultLockTimeout)
	}
}

func TestRunStopsWaiting(t *testing.T) {
	s := ubuntu(t)
	s.on("dpkg --add-architecture", result(2, "", dpkgLocked))
	ctx, cancel := context.WithCancel(t.Context())
	s.m.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	}

	_, err := s.m.Run(ctx, system.Cmd{Name: "dpkg", Args: []string{"--add-architecture", "i386"}, Root: true})
	if !errors.Is(err, context.Canceled) || len(s.runner.Commands()) != 1 {
		t.Errorf("Run() error = %v after %d tries, want context.Canceled after one", err, len(s.runner.Commands()))
	}
}

func TestRunOtherFailures(t *testing.T) {
	s := ubuntu(t)
	s.on("apt-get install", result(100, "", "E: Unable to locate package jqq\n"))
	err := s.m.Install(t.Context(), "jqq")
	if errString(err) != "apt-get install failed with exit code 100: E: Unable to locate package jqq" || s.slept != 0 {
		t.Errorf("Install() error = %v after waiting %s, want apt-get's error straight away", err, s.slept)
	}
}

func TestRefreshLists(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(s *testSystem)
		refresh bool
	}{
		{name: "refreshed 10 minutes ago", setup: func(*testSystem) {}},
		{name: "refreshed 2 hours ago", setup: func(s *testSystem) { s.lists(2 * time.Hour) }, refresh: true},
		{
			name: "the update stamp is recent",
			setup: func(s *testSystem) {
				s.lists(2 * time.Hour)
				s.write(updateStamp, "")
				s.touch(s.m.path(updateStamp), 5*time.Minute)
			},
		},
		{
			name: "never downloaded",
			setup: func(s *testSystem) {
				s.m.root = s.t.TempDir()
			},
			refresh: true,
		},
		{name: "the sources changed", setup: func(s *testSystem) { s.m.SourcesChanged() }, refresh: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ubuntu(t)
			tt.setup(s)
			var env []string
			s.on("apt-get update", func(_ context.Context, c system.Cmd) (system.Result, error) {
				env = c.Env
				return system.Result{}, nil
			})
			s.on("apt-get install", result(0, "", ""))

			if err := s.m.Install(t.Context(), "jq"); err != nil {
				t.Fatal(err)
			}
			want := []string{aptInstall + " jq"}
			if tt.refresh {
				want = append([]string{"apt-get update"}, want...)
				if !slices.Equal(env, noninteractive) {
					t.Errorf("apt-get update ran with environment %q, want %q", env, noninteractive)
				}
			}
			if got := s.runner.RootCommands(); !slices.Equal(got, want) {
				t.Errorf("commands run as root = %q, want %q", got, want)
			}
		})
	}
}

// TestRefreshListsOnce refreshes the package lists once in a sync, unless
// the sources change again.
func TestRefreshListsOnce(t *testing.T) {
	s := ubuntu(t)
	s.lists(3 * time.Hour)
	s.on("apt-get update", result(0, "", ""))
	s.on("apt-get install", result(0, "", ""))
	for _, step := range []func(){
		func() {},
		func() {},
		s.m.SourcesChanged,
		func() {},
	} {
		step()
		if err := s.m.Install(t.Context(), "jq"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"apt-get update", aptInstall + " jq", aptInstall + " jq", "apt-get update", aptInstall + " jq", aptInstall + " jq"}
	if got := s.runner.RootCommands(); !slices.Equal(got, want) {
		t.Errorf("commands run as root =\n%q\nwant\n%q", got, want)
	}
}

func TestRefreshListsFails(t *testing.T) {
	t.Run("a repo is broken", func(t *testing.T) {
		s := ubuntu(t)
		s.m.SourcesChanged()
		s.on("apt-get update", result(100, "", "E: The repository 'https://apt.releases.hashicorp.com bogus Release' does not have a Release file.\n"))
		s.on("apt-get install", result(0, "", ""))
		if err := s.m.Install(t.Context(), "jq"); err != nil {
			t.Fatalf("Install() error = %v, want it to install from the lists there are", err)
		}
		if want := "maisys: couldn't refresh the package lists, so packages are installed from the lists there are: apt-get update failed with exit code 100: "; !strings.HasPrefix(s.notes.String(), want) {
			t.Errorf("notes = %q, want them to start with %q", s.notes.String(), want)
		}
	})
	t.Run("the lists stay locked", func(t *testing.T) {
		s := ubuntu(t)
		s.m.SourcesChanged()
		s.on("apt-get update", result(100, "", aptListsLocked))
		if err := s.m.Install(t.Context(), "jq"); !strings.HasPrefix(errString(err), "another program kept apt busy for 5 minutes") {
			t.Errorf("Install() error = %v, want the lock's", err)
		}
		if got := s.commands("apt-get update"); len(got) != 0 {
			t.Errorf("commands = %q, want nothing installed", got)
		}
	})
}

func TestRemover(t *testing.T) {
	tests := []struct {
		name   string
		rec    state.Record
		wanted []string
		want   resource.Change
	}{
		{
			name: "still installed",
			rec:  state.Record{ID: "package:packages/jq", Kind: Kind, Module: "packages", Details: map[string]string{"name": "jq"}},
			want: resource.Change{Action: resource.Orphan, Summary: "package jq"},
		},
		{
			name: "a record without details",
			rec:  state.Record{ID: "package:packages/jq", Kind: Kind, Module: "packages"},
			want: resource.Change{Action: resource.Orphan, Summary: "package jq"},
		},
		{
			name: "gone",
			rec:  state.Record{ID: "package:packages/htop", Kind: Kind, Module: "packages", Details: map[string]string{"name": "htop"}},
		},
		{
			name:   "another module installs it",
			rec:    state.Record{ID: "package:packages/zsh", Kind: Kind, Module: "packages", Details: map[string]string{"name": "zsh"}},
			wanted: []string{"zsh"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := ubuntu(t)
			s.fakeApt(map[string]string{"jq": "1.7.1-3build1", "zsh": "5.9-6ubuntu2"}, map[string]string{"jq": "1.7.1-3build1", "zsh": "5.9-6ubuntu2"})
			Set(s.rt, "shell", tt.wanted)

			c, err := Remover{}.CheckRemove(t.Context(), s.rt, tt.rec)
			if err != nil || c.Action != tt.want.Action || c.Summary != tt.want.Summary {
				t.Errorf("CheckRemove() = %+v, %v; want %+v", c, err, tt.want)
			}
		})
	}

	s := ubuntu(t)
	if err := (Remover{}).Remove(t.Context(), s.rt, state.Record{ID: "package:packages/jq"}); err == nil {
		t.Error("Remove() worked, want an error: maisys never removes packages")
	}
	if len(s.runner.Calls()) > 0 || (Remover{}).Kind() != Kind {
		t.Errorf("Remove() ran %q, want nothing", s.runner.Commands())
	}
}

func TestRebootRecommended(t *testing.T) {
	const needsReboot = "Core libraries or services have been updated since boot-up:\n  * kernel\n  * systemd\n\n" +
		"Reboot is required to fully utilize these updates.\nMore information: https://access.redhat.com/solutions/27943\n"
	tests := []struct {
		name    string
		os      config.OS
		setup   func(s *testSystem)
		want    bool
		wantErr string
	}{
		{name: "ubuntu", os: config.Ubuntu, setup: func(*testSystem) {}},
		{name: "ubuntu after a kernel upgrade", os: config.Ubuntu, setup: func(s *testSystem) { s.write(rebootRequired, "*** System restart required ***\n") }, want: true},
		{
			name: "fedora",
			os:   config.Fedora,
			setup: func(s *testSystem) {
				s.on("dnf needs-restarting -r", result(0, "No core libraries or services have been updated since boot-up.\nReboot should not be necessary.\n", ""))
			},
		},
		{
			name:  "fedora after a kernel upgrade",
			os:    config.Fedora,
			setup: func(s *testSystem) { s.on("dnf needs-restarting -r", result(1, needsReboot, "")) },
			want:  true,
		},
		{
			name: "fedora without the plugin",
			os:   config.Fedora,
			setup: func(s *testSystem) {
				s.on("dnf needs-restarting -r", result(1, "", "No such command: needs-restarting. Please use /usr/bin/dnf --help\n"))
			},
			wantErr: "dnf needs-restarting isn't installed: No such command: needs-restarting. Please use /usr/bin/dnf --help",
		},
		{
			name: "fedora with dnf 5 without the plugin",
			os:   config.Fedora,
			setup: func(s *testSystem) {
				s.on("dnf needs-restarting -r", result(2, "", "Unknown argument \"needs-restarting\" for command \"dnf5\".\n"))
			},
			wantErr: "dnf needs-restarting failed with exit code 2: Unknown argument \"needs-restarting\" for command \"dnf5\".",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSystem(t, tt.os)
			tt.setup(s)
			got, err := s.m.rebootRecommended(t.Context())
			if got != tt.want || errString(err) != tt.wantErr {
				t.Errorf("rebootRecommended() = %t, %v; want %t, %q", got, err, tt.want, tt.wantErr)
			}
			for _, c := range s.runner.Calls() {
				if c.Cmd.Root {
					t.Errorf("%s ran as root", c.Cmd)
				}
			}
		})
	}

	got, err := RebootRecommended(t.Context(), systemtest.NewFakeRunner(t), &facts.System{OS: config.Windows})
	if got || err != nil {
		t.Errorf("RebootRecommended() on Windows = %t, %v; want false", got, err)
	}
}
