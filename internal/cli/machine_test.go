package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/settings"
)

// machineFiles is a config repo with machines for several systems.
var machineFiles = map[string]string{
	"maisys.toml":              "config_version = 1\n",
	"machines/fedora-a.toml":   "os = \"fedora\"\n",
	"machines/fedora-b.toml":   "os = \"fedora\"\narch = \"amd64\"\n",
	"machines/fedora-arm.toml": "os = \"fedora\"\narch = \"arm64\"\n",
	"machines/ubuntu-a.toml":   "os = \"ubuntu\"\n",
	"machines/win.toml":        "os = \"windows\"\n",
	"machines/broken.toml":     "os = \"fedora\"\nprofiles = [\"missing\"]\n",
	"machines/-odd.toml":       "os = \"fedora\"\n", // not a valid machine name
}

func newMachineRepo(t *testing.T, saved string) *configRepo {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, machineFiles)
	r := &configRepo{
		Repo: &config.Repo{FS: os.DirFS(dir), Modules: modules.All()},
		dir:  dir,
	}
	if saved != "" {
		r.settings = &settings.Settings{RepoDir: dir, Machine: saved}
	}
	return r
}

// selectWith runs selectMachine in e, and returns what it printed too.
func selectWith(t *testing.T, e *testEnv, stdin io.Reader, r *configRepo, req machineRequest) (name, source, stdout string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := e.environment(&out, &errOut)
	if stdin != nil {
		env.stdin = stdin
	}
	g := newGlobalOptions(nil, env)
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetContext(t.Context())
	name, source, err = g.selectMachine(cmd, r, req)
	if errOut.Len() > 0 {
		t.Errorf("selectMachine wrote to stderr: %q", errOut.String())
	}
	return name, source, out.String(), err
}

func TestSelectMachineOrder(t *testing.T) {
	tests := []struct {
		name       string
		flag       string
		env        string
		saved      string
		hostname   string
		want       string
		wantSource string
	}{
		{"the flag comes first", "fedora-b", "ubuntu-a", "win", "fedora-a", "fedora-b", fromFlag},
		{"then MAISYS_MACHINE", "", "ubuntu-a", "win", "fedora-a", "ubuntu-a", fromEnv},
		{"then the local settings", "", "", "win", "fedora-a", "win", fromSettings},
		{"then the hostname", "", "", "", "fedora-a", "fedora-a", fromHostname},
		{"the hostname in lower case", "", "", "", "Fedora-A", "fedora-a", fromHostname},
		{"a machine for another system", "", "win", "", "", "win", fromEnv},
		{"a machine that doesn't load", "", "broken", "", "", "broken", fromEnv},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.vars[machineEnv] = tt.env
			e.system.Hostname = tt.hostname
			r := newMachineRepo(t, tt.saved)
			name, source, _, err := selectWith(t, e, nil, r, machineRequest{flag: tt.flag, hasFlag: true})
			if err != nil {
				t.Fatal(err)
			}
			if name != tt.want || source != tt.wantSource {
				t.Errorf("selectMachine = %q from %q, want %q from %q", name, source, tt.want, tt.wantSource)
			}
		})
	}
}

func TestSelectMachineErrors(t *testing.T) {
	fedoraMachines := "machines for this system (fedora 44, amd64): fedora-a, fedora-b\n" +
		"skipped machine files that don't load: broken (run `maisys config validate --all` to see why)"
	tests := []struct {
		name     string
		flag     string
		hasFlag  bool
		env      string
		saved    string
		hostname string
		pick     bool // the picker is allowed, but there's no terminal
		system   *facts.System
		want     []string // %s is the config repo's folder
	}{
		{
			name: "--machine names a missing machine",
			flag: "nope", hasFlag: true, env: "fedora-a",
			want: []string{`machines/nope.toml doesn't exist in %s (the machine name "nope" comes from --machine)` + "\n" + fedoraMachines},
		},
		{
			name: "MAISYS_MACHINE names a missing machine",
			env:  "nope", saved: "fedora-a",
			want: []string{`machines/nope.toml doesn't exist in %s (the machine name "nope" comes from MAISYS_MACHINE)` + "\n" + fedoraMachines},
		},
		{
			name:  "the local settings name a missing machine",
			saved: "nope", hostname: "fedora-a",
			want: []string{`machines/nope.toml doesn't exist in %s (the machine name "nope" comes from the local settings)`},
		},
		{
			name:     "the hostname isn't a machine",
			hostname: "DESKTOP-7Q2K3F",
			want: []string{
				`can't tell which machine this is: no machine file in %s is named after the hostname "desktop-7q2k3f"` + "\n" + fedoraMachines,
				"\nset MAISYS_MACHINE to choose one",
			},
		},
		{
			name:     "the hostname isn't a valid machine name",
			hostname: "-odd",
			want:     []string{`is named after the hostname "-odd"`},
		},
		{
			name:     "no terminal for the picker",
			hostname: "nope", hasFlag: true, pick: true,
			want: []string{"\nchoose one with --machine or MAISYS_MACHINE"},
		},
		{
			name:   "arm64 machines",
			system: &facts.System{OS: config.Fedora, OSVersion: "44", Arch: config.ARM64, RawArch: "arm64", Hostname: "nope"},
			want:   []string{"machines for this system (fedora 44, arm64): fedora-a, fedora-arm\n"},
		},
		{
			name:   "no machine for this system",
			system: &facts.System{OSID: "debian", OSVersion: "13", Arch: config.AMD64, RawArch: "amd64", Hostname: "nope"},
			want:   []string{"no machine file in %s is for this system (debian 13, amd64)\nskipped machine files that don't load: broken"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.vars[machineEnv] = tt.env
			e.system.Hostname = tt.hostname
			if tt.system != nil {
				e.system = *tt.system
			}
			r := newMachineRepo(t, tt.saved)
			name, _, out, err := selectWith(t, e, nil, r, machineRequest{flag: tt.flag, hasFlag: tt.hasFlag, pick: tt.pick})
			if err == nil {
				t.Fatalf("selectMachine = %q, want an error", name)
			}
			for _, want := range tt.want {
				want = strings.ReplaceAll(want, "%s", r.dir)
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error doesn't contain %q:\n%v", want, err)
				}
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
		})
	}
}

