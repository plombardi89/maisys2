package winget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// testSystem is a Windows machine for tests: a runtime with a fake runner
// that answers winget's commands.
type testSystem struct {
	t      *testing.T
	rt     *resource.Runtime
	runner *systemtest.FakeRunner
	m      *Manager
}

func newTestSystem(t *testing.T) *testSystem {
	t.Helper()
	s := &testSystem{t: t, runner: systemtest.NewFakeRunner(t)}
	s.rt = &resource.Runtime{
		Runner: s.runner,
		Facts:  &facts.System{OS: config.Windows, OSID: string(config.Windows), Arch: config.AMD64, RawArch: "amd64"},
	}
	m, err := ManagerFor(s.rt)
	if err != nil {
		t.Fatal(err)
	}
	m.tempDir = t.TempDir()
	s.m = m
	return s
}

// exported answers winget export by writing the file testdata/name, as
// winget does.
func (s *testSystem) exported(name string) {
	s.t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		s.t.Fatal(err)
	}
	s.runner.On("winget export", func(_ context.Context, c system.Cmd) (system.Result, error) {
		i := slices.Index(c.Args, "--output")
		if i < 0 || i+1 >= len(c.Args) {
			return system.Result{ExitCode: exitCode(invalidArguments)}, nil
		}
		if err := os.WriteFile(c.Args[i+1], data, 0o644); err != nil {
			return system.Result{ExitCode: -1}, err
		}
		return system.Result{Stdout: []byte("Exported package list to file.\n")}, nil
	})
}

// dsc answers winget dscv3 package --get as WinGet's DSC resource does:
// latest says, for each installed package, whether it's at its latest
// version.
func (s *testSystem) dsc(latest map[string]bool) {
	s.runner.On("winget dscv3 package --get", func(_ context.Context, c system.Cmd) (system.Result, error) {
		var in dscPackage
		if err := json.NewDecoder(c.Stdin).Decode(&in); err != nil {
			return system.Result{ExitCode: -1}, fmt.Errorf("reading the input: %w", err)
		}
		if in.Source != "winget" || in.MatchOption != "equals" {
			return system.Result{ExitCode: -1}, fmt.Errorf("unexpected input %+v", in)
		}
		out := map[string]any{"id": in.ID, "source": in.Source}
		useLatest, installed := latest[in.ID]
		out["_exist"] = installed
		if installed {
			out["version"] = "1.0"
			out["useLatest"] = useLatest
		}
		line, err := json.Marshal(out)
		return system.Result{Stdout: append(line, '\n')}, err
	})
}

// shows answers winget show for ids, which the source has, and fails with
// NO_APPLICATIONS_FOUND for any other ID.
func (s *testSystem) shows(ids ...string) {
	s.runner.On("winget show", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if len(c.Args) > 2 && slices.Contains(ids, c.Args[2]) {
			return system.Result{Stdout: []byte("Found " + c.Args[2] + "\nVersion: 1.0\n")}, nil
		}
		return system.Result{ExitCode: exitCode(noApplicationsFound), Stdout: []byte("No package found matching input criteria.\n")}, nil
	})
}

// on answers command prefix with a command that writes stdout and exits
// with code.
func (s *testSystem) on(prefix string, code uint32, stdout string) {
	s.runner.On(prefix, systemtest.Respond(exitCode(code), stdout))
}

// set returns the package set of the winget module for specs.
func (s *testSystem) set(specs ...Spec) []resource.Resource {
	return Set(s.rt, "winget", specs...)
}

// commands returns the command lines that ran.
func (s *testSystem) commands() []string {
	return s.runner.Commands()
}

func TestValidID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"Git.Git", true},
		{"Microsoft.WindowsTerminal", true},
		{"Microsoft.VisualStudio.2022.Community", true},
		{"jqlang.jq", true},
		{"9NBLGGH4NNS1", false}, // a Microsoft Store ID
		{"Git", false},
		{"Git.", false},
		{".Git", false},
		{"Git Git.Git", false},
		{"Git.Git\n", false},
		{"Git/Git.Git", false},
		{"-Git.Git", false},
		{"", false},
		{strings.Repeat("a", 33) + ".b", false},
	}
	for _, tt := range tests {
		if got := ValidID(tt.id); got != tt.want {
			t.Errorf("ValidID(%q) = %t, want %t", tt.id, got, tt.want)
		}
	}
}

