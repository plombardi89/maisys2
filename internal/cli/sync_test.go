package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// notSetUp is how sync fails on a machine that isn't set up, when it isn't
// told which config repo to use.
var notSetUp = errNotSetUp.Error()

// fc44 is the example repo's machine for the test system (fedora 44, amd64).
const fc44 = "workbox-camwin-fc44"

// testSystem describes the test system as the plan does.
const testSystem = "fedora 44, amd64"

// emptyPlan returns the plan for machine on system with nothing to change,
// as for the example repo's machines, whose modules have no resources yet,
// with header's lines at the top.
func emptyPlan(machine, system string, header ...string) string {
	var b strings.Builder
	b.WriteString("Plan for " + machine + " (" + system + ")\n")
	for _, line := range header {
		b.WriteString(line + "\n")
	}
	b.WriteString("\nNothing to change.\n")
	return b.String()
}

// useGit lets maisys run git in e, kept away from the real user's git
// config. It skips the test if git isn't installed.
func (e *testEnv) useGit() {
	e.t.Helper()
	git, err := gitrepo.Find()
	if err != nil {
		e.t.Skipf("this test needs git: %v", err)
	}
	home := e.t.TempDir()
	writeFiles(e.t, home, map[string]string{"gitconfig": ""})
	e.gitBin = git
	e.gitEnv = []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, "gitconfig"),
		"GIT_AUTHOR_NAME=maisys test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=maisys test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	}
	e.findGit = func() (string, error) { return git, nil }
}

// git runs git in dir the way maisys runs it in e.
func (e *testEnv) git(dir string, args ...string) {
	e.t.Helper()
	cmd := exec.CommandContext(e.t.Context(), e.gitBin, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, e.gitEnv...)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// newRemote returns the path of a new bare repo whose main branch has the
// example config repo from issue #1.
func (e *testEnv) newRemote() string {
	e.t.Helper()
	return e.remoteOf(e.newClone(exampleRepo(e.t, nil)))
}

// newClone commits everything in dir to a new repo there, without a
// remote, and returns dir.
func (e *testEnv) newClone(dir string) string {
	e.t.Helper()
	e.git(dir, "init", "--quiet", "--initial-branch=main")
	e.git(dir, "add", "--all")
	e.git(dir, "commit", "--quiet", "--message", "Create the config repo")
	return dir
}

// remoteOf returns the path of a new bare clone of the repo in dir.
func (e *testEnv) remoteOf(dir string) string {
	e.t.Helper()
	bare := filepath.Join(e.t.TempDir(), "config.git")
	e.git(dir, "clone", "--quiet", "--bare", dir, bare)
	return bare
}

// cloneOf clones remote into a new folder, and returns the folder.
func (e *testEnv) cloneOf(remote string) string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "clone")
	e.git(e.t.TempDir(), "clone", "--quiet", remote, dir)
	return dir
}

// pushUpstream commits files to the main branch of the bare repo remote.
func (e *testEnv) pushUpstream(remote string, files map[string]string) {
	e.t.Helper()
	work := e.cloneOf(remote)
	writeFiles(e.t, work, files)
	e.git(work, "add", "--all")
	e.git(work, "commit", "--quiet", "--message", "Change the config")
	e.git(work, "push", "--quiet", "origin", "HEAD:main")
}

// defaultRepoDir is where sync clones the config repo unless told
// otherwise.
func (e *testEnv) defaultRepoDir() string {
	return filepath.Join(e.vars["HOME"], "maisys-config")
}

