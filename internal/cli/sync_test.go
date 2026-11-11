package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/settings"
)

// notSetUp is how sync fails on a machine that isn't set up, when it isn't
// told which config repo to use.
var notSetUp = errNotSetUp.Error()

// syncNotImplemented ends the output of a sync that got through every step
// so far.
const syncNotImplemented = `maisys: "sync" is not implemented yet` + "\n"

// fc44 is the example repo's machine for the test system (fedora 44, amd64).
const fc44 = "workbox-camwin-fc44"

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

// wantSynced checks that a sync got through every step so far.
func wantSynced(t *testing.T, code int, stderr string) {
	t.Helper()
	if code != exitError || !strings.HasSuffix(stderr, syncNotImplemented) {
		t.Fatalf("exit code = %d, stderr:\n%s\nwant %d, and stderr to end with %q", code, stderr, exitError, syncNotImplemented)
	}
}

// setUp runs a first sync of remote, cloned into the default folder, and
// returns the clone.
func (e *testEnv) setUp(remote string) string {
	e.t.Helper()
	e.system.Hostname = fc44
	code, _, stderr := e.run("sync", remote)
	wantSynced(e.t, code, stderr)
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
			wantSynced(t, code, stderr)
			dir := e.defaultRepoDir()
			if tt.dir != "" {
				dir = filepath.Join(e.wd, tt.dir)
			}
			e.wantSettings(dir, remote, fc44)
			if !exists(filepath.Join(dir, "machines", fc44+".toml")) {
				t.Errorf("%s isn't a clone of the config repo", dir)
			}
			want := "maisys: set up this machine as " + fc44 + " (from " + tt.source + "), with the config repo " + remote + ", cloned into " + dir + "\n"
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
	wantSynced(t, code, stderr)
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
			wantSynced(t, code, stderr)
			e.wantSettings(dir, url, fc44)
			if pulled := exists(filepath.Join(dir, "machines", "new.toml")); pulled != tt.pulled {
				t.Errorf("pulled = %v, want %v", pulled, tt.pulled)
			}
			where := url + ", in " + dir
			if url == "" {
				where = dir + " (it has no remote)"
			}
			want := "maisys: set up this machine as " + fc44 + " (from --machine), with the config repo " + where + "\n"
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
			name: "git isn't installed",
			setup: func(e *testEnv) ([]string, string) {
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
			name: "the default folder holds an unfinished clone",
			setup: func(e *testEnv) ([]string, string) {
				// What a clone that's killed leaves: origin, and no commits.
				remote := e.newRemote()
				e.git(e.t.TempDir(), "init", "--quiet", e.defaultRepoDir())
				e.git(e.defaultRepoDir(), "remote", "add", "origin", remote)
				return []string{"sync", remote}, "maisys: can't clone " + remote + " into %s: the folder holds an unfinished clone of it, with no commits, " +
					"as an interrupted clone leaves behind. Delete the folder and try again, or choose another folder for it with --dir\n"
			},
			check: func(e *testEnv) {
				if !exists(filepath.Join(e.defaultRepoDir(), ".git")) {
					e.t.Errorf("%s was removed, want it kept", e.defaultRepoDir())
				}
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
			code, _, stderr := e.run(args...)
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			if want = strings.ReplaceAll(want, "%s", e.defaultRepoDir()); !strings.Contains(stderr, want) {
				t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
			}
			if strings.Contains(stderr, "not implemented") {
				t.Errorf("stderr = %q, want the sync to stop", stderr)
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

// TestSyncFirstRunCloneInterrupted checks that a clone that's interrupted,
// as Ctrl-C does, leaves nothing behind, so the next sync can clone again.
// git can't clean up itself: it's killed.
func TestSyncFirstRunCloneInterrupted(t *testing.T) {
	tests := []struct {
		name  string
		empty bool // the folder exists, and is empty
	}{
		{"into a new folder", false},
		{"into an empty folder", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			url := stalledServer(t, cancel) + "/config.git"
			dir := e.defaultRepoDir()
			if tt.empty {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			var out, errOut bytes.Buffer
			code := execute(ctx, []string{"sync", url}, e.environment(&out, &errOut))
			if code != exitError || !strings.Contains(errOut.String(), context.Canceled.Error()) {
				t.Errorf("exit code = %d, stderr:\n%s\nwant %d, and the clone cancelled", code, errOut.String(), exitError)
			}
			entries, err := os.ReadDir(dir)
			switch {
			case tt.empty && (err != nil || len(entries) != 0):
				t.Errorf("%s has %d entries (%v), want it kept, and empty", dir, len(entries), err)
			case !tt.empty && !errors.Is(err, fs.ErrNotExist):
				t.Errorf("%s exists (%v), want it removed", dir, err)
			}
			if s := e.loadSettings(); s != nil {
				t.Errorf("saved the local settings %+v, want none", s)
			}
		})
	}
}

// stalledServer starts an HTTP server that never answers in time: on each
// request, it calls interrupt, then fails the request once git has had
// time to be killed. It returns the server's URL.
func stalledServer(t *testing.T, interrupt func()) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		interrupt()
		// The git that's cloning is killed as soon as the context is
		// cancelled. Answering only after that, rather than never, lets
		// the helper git started for HTTP, which outlives it, exit.
		time.Sleep(200 * time.Millisecond)
		http.Error(w, "too late", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
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

			code, _, stderr := e.run(tt.args...)
			if tt.failed {
				if code != exitError || strings.Contains(stderr, "not implemented") {
					t.Errorf("exit code = %d, stderr:\n%s\nwant %d and the sync to stop", code, stderr, exitError)
				}
			} else {
				wantSynced(t, code, stderr)
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
		// stdout is what the next sync prints, with {old_url}, {old_dir},
		// {new_url} and {new_dir} for the settings before and after.
		stdout string
	}{
		{
			name: "another URL, with --dir",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.setUp(e.newRemote())
				remote, dir := e.newRemote(), filepath.Join(e.t.TempDir(), "other")
				return []string{"sync", remote, "--dir", dir}, settings.Settings{RepoDir: dir, RepoURL: remote, Machine: fc44}
			},
			stdout: "maisys: config repo: {old_url} → {new_url}\n",
		},
		{
			name: "another URL, into the default folder",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.system.Hostname = fc44
				code, _, stderr := e.run("sync", e.newRemote(), "--dir", filepath.Join(e.t.TempDir(), "first"))
				wantSynced(e.t, code, stderr)
				remote := e.newRemote()
				return []string{"sync", remote}, settings.Settings{RepoDir: e.defaultRepoDir(), RepoURL: remote, Machine: fc44}
			},
			stdout: "maisys: config repo: {old_url} → {new_url}\n",
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
			stdout: "maisys: config repo: {old_url} → {new_url}\nmaisys: machine: box (was " + fc44 + ")\n",
		},
		{
			name: "another clone of the same URL",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				remote := e.newRemote()
				e.setUp(remote)
				dir := e.cloneOf(remote)
				return []string{"sync", "--dir", dir}, settings.Settings{RepoDir: dir, RepoURL: remote, Machine: fc44}
			},
			stdout: "maisys: config repo: {old_dir} → {new_dir}\n",
		},
		{
			name: "a clone without a remote",
			setup: func(e *testEnv) ([]string, settings.Settings) {
				e.setUp(e.newRemote())
				dir := e.newClone(exampleRepo(e.t, nil))
				return []string{"sync", "--dir", dir}, settings.Settings{RepoDir: dir, Machine: fc44}
			},
			stdout: "maisys: config repo: {old_url} → {new_dir}\n",
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
			wantSynced(t, code, stderr)
			e.wantSettings(want.RepoDir, want.RepoURL, want.Machine)
			wantOut := strings.NewReplacer(
				"{old_url}", old.RepoURL, "{old_dir}", old.RepoDir,
				"{new_url}", want.RepoURL, "{new_dir}", want.RepoDir,
			).Replace(tt.stdout)
			if stdout != wantOut {
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
		name    string
		args    []string
		env     string
		code    int
		machine string // the machine saved afterwards
		stdout  string
		stderr  string
	}{
		{
			name:    "--machine",
			args:    []string{"sync", "--machine", "phil-workp14s"},
			machine: "phil-workp14s",
			stdout:  "maisys: machine: phil-workp14s (was " + fc44 + ")\n",
		},
		{
			name:    "the same machine",
			args:    []string{"sync", "--machine", fc44},
			machine: fc44,
		},
		{
			name:    "MAISYS_MACHINE is only for one run",
			args:    []string{"sync", "-v"},
			env:     "phil-workp14s",
			machine: fc44,
			stderr:  "maisys: machine phil-workp14s (from MAISYS_MACHINE)\n",
		},
		{
			name:    "a missing machine",
			args:    []string{"sync", "--machine", "nope"},
			code:    exitError,
			machine: fc44,
			stderr:  `maisys: machines/nope.toml doesn't exist in `,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.useGit()
			remote := e.newRemote()
			clone := e.setUp(remote)
			e.vars[machineEnv] = tt.env

			code, stdout, stderr := e.run(tt.args...)
			if tt.code == exitError {
				if code != exitError || strings.Contains(stderr, "not implemented") {
					t.Errorf("exit code = %d, stderr:\n%s\nwant %d and the sync to stop", code, stderr, exitError)
				}
			} else {
				wantSynced(t, code, stderr)
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