func TestSet(t *testing.T) {
	s := newTestSystem(t)
	rs := s.set(Spec{ID: "Git.Git", Machine: true}, Spec{ID: "jqlang.jq"}, Spec{ID: "Git.Git"})
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID())
		if r.Kind() != Kind || len(r.Targets()) != 0 {
			t.Errorf("%s: kind %q, targets %q", r.ID(), r.Kind(), r.Targets())
		}
	}
	if want := []string{"winget:winget/Git.Git", "winget:winget/jqlang.jq"}; !slices.Equal(ids, want) {
		t.Errorf("IDs = %q, want %q", ids, want)
	}
	if !rs[0].Privileged() || rs[1].Privileged() {
		t.Errorf("Privileged = %t, %t; want only the system-wide Git.Git to need admin", rs[0].Privileged(), rs[1].Privileged())
	}
	if !s.m.wants("Git.Git") || !s.m.wants("jqlang.jq") || s.m.wants("Mozilla.Firefox") {
		t.Error("the manager doesn't know which packages the sync wants")
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		spec     Spec
		setup    func(s *testSystem)
		want     resource.Change
		wantErr  string
		wantCmds []string
	}{
		{
			name: "missing",
			spec: Spec{ID: "jqlang.jq"},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.shows("jqlang.jq")
			},
			want: resource.Change{Action: resource.Create, Summary: "install jqlang.jq"},
			wantCmds: []string{
				"winget export --output * --source winget --include-versions --accept-source-agreements --disable-interactivity",
				"winget show --id jqlang.jq --exact --source winget --accept-source-agreements --disable-interactivity",
			},
		},
		{
			name: "missing, system-wide",
			spec: Spec{ID: "Git.Git", Machine: true},
			setup: func(s *testSystem) {
				s.on("winget export", noApplicationsFound, "No installed package found matching input criteria.\n")
				s.shows("Git.Git")
			},
			want: resource.Change{Action: resource.Create, Summary: "install Git.Git (system-wide)"},
		},
		{
			name: "unknown package",
			spec: Spec{ID: "jqlang.jqq"},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.shows("jqlang.jq")
			},
			wantErr: "unknown winget package jqlang.jqq: the winget source doesn't have it",
		},
		{
			name:    "IDs are case-sensitive",
			spec:    Spec{ID: "git.git"},
			setup:   func(s *testSystem) { s.exported("export.json"); s.shows("Git.Git") },
			wantErr: "unknown winget package git.git",
		},
		{
			name:     "invalid ID",
			spec:     Spec{ID: "--force"},
			wantErr:  `"--force" isn't a valid winget package ID`,
			wantCmds: []string{},
		},
		{
			name: "latest",
			spec: Spec{ID: "Git.Git", Machine: true},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.dsc(map[string]bool{"Git.Git": true})
			},
			want: resource.Change{},
			wantCmds: []string{
				"winget export --output * --source winget --include-versions --accept-source-agreements --disable-interactivity",
				"winget dscv3 package --get",
			},
		},
		{
			name: "upgrade",
			spec: Spec{ID: "Microsoft.WindowsTerminal"},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.dsc(map[string]bool{"Microsoft.WindowsTerminal": false})
			},
			want: resource.Change{Action: resource.Update, Summary: "upgrade Microsoft.WindowsTerminal 1.24.11911.0 → latest"},
		},
		{
			name:     "unknown version",
			spec:     Spec{ID: "Mozilla.Firefox"},
			setup:    func(s *testSystem) { s.exported("export.json") },
			want:     resource.Change{},
			wantCmds: []string{"winget export --output * --source winget --include-versions --accept-source-agreements --disable-interactivity"},
		},
		{
			name: "winget without dscv3",
			spec: Spec{ID: "Git.Git"},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.on("winget dscv3", invalidArguments, "Unrecognized command: 'dscv3'\n")
			},
			want: resource.Change{Notes: []string{"upgrades not checked: this winget is too old to say whether there are any (update App Installer from the Microsoft Store)"}},
		},
		{
			name: "winget with dscv3 as an experimental feature",
			spec: Spec{ID: "Git.Git"},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.on("winget dscv3", experimentalFeatureDisabled, "This feature is a work in progress.\n")
			},
			want: resource.Change{Notes: []string{"upgrades not checked: this winget is too old to say whether there are any (update App Installer from the Microsoft Store)"}},
		},
		{
			name: "dscv3 says nothing",
			spec: Spec{ID: "Git.Git"},
			setup: func(s *testSystem) {
				s.exported("export.json")
				s.on("winget dscv3", 0, "\n")
			},
			wantErr: "reading what winget dscv3 package --get said about Git.Git: it wrote no JSON",
		},
		{
			name: "export fails",
			spec: Spec{ID: "Git.Git"},
			setup: func(s *testSystem) {
				s.on("winget export", 0x8A150045, "Failed when opening source(s); try the 'source reset' command if the problem persists.\n")
			},
			wantErr: "winget export failed with exit code 0x8A150045\n  Failed when opening source(s)",
		},
		{
			name:    "export writes something else",
			spec:    Spec{ID: "Git.Git"},
			setup:   func(s *testSystem) { s.exported("not-export.json") },
			wantErr: `reading what winget export wrote: it isn't a winget packages file: its $schema is ""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSystem(t)
			if tt.setup != nil {
				tt.setup(s)
			}
			r := s.set(tt.spec)[0]
			got, err := r.Check(t.Context(), s.rt)
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Check error = %v, want one containing %q", err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("Check: %v", err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("Check = %+v, want %+v", got, tt.want)
			}
			if tt.wantCmds != nil {
				assertCommands(t, s.commands(), tt.wantCmds)
			}
			if root := s.runner.RootCommands(); len(root) > 0 {
				t.Errorf("ran commands as root: %q", root)
			}
		})
	}
}

// TestCheckExportsOnce checks that every package of a sync shares one
// export, and that a winget without dscv3 is only asked once.
func TestCheckExportsOnce(t *testing.T) {
	s := newTestSystem(t)
	s.exported("export.json")
	s.on("winget dscv3", invalidArguments, "Unrecognized command: 'dscv3'\n")
	s.shows("jqlang.jq")
	for _, r := range s.set(Spec{ID: "Git.Git"}, Spec{ID: "Microsoft.WindowsTerminal"}, Spec{ID: "jqlang.jq"}) {
		if _, err := r.Check(t.Context(), s.rt); err != nil {
			t.Fatal(err)
		}
	}
	assertCommands(t, s.commands(), []string{
		"winget export --output * --source winget --include-versions --accept-source-agreements --disable-interactivity",
		"winget dscv3 package --get",
		"winget show --id jqlang.jq --exact --source winget --accept-source-agreements --disable-interactivity",
	})
}

func TestApply(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		dsc     map[string]bool // nil: not installed
		code    uint32          // winget install's or upgrade's exit code
		wantCmd string
		wantErr string
		reboot  bool
	}{
		{
			name:    "install",
			spec:    Spec{ID: "jqlang.jq"},
			wantCmd: "winget install --id jqlang.jq --exact --source winget --silent --accept-package-agreements --accept-source-agreements --disable-interactivity",
		},
		{
			name:    "install system-wide",
			spec:    Spec{ID: "jqlang.jq", Machine: true},
			wantCmd: "winget install --id jqlang.jq --exact --source winget --scope machine --silent --accept-package-agreements --accept-source-agreements --disable-interactivity",
		},
		{
			name: "install, already installed",
			spec: Spec{ID: "jqlang.jq"},
			code: packageAlreadyInstalled,
		},
		{
			name: "install, already installed and nothing newer",
			spec: Spec{ID: "jqlang.jq"},
			code: updateNotApplicable,
		},
		{
			name:    "upgrade",
			spec:    Spec{ID: "Microsoft.WindowsTerminal"},
			dsc:     map[string]bool{"Microsoft.WindowsTerminal": false},
			wantCmd: "winget upgrade --id Microsoft.WindowsTerminal --exact --source winget --silent --accept-package-agreements --accept-source-agreements --disable-interactivity",
		},
		{
			name:    "upgrade of a system-wide package keeps its scope",
			spec:    Spec{ID: "Git.Git", Machine: true},
			dsc:     map[string]bool{"Git.Git": false},
			wantCmd: "winget upgrade --id Git.Git --exact --source winget --silent --accept-package-agreements --accept-source-agreements --disable-interactivity",
		},
		{
			name: "upgrade, nothing newer by now",
			spec: Spec{ID: "Git.Git"},
			dsc:  map[string]bool{"Git.Git": false},
			code: updateNotApplicable,
		},
		{
			name:    "application running",
			spec:    Spec{ID: "Git.Git"},
			dsc:     map[string]bool{"Git.Git": false},
			code:    0x8A150101,
			wantErr: "winget upgrade Git.Git failed with exit code 0x8A150101: the application is running: exit it, then try again",
		},
		{
			name:    "no admin rights",
			spec:    Spec{ID: "jqlang.jq", Machine: true},
			code:    commandRequiresAdmin,
			wantErr: "winget install jqlang.jq failed with exit code 0x8A150019: it needs admin rights",
		},
		{
			name:    "unknown exit code",
			spec:    Spec{ID: "jqlang.jq"},
			code:    0x8A15FFFF,
			wantErr: "winget install jqlang.jq failed with exit code 0x8A15FFFF\n  winget said so",
		},
		{
			name:    "restart to finish",
			spec:    Spec{ID: "jqlang.jq"},
			code:    rebootRequiredToFinish,
			wantErr: "Windows must restart to finish installing jqlang.jq (winget exit code 0x8A150109)",
			reboot:  true,
		},
		{
			name:    "restart before installing",
			spec:    Spec{ID: "jqlang.jq"},
			code:    rebootRequiredForInstall,
			wantErr: "Windows must restart before winget can install jqlang.jq (winget exit code 0x8A15010A)",
			reboot:  true,
		},
		{
			name:    "restarting",
			spec:    Spec{ID: "Git.Git"},
			dsc:     map[string]bool{"Git.Git": false},
			code:    rebootInitiated,
			wantErr: "Windows is restarting to finish installing Git.Git (winget exit code 0x8A15010B)",
			reboot:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSystem(t)
			if tt.dsc == nil {
				s.on("winget export", noApplicationsFound, "")
				s.shows(tt.spec.ID)
			} else {
				s.exported("export.json")
				s.dsc(tt.dsc)
			}
			s.on("winget install", tt.code, "winget said so\n")
			s.on("winget upgrade", tt.code, "winget said so\n")

			r := s.set(tt.spec)[0]
			c, err := r.Check(t.Context(), s.rt)
			if err != nil || !c.Action.Applies() {
				t.Fatalf("Check = %+v, %v; want a change", c, err)
			}
			rec, err := r.Apply(t.Context(), s.rt)
			switch {
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Apply error = %v, want one containing %q", err, tt.wantErr)
				}
			case err != nil:
				t.Fatalf("Apply: %v", err)
			default:
				if rec.Details["id"] != tt.spec.ID {
					t.Errorf("record = %+v, want the package's ID in it", rec)
				}
			}
			var rebooter interface{ RebootRequired() bool }
			if got := errors.As(fmt.Errorf("wrapped: %w", err), &rebooter) && rebooter.RebootRequired(); got != tt.reboot {
				t.Errorf("reboot required = %t, want %t (error %v)", got, tt.reboot, err)
			}
			if tt.wantCmd != "" {
				cmds := s.commands()
				if last := cmds[len(cmds)-1]; last != tt.wantCmd {
					t.Errorf("ran %q, want %q", last, tt.wantCmd)
				}
			}
			if root := s.runner.RootCommands(); len(root) > 0 {
				t.Errorf("ran commands as root: %q; admin rights come from the elevated helper", root)
			}

			// What was installed is exported again after a change.
			if _, _, err := s.m.Installed(t.Context(), tt.spec.ID); err != nil {
				t.Fatal(err)
			}
			if n := countPrefix(s.commands(), "winget export"); n != 2 {
				t.Errorf("exported %d times, want 2: before and after the change", n)
			}
		})
	}
}

// TestApplyChecksFirst checks that a package that wasn't checked, as in
// the elevated helper, is checked before it's applied.
func TestApplyChecksFirst(t *testing.T) {
	s := newTestSystem(t)
	s.exported("export.json")
	s.dsc(map[string]bool{"Git.Git": true})
	r := s.set(Spec{ID: "Git.Git", Machine: true})[0]
	rec, err := r.Apply(t.Context(), s.rt)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Details["id"] != "Git.Git" {
		t.Errorf("record = %+v", rec)
	}
	assertCommands(t, s.commands(), []string{
		"winget export --output * --source winget --include-versions --accept-source-agreements --disable-interactivity",
		"winget dscv3 package --get",
	})
}

func TestRemover(t *testing.T) {
	tests := []struct {
		name string
		rec  state.Record
		want resource.Change
	}{
		{
			name: "installed",
			rec:  state.Record{ID: "winget:winget/Microsoft.WindowsTerminal", Kind: Kind, Details: map[string]string{"id": "Microsoft.WindowsTerminal"}},
			want: resource.Change{Action: resource.Orphan, Summary: "winget package Microsoft.WindowsTerminal"},
		},
		{
			name: "installed, no details",
			rec:  state.Record{ID: "winget:winget/Mozilla.Firefox", Kind: Kind},
			want: resource.Change{Action: resource.Orphan, Summary: "winget package Mozilla.Firefox"},
		},
		{
			name: "gone",
			rec:  state.Record{ID: "winget:winget/jqlang.jq", Kind: Kind, Details: map[string]string{"id": "jqlang.jq"}},
		},
		{
			name: "still wanted",
			rec:  state.Record{ID: "winget:old/Git.Git", Kind: Kind, Details: map[string]string{"id": "Git.Git"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestSystem(t)
			s.exported("export.json")
			s.set(Spec{ID: "Git.Git", Machine: true})
			got, err := Remover{}.CheckRemove(t.Context(), s.rt, tt.rec)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("CheckRemove = %+v, want %+v", got, tt.want)
			}
		})
	}

	if err := (Remover{}).Remove(t.Context(), nil, state.Record{}); err == nil {
		t.Error("Remove removed a winget package")
	}
}

func TestParseExport(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "export.json"))
	if err != nil {
		t.Fatal(err)
	}
	// winget may write a byte order mark.
	got, err := parseExport(append([]byte("\xef\xbb\xbf"), data...))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Git.Git": "2.56.0.3", "Microsoft.WindowsTerminal": "1.24.11911.0", "Mozilla.Firefox": "Unknown"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("parseExport = %v, want %v", got, want)
	}

	other := `{"$schema": "https://aka.ms/winget-packages.schema.2.0.json", "Sources": [{"Packages": [{"PackageIdentifier": "9NBLGGH4NNS1"}], "SourceDetails": {"Name": "msstore"}}]}`
	if got, err := parseExport([]byte(other)); err != nil || len(got) != 0 {
		t.Errorf("parseExport of another source's packages = %v, %v; want nothing", got, err)
	}
}

func TestParseDSCOutput(t *testing.T) {
	out := "Some progress\n{\"_exist\":true,\"id\":\"Git.Git\",\"source\":\"winget\",\"useLatest\":false,\"version\":\"2.56.0.3\"}\n"
	got, err := parseDSCOutput([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "Git.Git" || got.Exist == nil || !*got.Exist || got.UseLatest == nil || *got.UseLatest || got.Version != "2.56.0.3" {
		t.Errorf("parseDSCOutput = %+v", got)
	}
}

// TestWingetMissing checks the error when winget isn't installed.
func TestWingetMissing(t *testing.T) {
	s := newTestSystem(t)
	s.runner.On("winget", func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{ExitCode: -1}, &exec.Error{Name: "winget", Err: exec.ErrNotFound}
	})
	_, err := s.set(Spec{ID: "Git.Git"})[0].Check(t.Context(), s.rt)
	if err == nil || !strings.Contains(err.Error(), "can't run winget, which comes with App Installer from the Microsoft Store") {
		t.Errorf("Check error = %v", err)
	}
}

// assertCommands checks that cmds are want, where a * in want matches one
// argument.
func assertCommands(t *testing.T, cmds, want []string) {
	t.Helper()
	match := len(cmds) == len(want)
	for i := 0; match && i < len(cmds); i++ {
		got, w := strings.Fields(cmds[i]), strings.Fields(want[i])
		match = len(got) == len(w)
		for j := 0; match && j < len(got); j++ {
			match = w[j] == "*" || got[j] == w[j]
		}
	}
	if !match {
		t.Errorf("commands:\n  %s\nwant:\n  %s", strings.Join(cmds, "\n  "), strings.Join(want, "\n  "))
	}
}

func countPrefix(cmds []string, prefix string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}
