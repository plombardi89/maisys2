package fixture_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/cli"
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/golang"
	"github.com/plombardi89/maisys2/test/fixture"
)

// TestValidate builds the fixture and checks it with maisys config validate
// --all, run in the repo. Since the fixture starts with what config init
// writes, this also checks that config init's output is valid.
func TestValidate(t *testing.T) {
	if _, err := gitrepo.Find(); err != nil {
		t.Skipf("building the fixture needs git: %v", err)
	}
	dir := t.TempDir()
	if err := fixture.Build(t.Context(), dir); err != nil {
		t.Fatal(err)
	}

	// maisys reads the local settings, but finds none in this home folder.
	home := t.TempDir()
	env := map[string]string{
		"HOME":         home,
		"USERPROFILE":  home,
		"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
		"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		"NO_COLOR":     "1",
	}
	t.Chdir(dir)
	var stdout, stderr bytes.Buffer
	code := cli.Execute(t.Context(), nil, []string{"config", "validate", "--all"}, &stdout, &stderr, func(name string) string { return env[name] })
	if code != 0 || stderr.Len() > 0 {
		t.Fatalf("maisys config validate --all exited with %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	machines := len(fixture.Distros) + 3 // and the Windows host and its two distros
	if want := fmt.Sprintf("maisys: all %d machines are valid\n", machines); stdout.String() != want {
		t.Errorf("maisys config validate --all printed %q, want %q", stdout.String(), want)
	}
}

// TestCommits checks that the fixture is committed in two commits, what
// config init writes and then the test machines, with nothing left out.
func TestCommits(t *testing.T) {
	git, err := gitrepo.Find()
	if err != nil {
		t.Skipf("building the fixture needs git: %v", err)
	}
	dir := t.TempDir()
	if err := fixture.Build(t.Context(), dir); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), git, "-C", dir, "log", "--format=%s")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "Add the test machines\nStart a maisys config\n"; string(out) != want {
		t.Errorf("git log shows the commits\n%s\nwant\n%s", out, want)
	}
	cmd = exec.CommandContext(t.Context(), git, "-C", dir, "status", "--porcelain")
	if out, err = cmd.Output(); err != nil || len(out) > 0 {
		t.Errorf("git status --porcelain: %v\n%s", err, out)
	}

	if err := fixture.Build(t.Context(), dir); err == nil {
		t.Error("Build() into a folder that isn't empty worked, want an error")
	}
}

// TestMachines checks the test machines: each declares its distro's os and
// os_version but no arch, and pins every version it can.
func TestMachines(t *testing.T) {
	files, err := fixture.Files()
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for name, f := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(f.Data), Mode: f.Mode}
	}
	r := &config.Repo{FS: fsys, Modules: modules.All()}

	for _, d := range fixture.Distros {
		t.Run(d.Name(), func(t *testing.T) {
			m, err := r.LoadMachine(d.Machine())
			if err != nil {
				t.Fatal(err)
			}
			if m.OS != d.OS || m.OSVersion != d.Version || m.Arch != "" {
				t.Errorf("the machine is os %q, os_version %q, arch %q; want %q, %q and no arch", m.OS, m.OSVersion, m.Arch, d.OS, d.Version)
			}
			for name, cfg := range m.Modules {
				if !cfg.IsEnabled() {
					continue
				}
				if v, ok := version(cfg); ok && !config.IsExactVersion(v) {
					t.Errorf("%s.version is %q, want an exact version", name, v)
				}
			}
			g, ok := m.Modules["golang"].(*golang.Config)
			if !ok || !g.Enabled {
				t.Fatal("golang isn't enabled")
			}
			for _, entry := range g.Tools {
				if _, v, err := golang.ParseTool(entry); err != nil || v == "latest" {
					t.Errorf("golang.tools has %q, want an exact version", entry)
				}
			}
		})
	}
}

// version returns the version a module's config asks for, if it has one.
func version(cfg config.ModuleConfig) (string, bool) {
	v := reflect.ValueOf(cfg)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", false
	}
	f := v.FieldByName("Versioned")
	if !f.IsValid() {
		return "", false
	}
	versioned, ok := reflect.TypeAssert[config.Versioned](f)
	return versioned.Version, ok
}
