package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// windows11 is a Windows 11 host, named after the example repo's Windows
// machine.
var windows11 = facts.System{
	OS: config.Windows, OSID: "windows", OSVersion: "26100", PrettyName: "Windows 11 (build 26100)",
	Arch: config.AMD64, RawArch: "amd64", Hostname: "workbox-camwin",
}

// readyHost is a Windows host that's ready for maisys, without WSL yet.
var readyHost = facts.Windows{Admin: true, Virtualization: facts.VirtualizationOn, Winget: "1.11.430"}

// useWindows makes the live system a Windows 11 host with the details w.
func (e *testEnv) useWindows(w facts.Windows) {
	e.system = windows11
	e.windows = w
}

func TestSyncWindowsFirstRun(t *testing.T) {
	registerPrefix := strings.Join(append([]string{registerWingetCmd.Name}, registerWingetCmd.Args[:3]...), " ")
	tests := []struct {
		name    string
		windows facts.Windows
		// setup scripts the commands a test expects.
		setup        func(e *testEnv)
		wantCommands []string
		wantStdout   string // the start of stdout
		wantStderr   string
	}{
		{name: "ready", windows: readyHost},
		{
			name:    "winget needs registering",
			windows: facts.Windows{Admin: true, Virtualization: facts.VirtualizationOn},
			setup: func(e *testEnv) {
				e.runner.On(registerPrefix, systemtest.Respond(0, ""))
				e.runner.On("winget --version", systemtest.Respond(0, "v1.11.430\r\n"))
			},
			wantCommands: []string{registerWingetCmd.String(), "winget --version"},
			wantStdout:   "maisys: registering winget, which a fresh Windows install may not have done yet\n",
		},
		{
			name:       "virtualization unknown",
			windows:    facts.Windows{Admin: true, Winget: "1.11.430", Warnings: []string{"couldn't tell whether virtualization is turned on: powershell.exe isn't installed"}},
			wantStderr: "maisys: warning: couldn't tell whether virtualization is turned on in this PC's firmware, which WSL 2 needs; carrying on\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			e.useWindows(tt.windows)
			if tt.setup != nil {
				tt.setup(e)
			}

			remote := e.newRemote()
			code, stdout, stderr := e.run("sync", remote)
			wantSynced(t, code, stdout, stderr)
			if !strings.HasPrefix(stdout, tt.wantStdout) || !strings.Contains(stdout, "Plan for workbox-camwin (windows 11 26100, amd64)\n") {
				t.Errorf("stdout:\n%s\nwant it to start with %q, and the plan for workbox-camwin", stdout, tt.wantStdout)
			}
			if want := tt.wantStderr + devRequiresWarning; stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if got := e.runner.Commands(); !slices.Equal(got, tt.wantCommands) {
				t.Errorf("commands = %q, want %q", got, tt.wantCommands)
			}
			if e.windowsDetections != 1 {
				t.Errorf("Windows details were detected %d times, want once", e.windowsDetections)
			}
			e.wantSettings(e.defaultRepoDir(), remote, "workbox-camwin")
		})
	}
}

// TestSyncWindowsNotReady runs a first sync on Windows hosts that fail the
// checks: it stops before it clones anything, saying what to do.
func TestSyncWindowsNotReady(t *testing.T) {
	const (
		windows11Problem = "maisys needs Windows 11 (build 22000 or newer), but this is Windows 10 (build 19045)"
		virtProblem      = "virtualization is turned off in this PC's firmware, and WSL 2 needs it"
		wingetProblem    = "winget isn't installed, and maisys installs Windows software with it"
		adminProblem     = "you aren't a local admin (a member of the Administrators group)"
	)
	tests := []struct {
		name    string
		build   string
		windows facts.Windows
		// registered reports whether registering winget works.
		registered bool
		want       []string
	}{
		{name: "Windows 10", build: "19045", windows: readyHost, want: []string{windows11Problem}},
		{name: "virtualization off", windows: facts.Windows{Admin: true, Virtualization: facts.VirtualizationOff, Winget: "1.11.430"}, want: []string{virtProblem}},
		{name: "not an admin", windows: facts.Windows{Virtualization: facts.VirtualizationOn, Winget: "1.11.430"}, want: []string{adminProblem}},
		{name: "winget can't be registered", windows: facts.Windows{Admin: true, Virtualization: facts.VirtualizationOn}, want: []string{wingetProblem}},
		{
			name: "winget is registered but doesn't work", windows: facts.Windows{Admin: true, Virtualization: facts.VirtualizationOn},
			registered: true, want: []string{wingetProblem},
		},
		{
			name: "everything", build: "19045", windows: facts.Windows{Virtualization: facts.VirtualizationOff},
			want: []string{windows11Problem, virtProblem, wingetProblem, adminProblem},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useWindows(tt.windows)
			if tt.build != "" {
				e.system.OSVersion, e.system.PrettyName = tt.build, "Windows 10 (build "+tt.build+")"
			}
			e.runner.On("powershell.exe", systemtest.Fail(1, "Add-AppxPackage : Deployment failed with HRESULT: 0x80073CF9\n"))
			if tt.registered {
				e.runner.On("powershell.exe", systemtest.Respond(0, ""))
			}
			e.runner.On("winget", func(context.Context, system.Cmd) (system.Result, error) {
				return system.Result{ExitCode: -1}, fmt.Errorf("winget --version: winget isn't installed: %w", exec.ErrNotFound)
			})

			code, _, stderr := e.run("sync", "https://example.com/config.git")
			lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
			if code != exitError || len(lines) != len(tt.want)+2 || lines[0] != "maisys: this Windows host isn't ready for maisys yet:" {
				t.Fatalf("exit code = %d, stderr:\n%s\nwant %d, and the %d problems", code, stderr, exitError, len(tt.want))
			}
			for i, want := range tt.want {
				if !strings.HasPrefix(lines[i+1], want) || !strings.HasSuffix(lines[i+1], "run maisys sync again") {
					t.Errorf("problem %d = %q, want it to start with %q and say to run maisys sync again", i+1, lines[i+1], want)
				}
			}
			if s := e.loadSettings(); s != nil || exists(e.defaultRepoDir()) {
				t.Errorf("settings %+v, and the default folder exists: %t; want nothing set up", s, exists(e.defaultRepoDir()))
			}
		})
	}
}