// loadSettings returns the local settings, or nil if there are none.
func (e *testEnv) loadSettings() *settings.Settings {
	e.t.Helper()
	s, err := settings.Load(e.settingsFile())
	if errors.Is(err, settings.ErrNoSettings) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// wantSettings checks the local settings.
func (e *testEnv) wantSettings(dir, url, machine string) {
	e.t.Helper()
	want := settings.Settings{FormatVersion: settings.FormatVersion, RepoDir: dir, RepoURL: url, Machine: machine}
	if got := e.loadSettings(); got == nil || *got != want {
		e.t.Errorf("local settings = %+v, want %+v", got, want)
	}
}

// wantSynced checks that a sync of a machine in the example repo got
// through every step, and found nothing to change.
func wantSynced(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if code != exitOK || !strings.HasSuffix(stdout, "\nNothing to change.\n") {
		t.Fatalf("exit code = %d, stdout:\n%s\nstderr:\n%s\nwant %d, and nothing to change", code, stdout, stderr, exitOK)
	}
}

// setUp runs a first sync of remote, cloned into the default folder, and
// returns the clone.
func (e *testEnv) setUp(remote string) string {
	e.t.Helper()
	e.system.Hostname = fc44
	code, stdout, stderr := e.run("sync", remote)
	wantSynced(e.t, code, stdout, stderr)
	return e.defaultRepoDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSyncFirstRun(t *testing.T) {
	tests := []struct {
		name string
		// args returns the arguments, given the remote.
		args     func(e *testEnv, remote string) []string
		hostname string
		env      string
		// dir is the clone's folder, relative to the working directory, if
		// it isn't the default one.
		dir    string
		source string
	}{
		{
			name:     "the hostname picks the machine",
			args:     func(_ *testEnv, remote string) []string { return []string{"sync", remote} },
			hostname: "WorkBox-CamWin-FC44",
			source:   fromHostname,
		},
		{
			name: "--machine picks the machine",
			args: func(_ *testEnv, remote string) []string {
				return []string{"sync", remote, "--machine", fc44, "--dry-run", "--only", "golang,kubectl"}
			},
			source: fromFlag,
		},
		{
			name: "MAISYS_MACHINE picks the machine",
			args: func(_ *testEnv, remote string) []string {
				return []string{"sync", "-n", "-y", "--local", remote}
			},
			env:    fc44,
			source: fromEnv,
		},
		{
			name: "a relative path and --dir",
			args: func(e *testEnv, remote string) []string {
				e.wd = filepath.Dir(remote)
				return []string{"sync", filepath.Base(remote), "--dir", "cfg", "--machine", fc44}
			},
			dir:    "cfg",
			source: fromFlag,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			e.system.Hostname = tt.hostname
			e.vars[machineEnv] = tt.env
			remote := e.newRemote()

			code, stdout, stderr := e.run(tt.args(e, remote)...)
			wantSynced(t, code, stdout, stderr)
			dir := e.defaultRepoDir()
			if tt.dir != "" {
				dir = filepath.Join(e.wd, tt.dir)
			}
			e.wantSettings(dir, remote, fc44)
			if !exists(filepath.Join(dir, "machines", fc44+".toml")) {
				t.Errorf("%s isn't a clone of the config repo", dir)
			}
			want := "maisys: set up this machine as " + fc44 + " (from " + tt.source + "), with the config repo " + remote + ", cloned into " + dir + "\n" +
				emptyPlan(fc44, testSystem)
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if n := strings.Count(stderr, devRequiresWarning); n != 1 {
				t.Errorf("stderr has the maisys.toml warning %d times, want once:\n%s", n, stderr)
			}
		})
	}
}

func TestSyncFirstRunPicker(t *testing.T) {
	e := newTestEnv(t)
	e.useGit()
	e.terminal = true
	e.stdin = "1\n"
	remote := e.newRemote()

	code, stdout, stderr := e.run("sync", remote)
	wantSynced(t, code, stdout, stderr)
	e.wantSettings(e.defaultRepoDir(), remote, fc44)
	for _, want := range []string{
		"maisys: which machine is this? The machines for this system (fedora 44, amd64) are:\n  1) " + fc44 + "\n",
		"maisys: set up this machine as " + fc44 + " (from your choice), with the config repo " + remote + ", cloned into " + e.defaultRepoDir() + "\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout doesn't contain %q:\n%s", want, stdout)
		}
	}
}

func TestSyncFirstRunExistingClone(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the arguments, the clone's folder, and its URL.
		setup func(e *testEnv) (args []string, dir, url string)
		// pulled reports whether the change pushed to the remote after
		// cloning it should be pulled.
		pulled bool
	}{
		{
			name: "--dir",
			setup: func(e *testEnv) ([]string, string, string) {
				remote := e.newRemote()
				dir := e.cloneOf(remote)
				return []string{"sync", "--dir", dir, "--machine", fc44}, dir, remote
			},
			pulled: true,
		},
		{
			name: "--dir and --local",
			setup: func(e *testEnv) ([]string, string, string) {
				remote := e.newRemote()
				dir := e.cloneOf(remote)
				return []string{"sync", "-n", "-y", "--local", "--machine", fc44, "--dir", dir}, dir, remote
			},
		},
		{
			name: "--dir without a remote",
			setup: func(e *testEnv) ([]string, string, string) {
				dir := e.newClone(exampleRepo(e.t, nil))
				return []string{"sync", "--dir", dir, "--machine", fc44}, dir, ""
			},
		},
		{
			name: "the default folder is already a clone of the URL",
			setup: func(e *testEnv) ([]string, string, string) {
				remote := e.newRemote()
				e.git(e.t.TempDir(), "clone", "--quiet", remote, e.defaultRepoDir())
				return []string{"sync", remote + "/", "--machine", fc44}, e.defaultRepoDir(), remote
			},
			pulled: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			args, dir, url := tt.setup(e)
			if url != "" {
				e.pushUpstream(url, map[string]string{"machines/new.toml": "os = \"fedora\"\n"})
			}

			code, stdout, stderr := e.run(args...)
			wantSynced(t, code, stdout, stderr)
			e.wantSettings(dir, url, fc44)
			if pulled := exists(filepath.Join(dir, "machines", "new.toml")); pulled != tt.pulled {
				t.Errorf("pulled = %v, want %v", pulled, tt.pulled)
			}
			where := url + ", in " + dir
			if url == "" {
				where = dir + " (it has no remote)"
			}
			want := "maisys: set up this machine as " + fc44 + " (from --machine), with the config repo " + where + "\n" +
				emptyPlan(fc44, testSystem)
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
		})
	}
}

