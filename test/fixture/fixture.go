// Package fixture builds the config repo the end-to-end tests sync test
// machines from. It starts with what maisys config init writes, then adds
// small test machines on top:
//
//   - test-<distro> for each Linux distro in Distros, with os and
//     os_version set but not arch, so the same machines run on amd64 and
//     arm64. They use bash, zsh (the login shell, with oh-my-zsh and a
//     custom plugin) and fish, and a few modules that cover the kinds of
//     resource without taking long: packages, files, sudo, golang with a go
//     install program, kubectl, yq and python_tools.
//   - test-windows, a Windows host, with two WSL distros whose machines
//     (test-windows-wsl and test-windows-wsl-fedora) use packages, the
//     shells, files, kubectl and wsl with the Windows Git Credential
//     Manager.
//
// Every version is pinned, so runs are repeatable. The exceptions are uv,
// which python_tools always keeps at its latest release, and the tools uv
// installs, which it keeps at theirs.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/modules"
)

// Distro is a Linux distro the end-to-end tests run on.
type Distro struct {
	OS      config.OS
	Version string
}

// Name returns the distro's name, such as "fedora-44".
func (d Distro) Name() string {
	return string(d.OS) + "-" + d.Version
}

// Machine returns the name of the distro's test machine, such as
// "test-fedora-44".
func (d Distro) Machine() string {
	return "test-" + d.Name()
}

// Distros are the Linux distros the end-to-end tests run on: the rolling
// support policy's releases (see internal/facts). Change them together with
// the e2e matrix in .github/workflows/ci.yml.
var Distros = []Distro{
	{config.Fedora, "43"},
	{config.Fedora, "44"},
	{config.Fedora, "45"},
	{config.Ubuntu, "24.04"},
	{config.Ubuntu, "26.04"},
}

// What the test machines install, at pinned versions.
const (
	GoVersion      = "1.27.3"
	KubectlVersion = "1.37.1"
	YqVersion      = "4.54.1"
	// GoProgram is the program golang installs with go install, at
	// GoProgramVersion. Its module has no dependencies, so it builds in a
	// moment.
	GoProgram        = "github.com/a8m/envsubst/cmd/envsubst"
	GoProgramVersion = "v1.4.3"
	// PythonTool is the tool python_tools installs with uv, which keeps it
	// at its latest version.
	PythonTool = "pyflakes"
	// OhMyZshRef is the oh-my-zsh commit the machines check out.
	OhMyZshRef = "4d4cfc287e9d887b81242c0e431b5f49f9cec5c1"
	// ZshPlugin is oh-my-zsh's custom plugin, checked out at the tag
	// ZshPluginRef, which is the commit ZshPluginCommit.
	ZshPlugin       = "zsh-autosuggestions"
	ZshPluginRepo   = "https://github.com/zsh-users/zsh-autosuggestions"
	ZshPluginRef    = "v0.7.1"
	ZshPluginCommit = "e52ee8ca55bcc56a17c828767a3f98f22a68d4eb"
	// Owner is the value of files.vars.owner, which the template shows.
	Owner = "maisys e2e"
)

// Packages are the packages the test machines install, besides the
// shells. They have the same names on Fedora and Ubuntu.
var Packages = []string{"jq", "tmux"}

// The Windows host and the user its distros create.
const (
	WindowsMachine = "test-windows"
	WSLUser        = "tester"
)

// fedoraWSLMachine is the machine of the Windows host's second distro.
const fedoraWSLMachine = WindowsMachine + "-wsl-fedora"

// File is a file in the fixture.
type File struct {
	Data string
	// Mode is the file's permissions. Only the executable bit is kept, as
	// git keeps it.
	Mode fs.FileMode
}

// Commit messages: the first commit is what config init writes, the second
// what the fixture adds.
const (
	scaffoldMessage = "Start a maisys config"
	machinesMessage = "Add the test machines"
)

// Files returns the fixture's files, keyed by their slash-separated paths:
// those config init writes, with the test machines added.
func Files() (map[string]File, error) {
	files, err := scaffold()
	if err != nil {
		return nil, err
	}
	if err := addMachines(files); err != nil {
		return nil, err
	}
	return files, nil
}