func TestPickMachine(t *testing.T) {
	const prompt = "Enter a number from 1 to 2, or nothing to cancel: "
	tests := []struct {
		name    string
		stdin   string
		system  *facts.System
		want    string
		wantErr string
		wantOut []string
	}{
		{
			name:  "a valid choice",
			stdin: "2\n",
			want:  "fedora-b",
			wantOut: []string{
				"maisys: which machine is this? The machines for this system (fedora 44, amd64) are:\n" +
					"  1) fedora-a\n" +
					"  2) fedora-b\n" +
					"maisys: skipped machine files that don't load: broken (run `maisys config validate --all` to see why)\n" +
					prompt,
			},
		},
		{
			name:  "bad answers, then a valid one",
			stdin: "x\n0\n3\n 1 \n",
			want:  "fedora-a",
			wantOut: []string{
				prompt + `maisys: "x" isn't one of the numbers above` + "\n" +
					prompt + `maisys: "0" isn't one of the numbers above` + "\n" +
					prompt + `maisys: "3" isn't one of the numbers above` + "\n" +
					prompt,
			},
		},
		{name: "a name", stdin: "fedora-b\r\n", want: "fedora-b"},
		{name: "a choice without a line ending", stdin: "1", want: "fedora-a"},
		{name: "an empty answer cancels", stdin: "\nignored\n", wantErr: "no machine chosen"},
		{name: "stdin ends", stdin: "", wantErr: "no machine chosen"},
		{name: "stdin ends after a bad answer", stdin: "x\n7", wantErr: `no machine chosen: "7" isn't one of the numbers`},
		{
			name:    "machines filtered by arch",
			stdin:   "2\n",
			system:  &facts.System{OS: config.Fedora, Arch: config.ARM64, RawArch: "arm64", Hostname: "nope"},
			want:    "fedora-arm",
			wantOut: []string{"(fedora, arm64) are:\n  1) fedora-a\n  2) fedora-arm\nmaisys: skipped"},
		},
		{
			name:    "machines filtered by os",
			stdin:   "1\n",
			system:  &facts.System{OS: config.Windows, OSVersion: "26100", Arch: config.AMD64, Hostname: "DESKTOP-7Q2K3F"},
			want:    "win",
			wantOut: []string{"(windows 26100, amd64) are:\n  1) win\nmaisys: skipped"},
		},
		{
			name:    "no machine for this system",
			stdin:   "1\n",
			system:  &facts.System{OSID: "debian", OSVersion: "13", RawArch: "riscv64", Hostname: "nope"},
			wantErr: "no machine file in %s is for this system (debian 13, riscv64); `maisys config init` can create one\nskipped machine files that don't load: broken",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.terminal = true
			if tt.system != nil {
				e.system = *tt.system
			}
			r := newMachineRepo(t, "")
			name, source, out, err := selectWith(t, e, strings.NewReader(tt.stdin), r, machineRequest{pick: true})
			if tt.wantErr != "" {
				want := strings.ReplaceAll(tt.wantErr, "%s", r.dir)
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("selectMachine = %q, %v; want an error containing %q", name, err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if name != tt.want || source != fromPicker {
				t.Errorf("selectMachine = %q from %q, want %q from %q", name, source, tt.want, fromPicker)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("stdout doesn't contain %q:\n%s", want, out)
				}
			}
			for _, unwanted := range []string{"ubuntu-a", "odd"} {
				if strings.Contains(out, unwanted) {
					t.Errorf("stdout lists %s:\n%s", unwanted, out)
				}
			}
		})
	}
}

func TestPickMachineInterrupted(t *testing.T) {
	e := newTestEnv(t)
	e.terminal = true
	stdin, w := io.Pipe() // nothing is ever written, so reading waits
	t.Cleanup(func() { _ = w.Close() })

	// Cancel, as Ctrl-C does, so the question stops waiting.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var out bytes.Buffer
	env := e.environment(&out, &out)
	env.stdin = stdin
	g := newGlobalOptions(nil, env)
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetContext(ctx)
	_, _, err := g.selectMachine(cmd, newMachineRepo(t, ""), machineRequest{pick: true})
	if !errors.Is(err, errNoMachineChosen) || !errors.Is(err, context.Canceled) {
		t.Errorf("selectMachine error = %v, want no machine chosen, because of context.Canceled", err)
	}
}
