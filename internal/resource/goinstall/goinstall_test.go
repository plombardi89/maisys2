package goinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
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

// versionM returns realistic go version -m output for a binary at bin
// built from package pkg of module mod at version v.
func versionM(bin, pkg, mod, v string) string {
	return bin + ": go1.26.1\n" +
		"\tpath\t" + pkg + "\n" +
		"\tmod\t" + mod + "\t" + v + "\th1:VO2moBcbUaVlCLmpQI9c7GT3vJ8JrIpylS+TJq53FMQ=\n" +
		"\tdep\tcel.dev/expr\tv0.25.1\th1:1KrZg61W6TWSxuNZ37Xy49ps13NUovb66QLprthtwi4=\n" +
		"\tdep\tgithub.com/Masterminds/semver/v3\tv3.4.0\th1:Zog+i5UMtVoCU8oKka5P7i9q9HgrJeGzI9SA1Xbatp0=\n" +
		"\t=>\tgithub.com/fork/semver/v3\tv3.4.1\th1:4CRtRnuZOdFDTWSff9r8QFt/9+z6Emubz3aDMnf/dx0=\n" +
		"\tbuild\t-buildmode=exe\n" +
		"\tbuild\t-compiler=gc\n" +
		"\tbuild\tDefaultGODEBUG=cryptocustomrand=1,tlssecpmlkem=0,urlstrictcolons=0\n" +
		"\tbuild\tCGO_ENABLED=0\n" +
		"\tbuild\tGOARCH=amd64\n" +
		"\tbuild\tGOOS=linux\n" +
		"\tbuild\tGOAMD64=v1\n"
}

func TestParseVersionM(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want buildInfo
	}{
		{
			name: "task",
			out:  versionM("/home/u/go/bin/task", taskPkg, "github.com/go-task/task/v3", "v3.50.0"),
			want: buildInfo{path: taskPkg, module: "github.com/go-task/task/v3", version: "v3.50.0"},
		},
		{
			name: "package at the module root",
			out:  versionM("/home/u/go/bin/gofumpt", "mvdan.cc/gofumpt", "mvdan.cc/gofumpt", "v0.9.2"),
			want: buildInfo{path: "mvdan.cc/gofumpt", module: "mvdan.cc/gofumpt", version: "v0.9.2"},
		},
		{
			name: "pseudo-version",
			out:  versionM("/home/u/go/bin/goimports", "golang.org/x/tools/cmd/goimports", "golang.org/x/tools", "v0.44.1-0.20261101120000-0123456789ab"),
			want: buildInfo{path: "golang.org/x/tools/cmd/goimports", module: "golang.org/x/tools", version: "v0.44.1-0.20261101120000-0123456789ab"},
		},
		{
			name: "built from a checkout",
			out:  "/home/u/go/bin/foo: go1.27.3\n\tpath\texample.com/foo\n\tmod\texample.com/foo\t(devel)\t\n\tbuild\t-buildmode=exe\n",
			want: buildInfo{path: "example.com/foo", module: "example.com/foo", version: "(devel)"},
		},
		{
			name: "built from files",
			out:  "/home/u/go/bin/main: go1.27.3\n\tpath\tcommand-line-arguments\n\tbuild\t-buildmode=exe\n",
			want: buildInfo{path: "command-line-arguments"},
		},
		{name: "nothing", out: "", want: buildInfo{}},
		{name: "windows line endings", out: "C:\\go\\bin\\task.exe: go1.27.3\r\n\tpath\t" + taskPkg + "\r\n\tmod\tgithub.com/go-task/task/v3\tv3.41.0\th1:x=\r\n", want: buildInfo{path: taskPkg, module: "github.com/go-task/task/v3", version: "v3.41.0"}},
	}
	for _, tt := range tests {
		if got := parseVersionM(tt.out); got != tt.want {
			t.Errorf("%s: parseVersionM() = %+v, want %+v", tt.name, got, tt.want)
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
	writeFile(t, f.bin, "\x7fELF")
	f.runner.On(f.goBin+" version -m "+f.bin, systemtest.Respond(0, versionM(f.bin, pkg, mod, v)))
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
				f.runner.On(f.goBin+" version -m "+f.bin, systemtest.Fail(1, ""))
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
		{
			name:    "the toolchain isn't installed yet",
			version: "latest",
			setup: func(t *testing.T, f *fixture) {
				t.Helper()
				if err := os.Remove(f.goBin); err != nil {
					t.Fatal(err)
				}
				f.installed(t, taskPkg, "github.com/go-task/task/v3", "v3.41.0")
			},
			want: resource.Change{Action: resource.Create, Summary: "task v3.41.0 (latest)"},
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
				f.runner.On(f.goBin+" version -m "+f.bin, systemtest.Fail(1, f.bin+": could not read Go build info from "+f.bin+": not a Go executable\n"))
			},
			want: resource.Change{Notes: []string{"~/go/bin/task isn't a Go program any more (left in place)"}},
			kept: true,
		},
		{
			name: "the toolchain was removed",
			setup: func(t *testing.T, f *fixture, _ *state.Record) {
				t.Helper()
				if err := os.Remove(f.goBin); err != nil {
					t.Fatal(err)
				}
				writeFile(t, f.bin, "\x7fELF")
				f.runner.On("go version -m "+f.bin, systemtest.Respond(0, versionM(f.bin, taskPkg, "github.com/go-task/task/v3", "v3.41.0")))
			},
			want: resource.Change{Action: resource.Remove, Summary: "task v3.41.0"},
		},
		{
			name: "no go at all",
			setup: func(t *testing.T, f *fixture, rec *state.Record) {
				t.Helper()
				delete(rec.Details, "go")
				writeFile(t, f.bin, "\x7fELF")
				f.runner.On("go", func(_ context.Context, c system.Cmd) (system.Result, error) {
					return system.Result{ExitCode: -1}, fmt.Errorf("%s: go isn't installed: %w", c.Label, exec.ErrNotFound)
				})
			},
			want: resource.Change{Notes: []string{"~/go/bin/task can't be checked without go (left in place)"}},
			kept: true,
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
