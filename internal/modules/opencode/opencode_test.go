package opencode

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/installer"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		out   string
		v     string
		major int
		ok    bool
	}{
		{out: "1.4.2\n", v: "1.4.2", major: 1, ok: true},
		{out: "opencode v2.0.3\n", v: "2.0.3", major: 2, ok: true},
		{out: "v2.1.0", v: "2.1.0", major: 2, ok: true},
		{out: "opencode 10.0.0\n", v: "10.0.0", major: 10, ok: true},
		{out: "0.0.0-beta-17236\n", v: "0.0.0", major: 0, ok: true},
		{out: "", ok: false},
		{out: "opencode dev\n", ok: false},
		{out: "2.1\n", ok: false},
	}
	for _, tt := range tests {
		v, major, ok := ParseVersion(tt.out)
		if v != tt.v || major != tt.major || ok != tt.ok {
			t.Errorf("ParseVersion(%q) = %q, %d, %t, want %q, %d, %t", tt.out, v, major, ok, tt.v, tt.major, tt.ok)
		}
	}
}

// newRuntime returns a runtime on Fedora with a temporary home folder, and
// the fake runner it uses.
func newRuntime(t *testing.T) (*resource.Runtime, *systemtest.FakeRunner) {
	t.Helper()
	home := t.TempDir()
	runner := systemtest.NewFakeRunner(t)
	return &resource.Runtime{
		Runner: runner,
		Facts:  &facts.System{OS: config.Fedora, Arch: config.AMD64},
		Paths:  settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
	}, runner
}

func TestResources(t *testing.T) {
	rt, _ := newRuntime(t)
	rs, err := Module{}.Resources(&Config{Enabled: true}, rt)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
	}
	if want := []string{"package:opencode/curl", "package:opencode/tar", "installer:opencode"}; !slices.Equal(ids, want) {
		t.Fatalf("Resources() = %q, want %q", ids, want)
	}
	in, ok := rs[len(rs)-1].(*install)
	if !ok {
		t.Fatalf("Resources() ends with a %T, want an *install", rs[len(rs)-1])
	}
	bin := filepath.Join(rt.Paths.Home, ".opencode", "bin")
	switch {
	case in.URL != "https://opencode.ai/v2/install":
		t.Errorf("URL = %q, want the v2 installer", in.URL)
	case !slices.Equal(in.Args, []string{"--no-modify-path"}):
		t.Errorf("Args = %q, want --no-modify-path", in.Args)
	case in.bin != filepath.Join(bin, "opencode") || !slices.Equal(in.Targets(), []string{bin}):
		t.Errorf("bin, Targets = %q, %q, want opencode in %s", in.bin, in.Targets(), bin)
	case len(in.Leftovers) > 0:
		t.Errorf("Leftovers = %q, want none: the installer replaces opencode in one step", in.Leftovers)
	}
	if deps := (Module{}).DependsOn(); len(deps) > 0 {
		t.Errorf("DependsOn() = %q, want nothing", deps)
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name string
		// out is what opencode --version prints, with code its exit code;
		// code -1 means opencode isn't installed.
		out  string
		code int
		want resource.Change
		// complete is what the install's completeness check finds.
		complete installer.Status
	}{
		{
			name:     "not installed",
			code:     -1,
			want:     resource.Change{Action: resource.Create, Summary: "install opencode"},
			complete: installer.Status{},
		},
		{
			name:     "v1",
			out:      "1.4.2\n",
			want:     resource.Change{Action: resource.Update, Summary: "upgrade opencode 1.4.2 → 2 (installer)"},
			complete: installer.Status{Reason: "version 1.4.2 is older than 2"},
		},
		{
			name:     "v2",
			out:      "opencode v2.0.3\n",
			complete: installer.Status{Complete: true},
		},
		{
			name:     "v3",
			out:      "opencode v3.1.0\n",
			complete: installer.Status{Complete: true},
		},
		{
			name:     "broken",
			out:      "Segmentation fault\n",
			code:     139,
			want:     resource.Change{Action: resource.Create, Summary: "install opencode (opencode --version failed)"},
			complete: installer.Status{Reason: "opencode --version failed"},
		},
		{
			name:     "no version",
			out:      "opencode\n",
			want:     resource.Change{Action: resource.Create, Summary: "install opencode (opencode --version printed no version)"},
			complete: installer.Status{Reason: "opencode --version printed no version"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, runner := newRuntime(t)
			in := newInstall(rt.Paths.Home)
			if tt.code >= 0 {
				if err := os.MkdirAll(filepath.Dir(in.bin), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(in.bin, nil, 0o755); err != nil {
					t.Fatal(err)
				}
				runner.On(in.bin+" --version", systemtest.Respond(tt.code, tt.out))
			}

			c, err := in.Check(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(c, tt.want) {
				t.Errorf("Check() = %+v, want %+v", c, tt.want)
			}
			st, err := in.complete(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if st != tt.complete {
				t.Errorf("complete() = %+v, want %+v", st, tt.complete)
			}
			for _, call := range runner.Calls() {
				if call.Cmd.Root || call.Cmd.Name != in.bin {
					t.Errorf("ran %s, want only %s --version, as the user", call.Cmd, in.bin)
				}
			}
		})
	}
}

func TestShellEnv(t *testing.T) {
	got, err := Module{}.ShellEnv(&Config{}, &resource.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if want := (shells.Env{Path: []string{"~/.opencode/bin"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("ShellEnv() = %+v, want %+v", got, want)
	}
}
