package config_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/plombardi89/maisys2/internal/config"
)

func newScaffold(o config.OS) *config.Scaffold {
	return &config.Scaffold{
		Machine:    "box",
		OS:         o,
		OSVersion:  "44",
		DistroUser: "phil",
		Modules:    renderModules,
		Docs:       testDocs,
		UpdateRepo: "someone/maisys-fork",
	}
}

func scaffoldFiles(t *testing.T, s *config.Scaffold) map[string]string {
	t.Helper()
	files, err := s.Files()
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestScaffoldFiles(t *testing.T) {
	common := []string{
		".gitattributes", ".github/workflows/validate.yaml", "README.md", "files/README.md", "maisys.toml",
		"profiles/base.toml", "shell/README.md", "shell/bash/.gitkeep", "shell/fish/.gitkeep", "shell/zsh/.gitkeep",
	}
	tests := []struct {
		os    config.OS
		files []string
	}{
		{config.Fedora, []string{"machines/box.toml", "profiles/fedora.toml"}},
		{config.Ubuntu, []string{"machines/box.toml", "profiles/ubuntu.toml"}},
		{config.Windows, []string{"machines/box-wsl.toml", "machines/box.toml", "profiles/ubuntu.toml", "profiles/windows.toml", "profiles/wsl.toml"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.os), func(t *testing.T) {
			files := scaffoldFiles(t, newScaffold(tt.os))
			want := slices.Sorted(slices.Values(append(slices.Clone(common), tt.files...)))
			if got := slices.Sorted(maps.Keys(files)); !slices.Equal(got, want) {
				t.Errorf("files = %q, want %q", got, want)
			}
			for name, data := range files {
				if strings.Contains(data, "\r") {
					t.Errorf("%s has a \\r in it", name)
				}
				if strings.Contains(data, "plombardi89") || strings.Contains(data, "{repo}") {
					t.Errorf("%s doesn't link to the update repo:\n%s", name, data)
				}
				if !strings.HasSuffix(name, ".toml") {
					continue
				}
				if name != "maisys.toml" && !strings.HasPrefix(data, "#:schema https://github.com/someone/maisys-fork/releases/latest/download/config.schema.json\n\n") {
					t.Errorf("%s doesn't start with the schema line:\n%s", name, data)
				}
				for line := range strings.Lines(data) {
					line = strings.TrimSuffix(line, "\n")
					if strings.HasPrefix(line, "#") && !strings.Contains(line, "https://") && utf8.RuneCountInString(line) > 78 {
						t.Errorf("%s: comment %q is longer than 78 characters", name, line)
					}
				}
			}
		})
	}
}

func TestScaffoldExamples(t *testing.T) {
	files := scaffoldFiles(t, newScaffold(config.Windows))
	tests := []struct {
		file           string
		want, unwanted []string
	}{
		// renderModules: golang, files and kinds work on Fedora, secret is
		// hidden, wide works everywhere and winonly only on Windows.
		{"profiles/base.toml", []string{"golang", "files", "kinds", "wide"}, []string{"secret", "winonly"}},
		{"profiles/windows.toml", []string{"wide", "winonly"}, []string{"golang", "files", "kinds", "secret"}},
	}
	for _, tt := range tests {
		data := files[tt.file]
		for _, name := range tt.want {
			if example := config.ExampleTOML(moduleNamed(t, name), testDocs); !strings.Contains(data, "\n\n"+example) {
				t.Errorf("%s doesn't have the example of %s:\n%s", tt.file, name, data)
			}
		}
		for _, name := range tt.unwanted {
			if strings.Contains(data, "# ["+name+"]") {
				t.Errorf("%s has an example of %s:\n%s", tt.file, name, data)
			}
		}
	}
	if !strings.Contains(files["profiles/base.toml"], "https://github.com/someone/maisys-fork/blob/main/docs/modules.md\n") {
		t.Errorf("profiles/base.toml doesn't link to the module docs:\n%s", files["profiles/base.toml"])
	}
}

func moduleNamed(t *testing.T, name string) config.Module {
	t.Helper()
	for _, mod := range renderModules {
		if mod.Info().Name == name {
			return mod
		}
	}
	t.Fatalf("no module %s", name)
	return nil
}

func TestScaffoldOptions(t *testing.T) {
	tests := []struct {
		name     string
		change   func(s *config.Scaffold)
		file     string
		want     []string
		unwanted []string
	}{
		{
			name:     "a development build",
			file:     "maisys.toml",
			want:     []string{"\nconfig_version = 1\n", "\n# requires = \">= 0.1\"\n"},
			unwanted: []string{"\nrequires"},
		},
		{
			name:   "a release",
			change: func(s *config.Scaffold) { s.Version = "0.4.2" },
			file:   "maisys.toml",
			want:   []string{"\n# requires = \">= 0.4.2\"\n"},
		},
		{
			name: "a Fedora machine",
			file: "machines/box.toml",
			want: []string{"\nos = \"fedora\"\n", "\nos_version = \"44\"\n", "\nprofiles = [\"base\", \"fedora\"]\n"},
		},
		{
			name:     "no os_version",
			change:   func(s *config.Scaffold) { s.OSVersion = "" },
			file:     "machines/box.toml",
			unwanted: []string{"os_version"},
		},
		{
			name:     "an os_version that machine files can't have",
			change:   func(s *config.Scaffold) { s.OSVersion = "rawhide" },
			file:     "machines/box.toml",
			unwanted: []string{"os_version"},
		},
		{
			name:   "an Ubuntu machine",
			change: func(s *config.Scaffold) { s.OS, s.OSVersion = config.Ubuntu, "26.04" },
			file:   "machines/box.toml",
			want:   []string{"\nos = \"ubuntu\"\n", "\nos_version = \"26.04\"\n", "\nprofiles = [\"base\", \"ubuntu\"]\n"},
		},
		{
			name:     "a Windows host",
			change:   func(s *config.Scaffold) { s.OS, s.OSVersion = config.Windows, "26100" },
			file:     "machines/box.toml",
			want:     []string{"\nos = \"windows\"\n", "\nprofiles = [\"windows\"]\n", "\n[wsl_host.distros.dev]\n", "\nmachine = \"box-wsl\"\n", "\nuser = \"phil\"\n"},
			unwanted: []string{"os_version", "config_url"},
		},
		{
			name: "a Windows host that clones over SSH",
			change: func(s *config.Scaffold) {
				s.OS, s.ConfigURL = config.Windows, "https://example.com/config.git"
			},
			file: "machines/box.toml",
			want: []string{"\nconfig_url = \"https://example.com/config.git\"\n"},
		},
		{
			name:   "a Windows host's distro",
			change: func(s *config.Scaffold) { s.OS = config.Windows },
			file:   "machines/box-wsl.toml",
			want:   []string{"\nos = \"ubuntu\"\n", "\nos_version = \"26.04\"\n", "\nprofiles = [\"base\", \"ubuntu\", \"wsl\"]\n"},
		},
		{
			name: "the workflow",
			file: ".github/workflows/validate.yaml",
			want: []string{
				"\non:\n  push:\n  pull_request:\n",
				"\n      - uses: actions/checkout@v7\n",
				"\n        run: curl -fsSL https://github.com/someone/maisys-fork/releases/latest/download/install.sh | bash\n",
				"\n        run: ~/.local/bin/maisys config validate --all\n",
			},
		},
		{
			name: "the README",
			file: "README.md",
			want: []string{
				"https://github.com/someone/maisys-fork/releases/latest/download/install.sh | bash -s -- CONFIG-URL\n",
				"`maisys sync CONFIG-URL`",
				"`maisys config validate --all`",
				"(https://github.com/someone/maisys-fork/blob/main/docs/modules.md)",
				"| `shell/bash/`, `shell/zsh/`, `shell/fish/` |",
			},
		},
		{
			name: ".gitattributes",
			file: ".gitattributes",
			want: []string{"* text=auto eol=lf\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScaffold(config.Fedora)
			if tt.change != nil {
				tt.change(s)
			}
			data, ok := scaffoldFiles(t, s)[tt.file]
			if !ok {
				t.Fatalf("there's no %s", tt.file)
			}
			for _, want := range tt.want {
				if !strings.Contains(data, want) {
					t.Errorf("%s doesn't contain %q:\n%s", tt.file, want, data)
				}
			}
			for _, unwanted := range tt.unwanted {
				if strings.Contains(data, unwanted) {
					t.Errorf("%s contains %q:\n%s", tt.file, unwanted, data)
				}
			}
		})
	}
}

func TestScaffoldErrors(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *config.Scaffold)
		want   string
	}{
		{"an invalid machine name", func(s *config.Scaffold) { s.Machine = "Box" }, `invalid machine name "Box"`},
		{"an unsupported OS", func(s *config.Scaffold) { s.OS = "debian" }, `os must be fedora, ubuntu or windows, not "debian"`},
		{"no OS", func(s *config.Scaffold) { s.OS = "" }, `os must be fedora, ubuntu or windows, not ""`},
		{"a Windows host without a distro user", func(s *config.Scaffold) { s.OS, s.DistroUser = config.Windows, "" }, "the Linux user for the WSL distro is missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScaffold(config.Fedora)
			tt.change(s)
			if _, err := s.Files(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Files() error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}
