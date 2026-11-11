package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/settings"
)

// testEnv is an environment for running maisys in tests, with a home
// folder and working directory of its own, a fake system, and no
// terminal.
type testEnv struct {
	t        *testing.T
	wd       string
	vars     map[string]string
	stdin    string
	terminal bool
	system   facts.System
	// gitEnv and findGit are how maisys runs git. useGit sets them up.
	gitEnv  []string
	findGit func() (string, error)
	// gitBin is the git binary the test runs itself.
	gitBin string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	home := t.TempDir()
	return &testEnv{
		t:  t,
		wd: t.TempDir(),
		vars: map[string]string{
			"HOME":         home,
			"USERPROFILE":  home,
			"APPDATA":      filepath.Join(home, "AppData", "Roaming"),
			"LOCALAPPDATA": filepath.Join(home, "AppData", "Local"),
		},
		system: facts.System{
			OS:         config.Fedora,
			OSID:       "fedora",
			OSVersion:  "44",
			PrettyName: "Fedora Linux 44 (Workstation Edition)",
			Arch:       config.AMD64,
			RawArch:    "amd64",
			Hostname:   "nothing-matches",
		},
	}
}

func (e *testEnv) environment(stdout, stderr *bytes.Buffer) *environment {
	return &environment{
		stdin:  strings.NewReader(e.stdin),
		stdout: stdout,
		stderr: stderr,
		getenv: func(key string) string { return e.vars[key] },
		getwd:  func() (string, error) { return e.wd, nil },
		detect: func() (*facts.System, error) {
			s := e.system
			return &s, nil
		},
		isTerminal: func() bool { return e.terminal },
		goos:       runtime.GOOS,
		gitEnv:     e.gitEnv,
		findGit:    e.findGit,
	}
}

// run runs maisys with args, and returns its exit code and output.
func (e *testEnv) run(args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code = execute(e.t.Context(), args, e.environment(&out, &errOut))
	return code, out.String(), errOut.String()
}

// settingsFile returns where the local settings are in this environment.
func (e *testEnv) settingsFile() string {
	e.t.Helper()
	paths, err := settings.DefaultPaths(runtime.GOOS, func(key string) string { return e.vars[key] })
	if err != nil {
		e.t.Fatal(err)
	}
	return paths.SettingsFile()
}

// saveSettings saves the local settings.
func (e *testEnv) saveSettings(s settings.Settings) {
	e.t.Helper()
	if err := s.Save(e.settingsFile()); err != nil {
		e.t.Fatal(err)
	}
}

// writeFiles writes files, keyed by their slash-separated paths in dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	return newTestEnv(t).run(args...)
}

// TestExecute checks the entry point cmd/maisys uses.
func TestExecute(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Execute(context.Background(), []string{"--version"}, &out, &errOut, func(string) string { return "" })
	if code != exitOK || !strings.HasPrefix(out.String(), "maisys dev") {
		t.Errorf("Execute(--version) = %d, stdout %q, stderr %q; want %d and the version", code, out.String(), errOut.String(), exitOK)
	}
}

func TestVersionFlag(t *testing.T) {
	code, stdout, stderr := run(t, "--version")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "maisys dev") {
		t.Errorf("stdout = %q, want it to start with %q", stdout, "maisys dev")
	}
}

func TestHelpListsCommands(t *testing.T) {
	code, stdout, _ := run(t, "--help")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
	for _, name := range []string{"sync", "config", "status", "--verbose", "--version"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("help output doesn't mention %q:\n%s", name, stdout)
		}
	}
}

func TestPlaceholderCommands(t *testing.T) {
	tests := []struct {
		args []string
		name string
	}{
		{[]string{"status", "-v"}, "status"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, stderr := run(t, tt.args...)
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			want := `maisys: "` + tt.name + `" is not implemented yet`
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, want)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"sync", "a", "b"}, "accepts at most 1 arg"},
		{[]string{"sync", "--bogus"}, "Run 'maisys sync --help' for usage"},
		{[]string{"config", "extra"}, `unknown command "extra"`},
		{[]string{"nope"}, `unknown command "nope"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, stderr := run(t, tt.args...)
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.want)
			}
		})
	}
}

func TestSyncSelfUpdateStep(t *testing.T) {
	// Tests run as a development build, which never checks for updates, so
	// these don't touch the network.
	const devMsg = "not checking for a newer maisys: this is a development build"

	_, _, stderr := run(t, "sync", "-v")
	if !strings.Contains(stderr, devMsg) {
		t.Errorf("sync -v: stderr = %q, want it to contain %q", stderr, devMsg)
	}
	if !strings.HasSuffix(stderr, "maisys: "+notSetUp+"\n") {
		t.Errorf("sync -v: stderr = %q, want the sync to carry on to the first-run setup", stderr)
	}

	_, _, stderr = run(t, "sync", "-v", "--local")
	if strings.Contains(stderr, devMsg) {
		t.Errorf("sync -v --local: stderr = %q, want no self-update step", stderr)
	}

	_, _, stderr = run(t, "sync")
	if strings.Contains(stderr, devMsg) {
		t.Errorf("sync without -v: stderr = %q, want the message only with --verbose", stderr)
	}
}

func TestNoColor(t *testing.T) {
	tests := []struct {
		value string
		color bool
	}{
		{"", true},
		{"1", false},
		{"0", false}, // any non-empty value turns colour off
	}
	for _, tt := range tests {
		g := newGlobalOptions(nil, &environment{getenv: func(string) string { return tt.value }})
		if g.color != tt.color {
			t.Errorf("NO_COLOR=%q: color = %v, want %v", tt.value, g.color, tt.color)
		}
	}
}
