package goinstall

import (
	"bytes"
	"context"
	"debug/plan9obj"
	encbinary "encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/version"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

const taskPkg = "github.com/go-task/task/v3/cmd/task"

// writeGoBinary writes a file debug/buildinfo reads as a Go program built
// from package pkg of module mod at version v: a Plan 9 a.out, the simplest
// executable format it reads, whose data segment is the build information
// the Go linker writes.
func writeGoBinary(t *testing.T, name, pkg, mod, v string) {
	t.Helper()
	modinfo := "path\t" + pkg + "\nmod\t" + mod + "\t" + v + "\th1:VO2moBcbUaVlCLmpQI9c7GT3vJ8JrIpylS+TJq53FMQ=\n" +
		"dep\tcel.dev/expr\tv0.25.1\th1:1KrZg61W6TWSxuNZ37Xy49ps13NUovb66QLprthtwi4=\n" +
		"build\t-buildmode=exe\nbuild\tGOOS=linux\n"
	// The linker puts 16 bytes before and after the module information.
	modinfo = strings.Repeat("\x00", 16) + modinfo + strings.Repeat("\x00", 16)
	blob := []byte("\xff Go buildinf:")
	blob = append(blob, 8, 2) // the pointer size, and the strings inline
	blob = append(blob, make([]byte, 16)...)
	for _, s := range []string{"go1.27.3", modinfo} {
		blob = encbinary.AppendUvarint(blob, uint64(len(s)))
		blob = append(blob, s...)
	}
	header := make([]byte, 32)
	encbinary.BigEndian.PutUint32(header, plan9obj.Magic386)
	encbinary.BigEndian.PutUint32(header[8:], uint32(len(blob)))
	writeFile(t, name, string(header)+string(blob))
}

func TestInspect(t *testing.T) {
	dir := t.TempDir()
	task := filepath.Join(dir, "task")
	writeGoBinary(t, task, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
	script := filepath.Join(dir, "script")
	writeFile(t, script, "#!/bin/sh\n")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self, ok := debug.ReadBuildInfo()
	if !ok {
		t.Fatal("the test binary has no build information")
	}

	tests := []struct {
		name    string
		bin     string
		want    buildInfo
		wantErr error
	}{
		{name: "go install", bin: task, want: buildInfo{path: taskPkg, version: "v3.41.0"}},
		{name: "a real Go binary", bin: exe, want: buildInfo{path: self.Path, version: self.Main.Version}},
		{name: "a script", bin: script, wantErr: errNotGo},
		{name: "missing", bin: filepath.Join(dir, "missing"), wantErr: fs.ErrNotExist},
	}
	for _, tt := range tests {
		got, err := inspect(tt.bin)
		if got != tt.want || !errors.Is(err, tt.wantErr) || (err == nil) != (tt.wantErr == nil) {
			t.Errorf("%s: inspect() = %+v, %v; want %+v, %v", tt.name, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestName(t *testing.T) {
	tests := map[string]string{
		taskPkg:                            "task",
		"golang.org/x/tools/cmd/goimports": "goimports",
		"mvdan.cc/gofumpt":                 "gofumpt",
		"github.com/magefile/mage":         "mage",
		"example.com/tool/v2":              "tool",
		"example.com/tool/v10":             "tool",
		"example.com/tool/v1":              "v1",
		"example.com/tool/v0":              "v0",
		"example.com/tool/v02":             "v02",
		"example.com/tool/vx":              "vx",
		"gopkg.in/yaml.v3":                 "yaml.v3",
	}
	for pkg, want := range tests {
		if got := (&Program{Package: pkg}).Name(); got != want {
			t.Errorf("Name() of %s = %q, want %q", pkg, got, want)
		}
	}
}

// fixture is a home folder with a fake go toolchain and runner.
type fixture struct {
	home   string
	goBin  string
	bin    string // where go install puts task
	runner *systemtest.FakeRunner
	rt     *resource.Runtime
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	home := t.TempDir()
	f := &fixture{
		home:   home,
		goBin:  filepath.Join(home, ".local", "share", "maisys", "tools", "go", "1.27.3", "bin", "go"),
		bin:    filepath.Join(home, "go", "bin", "task"),
		runner: systemtest.NewFakeRunner(t),
	}
	writeFile(t, f.goBin, "go")
	f.rt = &resource.Runtime{Runner: f.runner, Paths: settings.Paths{Home: home}}
	f.runner.On(f.goBin+" env GOBIN GOPATH GOEXE", systemtest.Respond(0, "\n"+filepath.Join(home, "go")+"\n\n"))
	return f
}

// installed puts a binary built from pkg of module mod at version v where
// go install puts task.
func (f *fixture) installed(t *testing.T, pkg, mod, v string) {
	t.Helper()
	writeGoBinary(t, f.bin, pkg, mod, v)
}

func (f *fixture) program(version string) *Program {
	return &Program{
		Package: taskPkg,
		Version: version,
		Resolve: func(_ context.Context, pkg string) (string, error) {
			if pkg != taskPkg {
				return "", fmt.Errorf("asked for the latest version of %s", pkg)
			}
			return "v3.41.0", nil
		},
		Go:  f.goBin,
		Env: []string{"GOFLAGS=-trimpath"},
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		version string
		setup   func(t *testing.T, f *fixture)
		want    resource.Change
	}{
		{
			name:    "missing",
			version: "v3.41.0",
			want:    resource.Change{Action: resource.Create, Summary: "task v3.41.0"},
		},
		{
			name:    "missing, latest",
			version: "latest",
			want:    resource.Change{Action: resource.Create, Summary: "task v3.41.0 (latest)"},
		},
		{
			name:    "up to date",
			version: "latest",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
			},
			want: resource.Change{},
		},
		{
			name:    "older",
			version: "latest",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.40.0")
			},
			want: resource.Change{Action: resource.Update, Summary: "task v3.40.0 → v3.41.0 (latest)"},
		},
		{
			name:    "newer than pinned",
			version: "v3.39.0",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
			},
			want: resource.Change{Action: resource.Update, Summary: "task v3.41.0 → v3.39.0"},
		},
		{
			name:    "built from a checkout",
			version: "v3.41.0",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "(devel)")
			},
			want: resource.Change{Action: resource.Update, Summary: "task (devel) → v3.41.0"},
		},
		{
			name:    "another program",
			version: "v3.41.0",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				f.installed(t, "github.com/go-task/task/v2/cmd/task", "github.com/go-task/task/v2", "v2.8.1")
			},
			want: resource.Change{Action: resource.Warn, Summary: "task: ~/go/bin/task is github.com/go-task/task/v2/cmd/task (will be replaced)"},
		},
		{
			name:    "not a Go program",
			version: "v3.41.0",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				writeFile(t, f.bin, "#!/bin/sh\n")
			},
			want: resource.Change{Action: resource.Warn, Summary: "task: ~/go/bin/task isn't a Go program (will be replaced)"},
		},
		{
			name:    "GOBIN",
			version: "v3.41.0",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				f.bin = filepath.Join(f.home, ".local", "bin", "task")
				f.runner.On(f.goBin+" env GOBIN GOPATH GOEXE", systemtest.Respond(0, filepath.Dir(f.bin)+"\n"+filepath.Join(f.home, "go")+"\n\n"))
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
			},
			want: resource.Change{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			p := f.program(tt.version)
			got, err := p.Check(t.Context(), f.rt)
			if err != nil {
				t.Fatal(err)
			}
			assertChange(t, got, tt.want)
			for _, c := range f.runner.Calls() {
				if !slices.Contains(c.Cmd.Env, "GOTOOLCHAIN=local") || !slices.Contains(c.Cmd.Env, "GOFLAGS=-trimpath") {
					t.Errorf("%s ran with environment %q, want GOTOOLCHAIN=local and the program's own", c.Cmd, c.Cmd.Env)
				}
			}
		})
	}
}

