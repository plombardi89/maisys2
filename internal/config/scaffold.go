package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ScaffoldDistro is the WSL distro the starter config of a Windows host
// creates.
const ScaffoldDistro = "dev"

// scaffoldImage is the image of ScaffoldDistro, and scaffoldImageVersion
// the os_version of its machine.
const (
	scaffoldImage        = "Ubuntu-26.04"
	scaffoldImageVersion = "26.04"
)

// Scaffold describes the starter config repo maisys config init writes.
type Scaffold struct {
	// Machine is the name of the machine file for this machine.
	Machine string
	// OS is this machine's operating system.
	OS OS
	// OSVersion is this machine's OS version, as os_version takes it. It's
	// only written for Linux machines, and only if it's a valid os_version.
	OSVersion string
	// DistroUser is the Linux user a Windows host's distro creates.
	DistroUser string
	// ConfigURL is the HTTPS URL a Windows host's distros clone the config
	// repo from, needed if the host clones it over SSH.
	ConfigURL string
	// Modules are the modules whose examples the profiles include, in the
	// order they're written.
	Modules []Module
	// Docs describe the modules' keys, for their examples.
	Docs Docs
	// UpdateRepo is the GitHub repo (owner/name) maisys is released from.
	// The schema, docs and installer links point at it.
	UpdateRepo string
	// Version is this maisys's release version, used in the example
	// requires, or "" if it isn't a release.
	Version string
}

// DistroMachine returns the name of the Linux machine file that a Windows
// host's distro is set up from.
func (s *Scaffold) DistroMachine() string {
	return s.Machine + "-wsl"
}

// Files returns the files of the starter repo, keyed by their
// slash-separated paths, with "\n" line endings:
//
//   - maisys.toml, README.md, .gitattributes, and a GitHub workflow that
//     validates every machine;
//   - profiles/base.toml, with every Linux module as a commented-out
//     example;
//   - for a Linux machine, its machine file, and a profile for its OS with
//     its package list;
//   - for a Windows host, its machine file, which creates a WSL distro, the
//     distro's Linux machine file, the ubuntu and wsl profiles the distro
//     uses, and profiles/windows.toml, with every Windows module as a
//     commented-out example;
//   - files/ and shell/, with a README.md saying what goes there, since git
//     doesn't keep empty folders.
func (s *Scaffold) Files() (map[string]string, error) {
	if !ValidName(s.Machine) {
		return nil, fmt.Errorf("invalid machine name %q: %s", s.Machine, NameRule)
	}
	files := map[string]string{
		markerFile:                        s.marker(),
		"README.md":                       s.expand(scaffoldReadme),
		".gitattributes":                  "* text=auto eol=lf\n",
		".github/workflows/validate.yaml": s.expand(scaffoldWorkflow),
		"files/README.md":                 scaffoldFilesReadme,
		"shell/README.md":                 scaffoldShellReadme,
		"shell/bash/.gitkeep":             "",
		"shell/zsh/.gitkeep":              "",
		"shell/fish/.gitkeep":             "",
		profileFile("base"):               s.baseProfile(),
	}
	switch s.OS {
	case Fedora, Ubuntu:
		files[profileFile(string(s.OS))] = s.osProfile(s.OS)
		files[MachineFile(s.Machine)] = s.linuxMachine()
	case Windows:
		if s.DistroUser == "" {
			return nil, errors.New("the Linux user for the WSL distro is missing")
		}
		files[profileFile(string(Ubuntu))] = s.osProfile(Ubuntu)
		files[profileFile("wsl")] = s.wslProfile()
		files[profileFile(string(Windows))] = s.windowsProfile()
		files[MachineFile(s.Machine)] = s.windowsMachine()
		files[MachineFile(s.DistroMachine())] = s.distroMachine()
	default:
		return nil, errors.New(invalidOS(s.OS))
	}
	return files, nil
}

// expand fills in the links to maisys's repo in text.
func (s *Scaffold) expand(text string) string {
	return strings.ReplaceAll(text, "{repo}", s.UpdateRepo)
}

