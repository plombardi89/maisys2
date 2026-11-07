package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/settings"
)

// gitSandbox is a git setup of its own, so tests never read or change the
// user's git config, credentials or identity.
type gitSandbox struct {
	t      *testing.T
	git    string
	global string // the global git config file
	env    []string
}

func newGitSandbox(t *testing.T) *gitSandbox {
	t.Helper()
	git, err := gitrepo.Find()
	if err != nil {
		t.Skipf("these tests need git: %v", err)
	}
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	writeFiles(t, home, map[string]string{"gitconfig": ""})
	return &gitSandbox{
		t:      t,
		git:    git,
		global: global,
		env: []string{
			"HOME=" + home,
			"USERPROFILE=" + home,
			"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + global,
			"GIT_AUTHOR_NAME=maisys test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=maisys test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		},
	}
}

// withoutIdentity makes git unable to tell who makes a commit.
func (s *gitSandbox) withoutIdentity() {
	s.t.Helper()
	s.run("", "config", "--file", s.global, "user.useConfigOnly", "true")
	s.env = append(s.env, "GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL=", "EMAIL=")
}

// run runs git in dir (anywhere, if it's "") and returns its output.
func (s *gitSandbox) run(dir string, args ...string) string {
	s.t.Helper()
	if dir == "" {
		dir = s.t.TempDir()
	}
	cmd := exec.CommandContext(s.t.Context(), s.git, args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, s.env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// runGit runs maisys in e, with git from s.
func (e *testEnv) runGit(s *gitSandbox, args ...string) (code int, stdout, stderr string) {
	e.t.Helper()
	var out, errOut bytes.Buffer
	env := e.environment(&out, &errOut)
	env.gitEnv = s.env
	env.findGit = func() (string, error) { return s.git, nil }
	code = execute(e.t.Context(), args, env)
	return code, out.String(), errOut.String()
}

// The systems config init is tested on.
var (
	fedoraSystem = facts.System{
		OS: config.Fedora, OSID: "fedora", OSVersion: "44", Arch: config.AMD64, RawArch: "amd64", Hostname: "Fedora-Box",
	}
	ubuntuSystem = facts.System{
		OS: config.Ubuntu, OSID: "ubuntu", OSVersion: "26.04", Arch: config.ARM64, RawArch: "arm64", Hostname: "-not-a-name",
	}
	windowsSystem = facts.System{
		OS: config.Windows, OSID: "windows", OSVersion: "26100", Arch: config.AMD64, RawArch: "amd64", Hostname: "DESKTOP-7Q2K3F",
	}
)

// starterFiles are the files every starter config repo has.
var starterFiles = []string{
	".gitattributes",
	".github/workflows/validate.yaml",
	"README.md",
	"files/README.md",
	"maisys.toml",
	"profiles/base.toml",
	"shell/README.md",
	"shell/bash/.gitkeep",
	"shell/fish/.gitkeep",
	"shell/zsh/.gitkeep",
}

func TestConfigInit(t *testing.T) {
	tests := []struct {
		name   string
		system facts.System
		args   []string
		// dir is the folder the repo should be in; %h is the home folder
		// and %w the working directory.
		dir      string
		empty    bool // dir exists, and is empty
		machine  string
		files    []string // besides starterFiles
		contents map[string][]string
	}{
		{
			name:    "fedora, in the default folder",
			system:  fedoraSystem,
			dir:     "%h/maisys-config",
			machine: "fedora-box",
			files:   []string{"machines/fedora-box.toml", "profiles/fedora.toml"},
			contents: map[string][]string{
				"machines/fedora-box.toml": {"\nos = \"fedora\"\n", "\nos_version = \"44\"\n", "\nprofiles = [\"base\", \"fedora\"]\n"},
				"profiles/fedora.toml":     {"\nos = \"fedora\"\n", "\n[packages]\nenabled = true\n", "\ninstall = []\n"},
			},
		},
		{
			name:    "ubuntu, in an empty folder relative to the working directory, with --machine",
			system:  ubuntuSystem,
			args:    []string{"cfg", "--machine", "laptop"},
			dir:     "%w/cfg",
			empty:   true,
			machine: "laptop",
			files:   []string{"machines/laptop.toml", "profiles/ubuntu.toml"},
			contents: map[string][]string{
				"machines/laptop.toml": {"\nos = \"ubuntu\"\n", "\nos_version = \"26.04\"\n", "\nprofiles = [\"base\", \"ubuntu\"]\n"},
				"profiles/ubuntu.toml": {"\nos = \"ubuntu\"\n", "\n[packages]\nenabled = true\n"},
			},
		},
		{
			name:    "a windows host",
			system:  windowsSystem,
			args:    []string{"%h/windows-config"},
			dir:     "%h/windows-config",
			machine: "desktop-7q2k3f",
			files: []string{
				"machines/desktop-7q2k3f-wsl.toml", "machines/desktop-7q2k3f.toml",
				"profiles/ubuntu.toml", "profiles/windows.toml", "profiles/wsl.toml",
			},
			contents: map[string][]string{
				"machines/desktop-7q2k3f.toml": {
					"\nos = \"windows\"\n", "\nprofiles = [\"windows\"]\n",
					"\n[wsl_host]\nenabled = true\n", "\ndefault_distro = \"dev\"\n",
					"\n[wsl_host.distros.dev]\n", "\nimage = \"Ubuntu-26.04\"\n", "\nmachine = \"desktop-7q2k3f-wsl\"\n", "\nuser = \"phil\"\n",
				},
				"machines/desktop-7q2k3f-wsl.toml": {"\nos = \"ubuntu\"\n", "\nos_version = \"26.04\"\n", "\nprofiles = [\"base\", \"ubuntu\", \"wsl\"]\n"},
				"profiles/windows.toml":            {"\nos = \"windows\"\n", "\n# [winget]\n", "\n# [wsl_host]\n", "\n# [windows_terminal]\n", "\n# [files]\n"},
				"profiles/wsl.toml":                {"\n[wsl]\nenabled = true\n"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := newGitSandbox(t)
			e := newTestEnv(t)
			e.system = tt.system
			e.vars["USERNAME"] = "Phil"
			expand := strings.NewReplacer("%h", e.vars["HOME"], "%w", e.wd)
			var args []string
			for _, arg := range append([]string{"config", "init"}, tt.args...) {
				args = append(args, expand.Replace(arg))
			}
			dir := filepath.FromSlash(expand.Replace(tt.dir))
			if tt.empty {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			code, stdout, stderr := e.runGit(git, args...)
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d; stdout:\n%s\nstderr:\n%s", code, exitOK, stdout, stderr)
			}
			for _, want := range []string{
				"maisys: created a config repo in " + dir + " for " + tt.machine + " (",
				"\nmaisys: committed them with git (\"Start a maisys config\")\n",
				"\nmaisys: saved it as this machine's config in " + e.settingsFile() + "\n",
				"\nNext:\n",
				"maisys config validate\n",
				"maisys sync --dry-run\n",
				"remote add origin URL\n",
			} {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout doesn't contain %q:\n%s", want, stdout)
				}
			}

			wantFiles := slices.Sorted(slices.Values(append(slices.Clone(starterFiles), tt.files...)))
			if got := strings.Split(git.run(dir, "ls-files"), "\n"); !slices.Equal(got, wantFiles) {
				t.Errorf("committed files:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(wantFiles, "\n"))
			}
			if got := git.run(dir, "log", "--format=%s"); got != "Start a maisys config" {
				t.Errorf("commits = %q, want just the first one", got)
			}
			if got := git.run(dir, "status", "--porcelain"); got != "" {
				t.Errorf("the repo has changes that aren't committed:\n%s", got)
			}
			for _, name := range wantFiles {
				data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(data, []byte("\r")) {
					t.Errorf("%s has a \\r in it", name)
				}
				if strings.HasSuffix(name, ".toml") && name != "maisys.toml" {
					schema := "#:schema https://github.com/plombardi89/maisys2/releases/latest/download/config.schema.json\n"
					if !strings.HasPrefix(string(data), schema) {
						t.Errorf("%s doesn't start with the schema line:\n%s", name, data)
					}
				}
				for _, want := range tt.contents[name] {
					if !strings.Contains(string(data), want) {
						t.Errorf("%s doesn't contain %q:\n%s", name, want, data)
					}
				}
			}

			repo := &config.Repo{FS: os.DirFS(dir), Modules: modules.All()}
			if err := repo.ValidateAll(); err != nil {
				t.Errorf("the starter config isn't valid:\n%v", err)
			}
			marker, err := repo.ReadMarker()
			if err != nil {
				t.Fatal(err)
			}
			if warnings, err := marker.Check(buildVersion()); err != nil || len(warnings) > 0 {
				t.Errorf("maisys.toml: warnings %q, error %v; want neither", warnings, err)
			}

			s, err := settings.Load(e.settingsFile())
			if err != nil {
				t.Fatal(err)
			}
			if want := (settings.Settings{FormatVersion: settings.FormatVersion, RepoDir: dir, Machine: tt.machine}); *s != want {
				t.Errorf("settings = %+v, want %+v", *s, want)
			}

			// The settings make the new repo this machine's config.
			e.wd = t.TempDir()
			code, stdout, stderr = e.run("config", "validate")
			if code != exitOK || stdout != "maisys: the config for "+tt.machine+" is valid\n" {
				t.Errorf("config validate: exit code %d, stdout %q, stderr %q", code, stdout, stderr)
			}
		})
	}
}

func TestConfigInitRequires(t *testing.T) {
	tests := []struct {
		release string // the version maisys acts as, if not a development build
		want    string
	}{
		{"", "\n# requires = \">= 0.1\"\n"},
		{"0.4.2", "\n# requires = \">= 0.4.2\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if tt.release != "" {
				actAsRelease(t, tt.release)
			}
			git := newGitSandbox(t)
			e := newTestEnv(t)
			dir := filepath.Join(t.TempDir(), "cfg")
			if code, _, stderr := e.runGit(git, "config", "init", dir); code != exitOK {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
			}
			data, err := os.ReadFile(filepath.Join(dir, "maisys.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), tt.want) || !strings.Contains(string(data), "\nconfig_version = 1\n") {
				t.Errorf("maisys.toml doesn't contain config_version = 1 and %q:\n%s", tt.want, data)
			}
		})
	}
}

func TestConfigInitFailures(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the environment, given the folder to create the
		// repo in.
		setup  func(e *testEnv, git *gitSandbox, dir string)
		args   []string
		want   []string // %d is the folder
		noGit  bool
		exists bool // dir exists, and must be left as it is
	}{
		{
			name: "the folder isn't empty",
			setup: func(e *testEnv, _ *gitSandbox, dir string) {
				writeFiles(e.t, dir, map[string]string{"notes.txt": "mine\n"})
			},
			want:   []string{"maisys: %d already exists, and isn't empty: choose a new or empty folder for the config repo\n"},
			exists: true,
		},
		{
			name: "the folder is a file",
			setup: func(e *testEnv, _ *gitSandbox, dir string) {
				writeFiles(e.t, filepath.Dir(dir), map[string]string{filepath.Base(dir): "mine\n"})
			},
			want:   []string{"maisys: %d already exists, and isn't a folder"},
			exists: true,
		},
		{
			name: "an unsupported system",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.system = facts.System{OSID: "debian", OSVersion: "13", Arch: config.AMD64, RawArch: "amd64", Hostname: "box"}
			},
			want: []string{"maisys: can't create a config for this system (debian 13, amd64): maisys supports Fedora, Ubuntu and Windows\n"},
		},
		{
			name: "the hostname isn't a valid machine name",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.system.Hostname = "-odd"
			},
			want: []string{`maisys: the hostname "-odd" isn't a valid machine name (use lower-case letters, digits, '.', '-' and '_', starting with a letter or digit): choose a name with --machine` + "\n"},
		},
		{
			name: "no hostname",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.system.Hostname = ""
			},
			want: []string{`maisys: the hostname "" isn't a valid machine name`},
		},
		{
			name: "--machine isn't a valid machine name",
			args: []string{"--machine", "Box"},
			want: []string{`maisys: invalid machine name "Box" (from --machine): use lower-case letters`},
		},
		{
			name: "MAISYS_MACHINE isn't a valid machine name",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.vars[machineEnv] = "a/b"
			},
			want: []string{`maisys: invalid machine name "a/b" (from MAISYS_MACHINE)`},
		},
		{
			name:  "no git",
			noGit: true,
			want:  []string{"maisys: git isn't installed\nInstall it with: "},
		},
		{
			name: "git doesn't know who makes the commit",
			setup: func(_ *testEnv, git *gitSandbox, _ string) {
				git.withoutIdentity()
			},
			want: []string{
				"maisys: git doesn't know your name and email address, which every commit records. Set them with:\n",
				`git config --global user.name "Your Name"`,
			},
		},
		{
			name: "broken local settings",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				writeFiles(e.t, filepath.Dir(e.settingsFile()), map[string]string{"maisys.toml": "format_version = 1\nrepo = 1\n"})
			},
			want: []string{"maisys.toml:2:1: unknown key repo\n"},
		},
		{
			name: "no Linux user for the distro, and no terminal to ask for one",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.system = windowsSystem
				e.vars["USERNAME"] = "Phil Lombardi"
			},
			want: []string{`maisys: can't choose the Linux user for the WSL distro dev: the Windows user name "Phil Lombardi" isn't a valid Linux user name, so run maisys config init in a terminal to choose one` + "\n"},
		},
		{
			name: "no answer for the distro's Linux user",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.system = windowsSystem
				e.terminal = true
				e.stdin = "\n"
			},
			want: []string{"maisys: no Linux user chosen\n"},
		},
		{
			name: "a remote that doesn't pass validation",
			setup: func(e *testEnv, _ *gitSandbox, _ string) {
				e.system = windowsSystem
				e.vars["USERNAME"] = "phil"
			},
			args: []string{"--remote", "ssh://example.com"},
			want: []string{
				"maisys: the starter config doesn't pass validation, so it wasn't written:\n",
				"machines/desktop-7q2k3f.toml:",
				"wsl_host.config_url is required, because this machine clones the config repo over SSH",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := newGitSandbox(t)
			e := newTestEnv(t)
			e.system = fedoraSystem
			dir := filepath.Join(t.TempDir(), "cfg")
			if tt.setup != nil {
				tt.setup(e, git, dir)
			}
			var before []string
			if tt.exists {
				before = listFiles(t, filepath.Dir(dir))
			}
			_, statErr := os.Stat(e.settingsFile())
			hadSettings := statErr == nil

			args := append([]string{"config", "init", dir}, tt.args...)
			var code int
			var stdout, stderr string
			if tt.noGit {
				var out, errOut bytes.Buffer
				env := e.environment(&out, &errOut)
				env.findGit = func() (string, error) { return "", gitrepo.ErrNoGit }
				code, stdout, stderr = execute(t.Context(), args, env), out.String(), errOut.String()
			} else {
				code, stdout, stderr = e.runGit(git, args...)
			}
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			for _, want := range tt.want {
				if want = strings.ReplaceAll(want, "%d", dir); !strings.Contains(stderr, want) {
					t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
				}
			}
			if strings.Contains(stdout, "created") {
				t.Errorf("stdout = %q, want nothing created", stdout)
			}

			if tt.exists {
				if after := listFiles(t, filepath.Dir(dir)); !slices.Equal(after, before) {
					t.Errorf("files after = %q, want them unchanged: %q", after, before)
				}
			} else if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s exists, want nothing written (stat error %v)", dir, err)
			}
			if _, err := os.Stat(e.settingsFile()); !hadSettings && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the local settings exist, want nothing saved (stat error %v)", err)
			}
		})
	}
}