func TestSyncFirstRunErrors(t *testing.T) {
	notConfig := map[string]string{"README.md": "not a config repo\n"}
	tests := []struct {
		name string
		// setup returns the arguments and what stderr should contain; %s
		// in it is the default folder.
		setup func(e *testEnv) (args []string, want string)
		// check checks what's left behind.
		check func(e *testEnv)
	}{
		{
			name: "no URL or --dir",
			setup: func(*testEnv) ([]string, string) {
				return []string{"sync", "--machine", fc44}, "maisys: " + notSetUp + "\n"
			},
		},
		{
			name: "git isn't installed, and maisys can't install it here",
			setup: func(e *testEnv) ([]string, string) {
				e.system.OS, e.system.OSID, e.system.PrettyName = "", "debian", "Debian GNU/Linux 13 (trixie)"
				e.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
				return []string{"sync", e.newRemote()}, "maisys: git isn't installed\nInstall it with: "
			},
			check: func(e *testEnv) {
				if exists(e.defaultRepoDir()) {
					e.t.Errorf("%s exists, want nothing cloned", e.defaultRepoDir())
				}
			},
		},
		{
			name: "not a config repo",
			setup: func(e *testEnv) ([]string, string) {
				remote := e.remoteOf(e.newClone(writeTemp(e.t, notConfig)))
				return []string{"sync", remote}, "maisys: " + remote + " isn't a maisys config repo: it has no maisys.toml at the top\n"
			},
			check: func(e *testEnv) {
				if exists(e.defaultRepoDir()) {
					e.t.Errorf("%s exists, want the clone removed", e.defaultRepoDir())
				}
			},
		},
		{
			name: "not a config repo, cloned into an empty folder",
			setup: func(e *testEnv) ([]string, string) {
				if err := os.Mkdir(e.defaultRepoDir(), 0o755); err != nil {
					e.t.Fatal(err)
				}
				remote := e.remoteOf(e.newClone(writeTemp(e.t, notConfig)))
				return []string{"sync", remote}, " isn't a maisys config repo"
			},
			check: func(e *testEnv) {
				entries, err := os.ReadDir(e.defaultRepoDir())
				if err != nil || len(entries) != 0 {
					e.t.Errorf("%s has %d entries (%v), want it kept, and empty", e.defaultRepoDir(), len(entries), err)
				}
			},
		},
		{
			name: "--dir isn't a config repo",
			setup: func(e *testEnv) ([]string, string) {
				dir := filepath.Join(e.wd, "cfg")
				writeFiles(e.t, dir, notConfig)
				e.newClone(dir)
				return []string{"sync", "--dir", "cfg"}, "maisys: " + dir + " isn't a maisys config repo: it has no maisys.toml at the top\n"
			},
			check: func(e *testEnv) {
				if !exists(filepath.Join(e.wd, "cfg", "README.md")) {
					e.t.Errorf("the clone given with --dir was changed")
				}
			},
		},
		{
			name: "--dir isn't a clone",
			setup: func(e *testEnv) ([]string, string) {
				dir := exampleRepo(e.t, nil)
				return []string{"sync", "--dir", dir}, "maisys: " + dir + " isn't a git repository\n"
			},
		},
		{
			name: "the default folder isn't empty",
			setup: func(e *testEnv) ([]string, string) {
				writeFiles(e.t, e.defaultRepoDir(), map[string]string{"notes.txt": "mine\n"})
				remote := e.newRemote()
				return []string{"sync", remote}, "maisys: can't clone " + remote + " into %s, because the folder is in use (it isn't empty): choose another folder for it with --dir\n"
			},
		},
		{
			name: "the default folder holds another clone",
			setup: func(e *testEnv) ([]string, string) {
				other := e.newRemote()
				e.git(e.t.TempDir(), "clone", "--quiet", other, e.defaultRepoDir())
				return []string{"sync", e.newRemote()}, "because the folder is in use (it holds a clone of " + other + "): choose another folder for it with --dir\n"
			},
		},
		{
			name: "no machine",
			setup: func(e *testEnv) ([]string, string) {
				return []string{"sync", e.newRemote()}, `maisys: can't tell which machine this is: no machine file in %s is named after the hostname "nothing-matches"`
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			args, want := tt.setup(e)
			code, stdout, stderr := e.run(args...)
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			if want = strings.ReplaceAll(want, "%s", e.defaultRepoDir()); !strings.Contains(stderr, want) {
				t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
			}
			if strings.Contains(stdout, "Plan for") {
				t.Errorf("stdout = %q, want the sync to stop before the plan", stdout)
			}
			if s := e.loadSettings(); s != nil {
				t.Errorf("saved the local settings %+v, want none", s)
			}
			if tt.check != nil {
				tt.check(e)
			}
		})
	}
}

