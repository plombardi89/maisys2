package pkg

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

// fakeApt answers dpkg-query and apt-cache policy for packages: installed
// maps those installed to their versions, and candidates the packages the
// repos have to the versions they'd install.
func (s *testSystem) fakeApt(installed, candidates map[string]string) {
	s.on("dpkg-query -W", func(_ context.Context, c system.Cmd) (system.Result, error) {
		var out, errOut strings.Builder
		for _, name := range c.Args[3:] {
			if v, ok := installed[name]; ok {
				fmt.Fprintf(&out, "%s\tamd64\tinstall ok installed\t%s\n", name, v)
			} else {
				fmt.Fprintf(&errOut, "dpkg-query: no packages found matching %s\n", name)
			}
		}
		code := 0
		if errOut.Len() > 0 {
			code = 1
		}
		return system.Result{ExitCode: code, Stdout: []byte(out.String()), Stderr: []byte(errOut.String())}, nil
	})
	s.on("apt-cache policy", func(_ context.Context, c system.Cmd) (system.Result, error) {
		var out strings.Builder
		for _, name := range c.Args[1:] {
			if v, ok := candidates[name]; ok {
				fmt.Fprintf(&out, "%s:\n  Installed: %s\n  Candidate: %s\n  Version table:\n", name, cmp.Or(installed[name], "(none)"), v)
			}
		}
		return system.Result{Stdout: []byte(out.String())}, nil
	})
}

// fakeDnf answers rpm -q, dnf repoquery and dnf check-update: installed
// maps the packages installed to their versions, available lists the
// packages the repos have, and upgrades maps packages to the versions
// they'd be upgraded to.
func (s *testSystem) fakeDnf(installed map[string]string, available []string, upgrades map[string]string) {
	s.on("rpm -q", func(_ context.Context, c system.Cmd) (system.Result, error) {
		var out strings.Builder
		missing := 0
		for _, name := range c.Args[3:] {
			if v, ok := installed[name]; ok {
				fmt.Fprintf(&out, "%s\tx86_64\t%s\n", name, v)
			} else {
				fmt.Fprintf(&out, "package %s is not installed\n", name)
				missing++
			}
		}
		return system.Result{ExitCode: missing, Stdout: []byte(out.String())}, nil
	})
	s.on("dnf -q repoquery", func(_ context.Context, c system.Cmd) (system.Result, error) {
		var out strings.Builder
		for _, name := range c.Args[4:] {
			if slices.Contains(available, name) {
				out.WriteString(name + "\n")
			}
		}
		return system.Result{Stdout: []byte(out.String())}, nil
	})
	s.on("dnf -q check-update", func(context.Context, system.Cmd) (system.Result, error) {
		var out strings.Builder
		for name, v := range upgrades {
			fmt.Fprintf(&out, "%s.x86_64    %s    updates\n", name, v)
		}
		code := 0
		if len(upgrades) > 0 {
			code = 100
		}
		return system.Result{ExitCode: code, Stdout: []byte(out.String())}, nil
	})
}

// moduleSet is a module's packages.
type moduleSet struct {
	module string
	names  []string
}

// members makes the package sets of sets, checks them, and returns the
// members of the group to apply: those with changes.
func (s *testSystem) members(sets ...moduleSet) []resource.Member {
	s.t.Helper()
	var members []resource.Member
	for _, set := range sets {
		for _, r := range Set(s.rt, set.module, set.names) {
			c, err := r.Check(s.t.Context(), s.rt)
			if err != nil {
				s.t.Fatalf("checking %s: %v", r.ID(), err)
			}
			p, ok := r.(*Package)
			if !ok {
				s.t.Fatalf("%s isn't a *Package", r.ID())
			}
			if c.Action.Applies() {
				members = append(members, resource.Member{Module: set.module, Resource: p})
			}
		}
	}
	return members
}

// applyGroup applies members as the engine does, with ApplyGroup on the
// first.
func (s *testSystem) applyGroup(members []resource.Member) []resource.Outcome {
	s.t.Helper()
	out := members[0].Resource.ApplyGroup(s.t.Context(), s.rt, members)
	if len(out) != len(members) {
		s.t.Fatalf("ApplyGroup() returned %d outcomes for %d members", len(out), len(members))
	}
	return out
}

// outcomes describes outcomes, as "ID: ok (name)" or "ID: error".
func outcomes(members []resource.Member, out []resource.Outcome) []string {
	var lines []string
	for i, o := range out {
		line := members[i].Resource.ID() + ": "
		if o.Err != nil {
			line += o.Err.Error()
		} else {
			line += "ok (" + o.Record.Details["name"] + ")"
		}
		lines = append(lines, line)
	}
	return lines
}

