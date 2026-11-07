package modules_test

import (
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

// ruleRepo holds the files the module rule tests refer to.
var ruleRepo = fstest.MapFS{
	"files/gitconfig.tmpl":  {Data: []byte("[user]\n\temail = {{ .Vars.email }}\n")},
	"files/nvim/init.lua":   {Data: []byte("vim.opt.number = true\n")},
	"shell/zsh/aliases.zsh": {Data: []byte("alias ll='ls -lh'\n")},
	"shell/bash/aliases.sh": {Data: []byte("alias ll='ls -lh'\n")},

	"machines/fedora-dev.toml": {Data: []byte("os = \"fedora\"\nos_version = \"44\"\n")},
	"machines/ubuntu-dev.toml": {Data: []byte("os = \"ubuntu\"\n")},
	"machines/arm.toml":        {Data: []byte("os = \"fedora\"\narch = \"arm64\"\n")},
	"machines/windows.toml":    {Data: []byte("os = \"windows\"\n")},
	"machines/no-os.toml":      {Data: []byte("")},
	"machines/broken.toml":     {Data: []byte("os = \"fedora\"\ncolour = \"blue\"\n")},
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
items.e = { source = "files/nvim/", target = "~/" }
items.f = { source = "files/gitconfig.tmpl", target = "~root/gitconfig" }
`,
			want: []string{
				`machines/box.toml:5:55: files.items.a.target must start with ~/, because files only go in your home folder, not "/etc/gitconfig"`,
				`machines/box.toml:6:55: files.items.b.target must be a path inside your home folder, not "~/../gitconfig"`,
				`machines/box.toml:7:55: files.items.c.target must be a path inside your home folder, not "~/a/../../gitconfig"`,
				`machines/box.toml:8:55: files.items.d.target must be a path inside your home folder, not "~/..\\gitconfig"`,
				`machines/box.toml:9:46: files.items.e.target must be a path inside your home folder, not "~/"`,
				`machines/box.toml:10:55: files.items.f.target must start with ~/, because files only go in your home folder, not "~root/gitconfig"`,
			},
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
			name: "files: a file inside a directory source's target",
			machine: `
os = "fedora"

[files]
enabled = true
items.nvim = { source = "files/nvim/", target = "~/.config/nvim" }
items.init = { source = "files/nvim/init.lua", target = "~/.config/nvim/init.lua" }
`,
			want: []string{
				`machines/box.toml:6:57: files.items.init.target (~/.config/nvim/init.lua) is inside files.items.nvim.target (~/.config/nvim/) on machine "box": only one of them can write it`,
			},
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
			name: "shell and files both write fish's environment file",
			machine: `
os = "fedora"

[shell]
enabled = true
fish.enabled = true

[files]
enabled = true
items.fish = { source = "files/nvim/", target = "~/.config/fish/" }
`,
			want: []string{
				`machines/box.toml:5:16: shell.fish.enabled (~/.config/fish/conf.d/maisys.fish) is inside files.items.fish.target (~/.config/fish/) on machine "box": only one of them can write it`,
			},
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
			want: []string{"machines/box.toml:3:2: wsl_host.config_url is required, because this machine clones the config repo over SSH (ssh://git@example.com/me/config.git), and new distros clone it over HTTPS"},
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
