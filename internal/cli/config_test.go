package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/plombardi89/maisys2/internal/settings"
)

// exampleRepo copies the example config repo from issue #1 into a new
// folder, adds files on top, and returns the folder.
func exampleRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "maisys-config")
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("..", "modules", "testdata", "example"))); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, files)
	return dir
}

// devRequiresWarning is the warning a development build gives for the
// example repo's requires.
const devRequiresWarning = `maisys: warning: maisys.toml:2:12: this maisys (dev) isn't a release, so requires = ">= 0.6" isn't checked` + "\n"

func TestFindConfigRepo(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares e, given the example repo's folder, and returns
		// the folder the config should come from.
		setup func(e *testEnv, repo string) string
	}{
		{
			name: "the working directory",
			setup: func(e *testEnv, repo string) string {
				e.wd = repo
				return repo
			},
		},
		{
			name: "a parent of the working directory",
			setup: func(e *testEnv, repo string) string {
				e.wd = filepath.Join(repo, "files", "nvim")
				return repo
			},
		},
		{
			name: "the working directory before the local settings",
			setup: func(e *testEnv, repo string) string {
				e.saveSettings(settings.Settings{RepoDir: exampleRepo(e.t, nil)})
				e.wd = filepath.Join(repo, "machines")
				return repo
			},
		},
		{
			name: "the local settings",
			setup: func(e *testEnv, repo string) string {
				e.saveSettings(settings.Settings{RepoDir: repo})
				return repo
			},
		},
		{
			name: "the local settings, from the folder that holds them",
			setup: func(e *testEnv, repo string) string {
				// The settings file is also called maisys.toml, but
				// isn't a config repo.
				e.saveSettings(settings.Settings{RepoDir: repo})
				e.wd = filepath.Dir(e.settingsFile())
				return repo
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.vars[machineEnv] = "phil-workp14s"
			want := tt.setup(e, exampleRepo(t, nil))

			code, stdout, stderr := e.run("config")
			if code != exitOK {
				t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
			}
			header := "# machine phil-workp14s (from MAISYS_MACHINE), config repo " + want + "\n\n"
			if !strings.HasPrefix(stdout, header) {
				t.Errorf("stdout doesn't start with %q:\n%s", header, stdout)
			}
			if stderr != devRequiresWarning {
				t.Errorf("stderr = %q, want only %q", stderr, devRequiresWarning)
			}
		})
	}
}

func TestNoConfigRepo(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *testEnv)
		want  string // %s is the settings file
	}{
		{
			name: "no repo and no local settings",
			want: "maisys: no config repo: run `maisys sync CONFIG-URL` to set one up, `maisys config init` to start one, or run this inside a config repo\n",
		},
		{
			name: "no repo, and nowhere to keep local settings",
			setup: func(e *testEnv) {
				e.vars = map[string]string{}
			},
			want: "maisys: no config repo: run `maisys sync CONFIG-URL` to set one up, `maisys config init` to start one, or run this inside a config repo; can't find where to keep maisys's files: ",
		},
		{
			name: "the repo in the local settings is gone",
			setup: func(e *testEnv) {
				e.saveSettings(settings.Settings{RepoDir: filepath.Join(e.t.TempDir(), "gone")})
			},
			want: "doesn't exist (it's set in %s)\n",
		},
		{
			name: "the repo in the local settings isn't a config repo",
			setup: func(e *testEnv) {
				e.saveSettings(settings.Settings{RepoDir: e.t.TempDir()})
			},
			want: ": maisys.toml doesn't exist, so this isn't a maisys config repo\n",
		},
		{
			name: "broken local settings",
			setup: func(e *testEnv) {
				writeFiles(e.t, filepath.Dir(e.settingsFile()), map[string]string{"maisys.toml": "format_version = 1\nrepo = 1\n"})
			},
			want: "maisys.toml:2:1: unknown key repo\n",
		},
	}
	for _, tt := range tests {
		for _, args := range [][]string{{"config"}, {"config", "validate", "--all"}} {
			t.Run(tt.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				e := newTestEnv(t)
				var settingsFile string
				if tt.setup != nil {
					settingsFile = e.settingsFile()
					tt.setup(e)
				}
				code, stdout, stderr := e.run(args...)
				if code != exitError {
					t.Errorf("exit code = %d, want %d", code, exitError)
				}
				if want := strings.ReplaceAll(tt.want, "%s", settingsFile); !strings.Contains(stderr, want) {
					t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
				}
				if stdout != "" {
					t.Errorf("stdout = %q, want nothing", stdout)
				}
			})
		}
	}
}