// listFiles lists the files and folders in dir and below it.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		files = append(files, path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestConfigInitReplacesSettings(t *testing.T) {
	git := newGitSandbox(t)
	e := newTestEnv(t)
	e.system = fedoraSystem
	oldDir := exampleRepo(t, nil)
	e.saveSettings(settings.Settings{RepoDir: oldDir, RepoURL: "https://example.com/old.git", Machine: "phil-workp14s"})
	dir := filepath.Join(t.TempDir(), "cfg")

	code, stdout, stderr := e.runGit(git, "config", "init", dir)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	want := "maisys: replaced the local settings in " + e.settingsFile() + "\n" +
		"maisys: this machine now uses " + dir + " (was " + oldDir + ")\n" +
		"maisys: this machine is now fedora-box (was phil-workp14s)\n"
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout doesn't contain:\n%s\nstdout:\n%s", want, stdout)
	}
	s, err := settings.Load(e.settingsFile())
	if err != nil {
		t.Fatal(err)
	}
	if s.RepoDir != dir || s.RepoURL != "" || s.Machine != "fedora-box" {
		t.Errorf("settings = %+v, want the new repo, without a URL, and machine fedora-box", *s)
	}
}

func TestConfigInitRemote(t *testing.T) {
	git := newGitSandbox(t)
	e := newTestEnv(t)
	e.system = fedoraSystem
	bare := filepath.Join(t.TempDir(), "config.git")
	git.run("", "init", "--quiet", "--bare", "--initial-branch=main", bare)
	dir := filepath.Join(t.TempDir(), "cfg")

	code, stdout, stderr := e.runGit(git, "config", "init", dir, "--remote", bare)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	if !strings.Contains(stdout, "\nmaisys: pushed it to origin\n") || strings.Contains(stdout, "remote add origin") {
		t.Errorf("stdout should say the repo was pushed, and not how to add a remote:\n%s", stdout)
	}
	if local, remote := git.run(dir, "rev-parse", "HEAD"), git.run(bare, "rev-parse", "main"); local != remote {
		t.Errorf("origin's main is %s, want %s", remote, local)
	}
	if upstream := git.run(dir, "rev-parse", "--abbrev-ref", "@{upstream}"); upstream != "origin/main" {
		t.Errorf("upstream = %q, want origin/main", upstream)
	}
	s, err := settings.Load(e.settingsFile())
	if err != nil {
		t.Fatal(err)
	}
	if s.RepoDir != dir || s.RepoURL != bare {
		t.Errorf("settings = %+v, want repo_dir %s and repo_url %s", *s, dir, bare)
	}
}