// schemaLine is the first line of profiles and machine files, so editors
// can complete and check their keys.
func (s *Scaffold) schemaLine() string {
	return "#:schema https://github.com/" + s.UpdateRepo + "/releases/latest/download/config.schema.json\n\n"
}

func (s *Scaffold) docsURL() string {
	return "https://github.com/" + s.UpdateRepo + "/blob/main/docs/modules.md"
}

func (s *Scaffold) marker() string {
	version := s.Version
	if version == "" {
		version = "0.1"
	}
	return fmt.Sprintf(`# This file marks the folder as a maisys config repo.

# The version of the config format. Each maisys reads its own version and the
# one before it.
config_version = %d

# The maisys versions this config needs, such as ">= 0.6" or ">= 0.6, < 2".
# Without it, any maisys that reads config_version will do. Set it when the
# config starts using an option that older releases of maisys don't have.
# requires = ">= %s"
`, ConfigVersion, version)
}

// examples writes the example of every module that isn't hidden and
// supports one of systems, each after a blank line.
func (s *Scaffold) examples(b *strings.Builder, systems ...OS) {
	for _, mod := range s.Modules {
		info := mod.Info()
		if info.Hidden || !slices.ContainsFunc(info.OS, func(o OS) bool { return slices.Contains(systems, o) }) {
			continue
		}
		b.WriteString("\n" + ExampleTOML(mod, s.Docs))
	}
}

// comment formats a comment, wrapped to the width of the examples, with
// "# " before each line.
func comment(format string, a ...any) string {
	var b strings.Builder
	for _, line := range wrap(fmt.Sprintf(format, a...), exampleWidth-len("# ")) {
		b.WriteString("# " + line + "\n")
	}
	return b.String()
}

// examplesHelp explains the commented-out examples in a profile. turnOn
// says how to turn a module on.
func (s *Scaffold) examplesHelp(which, turnOn string) string {
	return comment(`Below is every module that works on %s, commented out, with each key at
its default. To use one, remove the "# " at the start of each of its lines (lines
starting with "# # " stay comments), turn it on with %s, and change what you
need. Keys you remove keep their defaults. The modules and their keys are
described in`, which, turnOn) + "# " + s.docsURL() + "\n"
}

func (s *Scaffold) baseProfile() string {
	var b strings.Builder
	b.WriteString(s.schemaLine())
	b.WriteString(comment("The base profile: settings for every Linux machine."))
	b.WriteString("#\n")
	b.WriteString(s.examplesHelp("Linux", "enabled = true (or for sudo, nopasswd = true)"))
	s.examples(&b, LinuxOS()...)
	return b.String()
}

func (s *Scaffold) osProfile(o OS) string {
	return s.schemaLine() +
		comment("The %s profile: settings for every %s machine. It sets os, so only %s machines can use it.", o, osName(o), osName(o)) +
		fmt.Sprintf("os = %s\n\n[packages]\nenabled = true\n", tomlString(string(o))) +
		comment(`Packages to install and keep up to date, by their %s names, such as "htop"
or "tmux". Machine files can add more of their own.`, osName(o)) +
		"install = []\n"
}

func (s *Scaffold) wslProfile() string {
	return s.schemaLine() +
		comment("The wsl profile: settings for Linux machines that are WSL distros.") +
		"\n[wsl]\nenabled = true\n" +
		comment(`Use the Windows Git Credential Manager, so git in the distro shares the
sign-ins Windows has, including the one for this config repo.`) +
		"git_credential_manager = true\n"
}

func (s *Scaffold) windowsProfile() string {
	var b strings.Builder
	b.WriteString(s.schemaLine())
	b.WriteString(comment("The windows profile: settings for every Windows machine. It sets os, so only Windows machines can use it."))
	b.WriteString("os = \"windows\"\n\n")
	b.WriteString(s.examplesHelp("Windows", "enabled = true"))
	s.examples(&b, Windows)
	return b.String()
}