func TestConfigRepoMarker(t *testing.T) {
	tests := []struct {
		name    string
		marker  string
		release string // the version maisys acts as, if not a development build
		code    int
		stderr  string
	}{
		{
			name:   "requires isn't checked by development builds",
			marker: "config_version = 1\nrequires = \">= 99\"\n",
			code:   exitOK,
			stderr: `maisys: warning: maisys.toml:2:12: this maisys (dev) isn't a release, so requires = ">= 99" isn't checked` + "\n",
		},
		{
			name:    "requires is met",
			marker:  "config_version = 1\nrequires = \">= 0.6\"\n",
			release: "0.6.0",
			code:    exitOK,
		},
		{
			name:    "requires isn't met",
			marker:  "config_version = 1\nrequires = \">= 0.6\"\n",
			release: "0.1.0",
			code:    exitError,
			stderr: "maisys.toml:2:12: this config needs maisys >= 0.6, but this is maisys 0.1.0; update maisys\n" +
				"maisys: 1 problem found\n",
		},
		{
			name:   "a newer config_version",
			marker: "config_version = 2\n",
			code:   exitError,
			stderr: "maisys.toml:1:18: config_version 2 needs a newer maisys: this one reads config_version 1; update maisys\n" +
				"maisys: 1 problem found\n",
		},
		{
			name:   "an unknown key",
			marker: "config_version = 1\nmirror = \"https://example.com\"\n",
			code:   exitError,
			stderr: "maisys.toml:2:1: unknown key mirror\n" +
				"maisys: 1 problem found\n" +
				"maisys: an unknown key may mean the config needs a newer maisys than this one (dev)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.release != "" {
				actAsRelease(t, tt.release)
			}
			e := newTestEnv(t)
			e.wd = exampleRepo(t, map[string]string{"maisys.toml": tt.marker})
			code, _, stderr := e.run("config", "validate", "--all")
			if code != tt.code || stderr != tt.stderr {
				t.Errorf("exit code = %d, stderr:\n%s\nwant %d, stderr:\n%s", code, stderr, tt.code, tt.stderr)
			}
		})
	}
}

