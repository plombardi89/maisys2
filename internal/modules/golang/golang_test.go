package golang

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/goinstall"
	"github.com/plombardi89/maisys2/internal/resource/tool"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

const taskPkg = "github.com/go-task/task/v3/cmd/task"

func TestParseTool(t *testing.T) {
	tests := []struct {
		entry   string
		pkg, v  string
		wantErr string
	}{
		{entry: taskPkg + "@latest", pkg: taskPkg, v: "latest"},
		{entry: "golang.org/x/tools/cmd/goimports", pkg: "golang.org/x/tools/cmd/goimports", v: "latest"},
		{entry: "mvdan.cc/gofumpt@v0.9.2", pkg: "mvdan.cc/gofumpt", v: "v0.9.2"},
		{entry: "example.com/tool@v1.0.0-rc.1", pkg: "example.com/tool", v: "v1.0.0-rc.1"},
		{entry: "example.com/old@v2.0.0+incompatible", pkg: "example.com/old", v: "v2.0.0+incompatible"},
		{entry: "@v1.2.3", wantErr: "it has no package path"},
		{entry: "mvdan.cc/gofumpt@", wantErr: `the version must be "latest" or an exact version such as "v1.2.3", not ""`},
		{entry: "mvdan.cc/gofumpt@0.9.2", wantErr: `not "0.9.2"`},
		{entry: "mvdan.cc/gofumpt@v0.9", wantErr: `not "v0.9"`},
		{entry: "mvdan.cc/gofumpt@master", wantErr: `not "master"`},
		{entry: "mvdan.cc/gofumpt@v0.9.2+build", wantErr: `not "v0.9.2+build"`},
		{entry: "mvdan.cc/gofumpt@latest@latest", wantErr: `not "latest@latest"`},
		{entry: "mvdan.cc/gofumpt latest", wantErr: `"mvdan.cc/gofumpt latest" isn't a Go package path`},
		{entry: "-x@latest", wantErr: `"-x" isn't a Go package path`},
		{entry: "/abs/path@latest", wantErr: `"/abs/path" isn't a Go package path`},
	}
	for _, tt := range tests {
		pkg, v, err := ParseTool(tt.entry)
		switch {
		case tt.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("ParseTool(%q) error = %v, want one containing %q", tt.entry, err, tt.wantErr)
			}
		case err != nil:
			t.Errorf("ParseTool(%q) error = %v", tt.entry, err)
		case pkg != tt.pkg || v != tt.v:
			t.Errorf("ParseTool(%q) = %q, %q, want %q, %q", tt.entry, pkg, v, tt.pkg, tt.v)
		}
	}
}

// newRuntime returns a runtime with a temporary home folder, and the fake
// runner it uses.
func newRuntime(t *testing.T) (*resource.Runtime, *systemtest.FakeRunner) {
	t.Helper()
	home := t.TempDir()
	runner := systemtest.NewFakeRunner(t)
	return &resource.Runtime{
		Runner: runner,
		Facts:  &facts.System{OS: config.Ubuntu, Arch: config.ARM64},
		Paths:  settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
		Now:    time.Now,
	}, runner
}

// goBinary returns the go binary the module installs in rt's home folder.
func goBinary(rt *resource.Runtime) string {
	return filepath.Join(rt.Paths.Home, ".local", "bin", "go")
}