// TestCheckWithoutGo checks a program while the go binary doesn't exist
// yet, as when the same sync upgrades the toolchain to a new version's
// folder: the program is reinstalled only if its own version changes.
func TestCheckWithoutGo(t *testing.T) {
	tests := []struct {
		name    string
		version string
		env     []string          // the program's Env
		vars    map[string]string // maisys's own environment, which Check doesn't read: only Env, and the Runtime's home folder
		bin     string            // where the program is, relative to the home folder; "" means ~/go/bin/task
		at      string            // the version installed; "" means none
		want    resource.Change
	}{
		{name: "up to date", version: "latest", at: "v3.41.0", want: resource.Change{}},
		{name: "pinned", version: "v3.41.0", at: "v3.41.0", want: resource.Change{}},
		{name: "older", version: "latest", at: "v3.40.0", want: resource.Change{Action: resource.Update, Summary: "task v3.40.0 → v3.41.0 (latest)"}},
		{name: "missing", version: "latest", want: resource.Change{Action: resource.Create, Summary: "task v3.41.0 (latest)"}},
		{name: "GOBIN", version: "latest", env: []string{"GOBIN=~/bin"}, bin: "bin/task", at: "v3.41.0", want: resource.Change{}},
		{name: "GOPATH", version: "latest", env: []string{"GOPATH=~/gopath" + string(os.PathListSeparator) + "~/other"}, bin: "gopath/bin/task", at: "v3.41.0", want: resource.Change{}},
		{name: "the last entry wins", version: "latest", env: []string{"GOBIN=~/bin", "GOBIN=", "GOPATH=~/gopath"}, bin: "gopath/bin/task", at: "v3.41.0", want: resource.Change{}},
		{name: "not maisys's environment", version: "latest", vars: map[string]string{"GOBIN": "~/bin", "GOPATH": "~/gopath"}, at: "v3.41.0", want: resource.Change{}},
		{name: "somewhere else", version: "latest", bin: "elsewhere/task", at: "v3.41.0", want: resource.Change{Action: resource.Create, Summary: "task v3.41.0 (latest)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			if err := os.Remove(f.goBin); err != nil {
				t.Fatal(err)
			}
			home := func(s string) string { return strings.ReplaceAll(s, "~", f.home) }
			if tt.bin != "" {
				f.bin = filepath.Join(f.home, filepath.FromSlash(tt.bin))
			}
			if runtime.GOOS == "windows" {
				f.bin += ".exe"
			}
			if tt.at != "" {
				f.installed(t, taskPkg, "github.com/go-task/task/v3", tt.at)
			}
			for k, v := range tt.vars {
				t.Setenv(k, home(v))
			}
			p := f.program(tt.version)
			for _, kv := range tt.env {
				p.Env = append(p.Env, home(kv))
			}

			got, err := p.Check(t.Context(), f.rt)
			if err != nil {
				t.Fatal(err)
			}
			assertChange(t, got, tt.want)
			if got := f.runner.Commands(); len(got) != 0 {
				t.Errorf("Check() ran %q without go", got)
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		p    *Program
		want string
	}{
		{"no package", &Program{Version: "v1.0.0"}, `"" isn't a Go package path`},
		{"package with a version", &Program{Package: taskPkg + "@v3.41.0", Version: "v3.41.0"}, `"` + taskPkg + `@v3.41.0" isn't a Go package path`},
		{"no version", &Program{Package: taskPkg}, taskPkg + `: "" isn't a version`},
		{"latest without a resolver", &Program{Package: taskPkg, Version: "latest"}, taskPkg + ": nothing says what the latest version is"},
		{
			"resolver fails",
			&Program{Package: taskPkg, Version: "latest", Resolve: func(context.Context, string) (string, error) {
				return "", errors.New("proxy.golang.org: 503 Service Unavailable")
			}},
			"couldn't find the latest version of " + taskPkg + ": proxy.golang.org: 503 Service Unavailable",
		},
		{
			"resolver says latest",
			&Program{Package: taskPkg, Version: "latest", Resolve: func(context.Context, string) (string, error) { return "latest", nil }},
			`the latest version of ` + taskPkg + ` is "latest", which isn't a version`,
		},
	}
	for _, tt := range tests {
		_, err := tt.p.Check(t.Context(), f.rt)
		if err == nil || err.Error() != tt.want {
			t.Errorf("%s: Check() error = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestCheckBeforeGoIsInstalled(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.goBin); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	f.rt.Log = &log
	p := f.program("latest")
	// Like go list, the lookup needs go, which the same sync installs
	// first.
	p.Resolve = func(_ context.Context, pkg string) (string, error) {
		if _, err := os.Stat(f.goBin); err != nil {
			return "", &version.LookupError{Tool: pkg, Upstream: "go list -m (direct)", Err: err}
		}
		return "v3.41.0", nil
	}

	c, err := p.Check(t.Context(), f.rt)
	if err != nil {
		t.Fatalf("Check() error = %v, want the lookup left until go is installed", err)
	}
	assertChange(t, c, resource.Change{Action: resource.Create, Summary: "task (latest)"})
	if got := f.runner.Commands(); len(got) != 0 {
		t.Errorf("Check() ran %q before go is installed", got)
	}
	if !strings.Contains(log.String(), "looking up the latest version once go is installed") {
		t.Errorf("the log says %q, want why the version isn't known yet", log.String())
	}

	writeFile(t, f.goBin, "go")
	f.runner.On(f.goBin+" install", func(context.Context, system.Cmd) (system.Result, error) {
		writeFile(t, f.bin, "\x7fELF")
		return system.Result{}, nil
	})
	rec, err := p.Apply(t.Context(), f.rt)
	if err != nil {
		t.Fatal(err)
	}
	goCmd := system.Cmd{Name: f.goBin}.String()
	if got, want := f.runner.Commands(), []string{goCmd + " install " + taskPkg + "@v3.41.0", goCmd + " env GOBIN GOPATH GOEXE"}; !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if rec.Details["version"] != "v3.41.0" {
		t.Errorf("Apply() recorded version %q, want v3.41.0", rec.Details["version"])
	}

	// Once go is installed, a failed lookup stops the sync as any other.
	p = f.program("latest")
	p.Resolve = func(context.Context, string) (string, error) {
		return "", &version.LookupError{Tool: taskPkg, Upstream: "go list -m (direct)", Err: errors.New("exit code 1")}
	}
	if _, err := p.Check(t.Context(), f.rt); !resource.StopsSync(err) {
		t.Errorf("Check() with go installed: error = %v, want one that stops the sync", err)
	}
}

func TestApply(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.goBin); err != nil {
		t.Fatal(err)
	}
	p := f.program("latest")
	if _, err := p.Check(t.Context(), f.rt); err != nil {
		t.Fatal(err)
	}

	// The same sync installs the toolchain before the program.
	writeFile(t, f.goBin, "go")
	f.runner.On(f.goBin+" install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if !slices.Contains(c.Env, "GOTOOLCHAIN=local") || !slices.Contains(c.Env, "GOFLAGS=-trimpath") {
			t.Errorf("go install ran with environment %q, want GOTOOLCHAIN=local and the program's own", c.Env)
		}
		writeFile(t, f.bin, "\x7fELF")
		return system.Result{}, nil
	})
	rec, err := p.Apply(t.Context(), f.rt)
	if err != nil {
		t.Fatal(err)
	}
	// The runner quotes paths with backslashes, as on Windows.
	goCmd := system.Cmd{Name: f.goBin}.String()
	want := []string{goCmd + " install " + taskPkg + "@v3.41.0", goCmd + " env GOBIN GOPATH GOEXE"}
	if got := f.runner.Commands(); !slices.Equal(got, want) {
		t.Errorf("commands = %q, want %q", got, want)
	}
	details := map[string]string{"package": taskPkg, "version": "v3.41.0", "go": f.goBin}
	if !slices.Equal(rec.Paths, []string{f.bin}) || !maps.Equal(rec.Details, details) {
		t.Errorf("Apply() = %+v, want paths [%s] and details %v", rec, f.bin, details)
	}
}

func TestApplyFails(t *testing.T) {
	f := newFixture(t)
	f.runner.On(f.goBin+" install", systemtest.Fail(1, "go: github.com/go-task/task/v3@v3.99.0: invalid version: unknown revision v3.99.0\n"))
	p := f.program("v3.99.0")
	_, err := p.Apply(t.Context(), f.rt)
	want := "go install " + taskPkg + "@v3.99.0 failed with exit code 1: go: github.com/go-task/task/v3@v3.99.0: invalid version: unknown revision v3.99.0"
	if err == nil || err.Error() != want {
		t.Errorf("Apply() error = %v, want %q", err, want)
	}
}

func TestRemover(t *testing.T) {
	r := Remover{}
	if r.Kind() != Kind {
		t.Errorf("Kind() = %q, want %q", r.Kind(), Kind)
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, f *fixture, rec *state.Record)
		want  resource.Change
		kept  bool // whether the binary is still there after Remove
	}{
		{
			name: "installed",
			setup: func(t *testing.T, f *fixture, _ *state.Record) {
				t.Helper()
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
			},
			want: resource.Change{Action: resource.Remove, Summary: "task v3.41.0"},
		},
		{
			name: "gone",
			want: resource.Change{},
		},
		{
			name: "another program",
			setup: func(t *testing.T, f *fixture, _ *state.Record) {
				t.Helper()
				f.installed(t, "example.com/task", "example.com/task", "v1.0.0")
			},
			want: resource.Change{Notes: []string{"~/go/bin/task is example.com/task now (left in place)"}},
			kept: true,
		},
		{
			name: "not a Go program",
			setup: func(t *testing.T, f *fixture, _ *state.Record) {
				t.Helper()
				writeFile(t, f.bin, "#!/bin/sh\n")
			},
			want: resource.Change{Notes: []string{"~/go/bin/task isn't a Go program any more (left in place)"}},
			kept: true,
		},
		{
			name: "the toolchain was removed",
			setup: func(t *testing.T, f *fixture, rec *state.Record) {
				t.Helper()
				if err := os.Remove(f.goBin); err != nil {
					t.Fatal(err)
				}
				delete(rec.Details, "go")
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
			},
			want: resource.Change{Action: resource.Remove, Summary: "task v3.41.0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			rec := state.Record{
				ID:      "go-install:" + taskPkg,
				Kind:    Kind,
				Paths:   []string{f.bin},
				Details: map[string]string{"package": taskPkg, "version": "v3.41.0", "go": f.goBin},
			}
			if tt.setup != nil {
				tt.setup(t, f, &rec)
			}
			got, err := r.CheckRemove(t.Context(), f.rt, rec)
			if err != nil {
				t.Fatal(err)
			}
			assertChange(t, got, tt.want)
			if err := r.Remove(t.Context(), f.rt, rec); err != nil {
				t.Fatal(err)
			}
			_, err = os.Lstat(f.bin)
			if kept := !errors.Is(err, os.ErrNotExist); kept != tt.kept {
				t.Errorf("after Remove(), %s exists: %t, want %t", f.bin, kept, tt.kept)
			}
		})
	}
}

func TestResource(t *testing.T) {
	p := &Program{Package: taskPkg}
	if p.ID() != "go-install:"+taskPkg || p.Kind() != Kind || p.Privileged() || p.Targets() != nil {
		t.Errorf("ID, Kind, Privileged, Targets = %q, %q, %t, %q", p.ID(), p.Kind(), p.Privileged(), p.Targets())
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