func TestConfigOutput(t *testing.T) {
	e := newTestEnv(t)
	e.wd = exampleRepo(t, nil)
	e.system.Hostname = "Phil-WorkP14s"

	code, plain, stderr := e.run("config")
	if code != exitOK {
		t.Fatalf("config: exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	code, explained, stderr := e.run("config", "--explain")
	if code != exitOK {
		t.Fatalf("config --explain: exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}

	header := "# machine phil-workp14s (from the hostname), config repo " + e.wd + "\n\n"
	for name, out := range map[string]string{"config": plain, "config --explain": explained} {
		if !strings.HasPrefix(out, header) {
			t.Errorf("%s: output doesn't start with %q:\n%s", name, header, out)
		}
		var cfg struct {
			OS       string   `toml:"os"`
			Profiles []string `toml:"profiles"`
			Intellij struct {
				Enabled bool   `toml:"enabled"`
				Version string `toml:"version"`
			} `toml:"intellij"`
			Packages struct {
				Install []string `toml:"install"`
			} `toml:"packages"`
		}
		if err := toml.Unmarshal([]byte(out), &cfg); err != nil {
			t.Fatalf("%s: output isn't valid TOML: %v\n%s", name, err, out)
		}
		if cfg.OS != "ubuntu" || !cfg.Intellij.Enabled || cfg.Intellij.Version != "2026.2.3" || len(cfg.Packages.Install) != 9 {
			t.Errorf("%s: output has the wrong config: %+v", name, cfg)
		}
	}

	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^version = "2026\.2\.3" +# machines/phil-workp14s\.toml$`),
		regexp.MustCompile(`(?m)^install_deps = false +# default$`),
		regexp.MustCompile(`(?m)^install = \[.*"podman-compose"\] +# profiles/ubuntu\.toml, machines/phil-workp14s\.toml$`),
	} {
		if !want.MatchString(explained) {
			t.Errorf("config --explain output doesn't match %s:\n%s", want, explained)
		}
		if want.MatchString(plain) {
			t.Errorf("config output matches %s, want no origins:\n%s", want, plain)
		}
	}
}

func TestConfigFailures(t *testing.T) {
	tests := []struct {
		name   string
		files  map[string]string
		env    string
		stderr []string
	}{
		{
			name: "no machine",
			stderr: []string{
				`maisys: can't tell which machine this is: no machine file in %s is named after the hostname "nothing-matches"` + "\n" +
					"machines for this system (fedora 44, amd64): workbox-camwin-fc44\n" +
					"set MAISYS_MACHINE to choose one\n",
			},
		},
		{
			name:  "a machine that doesn't load",
			env:   "box",
			files: map[string]string{"machines/box.toml": "os = \"fedora\"\nprofiles = [\"base\", \"nope\"]\ncolour = \"blue\"\n"},
			stderr: []string{
				"machines/box.toml:3:1: unknown key colour\n" +
					"maisys: 1 problem found\n" +
					"maisys: an unknown key may mean the config needs a newer maisys than this one (dev)\n",
			},
		},
		{
			name:   "a missing profile",
			env:    "box",
			files:  map[string]string{"machines/box.toml": "os = \"fedora\"\nprofiles = [\"base\", \"nope\"]\n"},
			stderr: []string{"profiles/nope.toml doesn't exist\nmaisys: 1 problem found\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.wd = exampleRepo(t, tt.files)
			e.vars[machineEnv] = tt.env
			code, stdout, stderr := e.run("config")
			if code != exitError {
				t.Errorf("exit code = %d, want %d", code, exitError)
			}
			for _, want := range tt.stderr {
				want = strings.ReplaceAll(want, "%s", e.wd)
				if !strings.HasSuffix(stderr, want) {
					t.Errorf("stderr doesn't end with %q:\n%s", want, stderr)
				}
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	sshSettings := func(e *testEnv) {
		e.saveSettings(settings.Settings{RepoDir: e.wd, RepoURL: "git@github.com:plombardi89/maisys-config.git"})
	}
	// host is a Windows host that relies on the config URL to set up its
	// distro.
	host := map[string]string{
		"machines/host.toml": `os = "windows"

[wsl_host]
enabled = true

[wsl_host.distros.dev]
image = "FedoraLinux-44"
machine = "workbox-camwin-fc44"
user = "phil"
`,
	}
	tests := []struct {
		name   string
		args   []string
		files  map[string]string
		env    string
		setup  func(e *testEnv)
		code   int
		stdout string
		stderr string // after the development build's warning; %s is the repo's folder
	}{
		{
			name:   "this machine",
			args:   []string{"config", "validate"},
			env:    "workbox-camwin",
			code:   exitOK,
			stdout: "maisys: the config for workbox-camwin is valid\n",
		},
		{
			name:   "every machine",
			args:   []string{"config", "validate", "--all"},
			code:   exitOK,
			stdout: "maisys: all 3 machines are valid\n",
		},
		{
			name:   "the only machine",
			args:   []string{"config", "validate", "--all"},
			setup:  func(e *testEnv) { removeMachines(e.t, e.wd, "phil-workp14s", "workbox-camwin") },
			code:   exitOK,
			stdout: "maisys: the only machine, workbox-camwin-fc44, is valid\n",
		},
		{
			name: "problems",
			args: []string{"config", "validate"},
			env:  "box",
			files: map[string]string{"machines/box.toml": `os = "fedora"
arch = "arm64"
profiles = ["base", "gaming"]

[golang]
enabled = true
version = "1.27"
`},
			code: exitError,
			stderr: `machines/box.toml:7:11: golang.version must be a full Go version, such as "1.26.4", not "1.27": partial versions aren't supported` + "\n" +
				`profiles/gaming.toml:2:11: steam only works on amd64, but machine "box" is arm64` + "\n" +
				"maisys: 2 problems found\n",
		},
		{
			name:   "problems in every machine",
			args:   []string{"config", "validate", "--all"},
			files:  map[string]string{"machines/box.toml": "os = \"fedora\"\ncolour = \"blue\"\n", "profiles/extra.toml": "os = \"debian\"\n"},
			code:   exitError,
			stderr: "machines/box.toml:2:1: unknown key colour\nprofiles/extra.toml:1:6: os must be fedora, ubuntu or windows, not \"debian\"\nmaisys: 2 problems found\nmaisys: an unknown key may mean the config needs a newer maisys than this one (dev)\n",
		},
		{
			name:   "the URL in the local settings",
			args:   []string{"config", "validate"},
			files:  host,
			env:    "host",
			setup:  sshSettings,
			code:   exitError,
			stderr: "machines/host.toml:3:2: wsl_host.config_url is required, because this machine clones the config repo over SSH (git@github.com:plombardi89/maisys-config.git), and new distros clone it over HTTPS\nmaisys: 1 problem found\n",
		},
		{
			name:  "the URL in the local settings, from inside the repo",
			args:  []string{"config", "validate"},
			files: host,
			env:   "host",
			setup: func(e *testEnv) {
				sshSettings(e)
				e.wd = filepath.Join(e.wd, "machines")
			},
			code:   exitError,
			stderr: "machines/host.toml:3:2: wsl_host.config_url is required",
		},
		{
			name:   "the URL in the local settings, with every machine",
			args:   []string{"config", "validate", "--all"},
			files:  host,
			setup:  sshSettings,
			code:   exitOK,
			stdout: "maisys: all 4 machines are valid\n",
		},
		{
			name:  "the URL of another repo",
			args:  []string{"config", "validate", "--all"},
			files: host,
			setup: func(e *testEnv) {
				e.saveSettings(settings.Settings{RepoDir: exampleRepo(e.t, nil), RepoURL: "git@github.com:plombardi89/maisys-config.git"})
			},
			code:   exitOK,
			stdout: "maisys: all 4 machines are valid\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.wd = exampleRepo(t, tt.files)
			e.vars[machineEnv] = tt.env
			repo := e.wd
			if tt.setup != nil {
				tt.setup(e)
			}
			code, stdout, stderr := e.run(tt.args...)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if stdout != tt.stdout {
				t.Errorf("stdout = %q, want %q", stdout, tt.stdout)
			}
			want := devRequiresWarning + strings.ReplaceAll(tt.stderr, "%s", repo)
			if !strings.HasPrefix(stderr, want) {
				t.Errorf("stderr:\n%s\nwant it to start with:\n%s", stderr, want)
			}
			if tt.code == exitError {
				problem := regexp.MustCompile(`^[a-z/._-]+\.toml:[0-9]+:[0-9]+: `)
				for line := range strings.Lines(strings.TrimPrefix(stderr, devRequiresWarning)) {
					if !strings.HasPrefix(line, "maisys: ") && !problem.MatchString(line) {
						t.Errorf("problem %q doesn't start with file:line:column", line)
					}
				}
			}
		})
	}
}

func TestValidateVerbose(t *testing.T) {
	e := newTestEnv(t)
	e.wd = exampleRepo(t, nil)
	e.system.Hostname = "workbox-camwin-fc44"
	code, _, stderr := e.run("config", "validate", "-v")
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr:\n%s", code, exitOK, stderr)
	}
	for _, want := range []string{
		"maisys: config repo " + e.wd + " (from the working directory)\n",
		"maisys: machine workbox-camwin-fc44 (from the hostname)\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr doesn't contain %q:\n%s", want, stderr)
		}
	}
}

func removeMachines(t *testing.T, repo string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Remove(filepath.Join(repo, "machines", name+".toml")); err != nil {
			t.Fatal(err)
		}
	}
}