func TestResources(t *testing.T) {
	rt, _ := newRuntime(t)
	cfg := &Config{
		Enabled: true,
		Version: "1.26.4",
		Tools:   []string{taskPkg + "@latest", "golang.org/x/tools/cmd/goimports", "mvdan.cc/gofumpt@v0.9.2"},
	}
	rs, err := Module{}.Resources(cfg, rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	want := []string{"tool:go", "go-install:" + taskPkg, "go-install:golang.org/x/tools/cmd/goimports", "go-install:mvdan.cc/gofumpt"}
	if !slices.Equal(ids, want) {
		t.Fatalf("Resources() = %q, want %q", ids, want)
	}

	toolchain, ok := rs[0].(*tool.Tool)
	if !ok || toolchain.Spec.Name != "go" || toolchain.Version != "1.26.4" {
		t.Errorf("Resources()[0] = %#v, want the Go toolchain at 1.26.4", rs[0])
	}
	versions := []string{"latest", "latest", "v0.9.2"}
	for i, r := range rs[1:] {
		p, ok := r.(*goinstall.Program)
		if !ok {
			t.Fatalf("Resources()[%d] = %T, want a *goinstall.Program", i+1, r)
		}
		if p.Version != versions[i] || p.Go != goBinary(rt) || p.Resolve == nil {
			t.Errorf("%s: Version, Go = %q, %q, want %q, %q, with a resolver", p.ID(), p.Version, p.Go, versions[i], goBinary(rt))
		}
	}
	if deps := (Module{}).DependsOn(); len(deps) > 0 {
		t.Errorf("DependsOn() = %q, want nothing", deps)
	}
}

func TestResourcesInvalidTool(t *testing.T) {
	rt, _ := newRuntime(t)
	cfg := &Config{Version: config.Latest, Tools: []string{"mvdan.cc/gofumpt@master"}}
	_, err := Module{}.Resources(cfg, rt)
	if err == nil || !strings.Contains(err.Error(), `golang.tools has an invalid entry, "mvdan.cc/gofumpt@master"`) {
		t.Errorf("Resources() error = %v, want one about the invalid entry", err)
	}
}

// TestResourcesLastEntryWins checks that a program listed more than once,
// as when a machine file pins one a profile lists, is installed once, at
// the version of its last entry.
func TestResourcesLastEntryWins(t *testing.T) {
	rt, _ := newRuntime(t)
	cfg := &Config{
		Version: config.Latest,
		Tools:   []string{taskPkg + "@latest", "mvdan.cc/gofumpt", taskPkg + "@v3.40.0", "mvdan.cc/gofumpt@latest"},
	}
	rs, err := Module{}.Resources(cfg, rt)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs[1:] {
		p, ok := r.(*goinstall.Program)
		if !ok {
			t.Fatalf("%s is a %T, want a *goinstall.Program", r.ID(), r)
		}
		got = append(got, p.Package+"@"+p.Version)
	}
	if want := []string{taskPkg + "@v3.40.0", "mvdan.cc/gofumpt@latest"}; !slices.Equal(got, want) {
		t.Errorf("Resources() = %q, want %q", got, want)
	}
}

func TestShellEnv(t *testing.T) {
	rt, _ := newRuntime(t)
	got, err := Module{}.ShellEnv(&Config{}, rt)
	if err != nil {
		t.Fatal(err)
	}
	want := shells.Env{
		Path:      []string{"~/.local/bin", "$GOPATH/bin"},
		Variables: []shells.Variable{{Name: "GOPATH", Value: "~/go"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ShellEnv() = %+v, want %+v", got, want)
	}
	for _, v := range got.Variables {
		if v.Name == "GOROOT" {
			t.Error("ShellEnv() sets GOROOT")
		}
	}
}

// directLookups makes the Go module lookups go directly to the modules'
// repos, as for private modules, so they need go list.
func directLookups(t *testing.T) {
	t.Helper()
	t.Setenv("GOPROXY", "direct")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOENV", "off")
}

// fakeGo answers the go commands a lookup and install of task run with the
// go binary goBin, which puts programs in gopath/bin.
func fakeGo(runner *systemtest.FakeRunner, goBin, gopath string) {
	runner.On(goBin+" list -m -json", systemtest.Fail(1, "go: not a module"))
	runner.On(goBin+" list -m -json github.com/go-task/task/v3@latest", systemtest.Respond(0, `{"Path":"github.com/go-task/task/v3","Version":"v3.41.0"}`))
	runner.On(goBin+" env GOBIN GOPATH GOEXE", systemtest.Respond(0, "\n"+gopath+"\n\n"))
	runner.On(goBin+" install "+taskPkg+"@v3.41.0", systemtest.Respond(0, ""))
}

// installedWith returns the go binaries that ran go install.
func installedWith(runner *systemtest.FakeRunner) []string {
	var bins []string
	for _, c := range runner.Calls() {
		if len(c.Cmd.Args) > 0 && c.Cmd.Args[0] == "install" {
			bins = append(bins, c.Cmd.Name+" "+strings.Join(c.Cmd.Args, " "))
		}
	}
	return bins
}

func TestProgramDirectLookup(t *testing.T) {
	directLookups(t)
	rt, runner := newRuntime(t)
	goBin := goBinary(rt)
	writeFile(t, goBin)
	fakeGo(runner, goBin, filepath.Join(rt.Paths.Home, "go"))

	rs, err := Module{}.Resources(&Config{Version: config.Latest, Tools: []string{taskPkg}}, rt)
	if err != nil {
		t.Fatal(err)
	}
	p := rs[1]
	c, err := p.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if want := (resource.Change{Action: resource.Create, Summary: "task v3.41.0 (latest)"}); !reflect.DeepEqual(c, want) {
		t.Errorf("Check() = %+v, want %+v", c, want)
	}
	if _, err := p.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	if got, want := installedWith(runner), []string{goBin + " install " + taskPkg + "@v3.41.0"}; !slices.Equal(got, want) {
		t.Errorf("go install ran as %q, want %q", got, want)
	}
	for _, c := range runner.Calls() {
		if c.Cmd.Name != goBin {
			t.Errorf("ran %s, want only the managed go", c.Cmd)
		}
	}
}

func TestProgramLookupWaitsForGo(t *testing.T) {
	directLookups(t)
	rt, runner := newRuntime(t)
	goBin := goBinary(rt)

	rs, err := Module{}.Resources(&Config{Version: config.Latest, Tools: []string{taskPkg + "@latest"}}, rt)
	if err != nil {
		t.Fatal(err)
	}
	p := rs[1]
	c, err := p.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if want := (resource.Change{Action: resource.Create, Summary: "task (latest)"}); !reflect.DeepEqual(c, want) {
		t.Errorf("Check() = %+v, want %+v", c, want)
	}
	if calls := runner.Calls(); len(calls) > 0 {
		t.Errorf("Check() ran %s, want nothing before Go is installed", calls[0].Cmd)
	}

	// The toolchain is installed before the program is applied.
	writeFile(t, goBin)
	fakeGo(runner, goBin, filepath.Join(rt.Paths.Home, "go"))
	rec, err := p.Apply(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := installedWith(runner), []string{goBin + " install " + taskPkg + "@v3.41.0"}; !slices.Equal(got, want) {
		t.Errorf("go install ran as %q, want %q", got, want)
	}
	if rec.Details["version"] != "v3.41.0" || rec.Details["go"] != goBin {
		t.Errorf("Apply() recorded %q, want version v3.41.0 built with %s", rec.Details, goBin)
	}
}

func TestProgramExactVersion(t *testing.T) {
	directLookups(t)
	rt, runner := newRuntime(t)

	rs, err := Module{}.Resources(&Config{Version: config.Latest, Tools: []string{taskPkg + "@v3.40.0"}}, rt)
	if err != nil {
		t.Fatal(err)
	}
	c, err := rs[1].Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if want := (resource.Change{Action: resource.Create, Summary: "task v3.40.0"}); !reflect.DeepEqual(c, want) {
		t.Errorf("Check() = %+v, want %+v", c, want)
	}
	if calls := runner.Calls(); len(calls) > 0 {
		t.Errorf("Check() ran %s, want nothing before Go is installed", calls[0].Cmd)
	}
}

// writeFile creates an empty file at path, and its folder.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}