// profilesHelp explains a machine file's profiles.
func profilesHelp() string {
	return comment(`The profiles to apply, in order, then this file: a value set here replaces
the profiles' value, and lists such as packages.install add to theirs.`)
}

func (s *Scaffold) linuxMachine() string {
	var b strings.Builder
	b.WriteString(s.schemaLine())
	b.WriteString(comment("The machine file for %s, a %s machine.", s.Machine, osName(s.OS)))
	fmt.Fprintf(&b, "os = %s\n", tomlString(string(s.OS)))
	if s.OSVersion != "" && osVersions[s.OS].pattern.MatchString(s.OSVersion) {
		b.WriteString(comment(`maisys only syncs this machine while it runs this version of %s. Change it
when you upgrade, or remove it to allow any version.`, osName(s.OS)))
		fmt.Fprintf(&b, "os_version = %s\n", tomlString(s.OSVersion))
	}
	b.WriteString(profilesHelp())
	fmt.Fprintf(&b, "profiles = [\"base\", %s]\n", tomlString(string(s.OS)))
	return b.String()
}

func (s *Scaffold) windowsMachine() string {
	var b strings.Builder
	b.WriteString(s.schemaLine())
	b.WriteString(comment("The machine file for %s, a Windows host for WSL distros.", s.Machine))
	b.WriteString("os = \"windows\"\n")
	b.WriteString(profilesHelp())
	b.WriteString("profiles = [\"windows\"]\n\n[wsl_host]\nenabled = true\n")
	b.WriteString(comment("The distro WSL starts by default."))
	fmt.Fprintf(&b, "default_distro = %s\n", tomlString(ScaffoldDistro))
	if s.ConfigURL != "" {
		b.WriteString(comment(`New distros clone this config repo over HTTPS, signing in with the Windows
Git Credential Manager. This is the HTTPS URL of the repo this machine clones
over SSH: check that it's right.`))
		fmt.Fprintf(&b, "config_url = %s\n", tomlString(s.ConfigURL))
	}
	b.WriteString("\n")
	b.WriteString(comment(`The distros to create, by name. maisys creates and sets up each one once,
from its Linux machine file; after that, run maisys sync inside the distro to
keep it up to date. maisys never deletes or recreates a distro.`))
	fmt.Fprintf(&b, "[wsl_host.distros.%s]\n", tomlKey(ScaffoldDistro))
	b.WriteString(comment("The image to install, by its name in wsl --list --online."))
	fmt.Fprintf(&b, "image = %s\n", tomlString(scaffoldImage))
	b.WriteString(comment("The Linux machine file to set the distro up from."))
	fmt.Fprintf(&b, "machine = %s\n", tomlString(s.DistroMachine()))
	b.WriteString(comment("The Linux user to create, with a random password that's shown once."))
	fmt.Fprintf(&b, "user = %s\n", tomlString(s.DistroUser))
	return b.String()
}

func (s *Scaffold) distroMachine() string {
	var b strings.Builder
	b.WriteString(s.schemaLine())
	b.WriteString(comment(`The machine file for the WSL distro %s, which the Windows host %s creates
(see %s). It's synced from inside the distro.`, ScaffoldDistro, s.Machine, MachineFile(s.Machine)))
	b.WriteString("os = \"ubuntu\"\n")
	b.WriteString(comment(`The version of the distro's image. maisys only syncs the distro while it
runs this version of Ubuntu.`))
	fmt.Fprintf(&b, "os_version = %s\n", tomlString(scaffoldImageVersion))
	b.WriteString(profilesHelp())
	b.WriteString("profiles = [\"base\", \"ubuntu\", \"wsl\"]\n")
	return b.String()
}

// osName returns the name people use for o, such as "Fedora".
func osName(o OS) string {
	switch o {
	case Fedora:
		return "Fedora"
	case Ubuntu:
		return "Ubuntu"
	case Windows:
		return "Windows"
	default:
		return string(o)
	}
}