// writeTemp writes files into a new folder, and returns the folder.
func writeTemp(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, files)
	return dir
}

// TestSyncFirstRunInstallsGit runs a first sync on a Fedora or Ubuntu
// without git: it installs git first, as root.
func TestSyncFirstRunInstallsGit(t *testing.T) {
	tests := []struct {
		name string
		// system is the live system, if it isn't the test system.
		system  *facts.System
		machine string
		pkg     string
		install string
	}{
		{name: "fedora", machine: fc44, pkg: "git-core", install: "dnf install -y git-core"},
		{
			name:    "ubuntu",
			system:  &facts.System{OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", PrettyName: "Ubuntu 26.04 LTS", Arch: config.AMD64, RawArch: "amd64"},
			machine: "phil-workp14s",
			pkg:     "git",
			install: "apt-get install -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold git",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			if tt.system != nil {
				e.system = *tt.system
			}
			e.runner.SudoStatus = system.SudoNeedsPassword
			git, installed := e.findGit, false
			e.findGit = func() (string, error) {
				if !installed {
					return "", gitrepo.ErrNoGit
				}
				return git()
			}
			// Whether apt's lists need refreshing depends on the machine
			// the test runs on.
			e.runner.On("apt-get update", systemtest.Respond(0, ""))
			e.runner.On(strings.Fields(tt.install)[0]+" install", func(context.Context, system.Cmd) (system.Result, error) {
				installed = true
				return system.Result{}, nil
			})

			code, stdout, stderr := e.run("sync", e.newRemote(), "--machine", tt.machine)
			wantSynced(t, code, stdout, stderr)
			if want := "maisys: installing git (" + tt.pkg + "), which maisys needs for the config repo\n"; !strings.HasPrefix(stdout, want) {
				t.Errorf("stdout = %q, want it to start with %q", stdout, want)
			}
			var installs []string
			for _, c := range e.runner.Calls() {
				if !c.Cmd.Root || !c.InSession {
					t.Errorf("%s ran outside the sudo session", c.Cmd)
				}
				if line := c.Cmd.String(); line != "apt-get update" {
					installs = append(installs, line)
				}
			}
			if !slices.Equal(installs, []string{tt.install}) || e.runner.SudoStarts() != 1 {
				t.Errorf("commands = %q, sudo started %d times; want %q in one sudo session", installs, e.runner.SudoStarts(), tt.install)
			}
		})
	}
}