// Build writes the fixture into dir, which must be empty or not exist yet,
// and commits it with git in two commits: what config init writes, then
// the test machines. git runs with a config of its own, so the user's
// settings don't change the repo, and fixed dates, so the same fixture has
// the same commits.
func Build(ctx context.Context, dir string) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s isn't empty", dir)
	}
	first, err := scaffold()
	if err != nil {
		return err
	}
	all, err := Files()
	if err != nil {
		return err
	}

	git, err := gitrepo.Find()
	if err != nil {
		return err
	}
	global, err := os.CreateTemp("", "maisys-fixture-*.gitconfig")
	if err != nil {
		return err
	}
	_ = global.Close()
	defer func() { _ = os.Remove(global.Name()) }()
	const date = "2026-01-01T00:00:00Z"
	opts := gitrepo.Options{Git: git, Env: []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + global.Name(),
		"GIT_AUTHOR_NAME=maisys e2e",
		"GIT_AUTHOR_EMAIL=e2e@example.com",
		"GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=maisys e2e",
		"GIT_COMMITTER_EMAIL=e2e@example.com",
		"GIT_COMMITTER_DATE=" + date,
	}}

	if err := write(dir, first); err != nil {
		return err
	}
	repo, err := gitrepo.Init(ctx, dir, opts)
	if err != nil {
		return err
	}
	if err := repo.CommitAll(ctx, scaffoldMessage); err != nil {
		return err
	}
	if err := write(dir, all); err != nil {
		return err
	}
	return repo.CommitAll(ctx, machinesMessage)
}

// write writes files into dir.
func write(dir string, files map[string]File) error {
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		f := files[name]
		if err := os.WriteFile(path, []byte(f.Data), f.Mode); err != nil {
			return err
		}
		// WriteFile only sets the mode of new files.
		if err := os.Chmod(path, f.Mode); err != nil {
			return err
		}
	}
	return nil
}

// scaffold returns what maisys config init writes on the Windows host,
// which includes its WSL distro's machine, and on each test machine, all in
// one repo. Their common files are the same.
func scaffold() (map[string]File, error) {
	scaffolds := []*config.Scaffold{{Machine: WindowsMachine, OS: config.Windows, DistroUser: WSLUser}}
	for _, d := range Distros {
		scaffolds = append(scaffolds, &config.Scaffold{Machine: d.Machine(), OS: d.OS, OSVersion: d.Version})
	}
	files := map[string]File{}
	for _, s := range scaffolds {
		s.Modules = modules.All()
		s.Docs = modules.Docs()
		s.UpdateRepo = buildinfo.UpdateRepo()
		written, err := s.Files()
		if err != nil {
			return nil, fmt.Errorf("config init for %s: %w", s.Machine, err)
		}
		for name, data := range written {
			if f, ok := files[name]; ok && f.Data != data {
				return nil, fmt.Errorf("config init writes %s differently for %s", name, s.Machine)
			}
			files[name] = File{Data: data, Mode: 0o644}
		}
	}
	return files, nil
}

// addMachines adds the test machines to what config init wrote. It fails
// if what it changes isn't there, as when config init writes something
// else.
func addMachines(files map[string]File) error {
	var errs []error
	add := func(name, data string) {
		if _, ok := files[name]; ok {
			errs = append(errs, fmt.Errorf("config init already writes %s", name))
		}
		files[name] = File{Data: data, Mode: 0o644}
	}
	appendTo := func(name, data string) {
		f, ok := files[name]
		if !ok {
			errs = append(errs, fmt.Errorf("config init doesn't write %s", name))
		}
		f.Data += data
		files[name] = f
	}
	replace := func(name, old, data string) {
		f := files[name]
		if strings.Count(f.Data, old) != 1 {
			errs = append(errs, fmt.Errorf("%s from config init doesn't have %q once", name, old))
		}
		f.Data = strings.Replace(f.Data, old, data, 1)
		files[name] = f
	}

	appendTo("profiles/base.toml", baseProfile)
	add("profiles/dev.toml", schemaLine()+devProfile)
	for _, o := range config.LinuxOS() {
		replace("profiles/"+string(o)+".toml", "install = []\n", "install = "+tomlList(Packages)+"\n")
	}
	for _, d := range Distros {
		machine := config.MachineFile(d.Machine())
		profiles := fmt.Sprintf("profiles = [\"base\", %q]\n", d.OS)
		replace(machine, profiles, fmt.Sprintf("profiles = [\"base\", %q, \"dev\"]\n", d.OS))
		appendTo(machine, testMachine)
	}
	appendTo(config.MachineFile(WindowsMachine), windowsMachine)
	add(config.MachineFile(fedoraWSLMachine), schemaLine()+fedoraWSL)

	for name, data := range snippets {
		add(name, data)
	}
	for name, f := range repoFiles {
		if _, ok := files[name]; ok {
			errs = append(errs, fmt.Errorf("config init already writes %s", name))
		}
		files[name] = f
	}
	return errors.Join(errs...)
}

