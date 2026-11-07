package sdkman_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/sdkman"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/installer"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// newRuntime returns a runtime on Ubuntu with a temporary home folder, and
// the fake runner it uses.
func newRuntime(t *testing.T) (*resource.Runtime, *systemtest.FakeRunner) {
	t.Helper()
	home := t.TempDir()
	runner := systemtest.NewFakeRunner(t)
	return &resource.Runtime{
		Runner: runner,
		Facts:  &facts.System{OS: config.Ubuntu, Arch: config.AMD64},
		Paths:  settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
	}, runner
}

func TestResources(t *testing.T) {
	rt, _ := newRuntime(t)
	rs, err := sdkman.Module{}.Resources(&sdkman.Config{Enabled: true}, rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	want := []string{"package:sdkman/curl", "package:sdkman/tar", "package:sdkman/unzip", "package:sdkman/zip", "installer:sdkman"}
	if !slices.Equal(ids, want) {
		t.Fatalf("Resources() = %q, want %q", ids, want)
	}

	in, ok := rs[len(rs)-1].(*installer.Installer)
	if !ok {
		t.Fatalf("Resources() ends with a %T, want an *installer.Installer", rs[len(rs)-1])
	}
	dir := filepath.Join(rt.Paths.Home, ".sdkman")
	switch {
	case in.URL != "https://get.sdkman.io?rcupdate=false":
		t.Errorf("URL = %q, want SDKMAN's installer without rc file updates", in.URL)
	case in.SHA256 != "":
		t.Errorf("SHA256 = %q, want none: the script changes with every release", in.SHA256)
	case !slices.Equal(in.Env, []string{"SDKMAN_DIR=" + dir}):
		t.Errorf("Env = %q, want SDKMAN_DIR=%s", in.Env, dir)
	case !slices.Equal(in.Leftovers, []string{dir}) || !slices.Equal(in.Paths, []string{dir}):
		t.Errorf("Leftovers, Paths = %q, %q, want %s", in.Leftovers, in.Paths, dir)
	case in.Complete == nil:
		t.Error("the installer has no completeness check")
	}

	if deps := (sdkman.Module{}).DependsOn(); len(deps) > 0 {
		t.Errorf("DependsOn() = %q, want nothing", deps)
	}
}

func TestComplete(t *testing.T) {
	tests := []struct {
		name string
		// files are created in ~/.sdkman.
		files []string
		// sdk is the exit code of sdk version, or -1 if it mustn't run.
		sdk  int
		want installer.Status
	}{
		{name: "not installed", sdk: -1, want: installer.Status{}},
		{name: "half-finished", files: []string{"bin/sdkman-init.sh", "var/version"}, sdk: -1, want: installer.Status{Reason: "var/version_native is missing"}},
		{name: "complete", files: []string{"bin/sdkman-init.sh", "var/version", "var/version_native"}, sdk: 0, want: installer.Status{Complete: true}},
		{name: "sdk doesn't load", files: []string{"var/version_native"}, sdk: 1, want: installer.Status{Reason: "sdk doesn't load"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, runner := newRuntime(t)
			dir := sdkman.Dir(rt.Paths.Home)
			for _, f := range tt.files {
				path := filepath.Join(dir, filepath.FromSlash(f))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tt.sdk >= 0 {
				runner.On("bash -c", systemtest.Respond(tt.sdk, "SDKMAN!\nscript: 5.24.0\nnative: 0.8.0\n"))
			}

			got, err := sdkman.Complete(dir)(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Complete() = %+v, want %+v", got, tt.want)
			}

			calls := runner.Calls()
			if tt.sdk < 0 {
				if len(calls) > 0 {
					t.Errorf("ran %s, want nothing", calls[0].Cmd)
				}
				return
			}
			if len(calls) != 1 {
				t.Fatalf("ran %d commands, want sdk version", len(calls))
			}
			c := calls[0].Cmd
			if c.Root || len(c.Args) != 2 || !strings.Contains(c.Args[1], `source "$SDKMAN_DIR/bin/sdkman-init.sh"`) || !strings.HasSuffix(c.Args[1], "sdk version") {
				t.Errorf("ran %s, want sdkman-init.sh sourced and sdk version run as the user", c)
			}
			if !slices.Equal(c.Env, []string{"SDKMAN_DIR=" + dir}) {
				t.Errorf("sdk version ran with %q, want SDKMAN_DIR=%s", c.Env, dir)
			}
		})
	}
}

// TestCheck checks what the plan says about an install in each state.
func TestCheck(t *testing.T) {
	rt, runner := newRuntime(t)
	rs, err := sdkman.Module{}.Resources(&sdkman.Config{}, rt)
	if err != nil {
		t.Fatal(err)
	}
	in := rs[len(rs)-1]

	c, err := in.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	if want := (resource.Change{Action: resource.Create, Summary: "install sdkman"}); !reflect.DeepEqual(c, want) {
		t.Errorf("Check() with nothing installed = %+v, want %+v", c, want)
	}

	if err := os.MkdirAll(filepath.Join(sdkman.Dir(rt.Paths.Home), "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err = in.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	want := resource.Change{
		Action:  resource.Update,
		Summary: "repair sdkman (var/version_native is missing)",
		Notes:   []string{"~/.sdkman will be deleted first"},
	}
	if !reflect.DeepEqual(c, want) {
		t.Errorf("Check() with a half-finished install = %+v, want %+v", c, want)
	}
	if calls := runner.Calls(); len(calls) > 0 {
		t.Errorf("Check() ran %s, want nothing", calls[0].Cmd)
	}
}

func TestShellEnv(t *testing.T) {
	got, err := sdkman.Module{}.ShellEnv(&sdkman.Config{}, &resource.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []shells.Variable{{Name: "SDKMAN_DIR", Value: "~/.sdkman"}}; !reflect.DeepEqual(got.Variables, want) {
		t.Errorf("Variables = %+v, want %+v", got.Variables, want)
	}
	if len(got.Path) > 0 {
		t.Errorf("Path = %q, want nothing: sdkman-init.sh sets it", got.Path)
	}
	if len(got.Init) != 1 {
		t.Fatalf("Init = %+v, want one step", got.Init)
	}
	step := got.Init[0]
	switch {
	case step.Name != "sdkman" || !step.Last:
		t.Errorf("Init = %+v, want the sdkman step, marked Last", step)
	case step.Fish != "":
		t.Errorf("Fish = %q, want none: SDKMAN has no fish integration", step.Fish)
	}
	for shell, s := range map[string]string{"bash": step.Bash, "zsh": step.Zsh} {
		if !strings.Contains(s, `source "$SDKMAN_DIR/bin/sdkman-init.sh"`) || !strings.Contains(s, `-s "$SDKMAN_DIR/bin/sdkman-init.sh"`) {
			t.Errorf("%s = %q, want sdkman-init.sh sourced if it's there", shell, s)
		}
	}
}