func TestSyncFirstRunCantInstallGit(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *testEnv)
		want  string
	}{
		{
			name: "sudo is missing",
			setup: func(e *testEnv) {
				e.runner.SudoStatus, e.runner.OS, e.runner.User = system.SudoMissing, config.Fedora, "phil"
			},
			want: "maisys: installing git: sudo isn't installed, and some steps need to run as root.\n" +
				"To fix this, run these commands as root, then log out and back in and run maisys sync again:\n" +
				"  dnf install -y sudo\n  usermod -aG wheel phil\n",
		},
		{
			name: "the wrong password",
			setup: func(e *testEnv) {
				e.runner.SudoStatus = system.SudoNeedsPassword
				e.runner.StartErr = errors.New("sudo couldn't check your password: sudo: 3 incorrect password attempts")
			},
			want: "maisys: installing git: sudo couldn't check your password: sudo: 3 incorrect password attempts\n",
		},
		{
			name: "dnf fails",
			setup: func(e *testEnv) {
				e.runner.On("dnf install", systemtest.Fail(1, "Errors during downloading metadata for repository 'fedora':\n"+
					"  - Curl error (6): Couldn't resolve host name for https://mirrors.fedoraproject.org/metalink?repo=fedora-44&arch=x86_64\n"))
			},
			want: "maisys: installing git: dnf install failed with exit code 1:\n  Errors during downloading metadata for repository 'fedora':\n" +
				"  - Curl error (6): Couldn't resolve host name for https://mirrors.fedoraproject.org/metalink?repo=fedora-44&arch=x86_64\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
			tt.setup(e)

			code, stdout, stderr := e.run("sync", filepath.Join(t.TempDir(), "config.git"))
			if code != exitError || !strings.HasPrefix(stderr, tt.want) {
				t.Errorf("exit code = %d, stderr:\n%s\nwant %d, and stderr to start with:\n%s", code, stderr, exitError, tt.want)
			}
			if want := "maisys: installing git (git-core), which maisys needs for the config repo\n"; stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if s := e.loadSettings(); s != nil || exists(e.defaultRepoDir()) {
				t.Errorf("settings %+v, and the default folder exists: %t; want nothing set up", s, exists(e.defaultRepoDir()))
			}
		})
	}
}

func TestGitOptions(t *testing.T) {
	e := newTestEnv(t)
	e.vars[configTokenEnv] = "s3cret"
	e.gitEnv = []string{"GIT_CONFIG_NOSYSTEM=1"}
	e.findGit = func() (string, error) { return "/usr/bin/git", nil }
	paths, err := settings.DefaultPaths(runtime.GOOS, func(key string) string { return e.vars[key] })
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	g := newGlobalOptions(nil, e.environment(&out, &out))
	opts, err := g.gitOptions(paths)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Git != "/usr/bin/git" || opts.Auth.Token != "s3cret" || opts.Auth.CredentialsFile != paths.CredentialsFile() ||
		len(opts.Env) != 1 || opts.Env[0] != "GIT_CONFIG_NOSYSTEM=1" {
		t.Errorf("gitOptions() = %+v, want git, the token, the credentials file and the git environment", opts)
	}

	e.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
	g = newGlobalOptions(nil, e.environment(&out, &out))
	if _, err := g.gitOptions(paths); !errors.Is(err, gitrepo.ErrNoGit) || !strings.Contains(err.Error(), "\nInstall it with: ") {
		t.Errorf("gitOptions() without git: error = %v, want ErrNoGit and how to install git", err)
	}
}

func TestSyncPull(t *testing.T) {
	newMachine := map[string]string{"machines/newbox.toml": "os = \"fedora\"\n"}
	tests := []struct {
		name string
		args []string
		// prepare changes the clone, after the remote has a new commit.
		prepare func(e *testEnv, clone string)
		pulled  bool
		stderr  string // what stderr contains, after the sync stops or carries on
		failed  bool   // the sync stops with an error
	}{
		{
			name:   "a new commit upstream",
			args:   []string{"sync", "-v", "--machine", "newbox"},
			pulled: true,
			stderr: "maisys: pulling the config repo in %s from ",
		},
		{
			name: "uncommitted changes",
			args: []string{"sync"},
			prepare: func(e *testEnv, clone string) {
				writeFiles(e.t, clone, map[string]string{"profiles/new.toml": ""})
			},
			stderr: "maisys: warning: the config repo in %s has uncommitted changes: using the config as it is, without pulling\n",
		},
		{
			name: "no remote",
			args: []string{"sync", "-v"},
			prepare: func(e *testEnv, clone string) {
				e.git(clone, "remote", "remove", "origin")
			},
			stderr: "maisys: not pulling the config repo in %s: it has no remote\n",
		},
		{
			name:   "--local",
			args:   []string{"sync", "-v", "--local"},
			stderr: "maisys: not pulling the config repo: --local\n",
		},
		{
			name: "--local without git",
			args: []string{"sync", "--local"},
			prepare: func(e *testEnv, _ string) {
				e.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
			},
		},
		{
			name: "no fast-forward",
			args: []string{"sync"},
			prepare: func(e *testEnv, clone string) {
				writeFiles(e.t, clone, map[string]string{"profiles/new.toml": ""})
				e.git(clone, "add", "--all")
				e.git(clone, "commit", "--quiet", "--message", "Change the config here too")
			},
			stderr: "maisys: can't fast-forward the config repo in %s: both it and ",
			failed: true,
		},
		{
			name: "git isn't installed",
			args: []string{"sync"},
			prepare: func(e *testEnv, _ string) {
				e.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
			},
			stderr: "maisys: git isn't installed\nInstall it with: ",
			failed: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			remote := e.newRemote()
			clone := e.setUp(remote)
			e.pushUpstream(remote, newMachine)
			if tt.prepare != nil {
				tt.prepare(e, clone)
			}

			code, stdout, stderr := e.run(tt.args...)
			if tt.failed {
				if code != exitError || strings.Contains(stdout, "Plan for") {
					t.Errorf("exit code = %d, stderr:\n%s\nwant %d and the sync to stop", code, stderr, exitError)
				}
			} else {
				wantSynced(t, code, stdout, stderr)
			}
			if want := strings.ReplaceAll(tt.stderr, "%s", clone); !strings.Contains(stderr, want) {
				t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
			}
			if pulled := exists(filepath.Join(clone, "machines", "newbox.toml")); pulled != tt.pulled {
				t.Errorf("pulled = %v, want %v", pulled, tt.pulled)
			}
			machine := fc44
			if tt.pulled {
				machine = "newbox"
			}
			e.wantSettings(clone, remote, machine)
		})
	}
}