func TestSyncWindowsDetectionFails(t *testing.T) {
	e := newTestEnv(t)
	e.useWindows(readyHost)
	e.windowsErr = errors.New("reading WSL's distros from the registry: access is denied")
	e.useWindowsFakes(fake("winget", nil, "winget:jq"))

	code, stdout, stderr := e.run("sync", "--local", "--yes")
	if want := "maisys: detecting this Windows host's details: reading WSL's distros from the registry: access is denied\n"; code != exitError || stdout != "" || stderr != want {
		t.Errorf("exit code = %d, stdout %q, stderr %q; want %d and only %q", code, stdout, stderr, exitError, want)
	}
}

// useWindowsFakes makes mods the only modules maisys has, and sets maisys
// up with a config repo for them whose one machine, host, is a Windows
// host, as useFakes does.
func (e *testEnv) useWindowsFakes(mods ...config.Module) {
	e.t.Helper()
	e.modules = mods
	machine := "os = \"windows\"\n"
	for _, m := range mods {
		machine += "\n[" + m.Info().Name + "]\nenabled = true\n"
	}
	dir := writeTemp(e.t, map[string]string{"maisys.toml": "config_version = 1\n", "machines/host.toml": machine})
	e.saveSettings(settings.Settings{RepoDir: dir, Machine: "host"})
}

// TestSyncWindowsAdmin checks that a Windows plan says when it needs admin
// rights, and that applying it starts the elevated helper once, before the
// first change that needs them.
func TestSyncWindowsAdmin(t *testing.T) {
	tests := []struct {
		name     string
		elevated bool
		summary  string
	}{
		{name: "unelevated", summary: "2 to change. Needs admin (1 UAC prompt).\n"},
		{name: "elevated", elevated: true, summary: "2 to change. Needs admin.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			host := readyHost
			host.Elevated = tt.elevated
			host.Warnings = []string{"couldn't tell which version of WSL is installed: wsl --version failed"}
			e.useWindows(host)
			wsl := fake("wsl_host", nil, "wsl:install")
			wsl.resources[0].privileged = true
			wsl.resources[0].onApply = func(ctx context.Context) error {
				_, err := e.runner.Run(ctx, system.Cmd{Name: "wsl.exe", Args: []string{"--install", "--no-distribution"}, Root: true})
				return err
			}
			e.runner.On("wsl.exe --install", systemtest.Respond(0, ""))
			mods := []*fakeModule{fake("winget", nil, "winget:jq"), wsl}
			e.useWindowsFakes(mods[0], mods[1])

			code, stdout, stderr := e.run("sync", "--dry-run", "--local", "-v")
			if code != exitChanges || !strings.Contains(stdout, "  + create wsl:install") || !strings.HasSuffix(stdout, "\n"+tt.summary) {
				t.Errorf("dry run: exit code = %d, stdout:\n%s\nwant %d, and a plan ending with %q", code, stdout, exitChanges, tt.summary)
			}
			if !strings.Contains(stderr, "maisys: "+host.Warnings[0]+"\n") {
				t.Errorf("stderr:\n%s\nwant the detection's warning with --verbose", stderr)
			}

			code, stdout, stderr = e.run("sync", "--local", "--yes")
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d; stdout:\n%s\nstderr:\n%s", code, exitOK, stdout, stderr)
			}
			wantApplied(t, mods, "winget:jq", "wsl:install")
			if e.runner.SudoStarts() != 1 {
				t.Errorf("the elevated helper was started %d times, want once", e.runner.SudoStarts())
			}
			for _, c := range e.runner.Calls() {
				if c.Cmd.Root && !c.InSession {
					t.Errorf("%s ran as admin outside the elevated helper", c.Cmd)
				}
			}
			if e.windowsDetections != 2 {
				t.Errorf("Windows details were detected %d times, want once a sync", e.windowsDetections)
			}
		})
	}
}

