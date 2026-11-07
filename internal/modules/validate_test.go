package modules_test

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
)

// TestExampleValidates checks that the example config repo from issue #1
// passes validation, including its marker.
func TestExampleValidates(t *testing.T) {
	repo := &config.Repo{
		FS:      os.DirFS("testdata/example"),
		Modules: modules.All(),
		URL:     "git@github.com:plombardi89/maisys-config.git",
		Strict:  true,
	}
	if _, err := repo.ValidateAll(); err != nil {
		t.Errorf("ValidateAll:\n%v", err)
	}

	marker, err := repo.ReadMarker()
	if err != nil {
		t.Fatal(err)
	}
	warnings, err := marker.Check("0.6.0")
	if err != nil || len(warnings) > 0 {
		t.Errorf("Check = %q, %v, want no warnings or errors", warnings, err)
	}
}

// TestValidateAllIgnoresTheCloneURL checks that the URL this machine clones
// the config repo from only applies when this machine is validated: other
// Windows hosts may clone the repo over HTTPS, and need no config_url.
func TestValidateAllIgnoresTheCloneURL(t *testing.T) {
	repo := &config.Repo{
		FS: fstest.MapFS{
			"machines/host.toml": {Data: []byte(`os = "windows"

[wsl_host]
enabled = true
distros.dev = { image = "FedoraLinux-44", machine = "fedora-dev", user = "phil" }
`)},
			"machines/fedora-dev.toml": {Data: []byte("os = \"fedora\"\nos_version = \"44\"\n")},
		},
		Modules: modules.All(),
		URL:     "git@github.com:me/config.git",
	}
	if _, err := repo.ValidateAll(); err != nil {
		t.Errorf("ValidateAll:\n%v\nwant no errors", err)
	}
	want := "machines/host.toml:3:2: wsl_host.config_url is required, because this machine clones the config repo over SSH (git@github.com:me/config.git), and new distros clone it over HTTPS"
	if _, err := repo.Validate("host"); err == nil || err.Error() != want {
		t.Errorf("Validate(host) = %v, want %s", err, want)
	}
}

// ruleRepo holds the files the module rule tests refer to.
var ruleRepo = fstest.MapFS{
	"files/gitconfig.tmpl":              {Data: []byte("[user]\n\temail = {{ .Vars.email }}\n")},
	"files/nvim/init.lua":               {Data: []byte("vim.opt.number = true\n")},
	"files/fish/config.fish":            {Data: []byte("set -g fish_greeting\n")},
	"files/home/.vimrc":                 {Data: []byte("set number\n")},
	"files/fish-env/conf.d/maisys.fish": {Data: []byte("set -gx EDITOR nvim\n")},
	"shell/zsh/aliases.zsh":             {Data: []byte("alias ll='ls -lh'\n")},
	"shell/bash/aliases.sh":             {Data: []byte("alias ll='ls -lh'\n")},

	"machines/fedora-dev.toml": {Data: []byte("os = \"fedora\"\nos_version = \"44\"\n")},
	"machines/ubuntu-dev.toml": {Data: []byte("os = \"ubuntu\"\n")},
	"machines/arm.toml":        {Data: []byte("os = \"fedora\"\narch = \"arm64\"\n")},
	"machines/windows.toml":    {Data: []byte("os = \"windows\"\n")},
	"machines/no-os.toml":      {Data: []byte("")},
	"machines/broken.toml":     {Data: []byte("os = \"fedora\"\nprofiles = \"base\"\n")},
}