func TestConfigInitPushFails(t *testing.T) {
	git := newGitSandbox(t)
	e := newTestEnv(t)
	e.system = fedoraSystem
	missing := filepath.Join(t.TempDir(), "missing.git")
	dir := filepath.Join(t.TempDir(), "my cfg")

	code, stdout, stderr := e.runGit(git, "config", "init", dir, "--remote", missing)
	if code != exitError {
		t.Errorf("exit code = %d, want %d", code, exitError)
	}
	want := "\nThe files are committed in " + dir + ". To push them later, run:\n  git -C \"" + dir + "\" push -u origin HEAD\n"
	if !strings.Contains(stderr, "maisys: couldn't push ") || !strings.HasSuffix(stderr, want) {
		t.Errorf("stderr:\n%s\nwant it to contain %q and end with:\n%s", stderr, "maisys: couldn't push ", want)
	}
	if !strings.Contains(stdout, "maisys: committed them with git") || strings.Contains(stdout, "pushed") {
		t.Errorf("stdout should say the repo was committed, but not pushed:\n%s", stdout)
	}
	if got := git.run(dir, "log", "--format=%s"); got != "Start a maisys config" {
		t.Errorf("commits = %q, want the first one", got)
	}
	if got := git.run(dir, "remote", "get-url", "origin"); got != missing {
		t.Errorf("origin = %q, want %q", got, missing)
	}
	s, err := settings.Load(e.settingsFile())
	if err != nil {
		t.Fatal(err)
	}
	if s.RepoDir != dir || s.RepoURL != missing {
		t.Errorf("settings = %+v, want repo_dir %s and repo_url %s", *s, dir, missing)
	}
}

