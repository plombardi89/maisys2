package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
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
	// now is the time maisys runs at.
	now time.Time
	// abort is closed to act as a second Ctrl-C.
	abort chan struct{}
	// modules are the modules maisys has: the built-in ones without their
	// resources (see configOnly), unless a test uses fake ones.
	modules []config.Module
	// removers remove records of what's taken out of the config.
	removers resource.Removers
	// runner runs a sync's commands; runnerOpts are the options sync made
	// it with.
	runner     *systemtest.FakeRunner
	runnerOpts system.Options
	// windows is what detecting a Windows host's details finds, and
	// windowsErr how it fails, if it does. windowsDetections counts the
	// detections.
	windows           facts.Windows
	windowsErr        error
	windowsDetections int
	// helper records how the elevated helper was started, as its pipe and
	// secret.
	helper []string
}

// testNow is the time maisys runs at in tests.
var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

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
		now:     testNow,
		modules: configOnly(modules.All()),
		runner:  systemtest.NewFakeRunner(t),
	}
}

// configOnly returns mods without their resources: only their config, so
// a sync plans nothing for them. The tests of the sync command test it
// with fake modules, and each module's resources are tested on their own.
func configOnly(mods []config.Module) []config.Module {
	out := make([]config.Module, len(mods))
	for i, mod := range mods {
		out[i] = configModule{mod}
	}
	return out
}

// configModule is a module's config side, without its resources.
type configModule struct {
	config.Module
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
		isTerminal:     func() bool { return e.terminal },
		stdoutTerminal: func() bool { return e.terminal },
		goos:           runtime.GOOS,
		gitEnv:         e.gitEnv,
		findGit:        e.findGit,
		abort:          e.abort,
		now:            func() time.Time { return e.now },
		modules:        e.modules,
		removers:       e.removers,
		newRunner: func(opts system.Options) system.Runner {
			e.runnerOpts = opts
			return e.runner
		},
		detectWindows: func(_ context.Context, r system.Runner) (*facts.Windows, error) {
			e.windowsDetections++
			if r != system.Runner(e.runner) {
				e.t.Error("Windows details were detected with another runner than the sync's")
			}
			if e.windowsErr != nil {
				return nil, e.windowsErr
			}
			w := e.windows
			w.Distros = slices.Clone(w.Distros)
			return &w, nil
		},
		serveHelper: func(_ context.Context, pipe, secret string) error {
			e.helper = []string{pipe, secret}
			return nil
		},
	}
}

// run runs maisys with args, and returns its exit code and output.
func (e *testEnv) run(args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	return e.runContext(e.t.Context(), args...)
}

// runContext runs maisys with args and the context ctx, which a test can
// cancel to act as Ctrl-C.
func (e *testEnv) runContext(ctx context.Context, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	code = execute(ctx, args, e.environment(&out, &errOut))
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
	code := Execute(context.Background(), nil, []string{"--version"}, &out, &errOut, func(string) string { return "" })
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

func TestNotImplemented(t *testing.T) {
	root := &cobra.Command{Use: "maisys"}
	cmd := &cobra.Command{Use: "helper", RunE: notImplemented}
	root.AddCommand(cmd)
	err := notImplemented(cmd, nil)
	if !errors.Is(err, errNotImplemented) || err.Error() != `"helper" is not implemented yet` {
		t.Errorf("notImplemented() = %v, want %q", err, `"helper" is not implemented yet`)
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