// anyModule is a module with resources of any type.
type anyModule struct {
	name      string
	deps      []string
	resources []resource.Resource
}

var _ engine.Module = (*anyModule)(nil)

func (m *anyModule) Info() config.ModuleInfo {
	return config.ModuleInfo{Name: m.name, Summary: "A test module.", OS: config.AllOS()}
}

func (*anyModule) NewConfig() config.ModuleConfig { return &config.Toggle{} }

func (m *anyModule) DependsOn() []string { return m.deps }

func (m *anyModule) Resources(config.ModuleConfig, *resource.Runtime) ([]resource.Resource, error) {
	return m.resources, nil
}

// rebootResource is a fake resource that needs a reboot once it's applied.
type rebootResource struct {
	*fakeResource
}

func (r rebootResource) RebootNeeded() (string, bool) {
	return "WSL was installed", r.applied.Load()
}

// TestSyncReboot applies a change that needs a reboot: what depends on it
// waits, sync exits with 3, and the next sync carries on.
func TestSyncReboot(t *testing.T) {
	e := newTestEnv(t)
	e.useWindows(readyHost)
	install := &fakeResource{id: "wsl:install", privileged: true}
	terminal := fake("windows_terminal", []string{"wsl_host"}, "terminal:default-profile")
	files := fake("files", nil, "file:~/.wslconfig")
	e.useWindowsFakes(&anyModule{name: "wsl_host", resources: []resource.Resource{rebootResource{install}}}, terminal, files)

	code, stdout, stderr := e.run("sync", "--local", "--yes")
	if code != exitReboot {
		t.Fatalf("exit code = %d, want %d; stdout:\n%s\nstderr:\n%s", code, exitReboot, stdout, stderr)
	}
	wantEnd := "Applied 2 changes.\n" +
		"wsl_host needs a reboot before it can carry on: WSL was installed.\n" +
		"windows_terminal waits for the reboot.\n" +
		"Reboot, then run `maisys sync` again to finish.\n"
	if !strings.HasSuffix(stdout, "\n\n"+wantEnd) || stderr != "" {
		t.Errorf("stdout:\n%s\nstderr:\n%s\nwant stdout to end with:\n%s", stdout, stderr, wantEnd)
	}
	if !install.applied.Load() {
		t.Error("the change that needs a reboot wasn't applied")
	}
	wantApplied(t, []*fakeModule{terminal, files}, "file:~/.wslconfig")
	s := e.state()
	if !slices.Contains(s.IDs(), "wsl:install") || s.LastSyncResult != state.ResultInterrupted {
		t.Errorf("records = %q, last sync %q; want wsl:install recorded, and %q", s.IDs(), s.LastSyncResult, state.ResultInterrupted)
	}

	// After the reboot.
	code, stdout, stderr = e.run("sync", "--local", "--yes")
	if code != exitOK || !strings.HasSuffix(stdout, "\nApplied 1 change.\n") {
		t.Errorf("the sync after the reboot: exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, and the rest applied", code, stdout, stderr, exitOK)
	}
	wantApplied(t, []*fakeModule{terminal, files}, "terminal:default-profile", "file:~/.wslconfig")
	if s := e.state(); s.LastSyncResult != state.ResultOK {
		t.Errorf("last sync %q, want %q", s.LastSyncResult, state.ResultOK)
	}
}

func TestHelperCommand(t *testing.T) {
	e := newTestEnv(t)
	code, stdout, stderr := e.run(system.HelperCommand, "--pipe", `\\.\pipe\maisys-helper-0123`, "--secret", "s3cret")
	if code != exitOK || stdout != "" || stderr != "" {
		t.Errorf("exit code = %d, stdout %q, stderr %q; want %d and no output", code, stdout, stderr, exitOK)
	}
	if want := []string{`\\.\pipe\maisys-helper-0123`, "s3cret"}; !slices.Equal(e.helper, want) {
		t.Errorf("the helper served %q, want %q", e.helper, want)
	}

	e = newTestEnv(t)
	code, _, stderr = e.run(system.HelperCommand, "--pipe", `\\.\pipe\maisys-helper-0123`)
	if code != exitError || !strings.Contains(stderr, `required flag(s) "secret" not set`) || e.helper != nil {
		t.Errorf("without --secret: exit code = %d, stderr %q; want %d, a missing flag, and no helper", code, stderr, exitError)
	}

	_, stdout, _ = e.run("--help")
	if strings.Contains(stdout, system.HelperCommand) {
		t.Errorf("--help lists the hidden helper command:\n%s", stdout)
	}
}

func TestProcessEnvironmentWindows(t *testing.T) {
	env := processEnvironment(nil, nil, nil, func(string) string { return "" })
	if env.detectWindows == nil || env.serveHelper == nil {
		t.Error("the process's environment can't detect Windows details or serve the elevated helper")
	}
}