func TestModuleRules(t *testing.T) {
	tests := []struct {
		name    string
		machine string // machines/box.toml
		url     string // the config repo's URL
		want    []string
	}{
		// golang
		{
			name: "golang: a full version",
			machine: `
os = "fedora"

[golang]
enabled = true
version = "1.26.4"
`,
		},
		{
			name: "golang: a partial version",
			machine: `
os = "fedora"

[golang]
enabled = true
version = "1.27"
`,
			want: []string{`machines/box.toml:5:11: golang.version must be a full Go version, such as "1.26.4", not "1.27": partial versions aren't supported`},
		},
		{
			name: "golang: a malformed version is only reported once",
			machine: `
os = "fedora"

[golang]
enabled = true
version = "1.x"
`,
			want: []string{`machines/box.toml:5:11: golang.version must be "latest" or an exact version such as "1.2.3", not "1.x"`},
		},
		{
			name: "golang: an empty tool",
			machine: `
os = "fedora"

[golang]
enabled = true
tools = ["mvdan.cc/gofumpt@latest", ""]
`,
			want: []string{"machines/box.toml:5:37: golang.tools has an empty entry"},
		},
		{
			name: "golang: tools with and without versions",
			machine: `
os = "fedora"

[golang]
enabled = true
tools = ["mvdan.cc/gofumpt@latest", "golang.org/x/tools/cmd/goimports", "github.com/go-task/task/v3/cmd/task@v3.41.0"]
`,
		},
		{
			name: "golang: invalid tools",
			machine: `
os = "fedora"

[golang]
enabled = true
tools = ["mvdan.cc/gofumpt@master", "mvdan.cc/gofumpt latest"]
`,
			want: []string{
				`machines/box.toml:5:10: golang.tools has an invalid entry, "mvdan.cc/gofumpt@master": the version must be "latest" or an exact version such as "v1.2.3", not "master"`,
				`machines/box.toml:5:37: golang.tools has an invalid entry, "mvdan.cc/gofumpt latest": "mvdan.cc/gofumpt latest" isn't a Go package path, such as "mvdan.cc/gofumpt"`,
			},
		},

		// Lists of names
		{
			name: "packages: an empty package",
			machine: `
os = "fedora"

[packages]
enabled = true
install = ["jq", " "]
`,
			want: []string{"machines/box.toml:5:18: packages.install has an empty entry"},
		},
		{
			name: "winget: an empty package",
			machine: `
os = "windows"

[winget]
enabled = true
install = [""]
`,
			want: []string{"machines/box.toml:5:12: winget.install has an empty entry"},
		},
		{
			name: "winget: not a package ID",
			machine: `
os = "windows"

[winget]
enabled = true
install = ["Microsoft.WindowsTerminal", "Windows Terminal"]
`,
			want: []string{`machines/box.toml:5:41: winget.install lists "Windows Terminal", which isn't a winget package ID, such as "Microsoft.WindowsTerminal"`},
		},
		{
			name: "hashicorp: an empty tool",
			machine: `
os = "fedora"

[hashicorp]
enabled = true
tools = [""]
`,
			want: []string{"machines/box.toml:5:10: hashicorp.tools has an empty entry"},
		},
		{
			name: "python_tools: an empty tool",
			machine: `
os = "fedora"

[python_tools]
enabled = true
tools = [""]
`,
			want: []string{"machines/box.toml:5:10: python_tools.tools has an empty entry"},
		},

		// files
		{
			name: "files: valid items",
			machine: `
os = "fedora"

[files]
enabled = true

[files.items.gitconfig]
source = "files/gitconfig.tmpl"
target = "~/.gitconfig"
template = true
mode = "0600"

[files.items.nvim]
source = "files/nvim/"
target = "~/.config/nvim/"
mode = "755"

[files.items.windows]
source = "files/nvim/init.lua"
target = "~/AppData/Local/nvim/init.lua"
eol = "crlf"
`,
		},
		{
			name: "files: an item without a source or target",
			machine: `
os = "fedora"

[files]
enabled = true

[files.items.x]
template = true
`,
			want: []string{
				"machines/box.toml:6:14: files.items.x has no source: set it to a file or directory in the config repo",
				`machines/box.toml:6:14: files.items.x has no target: set it to a path in your home folder, such as "~/.gitconfig"`,
			},
		},
		{
			name: "files: sources that aren't in the config repo",
			machine: `
os = "fedora"

[files]
enabled = true
items.missing = { source = "files/nope", target = "~/a" }
items.outside = { source = "../secret", target = "~/b" }
items.absolute = { source = "/etc/passwd", target = "~/c" }
`,
			want: []string{
				`machines/box.toml:7:29: files.items.absolute.source must be a path inside the config repo, such as "files/gitconfig", not "/etc/passwd"`,
				`machines/box.toml:5:28: files.items.missing.source is "files/nope", which doesn't exist in the config repo`,
				`machines/box.toml:6:28: files.items.outside.source must be a path inside the config repo, such as "files/gitconfig", not "../secret"`,
			},
		},
		{
			name: "files: files and directories",
			machine: `
os = "fedora"

[files]
enabled = true
items.a = { source = "files/gitconfig.tmpl/", target = "~/a" }
items.b = { source = "files/nvim", target = "~/b" }
items.c = { source = "files/nvim/", target = "~/c/", template = true }
`,
			want: []string{
				`machines/box.toml:5:22: files.items.a.source is "files/gitconfig.tmpl/", which is a file: leave out the trailing / to copy a file`,
				`machines/box.toml:6:22: files.items.b.source is "files/nvim", which is a directory: end it with / to copy the directory file by file`,
				`machines/box.toml:7:65: files.items.c.template is true, but the source "files/nvim/" is a directory: only a file can be a template`,
			},
		},
		{
			name: "files: targets outside the home folder",
			machine: `
os = "fedora"

[files]
enabled = true
items.a = { source = "files/gitconfig.tmpl", target = "/etc/gitconfig" }
items.b = { source = "files/gitconfig.tmpl", target = "~/../gitconfig" }
items.c = { source = "files/gitconfig.tmpl", target = "~/a/../../gitconfig" }
items.d = { source = "files/gitconfig.tmpl", target = '~/..\gitconfig' }
items.e = { source = "files/gitconfig.tmpl", target = "~/" }
items.f = { source = "files/gitconfig.tmpl", target = "~root/gitconfig" }
items.g = { source = "files/nvim/", target = "~/.." }
`,
			want: []string{
				`machines/box.toml:5:55: files.items.a.target must start with ~/, because files only go in your home folder, not "/etc/gitconfig"`,
				`machines/box.toml:6:55: files.items.b.target must be a path inside your home folder, not "~/../gitconfig"`,
				`machines/box.toml:7:55: files.items.c.target must be a path inside your home folder, not "~/a/../../gitconfig"`,
				`machines/box.toml:8:55: files.items.d.target must be a path inside your home folder, not "~/..\\gitconfig"`,
				`machines/box.toml:9:55: files.items.e.target must be a path inside your home folder, such as "~/.gitconfig", not "~/": only a directory can be copied into ~/ itself`,
				`machines/box.toml:10:55: files.items.f.target must start with ~/, because files only go in your home folder, not "~root/gitconfig"`,
				`machines/box.toml:11:46: files.items.g.target must be a path inside your home folder, not "~/.."`,
			},
		},
		{
			name: "files: a directory copied into the home folder itself",
			machine: `
os = "fedora"

[files]
enabled = true
items.home = { source = "files/home/", target = "~/" }
items.dot = { source = "files/nvim/", target = "~/." }
items.gitconfig = { source = "files/gitconfig.tmpl", target = "~/.gitconfig" }
`,
		},
		{
			name: "files: two directories with the same file",
			machine: `
os = "fedora"

[files]
enabled = true
items.home = { source = "files/home/", target = "~/" }
items.vim = { source = "files/home/", target = "~/./" }
`,
			want: []string{`machines/box.toml:6:48: files.items.home.target and files.items.vim.target both write ~/./.vimrc on machine "box"`},
		},
		{
			name: "files: mode and eol",
			machine: `
os = "fedora"

[files]
enabled = true
items.a = { source = "files/gitconfig.tmpl", target = "~/a", mode = "rw" }
items.b = { source = "files/gitconfig.tmpl", target = "~/b", mode = "06000" }
items.c = { source = "files/gitconfig.tmpl", target = "~/c", mode = "0800" }
items.d = { source = "files/gitconfig.tmpl", target = "~/d", eol = "lf" }
`,
			want: []string{
				`machines/box.toml:5:69: files.items.a.mode must be 3 or 4 octal digits, such as "0600", not "rw"`,
				`machines/box.toml:6:69: files.items.b.mode must be 3 or 4 octal digits, such as "0600", not "06000"`,
				`machines/box.toml:7:69: files.items.c.mode must be 3 or 4 octal digits, such as "0600", not "0800"`,
				`machines/box.toml:8:68: files.items.d.eol must be "crlf", or left out to keep line endings as they are, not "lf"`,
			},
		},
		{
			name: "files: items that are off aren't checked",
			machine: `
os = "fedora"

[files]
enabled = true
items.a = { enabled = false, source = "files/nope", target = "/etc/x", mode = "rw" }
items.b = { enabled = false, source = "files/gitconfig.tmpl", target = "~/.zshrc" }

[shell]
enabled = true
zsh.enabled = true
`,
		},
		{
			name: "files: two items with the same target",
			machine: `
os = "fedora"

[files]
enabled = true
items.a = { source = "files/gitconfig.tmpl", target = "~/.gitconfig" }
items.b = { source = "files/gitconfig.tmpl", target = "~/./.gitconfig" }
`,
			want: []string{`machines/box.toml:6:55: files.items.a.target and files.items.b.target both write ~/./.gitconfig on machine "box"`},
		},
		{
			name: "files: a file that a directory item copies too",
			machine: `
os = "fedora"

[files]
enabled = true
items.nvim = { source = "files/nvim/", target = "~/.config/nvim" }
items.init = { source = "files/nvim/init.lua", target = "~/.config/nvim/init.lua" }
`,
			want: []string{
				`machines/box.toml:5:49: files.items.init.target and files.items.nvim.target both write ~/.config/nvim/init.lua on machine "box"`,
			},
		},
		{
			name: "files: a file in a directory item's target that its source doesn't have",
			machine: `
os = "fedora"

[files]
enabled = true
items.nvim = { source = "files/nvim/", target = "~/.config/nvim/" }
items.lazy = { source = "files/gitconfig.tmpl", target = "~/.config/nvim/lazy-lock.json" }
`,
		},

		// shell
		{
			name: "shell: a valid config",
			machine: `
os = "fedora"

[shell]
enabled = true
login = "zsh"

[shell.zsh]
enabled = true
snippets = ["shell/zsh/aliases.zsh"]

[shell.zsh.oh_my_zsh]
enabled = true
custom_themes.t = { repo = "https://example.com/t", ref = "main", file = "t.zsh-theme" }
custom_plugins.p = { repo = "https://example.com/p", ref = "v1" }

[shell.bash]
enabled = true
manage_rc = false
pre_snippets = ["shell/bash/aliases.sh"]
`,
		},
		{
			name: "shell: an unknown login shell",
			machine: `
os = "fedora"

[shell]
enabled = true
login = "sh"
`,
			want: []string{`machines/box.toml:5:9: shell.login must be bash, zsh or fish, not "sh"`},
		},
		{
			name: "shell: a login shell that isn't enabled",
			machine: `
os = "fedora"

[shell]
enabled = true
login = "fish"
bash.enabled = true
`,
			want: []string{`machines/box.toml:5:9: shell.login is fish, but machine "box" doesn't enable shell.fish`},
		},
		{
			name: "shell: oh-my-zsh without zsh",
			machine: `
os = "fedora"

[shell]
enabled = true
zsh.oh_my_zsh.enabled = true
`,
			want: []string{`machines/box.toml:5:25: shell.zsh.oh_my_zsh.enabled is true, but machine "box" doesn't enable shell.zsh, which oh-my-zsh needs`},
		},
		{
			name: "shell: incomplete custom themes and plugins",
			machine: `
os = "fedora"

[shell]
enabled = true
zsh.enabled = true
zsh.oh_my_zsh.enabled = true

[shell.zsh.oh_my_zsh.custom_themes.t]
repo = "https://example.com/t"

[shell.zsh.oh_my_zsh.custom_plugins.p]
ref = "main"
`,
			want: []string{
				"machines/box.toml:8:36: shell.zsh.oh_my_zsh.custom_themes.t.ref is missing: set it to the branch, tag or commit to check out",
				`machines/box.toml:8:36: shell.zsh.oh_my_zsh.custom_themes.t.file is missing: set it to the theme file in the repo, such as "aphrodite.zsh-theme"`,
				"machines/box.toml:11:37: shell.zsh.oh_my_zsh.custom_plugins.p.repo is missing: set it to the URL of the git repo the plugin is in",
			},
		},
		{
			name: "shell: custom themes aren't checked without oh-my-zsh",
			machine: `
os = "fedora"

[shell]
enabled = true
zsh.enabled = true
zsh.oh_my_zsh.custom_themes.t = {}
`,
		},
		{
			name: "shell: snippets that aren't files in the config repo",
			machine: `
os = "fedora"

[shell]
enabled = true

[shell.zsh]
enabled = true
pre_snippets = ["shell/zsh/nope.zsh", "shell/zsh"]
snippets = ["", "../aliases.zsh"]
`,
			want: []string{
				`machines/box.toml:8:17: shell.zsh.pre_snippets lists "shell/zsh/nope.zsh", which doesn't exist in the config repo`,
				`machines/box.toml:8:39: shell.zsh.pre_snippets lists "shell/zsh", which is a directory, not a file`,
				`machines/box.toml:9:13: shell.zsh.snippets lists "", which isn't a path inside the config repo`,
				`machines/box.toml:9:17: shell.zsh.snippets lists "../aliases.zsh", which isn't a path inside the config repo`,
			},
		},
		{
			name: "shell: snippets of a shell that's off",
			machine: `
os = "fedora"

[shell]
enabled = true
fish.snippets = ["shell/fish/nope.fish"]
`,
		},
		{
			name: "shell: snippets copied to the same file",
			machine: `
os = "fedora"

[shell]
enabled = true

[shell.zsh]
enabled = true
pre_snippets = ["shell/zsh/aliases.zsh"]
snippets = ["shell/zsh/aliases.zsh", "aliases.zsh"]
`,
			want: []string{
				`machines/box.toml:9:38: shell.zsh.snippets lists "aliases.zsh", which doesn't exist in the config repo`,
				`machines/box.toml:9:38: shell.zsh.snippets lists "aliases.zsh", which would be copied to ~/.config/maisys/shell/zsh/aliases.zsh like "shell/zsh/aliases.zsh": move one of them`,
			},
		},
		{
			name: "shell: variables and oh-my-zsh names that aren't valid",
			machine: `
os = "fedora"

[shell]
enabled = true

[shell.zsh]
enabled = true
variables = { OK_1 = "a", "NOT-OK" = "b", 1X = "c" }

[shell.zsh.oh_my_zsh]
enabled = true
theme = "a theme"
plugins = ["git", "$(id)"]
custom_themes."../t" = { repo = "https://example.com/t", ref = "main", file = "../t.zsh-theme" }
custom_plugins."p/q" = { repo = "https://example.com/p", ref = "main" }
`,
			want: []string{
				`machines/box.toml:8:48: shell.zsh.variables.1X isn't a valid variable name: use letters, digits and underscores, not starting with a digit`,
				`machines/box.toml:8:38: shell.zsh.variables.NOT-OK isn't a valid variable name: use letters, digits and underscores, not starting with a digit`,
				`machines/box.toml:12:9: shell.zsh.oh_my_zsh.theme "a theme" isn't a valid theme name`,
				`machines/box.toml:13:19: shell.zsh.oh_my_zsh.plugins lists "$(id)", which isn't a valid plugin name`,
				`machines/box.toml:14:24: shell.zsh.oh_my_zsh.custom_themes."../t" has a name that isn't a valid theme name: use letters, digits, dots, dashes and underscores`,
				`machines/box.toml:14:79: shell.zsh.oh_my_zsh.custom_themes."../t".file is "../t.zsh-theme", which isn't a path inside the theme's repo`,
				`machines/box.toml:15:24: shell.zsh.oh_my_zsh.custom_plugins.p/q has a name that isn't a valid plugin name: use letters, digits, dots, dashes and underscores`,
			},
		},
		{
			name: "shell and files both write ~/.bashrc",
			machine: `
os = "fedora"

[shell]
enabled = true
bash.enabled = true

[files]
enabled = true
items.bashrc = { source = "files/gitconfig.tmpl", target = "~/.bashrc" }
`,
			want: []string{`machines/box.toml:9:60: files.items.bashrc.target and shell.bash.manage_rc both write ~/.bashrc on machine "box"`},
		},
		{
			name: "shell and files: files can write ~/.bashrc when shell doesn't manage it",
			machine: `
os = "fedora"

[shell]
enabled = true
bash.enabled = true
bash.manage_rc = false

[files]
enabled = true
items.bashrc = { source = "files/gitconfig.tmpl", target = "~/.bashrc" }
`,
		},
		{
			name: "shell and files: files can copy a directory into fish's folder",
			machine: `
os = "fedora"

[shell]
enabled = true
fish.enabled = true

[files]
enabled = true
items.fish = { source = "files/fish/", target = "~/.config/fish/" }
`,
		},
		{
			name: "shell and files both write fish's environment file",
			machine: `
os = "fedora"

[shell]
enabled = true
fish.enabled = true

[files]
enabled = true
items.fish = { source = "files/fish-env/", target = "~/.config/fish/" }
`,
			want: []string{
				`machines/box.toml:5:16: files.items.fish.target and shell.fish.enabled both write ~/.config/fish/conf.d/maisys.fish on machine "box"`,
			},
		},

		// wsl
		{
			name: "wsl: X410 with systemd",
			machine: `
os = "fedora"

[wsl]
enabled = true
wslg = false
`,
		},
		{
			name: "wsl: X410 without systemd",
			machine: `
os = "ubuntu"

[wsl]
enabled = true
wslg = false
systemd = false
`,
			want: []string{"machines/box.toml:5:8: wsl.wslg is false, which sets up the X410 bridge as a systemd service, but wsl.systemd is false: turn systemd on, or wslg back on"},
		},

		// wsl_host
		{
			name: "wsl_host: a valid config",
			url:  "git@github.com:me/config.git",
			machine: `
os = "windows"

[wsl_host]
enabled = true
default_distro = "dev"
config_url = "https://github.com/me/config.git"

[wsl_host.distros.dev]
image = "FedoraLinux-44"
machine = "fedora-dev"
user = "phil"

[wsl_host.distros.ubuntu]
image = "Ubuntu-24.04"
machine = "ubuntu-dev"
user = "_build-1"
`,
		},
		{
			name: "wsl_host: a default distro that isn't one of the distros",
			machine: `
os = "windows"

[wsl_host]
enabled = true
default_distro = "dev"
`,
			want: []string{`machines/box.toml:5:18: wsl_host.default_distro is "dev", which isn't one of wsl_host.distros`},
		},
		{
			name: "wsl_host: config_url over SSH",
			machine: `
os = "windows"

[wsl_host]
enabled = true
config_url = "git@github.com:me/config.git"
`,
			want: []string{`machines/box.toml:5:14: wsl_host.config_url must be an HTTPS URL, not "git@github.com:me/config.git"`},
		},
		{
			name: "wsl_host: no config_url when the host clones over SSH",
			url:  "git@github.com:me/config.git",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.dev = { image = "FedoraLinux-44", machine = "fedora-dev", user = "phil" }
`,
			want: []string{"machines/box.toml:3:2: wsl_host.config_url is required, because this machine clones the config repo over SSH (git@github.com:me/config.git), and new distros clone it over HTTPS"},
		},
		{
			name: "wsl_host: no config_url when the host clones over ssh://",
			url:  "ssh://git@example.com/me/config.git",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.dev = { image = "FedoraLinux-44", machine = "fedora-dev", user = "phil" }
`,
			want: []string{"machines/box.toml:3:2: wsl_host.config_url is required, because this machine clones the config repo over SSH (ssh://xxxxx@example.com/me/config.git), and new distros clone it over HTTPS"},
		},
		{
			name: "wsl_host: no config_url when the host clones over HTTPS",
			url:  "https://user@example.com:8443/me/config.git",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.dev = { image = "FedoraLinux-44", machine = "fedora-dev", user = "phil" }
`,
		},
		{
			name: "wsl_host: no config_url and no distros",
			url:  "git@github.com:me/config.git",
			machine: `
os = "windows"

[wsl_host]
enabled = true
`,
		},
		{
			name: "wsl_host: a distro with nothing set",
			machine: `
os = "windows"

[wsl_host]
enabled = true

[wsl_host.distros.dev]
`,
			want: []string{
				`machines/box.toml:6:19: wsl_host.distros.dev has no image: set it to a name from wsl --list --online, such as "FedoraLinux-44" or "Ubuntu-26.04"`,
				"machines/box.toml:6:19: wsl_host.distros.dev has no user: set it to the Linux user to create",
				"machines/box.toml:6:19: wsl_host.distros.dev has no machine: set it to the Linux machine to set the distro up with",
			},
		},
		{
			name: "wsl_host: images maisys can't set up",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.a = { image = "Debian-12", machine = "fedora-dev", user = "phil" }
distros.b = { image = "FedoraLinux", machine = "fedora-dev", user = "phil" }
`,
			want: []string{
				`machines/box.toml:5:23: wsl_host.distros.a.image must be a Fedora or Ubuntu image from wsl --list --online, such as "FedoraLinux-44" or "Ubuntu-26.04", not "Debian-12"`,
				`machines/box.toml:6:23: wsl_host.distros.b.image must be a Fedora or Ubuntu image from wsl --list --online, such as "FedoraLinux-44" or "Ubuntu-26.04", not "FedoraLinux"`,
			},
		},
		{
			name: "wsl_host: users",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.a = { image = "FedoraLinux-44", machine = "fedora-dev", user = "root" }
distros.b = { image = "FedoraLinux-44", machine = "fedora-dev", user = "Phil" }
distros.c = { image = "FedoraLinux-44", machine = "fedora-dev", user = "1phil" }
distros.d = { image = "FedoraLinux-44", machine = "fedora-dev", user = "abcdefghijklmnopqrstuvwxyz0123456" }
`,
			want: []string{
				"machines/box.toml:5:72: wsl_host.distros.a.user can't be root: set it to the Linux user to create",
				`machines/box.toml:6:72: wsl_host.distros.b.user must be a Linux user name, with lower-case letters, digits, '_' and '-', starting with a letter or '_', and up to 32 characters, not "Phil"`,
				`machines/box.toml:7:72: wsl_host.distros.c.user must be a Linux user name, with lower-case letters, digits, '_' and '-', starting with a letter or '_', and up to 32 characters, not "1phil"`,
				`machines/box.toml:8:72: wsl_host.distros.d.user must be a Linux user name, with lower-case letters, digits, '_' and '-', starting with a letter or '_', and up to 32 characters, not "abcdefghijklmnopqrstuvwxyz0123456"`,
			},
		},
		{
			name: "wsl_host: machines that don't exist or don't load",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.a = { image = "FedoraLinux-44", machine = "nope", user = "phil" }
distros.b = { image = "FedoraLinux-44", machine = "broken", user = "phil" }
`,
			want: []string{
				`machines/box.toml:5:51: wsl_host.distros.a.machine is "nope", but machines/nope.toml doesn't exist`,
				`machines/box.toml:6:51: wsl_host.distros.b.machine is "broken", but machines/broken.toml doesn't load: maisys config validate --all lists its errors`,
			},
		},
		{
			name: "wsl_host: machines that aren't Linux",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.a = { image = "FedoraLinux-44", machine = "windows", user = "phil" }
distros.b = { image = "FedoraLinux-44", machine = "no-os", user = "phil" }
`,
			want: []string{
				`machines/box.toml:5:51: wsl_host.distros.a.machine is "windows", which is a windows machine: a distro needs a fedora or ubuntu machine`,
				`machines/box.toml:6:51: wsl_host.distros.b.machine is "no-os", which doesn't set os: a distro needs a fedora or ubuntu machine`,
			},
		},
		{
			name: "wsl_host: images that don't match the machine",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.a = { image = "Ubuntu-26.04", machine = "fedora-dev", user = "phil" }
distros.b = { image = "FedoraLinux-43", machine = "fedora-dev", user = "phil" }
distros.c = { image = "FedoraLinux-44", machine = "ubuntu-dev", user = "phil" }
`,
			want: []string{
				`machines/box.toml:5:23: wsl_host.distros.a.image is "Ubuntu-26.04", but machine "fedora-dev" is fedora: use a FedoraLinux-* image`,
				`machines/box.toml:6:23: wsl_host.distros.b.image is "FedoraLinux-43", but machine "fedora-dev" sets os_version = "44": use FedoraLinux-44`,
				`machines/box.toml:7:23: wsl_host.distros.c.image is "FedoraLinux-44", but machine "ubuntu-dev" is ubuntu: use a Ubuntu-* image`,
			},
		},
		{
			name: "wsl_host: an arm64 machine",
			machine: `
os = "windows"

[wsl_host]
enabled = true
distros.a = { image = "FedoraLinux-44", machine = "arm", user = "phil" }
`,
			want: []string{`machines/box.toml:5:51: wsl_host.distros.a.machine is "arm", which sets arch = "arm64", but distros on a Windows host are amd64`},
		},
		{
			name: "wsl_host: .wslconfig",
			machine: `
os = "windows"

[wsl_host]
enabled = true
wslconfig = { memory = "16GB", processors = 4, swap = "0", networking_mode = "mirrored", auto_memory_reclaim = "dropcache", dns_tunneling = true }
`,
		},
		{
			name: "wsl_host: .wslconfig values WSL doesn't take",
			machine: `
os = "windows"

[wsl_host]
enabled = true

[wsl_host.wslconfig]
memory = "16"
processors = 0
swap = "lots"
networking_mode = "bridged"
auto_memory_reclaim = "dropCache"
`,
			want: []string{
				`machines/box.toml:7:10: wsl_host.wslconfig.memory must be a size such as "16GB" or "512MB", not "16"`,
				`machines/box.toml:9:8: wsl_host.wslconfig.swap must be a size such as "8GB", or "0" for no swap file, not "lots"`,
				`machines/box.toml:8:14: wsl_host.wslconfig.processors must be at least 1, not 0`,
				`machines/box.toml:10:19: wsl_host.wslconfig.networking_mode must be "nat" or "mirrored", not "bridged"`,
				`machines/box.toml:11:23: wsl_host.wslconfig.auto_memory_reclaim must be "disabled", "gradual" or "dropcache", not "dropCache"`,
			},
		},
		{
			name: "wsl_host and files both write ~/.wslconfig",
			machine: `
os = "windows"

[wsl_host]
enabled = true

[files]
enabled = true
items.wslconfig = { source = "files/gitconfig.tmpl", target = "~/.wslconfig" }
`,
			want: []string{`machines/box.toml:8:63: files.items.wslconfig.target and wsl_host.wslconfig both write ~/.wslconfig on machine "box"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := maps.Clone(ruleRepo)
			files["machines/box.toml"] = &fstest.MapFile{Data: []byte(strings.TrimPrefix(tt.machine, "\n"))}
			repo := &config.Repo{FS: files, Modules: modules.All(), URL: tt.url}
			_, err := repo.Validate("box")
			var got []string
			if err != nil {
				got = strings.Split(err.Error(), "\n")
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("errors:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

// TestWSLHostConfigURL checks wsl_host.config_url against the config repo's
// URL: new distros clone config_url if it's set, and otherwise the config
// repo's own URL, over HTTPS, or from a folder on the Windows machine.
func TestWSLHostConfigURL(t *testing.T) {
	const (
		required = "machines/box.toml:3:2: wsl_host.config_url is required, because this machine clones the config repo "
		notHTTPS = "machines/box.toml:5:14: wsl_host.config_url must be an HTTPS URL, not "
		overHTTP = ", and new distros clone it over HTTPS"
	)
	tests := []struct {
		name      string
		configURL string
		url       string // the config repo's URL
		noDistros bool
		want      string // the error, if any
	}{
		{name: "config_url, and an SSH URL", configURL: "https://github.com/me/config.git", url: "git@github.com:me/config.git"},
		{name: "config_url, and no URL", configURL: "https://github.com/me/config.git"},
		{name: "config_url over SSH", configURL: "git@github.com:me/config.git", want: notHTTPS + `"git@github.com:me/config.git"`},
		{name: "config_url without a host", configURL: "https://", want: notHTTPS + `"https://"`},
		{name: "config_url over HTTP", configURL: "http://me:s3cret@example.com/config.git", want: notHTTPS + `"http://xxxxx@example.com/config.git"`},
		{name: "config_url is a folder", configURL: `C:\config.git`, want: notHTTPS + `"C:\\config.git"`},

		{name: "an HTTPS URL", url: "https://me@example.com:8443/me/config.git"},
		{name: "no URL"},
		{name: "a folder, and no distros", url: "/srv/git/config.git", noDistros: true},
		{name: "an SSH URL", url: "git@github.com:me/config.git", want: required + "over SSH (git@github.com:me/config.git)" + overHTTP},
		{name: "an SSH URL with an alias", url: "github-work:org/config.git", want: required + "over SSH (github-work:org/config.git)" + overHTTP},
		{name: "an ssh:// URL", url: "ssh://git@example.com:2222/me/config.git", want: required + "over SSH (ssh://xxxxx@example.com:2222/me/config.git)" + overHTTP},
		{name: "an HTTP URL", url: "http://me:s3cret@example.com/config.git", want: required + "from http://xxxxx@example.com/config.git, which isn't an HTTPS URL or a folder on this machine" + overHTTP},
		{name: "an HTTPS URL without a host", url: "https:///config.git", want: required + "from https:///config.git, which isn't an HTTPS URL or a folder on this machine" + overHTTP},
		{name: "a folder", url: "/srv/git/config.git", want: required + "from /srv/git/config.git, which isn't an HTTPS URL or a folder on this machine" + overHTTP},
		{name: "a file URL", url: "file:///D:/a/config.git"},
		{name: "a Windows folder", url: `C:\Users\me\config.git`},
		{name: "a Windows folder with slashes", url: `D:/a/config.git`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			machine := "os = \"windows\"\n\n[wsl_host]\nenabled = true\n"
			if tt.configURL != "" {
				machine += fmt.Sprintf("config_url = %q\n", tt.configURL)
			}
			if !tt.noDistros {
				machine += `distros.dev = { image = "FedoraLinux-44", machine = "fedora-dev", user = "phil" }` + "\n"
			}
			files := maps.Clone(ruleRepo)
			files["machines/box.toml"] = &fstest.MapFile{Data: []byte(machine)}
			repo := &config.Repo{FS: files, Modules: modules.All(), URL: tt.url}
			_, err := repo.Validate("box")
			var got string
			if err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("errors:\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}