// schemaLine is the first line of the profiles and machine files config
// init writes.
func schemaLine() string {
	return "#:schema https://github.com/" + buildinfo.UpdateRepo() + "/releases/latest/download/config.schema.json\n\n"
}

// tomlList writes items as a TOML list of strings.
func tomlList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// baseProfile is added to the end of profiles/base.toml: what every Linux
// test machine has, including the WSL distros'.
const baseProfile = `
# Added by the end-to-end tests' fixture: settings for every Linux test
# machine, including the WSL distros'.

[shell]
enabled = true
login = "zsh"

[shell.bash]
enabled = true
snippets = ["shell/bash/e2e.sh"]

[shell.bash.variables]
MAISYS_E2E_SHELL = "bash"

[shell.zsh]
enabled = true
snippets = ["shell/zsh/e2e.zsh"]

[shell.zsh.variables]
MAISYS_E2E_SHELL = "zsh"

[shell.zsh.oh_my_zsh]
enabled = true
ref = "` + OhMyZshRef + `"
plugins = ["git", "` + ZshPlugin + `"]

[shell.zsh.oh_my_zsh.custom_plugins.` + ZshPlugin + `]
repo = "` + ZshPluginRepo + `"
ref = "` + ZshPluginRef + `"

[shell.fish]
enabled = true
snippets = ["shell/fish/e2e.fish"]

[shell.fish.variables]
MAISYS_E2E_SHELL = "fish"

[files]
enabled = true

[files.vars]
owner = "` + Owner + `"

[files.items.plain]
source = "files/e2e/plain.txt"
target = "~/e2e/plain.txt"
mode = "0600"

[files.items.about]
source = "files/e2e/about.tmpl"
target = "~/e2e/about.txt"
template = true

[files.items.dir]
source = "files/e2e/dir/"
target = "~/.config/maisys-e2e/"

[kubectl]
enabled = true
version = "` + KubectlVersion + `"
`

// devProfile is profiles/dev.toml, which the test-<distro> machines use.
const devProfile = `# The dev profile: toolchains and command-line tools, for the test machines.

[golang]
enabled = true
version = "` + GoVersion + `"
tools = ["` + GoProgram + "@" + GoProgramVersion + `"]

[yq]
enabled = true
version = "` + YqVersion + `"

[python_tools]
enabled = true
tools = ["` + PythonTool + `"]
`

// testMachine is added to the end of each test-<distro> machine file.
const testMachine = `
[sudo]
nopasswd = true
`

// windowsMachine is added to the end of the Windows host's machine file,
// after its first distro.
const windowsMachine = `
[wsl_host.distros.fedora]
image = "FedoraLinux-44"
machine = "` + fedoraWSLMachine + `"
user = "` + WSLUser + `"

[winget]
enabled = true
install = ["jqlang.jq"]

[files]
enabled = true

[files.items.plain]
source = "files/e2e/plain.txt"
target = "~/maisys-e2e/plain.txt"
`

// fedoraWSL is the machine file of the Windows host's second distro.
const fedoraWSL = `# The machine file for the WSL distro fedora, which the Windows host
# ` + WindowsMachine + ` creates.
os = "fedora"
os_version = "44"
profiles = ["base", "fedora", "wsl"]
`

// snippets are the shells' snippets. Each sets a variable the tests check,
// so they know the shell sourced it.
var snippets = map[string]string{
	"shell/bash/e2e.sh":   "export MAISYS_E2E_SNIPPET=bash\n",
	"shell/zsh/e2e.zsh":   "export MAISYS_E2E_SNIPPET=zsh\n",
	"shell/fish/e2e.fish": "set -gx MAISYS_E2E_SNIPPET fish\n",
}

// repoFiles are the sources of the files module's items: a plain file, a
// template and a directory, which has a file with the executable bit.
var repoFiles = map[string]File{
	"files/e2e/plain.txt": {Data: "A plain file, copied as it is.\n", Mode: 0o644},
	"files/e2e/about.tmpl": {Data: `owner: {{ .Vars.owner }}
machine: {{ .Machine.Name }} ({{ .Machine.OS }} {{ .Machine.OSVersion }})
system: {{ .Facts.OS }} {{ .Facts.OSVersion }} {{ .Facts.Arch }}
`, Mode: 0o644},
	"files/e2e/dir/settings.conf": {Data: "# A file in a directory, which is copied file by file.\ncolor = true\n", Mode: 0o644},
	"files/e2e/dir/bin/hello.sh":  {Data: "#!/bin/sh\necho 'hello from maisys e2e'\n", Mode: 0o755},
}