const scaffoldReadme = "# maisys config\n\n" +
	"This repo is a config for [maisys](https://github.com/{repo}), which sets\n" +
	"up machines and keeps them up to date. maisys knows how to install\n" +
	"everything through its built-in modules; this repo says what each machine\n" +
	"gets, by turning modules on and setting their options.\n\n" +
	"## Using it\n\n" +
	"- **A new machine:** add a machine file for it in `machines/`, then install\n" +
	"  maisys and sync the machine with this repo. On Linux, where `CONFIG-URL`\n" +
	"  is this repo's URL:\n\n" +
	"  ```sh\n" +
	"  curl -fsSL https://github.com/{repo}/releases/latest/download/install.sh | bash -s -- CONFIG-URL\n" +
	"  ```\n\n" +
	"  On Windows 11, in PowerShell as your normal user:\n\n" +
	"  ```powershell\n" +
	"  iex \"& { $(irm https://github.com/{repo}/releases/latest/download/install.ps1) } CONFIG-URL\"\n" +
	"  ```\n\n" +
	"  With maisys already installed, run `maisys sync CONFIG-URL`.\n" +
	"- **Changing the config:** edit the files, and check them with\n" +
	"  `maisys config validate --all`. `maisys sync --dry-run` shows what a sync\n" +
	"  would change, and `maisys sync` applies it. Commit and push your changes,\n" +
	"  so your other machines get them on their next `maisys sync`.\n" +
	"- **What you can set:** every module and its keys are described in\n" +
	"  [modules.md](https://github.com/{repo}/blob/main/docs/modules.md).\n" +
	"  `maisys config --explain` shows a machine's merged config, and which file\n" +
	"  set each value.\n\n" +
	"## Layout\n\n" +
	"| Path | What it holds |\n" +
	"|---|---|\n" +
	"| `maisys.toml` | Marks this repo as a maisys config, with its `config_version` and an optional `requires` |\n" +
	"| `machines/<name>.toml` | One file per machine: its `os`, the profiles it uses, and its own settings |\n" +
	"| `profiles/<name>.toml` | Settings that machines share, such as `base.toml` for every Linux machine |\n" +
	"| `files/` | Files the `files` module copies into your home folder |\n" +
	"| `shell/bash/`, `shell/zsh/`, `shell/fish/` | Snippets the `shell` module loads in each shell |\n" +
	"| `.github/workflows/validate.yaml` | Checks every machine with `maisys config validate --all` on each push |\n\n" +
	"A machine's config is built in layers: each module's defaults, then the\n" +
	"profiles the machine file lists, in order, then the machine file itself.\n"

const scaffoldWorkflow = `# Checks every machine in this config repo with the latest maisys.
name: Validate

on:
  push:
  pull_request:

permissions:
  contents: read

jobs:
  validate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - name: Install maisys
        run: curl -fsSL https://github.com/{repo}/releases/latest/download/install.sh | bash
      - name: Validate the config
        run: ~/.local/bin/maisys config validate --all
`

const scaffoldFilesReadme = "# files\n\n" +
	"Files, directories and templates for the `files` module, which copies them\n" +
	"into your home folder. Each item in `files.items` names its source here and\n" +
	"its target, for example:\n\n" +
	"```toml\n" +
	"[files]\n" +
	"enabled = true\n\n" +
	"[files.items.gitconfig]\n" +
	"source = \"files/gitconfig\"\n" +
	"target = \"~/.gitconfig\"\n" +
	"```\n"

const scaffoldShellReadme = "# shell\n\n" +
	"Snippets for the `shell` module, in each shell's own syntax: `bash/`, `zsh/`\n" +
	"and `fish/`. A shell loads the snippets listed in its `pre_snippets` before\n" +
	"oh-my-zsh and maisys's environment, and those in its `snippets` after\n" +
	"everything else, for example:\n\n" +
	"```toml\n" +
	"[shell.zsh]\n" +
	"enabled = true\n" +
	"snippets = [\"shell/zsh/aliases.zsh\"]\n" +
	"```\n"
