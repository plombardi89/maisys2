package uvtool

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// Realistic output of uv tool list, with --outdated, and with --show-paths
// (where ~ is /home/u).
const (
	listOutput = `azure-cli v2.67.0
- az
codespell v2.3.0
- codespell
ftfy v6.3.1
- ftfy
`
	outdatedOutput = `azure-cli v2.67.0 [latest: 2.68.0]
- az
`
	pathsOutput = `azure-cli v2.68.0 (/home/u/.local/share/uv/tools/azure-cli)
- az (/home/u/.local/bin/az)
codespell v2.3.0 (/home/u/.local/share/uv/tools/codespell)
- codespell (/home/u/.local/bin/codespell)
`
)

func TestParseList(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []listed
	}{
		{
			name: "list",
			out:  listOutput,
			want: []listed{{name: "azure-cli", version: "2.67.0"}, {name: "codespell", version: "2.3.0"}, {name: "ftfy", version: "6.3.1"}},
		},
		{
			name: "outdated",
			out:  outdatedOutput,
			want: []listed{{name: "azure-cli", version: "2.67.0", latest: "2.68.0"}},
		},
		{
			name: "paths",
			out:  pathsOutput,
			want: []listed{
				{name: "azure-cli", version: "2.68.0", dir: "/home/u/.local/share/uv/tools/azure-cli", executables: []string{"/home/u/.local/bin/az"}},
				{name: "codespell", version: "2.3.0", dir: "/home/u/.local/share/uv/tools/codespell", executables: []string{"/home/u/.local/bin/codespell"}},
			},
		},
		{
			name: "several executables, extras and the Python version",
			out: "black v24.2.0 [required: ==24.2.0] [extras: d] [with: requests] [CPython 3.12.8] [latest: 25.1.0] (/home/u/.local/share/uv/tools/black)\n" +
				"- black (/home/u/.local/bin/black)\n" +
				"- blackd (/home/u/.local/bin/blackd)\n",
			want: []listed{{
				name: "black", version: "24.2.0", latest: "25.1.0", dir: "/home/u/.local/share/uv/tools/black",
				executables: []string{"/home/u/.local/bin/black", "/home/u/.local/bin/blackd"},
			}},
		},
		{name: "nothing installed", out: "No tools installed\n", want: nil},
		{name: "a warning", out: "warning: Ignoring malformed tool `broken` (run `uv tool uninstall broken` to remove)\nftfy v6.3.1\n- ftfy\n", want: []listed{{name: "ftfy", version: "6.3.1"}}},
	}
	for _, tt := range tests {
		got := parseList(tt.out)
		if !slices.EqualFunc(got, tt.want, func(a, b listed) bool {
			return a.name == b.name && a.version == b.version && a.latest == b.latest && a.dir == b.dir && slices.Equal(a.executables, b.executables)
		}) {
			t.Errorf("%s: parseList() = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"azure-cli":   "azure-cli",
		"Azure_CLI":   "azure-cli",
		"ruamel.yaml": "ruamel-yaml",
		"a__-.b":      "a-b",
	}
	for name, want := range tests {
		if got := normalize(name); got != want {
			t.Errorf("normalize(%q) = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"", "-x", "x-", "--force", "a b", "a==1.0", "a[b]"} {
		if validName(name) {
			t.Errorf("validName(%q) = true, want false", name)
		}
	}
}

// fixture is a fake uv and runner.
type fixture struct {
	uv     *UV
	runner *systemtest.FakeRunner
	rt     *resource.Runtime
	mu     sync.Mutex
	lists  int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".local", "share", "maisys", "tools", "uv", "0.11.2", "uv")
	writeFile(t, path, "uv")
	f := &fixture{
		uv:     &UV{Path: path, Env: []string{"UV_NO_PROGRESS=1"}},
		runner: systemtest.NewFakeRunner(t),
	}
	f.rt = &resource.Runtime{Runner: f.runner, Paths: settings.Paths{Home: home}}
	f.on("tool list --color never", 0, listOutput, "")
	f.on("tool list --outdated --color never", 0, outdatedOutput, "")
	return f
}

// on answers uv commands that start with args.
func (f *fixture) on(args string, code int, stdout, stderr string) {
	f.runner.On(f.uv.Path+" "+args, func(_ context.Context, c system.Cmd) (system.Result, error) {
		if c.Args[1] == "list" {
			f.mu.Lock()
			f.lists++
			f.mu.Unlock()
		}
		return system.Result{ExitCode: code, Stdout: []byte(stdout), Stderr: []byte(stderr)}, nil
	})
}

func TestCheck(t *testing.T) {
	tests := []struct {
		tool string
		want resource.Change
	}{
		{"azure-cli", resource.Change{Action: resource.Update, Summary: "upgrade azure-cli 2.67.0 → 2.68.0"}},
		{"codespell", resource.Change{}},
		{"FTFY", resource.Change{}},
		{"httpie", resource.Change{Action: resource.Create, Summary: "install httpie"}},
	}
	f := newFixture(t)
	var wg sync.WaitGroup
	got := make([]resource.Change, len(tests))
	for i, tt := range tests {
		wg.Go(func() {
			ch, err := f.uv.Tool(tt.tool).Check(t.Context(), f.rt)
			if err != nil {
				t.Error(err)
			}
			got[i] = ch
		})
	}
	wg.Wait()
	for i, tt := range tests {
		assertChange(t, got[i], tt.want)
	}
	if f.lists != 2 {
		t.Errorf("the tools were listed %d times, want twice (uv tool list, with and without --outdated)", f.lists)
	}
}

func TestCheckOldUV(t *testing.T) {
	f := newFixture(t)
	f.on("tool list --outdated --color never", 2, "", "error: unexpected argument '--outdated' found\n\nUsage: uv tool list [OPTIONS]\n\nFor more information, try '--help'.\n")
	got, err := f.uv.Tool("azure-cli").Check(t.Context(), f.rt)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, got, resource.Change{})
}

func TestCheckNothingInstalled(t *testing.T) {
	f := newFixture(t)
	f.on("tool list --color never", 0, "", "No tools installed\n")
	got, err := f.uv.Tool("ftfy").Check(t.Context(), f.rt)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, got, resource.Change{Action: resource.Create, Summary: "install ftfy"})
	if want := []string{system.Cmd{Name: f.uv.Path}.String() + " tool list --color never"}; !slices.Equal(f.runner.Commands(), want) {
		t.Errorf("commands = %q, want %q", f.runner.Commands(), want)
	}
}