const (
	aptInstall = "apt-get install -y --no-remove -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
	// brokenCompose is how apt-get install fails for a package whose
	// dependencies can't be installed.
	brokenCompose = `Reading package lists...
Building dependency tree...
Reading state information...
Some packages could not be installed. This may mean that you have
requested an impossible situation or if you are using the unstable
distribution that some required packages have not yet been created
or been moved out of Incoming.
The following information may help to resolve the situation:

The following packages have unmet dependencies:
 podman-compose : Depends: python3-dotenv but it is not installable
E: Unable to correct problems, you have held broken packages.
`
)

func TestApplyGroupOneTransaction(t *testing.T) {
	t.Run("ubuntu", func(t *testing.T) {
		s := ubuntu(t)
		s.fakeApt(map[string]string{"jq": "1.7.1-3build1", "tmux": "3.4-1build1"},
			map[string]string{"jq": "1.7.1-3ubuntu0.24.04.1", "tmux": "3.4-1build1", "htop": "3.3.0-4build1", "podman": "4.9.3+ds1-1ubuntu0.2"})
		var env []string
		s.on("apt-get install", func(_ context.Context, c system.Cmd) (system.Result, error) {
			env = c.Env
			return system.Result{}, nil
		})

		members := s.members(moduleSet{"packages", []string{"jq", "tmux", "htop"}}, moduleSet{"podman", []string{"podman", "htop"}})
		out := s.applyGroup(members)
		want := []string{
			"package:packages/jq: ok (jq)",
			"package:packages/htop: ok (htop)",
			"package:podman/podman: ok (podman)",
			"package:podman/htop: ok (htop)",
		}
		if got := outcomes(members, out); !slices.Equal(got, want) {
			t.Errorf("outcomes =\n%q\nwant\n%q", got, want)
		}
		if got, want := s.runner.RootCommands(), []string{aptInstall + " htop podman jq"}; !slices.Equal(got, want) {
			t.Errorf("commands run as root = %q, want one apt-get install for both modules: %q", got, want)
		}
		if !slices.Equal(env, []string{"DEBIAN_FRONTEND=noninteractive"}) {
			t.Errorf("apt-get install ran with environment %q, want DEBIAN_FRONTEND=noninteractive", env)
		}
	})
	t.Run("fedora", func(t *testing.T) {
		s := fedora(t)
		s.fakeDnf(map[string]string{"jq": "1.7.1-11.fc44", "tmux": "3.5a-3.fc44"}, []string{"htop", "podman"}, map[string]string{"tmux": "3.5a-4.fc44"})
		s.on("dnf install", result(0, "Complete!\n", ""))
		s.on("dnf upgrade", result(0, "Complete!\n", ""))

		members := s.members(moduleSet{"packages", []string{"jq", "tmux", "htop"}}, moduleSet{"podman", []string{"podman"}})
		out := s.applyGroup(members)
		want := []string{
			"package:packages/tmux: ok (tmux)",
			"package:packages/htop: ok (htop)",
			"package:podman/podman: ok (podman)",
		}
		if got := outcomes(members, out); !slices.Equal(got, want) {
			t.Errorf("outcomes =\n%q\nwant\n%q", got, want)
		}
		if got, want := s.runner.RootCommands(), []string{"dnf install -y htop podman", "dnf upgrade -y tmux"}; !slices.Equal(got, want) {
			t.Errorf("commands run as root = %q, want %q", got, want)
		}
	})
}