func TestConfigInitWindowsSSHRemote(t *testing.T) {
	git := newGitSandbox(t)
	// The push fails without trying to reach the host.
	git.env = append(git.env, "GIT_SSH_COMMAND=false")
	e := newTestEnv(t)
	e.system = windowsSystem
	e.vars["USERNAME"] = "phil"
	dir := filepath.Join(t.TempDir(), "cfg")

	code, _, stderr := e.runGit(git, "config", "init", dir, "--remote", "git@example.invalid:phil/config.git")
	if code != exitError || !strings.Contains(stderr, "couldn't push") {
		t.Fatalf("exit code = %d, stderr:\n%s\nwant %d and a failed push", code, stderr, exitError)
	}
	data, err := os.ReadFile(filepath.Join(dir, "machines", "desktop-7q2k3f.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "\nconfig_url = \"https://example.invalid/phil/config.git\"\n"; !strings.Contains(string(data), want) {
		t.Errorf("the machine file doesn't contain %q:\n%s", want, data)
	}
	repo := &config.Repo{FS: os.DirFS(dir), Modules: modules.All(), URL: "git@example.invalid:phil/config.git"}
	if err := repo.ValidateAll(); err != nil {
		t.Errorf("the starter config isn't valid:\n%v", err)
	}
}

func TestConfigInitDistroUser(t *testing.T) {
	const question = "maisys: the config creates a WSL distro, dev, with a Linux user for you.\n"
	tests := []struct {
		name        string
		windowsUser string
		terminal    bool
		stdin       string
		want        string
		wantOut     string
	}{
		{name: "no terminal", windowsUser: "Phil", want: "phil"},
		{name: "the suggested user", windowsUser: "Phil", terminal: true, stdin: "\n", want: "phil", wantOut: question + "Linux user name [phil]: "},
		{name: "the suggested user, when stdin ends", windowsUser: "Phil", terminal: true, want: "phil", wantOut: "Linux user name [phil]: \n"},
		{name: "another user", windowsUser: "Phil", terminal: true, stdin: " dev_1 \n", want: "dev_1"},
		{
			name: "invalid answers", windowsUser: "Phil", terminal: true, stdin: "Bad Name\nroot\nok\n", want: "ok",
			wantOut: `Linux user name [phil]: maisys: "Bad Name" isn't a valid Linux user name: use one with lower-case letters, digits, '_' and '-', starting with a letter or '_', and up to 32 characters, other than root` + "\n" +
				`Linux user name [phil]: maisys: "root" isn't a valid Linux user name`,
		},
		{
			name: "no suggestion", windowsUser: "Phil Lombardi", terminal: true, stdin: "\nphil\n", want: "phil",
			wantOut: question + "Linux user name: Linux user name: ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := newGitSandbox(t)
			e := newTestEnv(t)
			e.system = windowsSystem
			e.vars["USERNAME"] = tt.windowsUser
			e.terminal = tt.terminal
			e.stdin = tt.stdin
			dir := filepath.Join(t.TempDir(), "cfg")

			code, stdout, stderr := e.runGit(git, "config", "init", dir)
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
			}
			if !strings.Contains(stdout, tt.wantOut) {
				t.Errorf("stdout doesn't contain %q:\n%s", tt.wantOut, stdout)
			}
			if !tt.terminal && strings.Contains(stdout, "Linux user name") {
				t.Errorf("stdout asks for the user without a terminal:\n%s", stdout)
			}
			data, err := os.ReadFile(filepath.Join(dir, "machines", "desktop-7q2k3f.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if want := "\nuser = \"" + tt.want + "\"\n"; !strings.Contains(string(data), want) {
				t.Errorf("the machine file doesn't contain %q:\n%s", want, data)
			}
		})
	}
}

// TestConfigInitExamples checks that the commented-out examples in the
// starter profiles load when uncommented, and that one of them, turned on,
// passes validation.
func TestConfigInitExamples(t *testing.T) {
	git := newGitSandbox(t)
	e := newTestEnv(t)
	e.system = windowsSystem
	e.vars["USERNAME"] = "phil"
	dir := filepath.Join(t.TempDir(), "cfg")
	if code, _, stderr := e.runGit(git, "config", "init", dir); code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}

	for _, tt := range []struct {
		profile string
		os      config.OS
		modules []string // some of the modules it has examples of
	}{
		{"base", config.Fedora, []string{"sudo", "packages", "files", "shell", "golang", "wsl"}},
		{"windows", config.Windows, []string{"files", "winget", "wsl_host", "windows_terminal"}},
	} {
		data, err := os.ReadFile(filepath.Join(dir, "profiles", tt.profile+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		var examples []string
		for block := range strings.SplitSeq(string(data), "\n\n") {
			if strings.HasPrefix(block, "# # ") {
				examples = append(examples, uncommentLines(block))
			}
		}
		machine := "os = \"" + string(tt.os) + "\"\n\n" + strings.Join(examples, "\n\n") + "\n"
		r := &config.Repo{FS: fstest.MapFS{"machines/box.toml": {Data: []byte(machine)}}, Modules: modules.All()}
		if _, err := r.LoadMachine("box"); err != nil {
			t.Errorf("profiles/%s.toml: the examples don't load:\n%v", tt.profile, err)
		}
		for _, name := range tt.modules {
			if !strings.Contains(machine, "\n["+name+"]\n") {
				t.Errorf("profiles/%s.toml has no example for %s", tt.profile, name)
			}
		}
		if strings.Contains(machine, "[legacy]") {
			t.Errorf("profiles/%s.toml has an example for the hidden legacy module", tt.profile)
		}
	}

	// Turn on golang in the base profile with its example.
	base := filepath.Join(dir, "profiles", "base.toml")
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	blocks := strings.Split(string(data), "\n\n")
	found := false
	for i, block := range blocks {
		if strings.Contains(block, "\n# [golang]\n") {
			blocks[i] = strings.Replace(uncommentLines(block), "\nenabled = false\n", "\nenabled = true\n", 1)
			found = true
		}
	}
	if !found {
		t.Fatal("profiles/base.toml has no golang example")
	}
	writeFiles(t, dir, map[string]string{"profiles/base.toml": strings.Join(blocks, "\n\n")})
	repo := &config.Repo{FS: os.DirFS(dir), Modules: modules.All()}
	if err := repo.ValidateAll(); err != nil {
		t.Fatalf("the config with golang turned on isn't valid:\n%v", err)
	}
	m, err := repo.LoadMachine("desktop-7q2k3f-wsl")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Modules["golang"].IsEnabled() {
		t.Error("golang isn't turned on")
	}
}

// uncommentLines removes the leading "# " from every line of s.
func uncommentLines(s string) string {
	var b strings.Builder
	for line := range strings.Lines(s) {
		b.WriteString(strings.TrimPrefix(line, "# "))
	}
	return b.String()
}