func TestCheckUVNotInstalledYet(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.uv.Path); err != nil {
		t.Fatal(err)
	}
	got, err := f.uv.Tool("ftfy").Check(t.Context(), f.rt)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, got, resource.Change{Action: resource.Create, Summary: "install ftfy"})
	if cmds := f.runner.Commands(); cmds != nil {
		t.Errorf("Check() ran %q, want nothing", cmds)
	}
}

func TestCheckErrors(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		setup func(f *fixture)
		want  string
	}{
		{name: "bad name", tool: "--force", want: `"--force" isn't the name of a Python package`},
		{name: "version", tool: "ruff==0.5.0", want: `"ruff==0.5.0" isn't the name of a Python package`},
		{
			name:  "list fails",
			tool:  "ftfy",
			setup: func(f *fixture) { f.on("tool list --color never", 2, "", "error: failed to read tool receipts\n") },
			want:  "couldn't list the installed uv tools: uv tool list failed with exit code 2: error: failed to read tool receipts",
		},
		{
			name: "the index can't be reached",
			tool: "ftfy",
			setup: func(f *fixture) {
				f.on("tool list --outdated --color never", 2, "", "error: Request failed after 3 retries\n  Caused by: Failed to fetch: `https://pypi.org/simple/ftfy/`\n")
			},
			want: "couldn't check the installed uv tools for newer versions: uv tool list failed with exit code 2:\n  error: Request failed after 3 retries\n  Caused by: Failed to fetch: `https://pypi.org/simple/ftfy/`",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			_, err := f.uv.Tool(tt.tool).Check(t.Context(), f.rt)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Check() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		tool    string
		command string
		want    state.Record
	}{
		{
			tool:    "azure-cli",
			command: "tool upgrade azure-cli",
			want: state.Record{
				Paths:   []string{"/home/u/.local/bin/az"},
				Details: map[string]string{"name": "azure-cli", "version": "2.68.0", "dir": "/home/u/.local/share/uv/tools/azure-cli"},
			},
		},
		{
			tool:    "Codespell",
			command: "tool install Codespell",
			want: state.Record{
				Paths:   []string{"/home/u/.local/bin/codespell"},
				Details: map[string]string{"name": "Codespell", "version": "2.3.0", "dir": "/home/u/.local/share/uv/tools/codespell"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			f := newFixture(t)
			f.on("tool list --color never", 0, "azure-cli v2.67.0\n- az\n", "")
			f.on("tool install", 0, "", "Installed 1 executable: codespell\n")
			f.on("tool upgrade", 0, "", "Updated azure-cli v2.67.0 -> v2.68.0\n")
			f.on("tool list --show-paths", 0, pathsOutput, "")
			tool := f.uv.Tool(tt.tool)
			if _, err := tool.Check(t.Context(), f.rt); err != nil {
				t.Fatal(err)
			}
			rec, err := tool.Apply(t.Context(), f.rt)
			if err != nil {
				t.Fatal(err)
			}
			tt.want.Details["uv"] = f.uv.Path
			if !slices.Equal(rec.Paths, tt.want.Paths) || !maps.Equal(rec.Details, tt.want.Details) {
				t.Errorf("Apply() = %+v, want %+v", rec, tt.want)
			}
			cmds := f.runner.Commands()
			if !slices.Contains(cmds, system.Cmd{Name: f.uv.Path}.String()+" "+tt.command) {
				t.Errorf("commands = %q, want %q", cmds, tt.command)
			}
			for _, c := range f.runner.Calls() {
				if !slices.Contains(c.Cmd.Env, "UV_NO_PROGRESS=1") {
					t.Errorf("%s ran without the uv's environment", c.Cmd)
				}
			}
		})
	}
}

func TestApplyFails(t *testing.T) {
	f := newFixture(t)
	f.on("tool install", 2, "", "error: Executable already exists: ftfy (use `--force` to overwrite)\n")
	tool := f.uv.Tool("ftfy-cli")
	_, err := tool.Apply(t.Context(), f.rt)
	want := "uv tool install ftfy-cli failed with exit code 2: error: Executable already exists: ftfy (use `--force` to overwrite)"
	if err == nil || err.Error() != want {
		t.Errorf("Apply() error = %v, want %q", err, want)
	}
}

func TestRemover(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uv tools are only removed on Linux, where uv links their executables")
	}
	r := Remover{}
	if r.Kind() != Kind {
		t.Errorf("Kind() = %q, want %q", r.Kind(), Kind)
	}

	tests := []struct {
		name      string
		uv        func(t *testing.T, f *fixture, uv string)
		installed bool
		want      resource.Change
		commands  []string
	}{
		{
			name:      "with uv",
			installed: true,
			uv: func(_ *testing.T, f *fixture, uv string) {
				f.runner.On(uv+" tool uninstall", func(context.Context, system.Cmd) (system.Result, error) {
					// uv deletes the tool itself.
					return system.Result{Stdout: []byte("Uninstalled 1 executable: az\n")}, os.RemoveAll(filepath.Join(f.rt.Paths.Home, "tools"))
				})
			},
			want:     resource.Change{Action: resource.Remove, Summary: "azure-cli 2.68.0"},
			commands: []string{"UV tool uninstall azure-cli"},
		},
		{
			name:      "uv doesn't know it",
			installed: true,
			uv: func(_ *testing.T, f *fixture, uv string) {
				f.runner.On(uv+" tool uninstall", systemtest.Fail(2, "error: `azure-cli` is not installed\n"))
			},
			want:     resource.Change{Action: resource.Remove, Summary: "azure-cli 2.68.0"},
			commands: []string{"UV tool uninstall azure-cli"},
		},
		{
			name:      "without uv",
			installed: true,
			uv: func(t *testing.T, f *fixture, uv string) {
				t.Helper()
				if err := os.Remove(uv); err != nil {
					t.Fatal(err)
				}
				f.runner.On("uv", func(_ context.Context, c system.Cmd) (system.Result, error) {
					return system.Result{ExitCode: -1}, fmt.Errorf("%s: uv isn't installed: %w", c.Label, exec.ErrNotFound)
				})
			},
			want:     resource.Change{Action: resource.Remove, Summary: "azure-cli 2.68.0"},
			commands: []string{"uv tool uninstall azure-cli"},
		},
		{
			name: "already gone",
			want: resource.Change{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			home := f.rt.Paths.Home
			dir := filepath.Join(home, "tools", "azure-cli")
			exe := filepath.Join(home, "bin", "az")
			mine := filepath.Join(home, "bin", "mine")
			writeFile(t, mine, "")
			rec := state.Record{
				ID:      "uv-tool:azure-cli",
				Kind:    Kind,
				Paths:   []string{exe, mine},
				Details: map[string]string{"name": "azure-cli", "version": "2.68.0", "dir": dir, "uv": f.uv.Path},
			}
			if tt.installed {
				writeFile(t, filepath.Join(dir, "bin", "az"), "")
				if err := os.Symlink(filepath.Join("..", "tools", "azure-cli", "bin", "az"), exe); err != nil {
					t.Fatal(err)
				}
			}
			if tt.uv != nil {
				tt.uv(t, f, f.uv.Path)
			}

			got, err := r.CheckRemove(t.Context(), f.rt, rec)
			if err != nil {
				t.Fatal(err)
			}
			assertChange(t, got, tt.want)
			if got.Action == resource.Remove {
				if err := r.Remove(t.Context(), f.rt, rec); err != nil {
					t.Fatal(err)
				}
			}
			for _, p := range []string{dir, exe} {
				if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("Remove() left %s (%v)", p, err)
				}
			}
			if _, err := os.Lstat(mine); err != nil {
				t.Errorf("Remove() deleted %s, which isn't the tool's: %v", mine, err)
			}
			var want []string
			for _, c := range tt.commands {
				want = append(want, strings.Replace(c, "UV", f.uv.Path, 1))
			}
			if got := f.runner.Commands(); !slices.Equal(got, want) {
				t.Errorf("commands = %q, want %q", got, want)
			}
		})
	}
}