func TestApplyGroupFallback(t *testing.T) {
	t.Run("ubuntu", func(t *testing.T) {
		s := ubuntu(t)
		s.fakeApt(nil, map[string]string{"jq": "1.7.1-3build1", "podman": "4.9.3+ds1-1ubuntu0.2", "podman-compose": "1.0.6-1"})
		s.on("apt-get install", func(_ context.Context, c system.Cmd) (system.Result, error) {
			if slices.Contains(c.Args, "podman-compose") {
				return system.Result{ExitCode: 100, Stdout: []byte(brokenCompose)}, nil
			}
			return system.Result{}, nil
		})

		members := s.members(moduleSet{"packages", []string{"jq"}}, moduleSet{"podman", []string{"podman", "podman-compose"}})
		out := s.applyGroup(members)
		wantErr := "apt-get install failed with exit code 100:\n" +
			"  or been moved out of Incoming.\n" +
			"  The following information may help to resolve the situation:\n" +
			"  The following packages have unmet dependencies:\n" +
			"  podman-compose : Depends: python3-dotenv but it is not installable\n" +
			"  E: Unable to correct problems, you have held broken packages."
		want := []string{
			"package:packages/jq: ok (jq)",
			"package:podman/podman: " + wantErr,
			"package:podman/podman-compose: " + wantErr,
		}
		if got := outcomes(members, out); !slices.Equal(got, want) {
			t.Errorf("outcomes =\n%q\nwant\n%q", got, want)
		}
		wantCmds := []string{aptInstall + " jq podman podman-compose", aptInstall + " jq", aptInstall + " podman podman-compose"}
		if got := s.runner.RootCommands(); !slices.Equal(got, wantCmds) {
			t.Errorf("commands run as root =\n%q\nwant\n%q", got, wantCmds)
		}
		if want := "maisys: installing the packages of packages, podman together failed, so maisys is trying again module by module: "; !strings.HasPrefix(s.notes.String(), want) {
			t.Errorf("notes = %q, want them to start with %q", s.notes.String(), want)
		}
	})
	t.Run("fedora", func(t *testing.T) {
		s := fedora(t)
		s.fakeDnf(map[string]string{"tmux": "3.5a-3.fc44"}, []string{"jq", "steam"}, map[string]string{"tmux": "3.5a-4.fc44"})
		const conflict = "Failed to resolve the transaction:\nProblem: conflicting requests\n  - nothing provides libgcc(x86-32) needed by steam-1.0.0.85-1.fc44.i686 from rpmfusion-nonfree\n"
		s.on("dnf install", func(_ context.Context, c system.Cmd) (system.Result, error) {
			if slices.Contains(c.Args, "steam") {
				return system.Result{ExitCode: 1, Stderr: []byte(conflict)}, nil
			}
			return system.Result{}, nil
		})
		s.on("dnf upgrade", result(0, "", ""))

		members := s.members(moduleSet{"packages", []string{"jq", "tmux"}}, moduleSet{"steam", []string{"steam"}})
		out := s.applyGroup(members)
		wantErr := "dnf install failed with exit code 1:\n  Failed to resolve the transaction:\n  Problem: conflicting requests\n" +
			"  - nothing provides libgcc(x86-32) needed by steam-1.0.0.85-1.fc44.i686 from rpmfusion-nonfree"
		want := []string{"package:packages/jq: ok (jq)", "package:packages/tmux: ok (tmux)", "package:steam/steam: " + wantErr}
		if got := outcomes(members, out); !slices.Equal(got, want) {
			t.Errorf("outcomes =\n%q\nwant\n%q", got, want)
		}
		wantCmds := []string{"dnf install -y jq steam", "dnf install -y jq", "dnf upgrade -y tmux", "dnf install -y steam"}
		if got := s.runner.RootCommands(); !slices.Equal(got, wantCmds) {
			t.Errorf("commands run as root =\n%q\nwant\n%q", got, wantCmds)
		}
	})
}

// TestApplyGroupOneModule doesn't retry a group of one module's packages,
// which would fail the same way.
func TestApplyGroupOneModule(t *testing.T) {
	s := ubuntu(t)
	s.fakeApt(nil, map[string]string{"jq": "1.7.1-3build1", "podman-compose": "1.0.6-1"})
	s.on("apt-get install", result(100, brokenCompose, ""))

	members := s.members(moduleSet{"packages", []string{"jq", "podman-compose"}})
	out := s.applyGroup(members)
	for i, o := range out {
		if o.Err == nil {
			t.Errorf("%s: no error", members[i].Resource.ID())
		}
	}
	if got := s.runner.RootCommands(); len(got) != 1 {
		t.Errorf("commands run as root = %q, want one apt-get install", got)
	}
}

func TestApplyGroupLocked(t *testing.T) {
	s := fedora(t)
	s.fakeDnf(nil, []string{"jq", "podman"}, nil)
	s.on("dnf install", result(1, "", "Running transaction\nTransaction failed: Failed to obtain rpm transaction lock. Another transaction is in progress.\n"))

	members := s.members(moduleSet{"packages", []string{"jq"}}, moduleSet{"podman", []string{"podman"}})
	out := s.applyGroup(members)
	for i, o := range out {
		if _, ok := errors.AsType[*lockError](o.Err); !ok {
			t.Errorf("%s: error = %v, want the lock's", members[i].Resource.ID(), o.Err)
		}
	}
	if got := len(s.runner.RootCommands()); got != 61 {
		t.Errorf("dnf install ran %d times, want 61: every 5 seconds for 5 minutes, and not again module by module", got)
	}
}

func TestApplyOne(t *testing.T) {
	s := ubuntu(t)
	s.fakeApt(nil, map[string]string{"jq": "1.7.1-3build1"})
	s.on("apt-get install", result(0, "", ""))

	r := Set(s.rt, "packages", []string{"jq"})[0]
	if _, err := r.Check(t.Context(), s.rt); err != nil {
		t.Fatal(err)
	}
	rec, err := r.Apply(t.Context(), s.rt)
	if err != nil || rec.Details["name"] != "jq" {
		t.Errorf("Apply() = %+v, %v; want a record of jq", rec, err)
	}
	if got, want := s.runner.RootCommands(), []string{aptInstall + " jq"}; !slices.Equal(got, want) {
		t.Errorf("commands run as root = %q, want %q", got, want)
	}
}
