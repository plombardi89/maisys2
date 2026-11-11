package sudoers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/sudoers"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// machine is a fake machine whose /etc/sudoers.d is a temporary folder,
// which the fake runner's commands read and write as root would.
type machine struct {
	t      *testing.T
	rt     *resource.Runtime
	runner *systemtest.FakeRunner
	dir    string
	// groups is what id -Gn says.
	groups string
	// visudo is the exit code visudo gives.
	visudo int
	prev   map[string]state.Record
}

func newMachine(t *testing.T, osName config.OS) *machine {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sudo only exists on Linux")
	}
	t.Setenv("TMPDIR", t.TempDir())
	m := &machine{t: t, runner: systemtest.NewFakeRunner(t), dir: t.TempDir(), groups: "phil wheel sudo", prev: map[string]state.Record{}}
	m.rt = &resource.Runtime{
		Runner: m.runner,
		Facts:  &facts.System{OS: osName},
		Now:    func() time.Time { return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) },
		Previous: func(id string) (state.Record, bool) {
			rec, ok := m.prev[id]
			return rec, ok
		},
	}
	r := m.runner
	r.On("test -e", func(_ context.Context, c system.Cmd) (system.Result, error) {
		_, err := os.Stat(c.Args[1])
		if errors.Is(err, fs.ErrNotExist) {
			return system.Result{ExitCode: 1}, nil
		}
		return system.Result{}, err
	})
	r.On("stat -c", func(_ context.Context, c system.Cmd) (system.Result, error) {
		info, err := os.Stat(c.Args[3])
		if err != nil {
			return system.Result{}, err
		}
		return system.Result{Stdout: fmt.Appendf(nil, "%o root:root\n", info.Mode().Perm())}, nil
	})
	r.On("cat", func(_ context.Context, c system.Cmd) (system.Result, error) {
		data, err := os.ReadFile(c.Args[1])
		return system.Result{Stdout: data}, err
	})
	r.On("cp -p", func(_ context.Context, c system.Cmd) (system.Result, error) {
		return system.Result{}, copyFile(c.Args[2], c.Args[3])
	})
	r.On("install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if err := copyFile(c.Args[6], c.Args[7]); err != nil {
			return system.Result{}, err
		}
		return system.Result{}, os.Chmod(c.Args[7], 0o440)
	})
	r.On("visudo -cf", func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{ExitCode: m.visudo, Stderr: []byte("syntax error near line 1")}, nil
	})
	r.On("rm -f --", func(_ context.Context, c system.Cmd) (system.Result, error) {
		return system.Result{}, os.Remove(c.Args[2])
	})
	r.On("id -Gn", func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{Stdout: []byte(m.groups + "\n")}, nil
	})
	return m
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// The real commands run as root, which may overwrite read-only files.
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.WriteFile(dst, data, 0o440)
}

func (m *machine) rule() *sudoers.Rule {
	return &sudoers.Rule{User: "phil", Dir: m.dir}
}

func (m *machine) path(name string) string {
	return filepath.Join(m.dir, name)
}

func (m *machine) write(name, data string, perm fs.FileMode) {
	m.t.Helper()
	if err := os.Remove(m.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(m.path(name), []byte(data), perm); err != nil {
		m.t.Fatal(err)
	}
	if err := os.Chmod(m.path(name), perm); err != nil {
		m.t.Fatal(err)
	}
}

func (m *machine) read(name string) string {
	m.t.Helper()
	data, err := os.ReadFile(m.path(name))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		m.t.Fatal(err)
	}
	return string(data)
}

// commands returns the commands that ran as root since the last call, with
// the folder written as DIR and temporary files as TMP.
func (m *machine) commands(seen *int) []string {
	all := m.runner.RootCommands()
	cmds := all[*seen:]
	*seen = len(all)
	var out []string
	for _, c := range cmds {
		f := strings.Fields(strings.ReplaceAll(c, m.dir, "DIR"))
		for i, arg := range f {
			if strings.Contains(arg, "maisys-") && !strings.HasPrefix(arg, "DIR") {
				f[i] = "TMP"
			}
		}
		out = append(out, strings.Join(f, " "))
	}
	return out
}

func wantChange(t *testing.T, step string, got, want resource.Change) {
	t.Helper()
	if got.Action != want.Action || got.Summary != want.Summary || got.Privileged != want.Privileged || !slices.Equal(got.Notes, want.Notes) {
		t.Errorf("%s: change = %+v, want %+v", step, got, want)
	}
}