func TestRemoverWithoutPaths(t *testing.T) {
	// A record from an install whose paths couldn't be listed: only uv
	// knows whether the tool is there.
	f := newFixture(t)
	rec := state.Record{ID: "uv-tool:ftfy", Kind: Kind}
	got, err := Remover{}.CheckRemove(t.Context(), f.rt, rec)
	if err != nil {
		t.Fatal(err)
	}
	assertChange(t, got, resource.Change{Action: resource.Remove, Summary: "ftfy"})
	f.runner.On("uv tool uninstall ftfy", systemtest.Respond(0, ""))
	if err := (Remover{}).Remove(t.Context(), f.rt, rec); err != nil {
		t.Fatal(err)
	}
	if want := []string{"uv tool uninstall ftfy"}; !slices.Equal(f.runner.Commands(), want) {
		t.Errorf("commands = %q, want %q", f.runner.Commands(), want)
	}
}

func TestResource(t *testing.T) {
	tool := (&UV{}).Tool("Azure_CLI")
	if tool.ID() != "uv-tool:azure-cli" || tool.Kind() != Kind || tool.Privileged() || tool.Targets() != nil {
		t.Errorf("ID, Kind, Privileged, Targets = %q, %q, %t, %q", tool.ID(), tool.Kind(), tool.Privileged(), tool.Targets())
	}
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func assertChange(t *testing.T, got, want resource.Change) {
	t.Helper()
	if got.Action != want.Action || got.Summary != want.Summary || !slices.Equal(got.Notes, want.Notes) {
		t.Errorf("change = %s %q %q, want %s %q %q", got.Action, got.Summary, got.Notes, want.Action, want.Summary, want.Notes)
	}
}