func TestSyncSwitchRepo(t *testing.T) {
	// other is another config repo, with a machine for the test system.
	other := map[string]string{
		"maisys.toml":       "config_version = 1\n",
		"machines/box.toml": "os = \"fedora\"\n",
	}
	tests := []struct {
		name string
		// setup sets up maisys with a first sync, and returns the arguments
		// for the next sync, and the settings it should save.
		setup func(e *testEnv) (args []string, want settings.Settings)
		// header is the next sync's plan's header, with {old_url},
		// {old_dir}, {new_url} and {new_dir} for the settings before and
		// after.
		header []string
	}{
		{
			name: "another URL, with --dir",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.setUp(e.newRemote())
				remote, dir := e.newRemote(), filepath.Join(e.t.TempDir(), "other")
				return []string{"sync", remote, "--dir", dir}, settings.Settings{RepoDir: dir, RepoURL: remote, Machine: fc44}
			},
			header: []string{"config repo: {old_url} → {new_url}"},
		},
		{
			name: "another URL, into the default folder",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.system.Hostname = fc44
				code, stdout, stderr := e.run("sync", e.newRemote(), "--dir", filepath.Join(e.t.TempDir(), "first"))
				wantSynced(e.t, code, stdout, stderr)
				remote := e.newRemote()
				return []string{"sync", remote}, settings.Settings{RepoDir: e.defaultRepoDir(), RepoURL: remote, Machine: fc44}
			},
			header: []string{"config repo: {old_url} → {new_url}"},
		},
		{
			name: "another URL without the saved machine",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.setUp(e.newRemote())
				e.system.Hostname = "box"
				remote := e.remoteOf(e.newClone(writeTemp(e.t, other)))
				dir := filepath.Join(e.t.TempDir(), "other")
				return []string{"sync", remote, "--dir", dir}, settings.Settings{RepoDir: dir, RepoURL: remote, Machine: "box"}
			},
			header: []string{"config repo: {old_url} → {new_url}", "machine: box (was " + fc44 + ")"},
		},
		{
			name: "another clone of the same URL",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				remote := e.newRemote()
				e.setUp(remote)
				dir := e.cloneOf(remote)
				return []string{"sync", "--dir", dir}, settings.Settings{RepoDir: dir, RepoURL: remote, Machine: fc44}
			},
			header: []string{"config repo: {old_dir} → {new_dir}"},
		},
		{
			name: "a clone without a remote",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.setUp(e.newRemote())
				dir := e.newClone(exampleRepo(e.t, nil))
				return []string{"sync", "--dir", dir}, settings.Settings{RepoDir: dir, Machine: fc44}
			},
			header: []string{"config repo: {old_url} → {new_dir}"},
		},
		{
			name: "the same URL",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				remote := e.newRemote()
				e.setUp(remote)
				return []string{"sync", remote + "/"}, settings.Settings{RepoDir: e.defaultRepoDir(), RepoURL: remote, Machine: fc44}
			},
		},
		{
			name: "the same URL, after the clone was removed",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				remote := e.newRemote()
				if err := os.RemoveAll(e.setUp(remote)); err != nil {
					e.t.Fatal(err)
				}
				return []string{"sync", remote}, settings.Settings{RepoDir: e.defaultRepoDir(), RepoURL: remote, Machine: fc44}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			args, want := tt.setup(e)
			old := e.loadSettings()

			code, stdout, stderr := e.run(args...)
			wantSynced(t, code, stdout, stderr)
			e.wantSettings(want.RepoDir, want.RepoURL, want.Machine)
			r := strings.NewReplacer(
				"{old_url}", old.RepoURL, "{old_dir}", old.RepoDir,
				"{new_url}", want.RepoURL, "{new_dir}", want.RepoDir,
			)
			header := make([]string, len(tt.header))
			for i, line := range tt.header {
				header[i] = r.Replace(line)
			}
			if wantOut := emptyPlan(want.Machine, testSystem, header...); stdout != wantOut {
				t.Errorf("stdout = %q, want %q", stdout, wantOut)
			}
			if !exists(filepath.Join(want.RepoDir, "maisys.toml")) {
				t.Errorf("%s isn't a clone of the config repo", want.RepoDir)
			}
		})
	}
}

