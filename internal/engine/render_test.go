package engine

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/system"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name.golden, or rewrites the file with
// -update.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the tests with -update to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s (run the tests with -update to rewrite it):\n--- got\n%s--- want\n%s", path, got, want)
	}
}

// examplePlan sets env up with the example plan from the design.
func examplePlan(e *testEnv) {
	e.add("golang", nil,
		res("tool:golang", resource.Update, "toolchain 1.27.3 → 1.27.4 (latest)"),
		res("go-install:task", resource.Update, "task v3.40.0 → v3.41.0 (latest)"),
	)
	fish := res("file:~/.config/fish/conf.d/maisys.fish", resource.NoChange, "")
	fish.change.Notes = []string{"fish: sdkman has no fish integration (skipped)"}
	e.add("shell", nil,
		res("git:aphrodite", resource.Update, "aphrodite a1b2c3 → d4e5f6 (master)"),
		fish,
		res("file:~/.zshrc", resource.NoChange, ""),
	)
	e.add("packages", nil,
		res("package:jq", resource.Create, "install jq").sudo(),
		res("package:tmux", resource.Update, "upgrade tmux 3.4-1 → 3.4-2").sudo(),
	)
	e.add("files", nil,
		res("file:~/.config/nvim/init.lua", resource.Create, "~/.config/nvim/init.lua"),
		res("file:~/.gitconfig", resource.Warn, "~/.gitconfig changed outside maisys (will back up and overwrite)"),
	)
	e.modules = append(e.modules, configOnly{"kubectl"}, configOnly{"hashicorp"}, configOnly{"steam"})
	e.machine.Modules["steam"] = nil
	e.record("tool:kubectl", "tool", "kubectl")
	e.record("repo:hashicorp", "repo", "hashicorp")
	e.remover(&fakeRemover{kind: "tool", change: resource.Change{Action: resource.Remove, Summary: "kubectl 1.36.0"}})
}

func TestRenderPlan(t *testing.T) {
	tests := []struct {
		name  string
		color bool
		setup func(e *testEnv)
	}{
		{name: "example", setup: examplePlan},
		{name: "example-color", color: true, setup: examplePlan},
		{
			name: "nothing",
			setup: func(e *testEnv) {
				e.header = []string{"config repo: https://example.com/old.git → https://example.com/new.git", "machine: phil-workp14s (was philboxv2)"}
				e.warnings = []string{"profiles/dev.toml:3:1: golang.go_proxy is deprecated: use shell variables instead", "Ubuntu 25.10 isn't one of the LTS releases maisys tests on (24.04 and 26.04); maisys may still work"}
				e.add("golang", nil, res("tool:golang", resource.NoChange, "toolchain 1.27.4"))
				e.add("files", nil)
			},
		},
		{
			name: "unknown",
			setup: func(e *testEnv) {
				e.add("golang", nil, res("tool:golang", resource.Create, "toolchain 1.27.4 (latest)"))
				jq := res("package:jq", resource.NoChange, "install jq")
				jq.checkErr = fmt.Errorf("dpkg-query: %w", system.ErrNeedsPassword)
				e.add("packages", nil,
					jq,
					&fakeResource{id: "repo:docker", kind: "repo", change: resource.Change{Action: resource.Unknown}},
				)
				e.record("unit:x410", "unit", "wsl")
				e.remover(&fakeRemover{kind: "unit", checkErr: map[string]error{"unit:x410": system.ErrNeedsPassword}})
			},
		},
		{
			name: "failed",
			setup: func(e *testEnv) {
				e.add("packages", nil, res("package:jq", resource.Create, "install jq").sudo())
				golang := res("go-install:gopls", resource.Update, "gopls v0.20.0 → v0.21.0 (latest)")
				golang.checkErr = &system.ExitError{Label: "go version -m", Code: 1, Tail: []string{"gopls: not a Go binary", "check the file"}}
				e.add("golang", nil, res("tool:golang", resource.Update, "toolchain 1.27.3 → 1.27.4 (latest)"), golang)
				e.add("mage", []string{"golang"}, res("tool:mage", resource.Create, "mage 1.15.0"))
				m := e.add("kubectl", nil)
				m.err = errors.New("couldn't find the latest kubectl at https://dl.k8s.io/release/stable.txt: 503 Service Unavailable")
				e.record("tool:stern", "tool", "stern")
				e.record("tool:kubectl", "tool", "kubectl")
				e.modules = append(e.modules, configOnly{"stern"})
				e.remover(&fakeRemover{kind: "tool", checkErr: map[string]error{"tool:stern": errors.New("permission denied")}})
			},
		},
		{
			name: "windows",
			setup: func(e *testEnv) {
				e.windows()
				e.machine.Name = "workbox-camwin"
				e.add("winget", nil, res("winget:Microsoft.WindowsTerminal", resource.Create, "install Microsoft.WindowsTerminal"))
				distro := res("wsl-distro:fedora-dev", resource.Create, "create distro fedora-dev (FedoraLinux-44), user phil")
				distro.change.More = []string{"set up fedora-dev with Linux machine workbox-camwin-fc44"}
				e.add("wsl_host", nil,
					res("wsl", resource.Create, "install WSL").sudo().mayReboot(),
					res("file:~/.wslconfig", resource.Create, "write ~\\.wslconfig (run `wsl --shutdown` to apply)"),
					distro,
					res("wsl-distro:ubuntu-scratch", resource.Present, "distro ubuntu-scratch exists (synced from inside the distro)"),
				)
				e.add("windows_terminal", nil, res("terminal:default-profile", resource.Update, "default profile → fedora-dev"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.runner.SudoStatus = system.SudoNeedsPassword
			tt.setup(e)
			var b bytes.Buffer
			e.plan().Render(&b, tt.color)
			golden(t, "plan-"+tt.name, b.Bytes())
		})
	}
}

// TestSummaryWindows checks how a Windows plan's summary says that it
// needs admin rights, and whether that shows a UAC prompt, and that it may
// need a reboot.
func TestSummaryWindows(t *testing.T) {
	tests := []struct {
		name      string
		windows   *facts.Windows
		admin     bool
		mayReboot bool
		want      string
	}{
		{name: "nothing needs admin", windows: &facts.Windows{}, want: "1 to change."},
		{name: "needs the UAC prompt", windows: &facts.Windows{}, admin: true, want: "1 to change. Needs admin (1 UAC prompt)."},
		{name: "elevated already", windows: &facts.Windows{Admin: true, Elevated: true}, admin: true, want: "1 to change. Needs admin."},
		{name: "not detected", admin: true, want: "1 to change. Needs admin."},
		{name: "may reboot", windows: &facts.Windows{}, mayReboot: true, want: "1 to change. A reboot may be needed."},
		{
			name: "needs admin and may reboot", windows: &facts.Windows{}, admin: true, mayReboot: true,
			want: "1 to change. Needs admin (1 UAC prompt). A reboot may be needed.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.windows()
			e.facts.Windows = tt.windows
			r := res("wsl:install", resource.Create, "install WSL")
			r.privileged = tt.admin
			r.change.MayReboot = tt.mayReboot
			e.add("wsl_host", nil, r)
			if got := e.plan().summary(); got != tt.want {
				t.Errorf("summary() = %q, want %q", got, tt.want)
			}
		})
	}
}