func TestRule(t *testing.T) {
	m := newMachine(t, config.Fedora)
	r := m.rule()
	if r.ID() != "sudoers:phil" || r.Kind() != sudoers.Kind || !r.Privileged() || !slices.Equal(r.Targets(), []string{m.path("phil")}) {
		t.Errorf("ID, Kind, Privileged, Targets = %q, %q, %v, %q", r.ID(), r.Kind(), r.Privileged(), r.Targets())
	}
	describe := m.path("phil") + " (passwordless sudo for phil)"
	var seen int

	c, err := r.Check(t.Context(), m.rt)
	if err != nil {
		t.Fatal(err)
	}
	wantChange(t, "first check", c, resource.Change{Action: resource.Create, Summary: describe})
	rec, err := r.Apply(t.Context(), m.rt)
	if err != nil {
		t.Fatal(err)
	}
	const rule = "phil ALL=(ALL) NOPASSWD: ALL\n"
	if got := m.read("phil"); got != rule {
		t.Errorf("the rule is %q, want %q", got, rule)
	}
	wantCmds := []string{
		"test -e DIR/phil",
		"test -e DIR/phil",
		"visudo -cf TMP",
		"install -m 0440 -o root -g root TMP DIR/phil",
	}
	if got := m.commands(&seen); !slices.Equal(got, wantCmds) {
		t.Errorf("commands run as root =\n%q\nwant\n%q", got, wantCmds)
	}
	if rec.Details[sudoers.DetailUser] != "phil" || rec.Details[sudoers.DetailHash] == "" || rec.TookOver || !slices.Equal(rec.Paths, []string{m.path("phil")}) {
		t.Errorf("record = %+v, want the user, the hash and the path", rec)
	}
	m.prev[r.ID()] = rec

	c, err = r.Check(t.Context(), m.rt)
	if err != nil {
		t.Fatal(err)
	}
	wantChange(t, "after applying", c, resource.Change{})

	if err := os.Chmod(m.path("phil"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = r.Check(t.Context(), m.rt)
	wantChange(t, "after a chmod", c, resource.Change{Action: resource.Update, Summary: describe + " (permissions 644 root:root → 440 root:root)"})

	m.write("phil", "phil ALL=(root) NOPASSWD: /usr/bin/dnf\n", 0o440)
	c, _ = r.Check(t.Context(), m.rt)
	wantChange(t, "after it changed", c, resource.Change{Action: resource.Warn, Summary: m.path("phil") + " changed outside maisys (will back up and overwrite)"})
	m.commands(&seen)
	if _, err := r.Apply(t.Context(), m.rt); err != nil {
		t.Fatal(err)
	}
	backup := "phil.maisys-bak.20261106-120000"
	if got := m.read(backup); got != "phil ALL=(root) NOPASSWD: /usr/bin/dnf\n" {
		t.Errorf("the backup has %q, want the changed rule", got)
	}
	if got := m.read("phil"); got != rule {
		t.Errorf("the rule is %q, want %q", got, rule)
	}
	if got := m.commands(&seen); !slices.Contains(got, "cp -p -- DIR/phil DIR/"+backup) {
		t.Errorf("commands run as root = %q, want a backup", got)
	}
}

func TestRuleTakesOver(t *testing.T) {
	m := newMachine(t, config.Ubuntu)
	r := m.rule()
	m.write("phil", "phil ALL=(root) NOPASSWD:ALL\n", 0o440)
	c, err := r.Check(t.Context(), m.rt)
	if err != nil {
		t.Fatal(err)
	}
	wantChange(t, "check", c, resource.Change{Action: resource.Warn, Summary: m.path("phil") + " exists (will back up and replace)"})
	rec, err := r.Apply(t.Context(), m.rt)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.TookOver {
		t.Errorf("record = %+v, want it taken over", rec)
	}
}

func TestRuleVisudoFails(t *testing.T) {
	m := newMachine(t, config.Fedora)
	m.visudo = 1
	_, err := m.rule().Apply(t.Context(), m.rt)
	if err == nil || !strings.Contains(err.Error(), "checking the sudoers rule with visudo failed with exit code 1: syntax error near line 1") {
		t.Errorf("Apply() error = %v, want visudo's", err)
	}
	if got := m.read("phil"); got != "" {
		t.Errorf("the rule was written: %q", got)
	}
}

func TestRuleNeedsPassword(t *testing.T) {
	m := newMachine(t, config.Fedora)
	m.runner.SudoStatus = system.SudoNeedsPassword
	if _, err := m.rule().Check(t.Context(), m.rt); !errors.Is(err, system.ErrNeedsPassword) {
		t.Errorf("Check() error = %v, want ErrNeedsPassword, so the plan checks it during apply", err)
	}
}

func TestRuleBadUser(t *testing.T) {
	m := newMachine(t, config.Fedora)
	r := &sudoers.Rule{User: "phil ALL", Dir: m.dir}
	if _, err := r.Check(t.Context(), m.rt); err == nil {
		t.Error("Check() of a rule for \"phil ALL\": no error")
	}
	if got := sudoers.FileName("john.doe"); got != "john_doe" {
		t.Errorf("FileName(john.doe) = %q, want a name without a dot, which sudo would skip", got)
	}
}

func TestRemover(t *testing.T) {
	const rule = "phil ALL=(ALL) NOPASSWD: ALL\n"
	tests := []struct {
		name    string
		os      config.OS
		content string // the rule's file, if any
		groups  string
		want    func(path string) resource.Change
		removed bool
	}{
		{
			name: "in the wheel group", os: config.Fedora, content: rule, groups: "phil wheel",
			want: func(p string) resource.Change {
				return resource.Change{Action: resource.Remove, Summary: p + " (passwordless sudo for phil)", Privileged: true}
			},
			removed: true,
		},
		{
			name: "in the sudo group", os: config.Ubuntu, content: rule, groups: "phil adm sudo",
			want: func(p string) resource.Change {
				return resource.Change{Action: resource.Remove, Summary: p + " (passwordless sudo for phil)", Privileged: true}
			},
			removed: true,
		},
		{
			name: "not in the wheel group", os: config.Fedora, content: rule, groups: "phil sudo",
			want: func(p string) resource.Change {
				return resource.Change{Action: resource.Orphan, Summary: p + " (passwordless sudo for phil)", Notes: []string{
					p + " is left in place: phil isn't in the wheel group, so deleting it would take away their sudo access. Add them to it as root (usermod -aG wheel phil), and the next sync deletes it",
				}}
			},
		},
		{
			name: "not in the sudo group", os: config.Ubuntu, content: rule, groups: "phil wheel",
			want: func(p string) resource.Change {
				return resource.Change{Action: resource.Orphan, Summary: p + " (passwordless sudo for phil)", Notes: []string{
					p + " is left in place: phil isn't in the sudo group, so deleting it would take away their sudo access. Add them to it as root (usermod -aG sudo phil), and the next sync deletes it",
				}}
			},
		},
		{
			name: "changed outside maisys", os: config.Fedora, content: "phil ALL=(ALL) ALL\n", groups: "phil wheel",
			want: func(p string) resource.Change {
				return resource.Change{Notes: []string{p + " changed outside maisys (left in place)"}}
			},
		},
		{
			name: "already gone", os: config.Fedora, groups: "phil wheel",
			want: func(string) resource.Change { return resource.Change{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t, tt.os)
			m.groups = tt.groups
			if tt.content != "" {
				m.write("phil", tt.content, 0o440)
			}
			rec := state.Record{
				ID:      "sudoers:phil",
				Kind:    sudoers.Kind,
				Paths:   []string{m.path("phil")},
				Details: map[string]string{sudoers.DetailUser: "phil", sudoers.DetailHash: hashOf(rule)},
			}

			var rm sudoers.Remover
			c, err := rm.CheckRemove(t.Context(), m.rt, rec)
			if err != nil {
				t.Fatal(err)
			}
			wantChange(t, "CheckRemove", c, tt.want(m.path("phil")))

			err = rm.Remove(t.Context(), m.rt, rec)
			if c.Action == resource.Orphan {
				// Removing it during apply, after a check that needed a
				// password, keeps its record.
				if err == nil || !strings.Contains(err.Error(), "would take away their sudo access") {
					t.Errorf("Remove() error = %v, want it left in place", err)
				}
			} else if err != nil {
				t.Errorf("Remove() error = %v", err)
			}
			if gone := m.read("phil") == ""; gone != (tt.removed || tt.content == "") {
				t.Errorf("the rule is gone: %v, want %v", gone, tt.removed)
			}
		})
	}
}