func TestSyncSwitchRepoErrors(t *testing.T) {
	tests := []struct {
		name string
		// setup returns the arguments for the second sync, and what its
		// stderr should contain.
		setup func(e *testEnv, remote, clone string) (args []string, want string)
	}{
		{
			name: "the default folder holds the old clone",
			setup: func(e *testEnv, remote, clone string) ([]string, string) {
				other := e.newRemote()
				return []string{"sync", other}, "maisys: can't clone " + other + " into " + clone + ", because the folder is in use (it holds a clone of " + remote + "): choose another folder for it with --dir\n"
			},
		},
		{
			name: "the clone is gone",
			setup: func(e *testEnv, remote, clone string) ([]string, string) {
				if err := os.RemoveAll(clone); err != nil {
					e.t.Fatal(err)
				}
				return []string{"sync"}, "maisys: the config repo " + clone + " doesn't exist (it's set in " + e.settingsFile() + "): run `maisys sync " + remote + "` to clone it again\n"
			},
		},
		{
			name: "--dir isn't a clone",
			setup: func(e *testEnv, _, _ string) ([]string, string) {
				dir := e.t.TempDir()
				return []string{"sync", "--dir", dir}, "maisys: " + dir + " isn't a git repository\n"
			},
		},
		{
			name: "the local settings have no repo_dir",
			setup: func(e *testEnv, remote, _ string) ([]string, string) {
				e.saveSettings(settings.Settings{RepoURL: remote, Machine: fc44})
				return []string{"sync", remote}, "maisys: " + e.settingsFile() + " doesn't say where the config repo is: run `maisys sync --dir PATH` to use a clone, or `maisys sync CONFIG-URL` to clone one\n"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			remote := e.newRemote()
			clone := e.setUp(remote)
			args, want := tt.setup(e, remote, clone)
			before := e.loadSettings()

			code, _, stderr := e.run(args...)
			if code != exitError || !strings.HasSuffix(stderr, want) {
				t.Errorf("exit code = %d, stderr:\n%s\nwant %d, and stderr to end with %q", code, stderr, exitError, want)
			}
			if after := e.loadSettings(); *after != *before {
				t.Errorf("local settings = %+v, want them unchanged: %+v", after, before)
			}
		})
	}
}

func TestSyncSwitchMachine(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  string
		// ubuntu runs the second sync on Ubuntu 26.04, phil-workp14s's
		// system.
		ubuntu  bool
		code    int
		machine string // the machine saved afterwards
		stdout  string
		stderr  string
	}{
		{
			name:    "--machine",
			args:    []string{"sync", "--machine", "phil-workp14s"},
			ubuntu:  true,
			machine: "phil-workp14s",
			stdout:  emptyPlan("phil-workp14s", "ubuntu 26.04, amd64", "machine: phil-workp14s (was "+fc44+")"),
		},
		{
			name:    "the same machine",
			args:    []string{"sync", "--machine", fc44},
			machine: fc44,
			stdout:  emptyPlan(fc44, testSystem),
		},
		{
			name:    "MAISYS_MACHINE is only for one run",
			args:    []string{"sync", "-v"},
			env:     "phil-workp14s",
			ubuntu:  true,
			machine: fc44,
			stdout:  emptyPlan("phil-workp14s", "ubuntu 26.04, amd64"),
			stderr:  "maisys: machine phil-workp14s (from MAISYS_MACHINE)\n",
		},
		{
			name:    "a missing machine",
			args:    []string{"sync", "--machine", "nope"},
			code:    exitError,
			machine: fc44,
			stderr:  `maisys: machines/nope.toml doesn't exist in `,
		},
		{
			// The switch is only saved once a plan is applied, so the saved
			// machine stays the one that works here.
			name:    "a machine for another system",
			args:    []string{"sync", "--machine", "phil-workp14s"},
			code:    exitError,
			machine: fc44,
			stdout:  "maisys: machine: phil-workp14s (was " + fc44 + ")\n",
			stderr: "machines/phil-workp14s.toml:1:6: machine \"phil-workp14s\" is for Ubuntu 26.04, but it's running on Fedora 44\n" +
				"maisys: 1 problem found\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			remote := e.newRemote()
			clone := e.setUp(remote)
			e.vars[machineEnv] = tt.env
			if tt.ubuntu {
				e.system.OS, e.system.OSID, e.system.OSVersion = config.Ubuntu, "ubuntu", "26.04"
				e.system.PrettyName = "Ubuntu 26.04 LTS"
			}

			code, stdout, stderr := e.run(tt.args...)
			if tt.code == exitError {
				if code != exitError || strings.Contains(stdout, "Plan for") {
					t.Errorf("exit code = %d, stderr:\n%s\nwant %d and the sync to stop", code, stderr, exitError)
				}
			} else {
				wantSynced(t, code, stdout, stderr)
			}
			if stdout != tt.stdout {
				t.Errorf("stdout = %q, want %q", stdout, tt.stdout)
			}
			if !strings.Contains(stderr, tt.stderr) {
				t.Errorf("stderr doesn't contain %q:\n%s", tt.stderr, stderr)
			}
			e.wantSettings(clone, remote, tt.machine)
		})
	}
}

func TestSyncInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		stderr string
	}{
		{
			name: "a problem in the machine's config",
			files: map[string]string{"machines/" + fc44 + ".toml": `os = "fedora"
profiles = ["base", "fedora"]

[golang]
enabled = true
version = "1.27"
`},
			stderr: "machines/" + fc44 + `.toml:6:11: golang.version must be a full Go version, such as "1.26.4", not "1.27": partial versions aren't supported` + "\n" +
				"maisys: 1 problem found\n",
		},
		{
			name:  "an unknown key",
			files: map[string]string{"machines/" + fc44 + ".toml": "os = \"fedora\"\ncolour = \"blue\"\n"},
			stderr: "machines/" + fc44 + ".toml:2:1: unknown key colour\n" +
				"maisys: 1 problem found\n" +
				"maisys: an unknown key may mean the config needs a newer maisys than this one (dev)\n",
		},
		{
			name:   "a newer config_version",
			files:  map[string]string{"maisys.toml": "config_version = 2\n"},
			stderr: "maisys.toml:1:18: config_version 2 needs a newer maisys: this one reads config_version 1; update maisys\nmaisys: 1 problem found\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			remote := e.newRemote()
			e.setUp(remote)
			e.pushUpstream(remote, tt.files)

			code, stdout, stderr := e.run("sync")
			if code != exitError || !strings.HasSuffix(stderr, tt.stderr) {
				t.Errorf("exit code = %d, stderr:\n%s\nwant %d, and stderr to end with:\n%s", code, stderr, exitError, tt.stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
		})
	}
}

func TestSameURL(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"https://github.com/me/config.git", "https://github.com/me/config.git", true},
		{"https://github.com/me/config.git", "https://github.com/me/config", true},
		{"https://github.com/me/config.git", "https://github.com/me/config/", true},
		{"https://github.com/me/config.git", "HTTPS://GitHub.com/me/config.git/", true},
		{"https://github.com/me/config.git", "https://github.com/Me/config.git", false},
		{"https://github.com/me/config.git", "https://github.com/me/other.git", false},
		{"https://github.com/me/config.git", "git@github.com:me/config.git", false},
		{"git@github.com:me/config.git", "git@GitHub.com:me/config", true},
		{"git@github.com:me/config.git", "git@github.com:me/other.git", false},
		{"/srv/git/config.git", "/srv/git/config.git/", true},
		{"/srv/git/config.git", "/srv/git/other.git", false},
		{"https://github.com/me/config.git", "", false},
	}
	for _, tt := range tests {
		for _, pair := range [][2]string{{tt.a, tt.b}, {tt.b, tt.a}} {
			if got := sameURL(pair[0], pair[1]); got != tt.want {
				t.Errorf("sameURL(%q, %q) = %v, want %v", pair[0], pair[1], got, tt.want)
			}
		}
	}
}

func TestIsLocalPath(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://github.com/me/config.git", false},
		{"file:///srv/git/config.git", false},
		{"git@github.com:me/config.git", false},
		{"github.com:me/config.git", false},
		{"config.git", true},
		{"../git/config.git", true},
		{"./a:b", true},
		{filepath.Join(string(filepath.Separator)+"srv", "config.git"), true},
	}
	for _, tt := range tests {
		if got := isLocalPath(tt.url); got != tt.want {
			t.Errorf("isLocalPath(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
