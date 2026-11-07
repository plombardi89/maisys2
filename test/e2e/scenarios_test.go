package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/opencode"
	"github.com/plombardi89/maisys2/internal/shells"
	"github.com/plombardi89/maisys2/test/fixture"
)

// scenario is one of the scenarios from issue #1's Linux end-to-end tests.
// Each starts from a copy of a synced test machine (see TestSync), changes
// the fixture or the machine, syncs again and checks what happened. A
// change to the fixture is a commit to the repo the machine cloned its
// config from, which the next sync pulls.
type scenario struct {
	name string
	// arch, if it's set, is the only architecture the scenario runs on.
	arch config.Arch
	run  func(s *scene)
}

// runsOn reports whether the scenario runs on goarch.
func (sc scenario) runsOn(goarch string) bool {
	return sc.arch == "" || string(sc.arch) == goarch
}

// scenarios are the scenarios, in the order they're listed in issue #1.
var scenarios = []scenario{
	{name: "bump", run: versionBump},
	{name: "latest", run: latestVersion},
	{name: "plugin", run: pluginBranch},
	{name: "login-shell", run: loginShell},
	{name: "module-off", run: moduleOff},
	{name: "drift", run: drift},
	{name: "typo", run: packageTypo},
	{name: "interrupt", run: interrupt},
	{name: "wrong-os", run: wrongOS},
	{name: "wrong-arch", run: wrongArch},
	{name: "switch-repo", run: switchRepo},
	{name: "legacy", run: legacy},
	{name: "opencode", run: opencodeUpgrade},
	// Valve has no arm64 client.
	{name: "steam", arch: config.AMD64, run: steamOff},
}

// What the scenarios change the fixture to.
const (
	// newKubectlVersion is the kubectl release the version bump moves to,
	// a newer one than fixture.KubectlVersion.
	newKubectlVersion = "1.37.2"
	// The interrupted sync moves golang, kubectl and yq to these versions,
	// in that order.
	otherGoVersion = "1.27.2"
	newYqVersion   = "4.54.2"

	// yqRepo is where yq is released, for the latest release's version.
	yqRepo = "mikefarah/yq"

	// pluginName is the custom oh-my-zsh plugin the plugin scenario
	// follows on its branch, pluginBranchName, in the bare repo pluginRepo,
	// which it commits to from pluginWork.
	pluginName       = "maisys-e2e-plugin"
	pluginBranchName = "main"
	pluginRepo       = sharedDir + "/plugin.git"
	pluginWork       = sharedDir + "/plugin"

	// typoPackage is a package no distro has, which the typo scenario
	// asks for next to typoNeighbour, a real one.
	typoPackage   = "maisys-e2e-no-such-package"
	typoNeighbour = "zip"

	// otherRepo is the config repo the switch-repo scenario switches to,
	// which it clones into otherClone.
	otherRepo  = sharedDir + "/other"
	otherURL   = "file://" + otherRepo
	otherClone = "~/other-config"

	// opencodeV1Installer is opencode's installer for version 1, which
	// the opencode scenario installs it with before maisys upgrades it.
	opencodeV1Installer = "https://opencode.ai/install"
)

// What the scenarios add to the end of the test machine's file.
// TestScenarioConfigs checks that they're valid.
const (
	bumpConfig = `
[kubectl]
version = "` + newKubectlVersion + `"
`
	latestConfig = `
[yq]
version = "latest"
`
	// pluginConfig adds a plugin from a repo in the container. maisys
	// clones custom plugins from any URL git can fetch without signing
	// in, so a file:// URL works as an https:// one would.
	pluginConfig = `
[shell.zsh.oh_my_zsh]
plugins = ["` + pluginName + `"]

[shell.zsh.oh_my_zsh.custom_plugins.` + pluginName + `]
repo = "file://` + pluginRepo + `"
ref = "` + pluginBranchName + `"
`
	takeOverConfig = `
[files.items.taken]
source = "files/e2e/plain.txt"
target = "~/e2e/taken.txt"
`
	modulesOffConfig = `
[golang]
enabled = false

[files]
enabled = false

[packages]
enabled = false
`
	typoConfig = `
[packages]
install = ["` + typoNeighbour + `", "` + typoPackage + `"]
`
	interruptConfig = `
[golang]
version = "` + otherGoVersion + `"

[kubectl]
version = "` + newKubectlVersion + `"

[yq]
version = "` + newYqVersion + `"
`
	otherRepoConfig = `
[files.items.other]
source = "files/e2e/plain.txt"
target = "~/e2e/other.txt"
`
	legacyConfig = `
[legacy]
enabled = true
`
	opencodeConfig = `
[opencode]
enabled = true
`
	steamOnConfig = `
[steam]
enabled = true
`
	steamOffConfig = `
[steam]
enabled = false
`
)

// loginConfig makes shell the test machine's login shell.
func loginConfig(shell string) string {
	return "\n[shell]\nlogin = \"" + shell + "\"\n"
}

// otherOS returns an OS d isn't.
func otherOS(d fixture.Distro) config.OS {
	if d.OS == config.Fedora {
		return config.Ubuntu
	}
	return config.Fedora
}

// otherArch returns the architecture this machine, and so the containers,
// aren't.
func otherArch() config.Arch {
	if runtime.GOARCH == string(config.ARM64) {
		return config.AMD64
	}
	return config.ARM64
}

// wrongOSMachine returns the name and file of a machine for another OS than
// d's.
func wrongOSMachine(d fixture.Distro) (name, data string) {
	o := otherOS(d)
	return d.Machine() + "-wrong-os", fmt.Sprintf("os = %q\nprofiles = [\"base\", %q]\n", o, o)
}

// wrongArchMachine returns the name and file of a machine like d's test
// machine, but for another architecture than this one.
func wrongArchMachine(d fixture.Distro) (name, data string) {
	return d.Machine() + "-wrong-arch", fmt.Sprintf("os = %q\nos_version = %q\narch = %q\nprofiles = [\"base\", %q, \"dev\"]\n",
		d.OS, d.Version, otherArch(), d.OS)
}

// protocReadme is the readme.txt in protoc's release zips (sha256
// cb87be42344000337bef2e65de178c7e2f9cd7a1b7cd0e4284377a5d375db82f), which
// the Ansible setup unpacked into the home folder.
const protocReadme = `Protocol Buffers - Google's data interchange format
Copyright 2008 Google Inc.
https://developers.google.com/protocol-buffers/
This package contains a precompiled binary version of the protocol buffer
compiler (protoc). This binary is intended for users who want to use Protocol
Buffers in languages other than C++ but do not want to compile protoc
themselves. To install, simply place this binary somewhere in your PATH.
If you intend to use the included well known types then don't forget to
copy the contents of the 'include' directory somewhere as well, for example
into '/usr/local/include/'.
Please refer to our official github site for more installation instructions:
  https://github.com/protocolbuffers/protobuf
`

// legacyPaths are what the Ansible setup left behind, which the legacy
// module deletes. The folders among them are marked with a trailing slash.
var legacyPaths = []string{
	"/usr/local/go/",
	"~/bin/kubectl",
	"~/bin/stern",
	"~/bin/yq",
	"~/bin/lnav",
	"~/bin/protoc",
	"~/bin/mutagen/",
	"~/go/bin/mage",
	"~/go/bin/golangci-lint",
	"~/include/google/",
	"~/Apps/idea/",
	"~/Apps/idea.bak/",
	"~/venv/maisys/",
}

// versionBump pins kubectl to a newer version: the new version is
// installed and linked, and the old one deleted.
func versionBump(s *scene) {
	s.setMachine("Bump kubectl", bumpConfig)
	out := s.sync("dry run", exitChanges, "--dry-run")
	s.wantOutput("the dry run", out, "~ kubectl "+fixture.KubectlVersion+" → "+newKubectlVersion)
	s.apply()
	s.settled()
	s.wantTool("kubectl", newKubectlVersion, "kubectl")
	s.wantCommand("kubectl", "Client Version: v"+newKubectlVersion, "version", "--client")
}

// latestVersion moves yq from its pinned version to "latest": the sync
// installs yq's latest release, and a second sync has nothing to change.
func latestVersion(s *scene) {
	want, err := latestRelease(s.ctx, yqRepo)
	if err != nil {
		s.t.Fatalf("looking up yq's latest release: %v", err)
	}
	s.setMachine("Follow yq's latest release", latestConfig)
	out := s.apply()
	if want != fixture.YqVersion {
		s.wantOutput("the sync", out, "~ yq "+fixture.YqVersion+" → "+want+" (latest)")
	}
	s.settled()
	s.wantTool("yq", want, "yq")
	s.wantCommand("yq", "version v"+want, "--version")
}

// pluginBranch adds a custom oh-my-zsh plugin on a branch of a repo in
// the container, then commits to the branch: the next sync moves the
// plugin's checkout to the new commit, and the plan says so.
func pluginBranch(s *scene) {
	first := s.commitPlugin("first")
	s.setMachine("Add a custom plugin on a branch", pluginConfig)
	out := s.apply()
	s.wantOutput("the sync", out, "+ "+pluginName+" ("+pluginBranchName+")")
	s.wantCheckout(first)
	s.settled()

	second := s.commitPlugin("second")
	out = s.sync("dry run", exitChanges, "--dry-run", "--local")
	s.wantOutput("the dry run", out, fmt.Sprintf("~ %s %s → %s (%s)", pluginName, first[:7], second[:7], pluginBranchName))
	s.sync("sync", exitOK, "--yes", "--local")
	s.wantCheckout(second)
	s.settled()
	got := s.must(testUser, `SHELL=/usr/bin/zsh zsh -l -i -c 'print -r -- "$MAISYS_E2E_PLUGIN"' 2>/dev/null </dev/null`)
	if got != "second" {
		s.t.Errorf("zsh sets MAISYS_E2E_PLUGIN to %q, want %q: it should load the plugin at its second commit", got, "second")
	}
}

// loginShell makes bash, then fish, then zsh again the login shell.
func loginShell(s *scene) {
	for _, sh := range []shells.Shell{shells.Bash{}, shells.Fish{}, shells.Zsh{}} {
		before := s.loginShell()
		extra := loginConfig(sh.Name())
		if sh.Name() == "zsh" {
			extra = "" // the fixture's login shell
		}
		s.setMachine("Log in with "+sh.Name(), extra)
		out := s.apply()
		s.wantOutput("the sync", out, "~ login shell "+before+" → "+sh.Binary())
		if got := s.loginShell(); got != sh.Binary() {
			s.t.Errorf("after a sync with shell.login = %q, getent passwd shows the login shell %s, want %s", sh.Name(), got, sh.Binary())
		}
		s.settled()
	}
}

// moduleOff turns golang, files and packages off: the Go toolchain and the
// program go install installed are deleted, and so are the files maisys
// created, but a file it took over is left in place, and so are the
// packages, which the plan says are no longer used.
func moduleOff(s *scene) {
	taken := testHome + "/e2e/taken.txt"
	s.writeFile(testUser, taken, "Written by hand, before maisys took it over.\n")
	s.setMachine("Take over a file", takeOverConfig)
	out := s.apply()
	s.wantOutput("the sync", out, "! ~/e2e/taken.txt exists (will back up and take over)")
	if s.must("root", `grep -l '^Written by hand' "$1".maisys-bak.* || true`, taken) == "" {
		s.t.Errorf("there's no backup of ~/e2e/taken.txt as it was before maisys took it over, ~/e2e/taken.txt.maisys-bak.<time>")
	}
	s.settled()

	s.setMachine("Turn off golang, files and packages", modulesOffConfig)
	created := []string{"~/e2e/plain.txt", "~/e2e/about.txt", "~/.config/maisys-e2e/settings.conf", "~/.config/maisys-e2e/bin/hello.sh"}
	program := path.Base(fixture.GoProgram)
	want := []string{
		"- go " + fixture.GoVersion,
		"- " + program + " " + fixture.GoProgramVersion,
		"! ~/e2e/taken.txt left in place; no longer managed",
	}
	for _, p := range created {
		want = append(want, "- "+p)
	}
	for _, p := range fixture.Packages {
		want = append(want, "? package "+p+" is no longer used (not removed)")
	}
	out = s.sync("dry run", exitChanges, "--dry-run")
	s.wantOutput("the dry run", out, want...)
	s.apply()
	s.settled()

	for _, p := range append(created, "~/.local/share/maisys/tools/go", "~/.local/bin/go", "~/.local/bin/gofmt", "~/go/bin/"+program) {
		if s.exists(p) {
			s.t.Errorf("%s is still there after its module was turned off, want it deleted", p)
		}
	}
	if got, want := s.must("root", `cat "$1"`, taken), strings.TrimSpace(s.files["files/e2e/plain.txt"].Data); got != want {
		s.t.Errorf("%s has %q, want it left in place as maisys last wrote it, %q", taken, got, want)
	}
	for _, p := range fixture.Packages {
		if !s.installed(p) {
			s.t.Errorf("package %s isn't installed any more after packages was turned off, want it left installed", p)
		}
	}
}

// drift edits ~/.zshrc, which the shell module writes, and a files target
// by hand: the plan says they changed, and the sync backs them up and
// writes them again.
func drift(s *scene) {
	edits := []string{testHome + "/.zshrc", testHome + "/e2e/plain.txt"}
	before := map[string]string{}
	for _, f := range edits {
		before[f] = s.must("root", `sha256sum <"$1"`, f)
		s.must(testUser, `echo '# edited by hand' >>"$1"`, f)
	}
	out := s.sync("dry run", exitChanges, "--dry-run", "--local")
	var want []string
	for _, f := range edits {
		want = append(want, "! "+tilde(f)+" changed outside maisys (will back up and overwrite)")
	}
	s.wantOutput("the dry run", out, want...)
	s.sync("sync", exitOK, "--yes", "--local")
	s.settled()
	for _, f := range edits {
		if got := s.must("root", `sha256sum <"$1"`, f); got != before[f] {
			s.t.Errorf("%s isn't as maisys wrote it after the sync", tilde(f))
		}
		backups := s.must("root", `grep -l '^# edited by hand$' "$1".maisys-bak.* || true`, f)
		if backups == "" {
			s.t.Errorf("there's no backup of %s with the edit, %s.maisys-bak.<time>", tilde(f), tilde(f))
		}
	}
	if got := s.must("root", `stat -c %a "$1"`, testHome+"/e2e/plain.txt"); got != "600" {
		s.t.Errorf("~/e2e/plain.txt has mode %s after the sync, want 600", got)
	}
}

// packageTypo asks for a package that doesn't exist: the plan fails, and
// nothing of the packages module is installed, not even the package next
// to it.
func packageTypo(s *scene) {
	s.setMachine("Ask for a package that doesn't exist", typoConfig)
	want := []string{"unknown package " + typoPackage, "packages failed, so it won't be applied."}
	out := s.sync("dry run", exitFailed, "--dry-run")
	s.wantOutput("the dry run", out, want...)
	out = s.sync("sync", exitFailed, "--yes")
	s.wantOutput("the sync", out, want...)
	if s.installed(typoNeighbour) {
		s.t.Errorf("the sync installed %s, want nothing of packages installed, since it failed in the plan", typoNeighbour)
	}
}

// interrupt moves golang, kubectl and yq to other versions, in that order,
// and sends the sync SIGINT, as Ctrl-C does, as soon as it starts on
// golang. Nothing needs slowing down for that: downloading and unpacking
// the Go toolchain takes seconds, and kubectl's download comes after it,
// so the sync stops after golang (or kubectl, if the signal is late), with
// yq still to do. The next sync finishes the job.
func interrupt(s *scene) {
	s.setMachine("Move golang, kubectl and yq", interruptConfig)
	out, code, sent := s.interrupt(s.ctx, "interrupted sync", "golang: ", maisysPath, "sync", "--yes")
	if !sent {
		s.t.Fatal("the sync never started on golang, so it wasn't interrupted")
	}
	if code != exitFailed {
		s.t.Errorf("the interrupted sync exited with %d, want %d", code, exitFailed)
	}
	s.wantOutput("the interrupted sync", out, "maisys: stopping after the current step", "Interrupted: run maisys sync again to finish.")
	if got := s.status("last sync"); !strings.HasSuffix(got, "(interrupted)") {
		s.t.Errorf("maisys status shows the last sync as %q, want it interrupted", got)
	}
	s.wantTool("go", otherGoVersion, "go/bin/go")
	s.wantTool("yq", fixture.YqVersion, "yq")

	out = s.sync("dry run", exitChanges, "--dry-run", "--local")
	s.wantOutput("the dry run after the interrupted sync", out, "~ yq "+fixture.YqVersion+" → "+newYqVersion)
	s.wantNoOutput("the dry run after the interrupted sync", out, "~ go ")
	s.sync("sync", exitOK, "--yes", "--local")
	s.settled()
	s.wantTool("go", otherGoVersion, "go/bin/go")
	s.wantTool("kubectl", newKubectlVersion, "kubectl")
	s.wantTool("yq", newYqVersion, "yq")
	if got := s.status("last sync"); !strings.HasSuffix(got, "(ok)") {
		s.t.Errorf("maisys status shows the last sync as %q, want it ok", got)
	}
}

// wrongOS syncs a machine for another OS: maisys refuses before it
// changes anything.
func wrongOS(s *scene) {
	name, data := wrongOSMachine(s.d)
	s.refused(name, data, fmt.Sprintf("machine %q is for %s, but it's running on", name, title(otherOS(s.d))))
}

// wrongArch syncs a machine for another architecture: maisys refuses
// before it changes anything. On arm64, that's a machine with arch =
// "amd64", as issue #1 asks for; on amd64, the other way round.
func wrongArch(s *scene) {
	name, data := wrongArchMachine(s.d)
	s.refused(name, data, fmt.Sprintf("machine %q is for %s, but it's running on %s", name, otherArch(), runtime.GOARCH))
}

// switchRepo switches to a second config repo: the plan's header shows
// the switch, and it's saved only once the plan is applied.
func switchRepo(s *scene) {
	// A local clone hard-links the fixture's objects, which git finds
	// aren't the same files afterwards, on the overlay file system of a
	// container.
	s.must(testUser, `git clone --quiet --no-local "$1" "$2"`, fixtureDir, otherRepo)
	s.commit(otherRepo, "Add a file", map[string]string{s.machineFile(): s.machineWith(otherRepoConfig)})
	header := "config repo: " + fixtureURL + " → " + otherURL

	out := s.sync("dry run", exitChanges, otherURL, "--dir", otherClone, "--dry-run")
	s.wantOutput("the dry run", out, header, "+ ~/e2e/other.txt")
	if got := s.status("url"); got != fixtureURL {
		s.t.Errorf("after a dry run, maisys status shows the config repo %s, want %s still: a switch is only saved once it's applied", got, fixtureURL)
	}
	out = s.sync("sync", exitOK, otherURL, "--dir", otherClone, "--yes")
	s.wantOutput("the sync", out, header)
	if got := s.status("url"); got != otherURL {
		s.t.Errorf("after the sync, maisys status shows the config repo %s, want %s", got, otherURL)
	}
	if got, want := s.status("config repo"), testHome+"/other-config"; got != want {
		s.t.Errorf("after the sync, maisys status shows the config repo's clone in %s, want %s", got, want)
	}
	if !s.exists("~/e2e/other.txt") {
		s.t.Error("~/e2e/other.txt, which only the second config repo has, isn't there")
	}
	s.sync("dry run", exitOK, "--dry-run")
}

// legacy creates what the Ansible setup left behind, and turns on the
// legacy module: everything it lists is deleted, except a ~/readme.txt
// that isn't protoc's; a later ~/readme.txt that is, is deleted too.
func legacy(s *scene) {
	for _, p := range legacyPaths {
		user := testUser
		if !strings.HasPrefix(p, "~/") {
			user = "root"
		}
		abs, isDir := strings.CutSuffix(expand(p), "/")
		if isDir {
			abs += "/file"
		}
		s.writeFile(user, abs, "Left behind by the Ansible setup.\n")
	}
	keep := []string{"~/bin/keep", "~/readme.txt"}
	for _, p := range keep {
		s.writeFile(testUser, expand(p), "Not left by the Ansible setup.\n")
	}

	s.setMachine("Clean up after the Ansible setup", legacyConfig)
	var want []string
	for _, p := range legacyPaths {
		want = append(want, "- "+strings.TrimSuffix(p, "/"))
	}
	out := s.sync("dry run", exitChanges, "--dry-run")
	s.wantLines("the dry run", out, want...)
	s.wantNoOutput("the dry run", out, "- ~/readme.txt", "- ~/bin/keep")
	s.apply()
	s.settled()
	for _, p := range legacyPaths {
		if s.exists(strings.TrimSuffix(p, "/")) {
			s.t.Errorf("%s is still there, want the legacy module to delete it", strings.TrimSuffix(p, "/"))
		}
	}
	for _, p := range keep {
		if !s.exists(p) {
			s.t.Errorf("%s is gone, want the legacy module to leave it alone", p)
		}
	}

	s.writeFile(testUser, testHome+"/readme.txt", protocReadme)
	out = s.sync("dry run", exitChanges, "--dry-run", "--local")
	s.wantOutput("the dry run", out, "- ~/readme.txt")
	s.sync("sync", exitOK, "--yes", "--local")
	s.settled()
	if s.exists("~/readme.txt") {
		s.t.Error("~/readme.txt, which is protoc's readme, is still there, want the legacy module to delete it")
	}
}

// opencodeUpgrade installs opencode v1 with its installer, then turns on
// opencode: maisys upgrades it to v2.
func opencodeUpgrade(s *scene) {
	bin := testHome + "/.opencode/bin/opencode"
	// The installer needs curl and tar, which the opencode module installs
	// too.
	s.installPackages("curl", "tar")
	install := []string{"bash", "-c", `set -o pipefail; curl -fsSL "$1" | bash -s -- --no-modify-path`, "bash", opencodeV1Installer}
	if _, code := s.output(s.ctx, "install opencode v1", testUser, nil, install...); code != 0 {
		s.t.Fatalf("opencode's v1 installer, %s, exited with %d", opencodeV1Installer, code)
	}
	old, major, ok := opencode.ParseVersion(s.must(testUser, `"$1" --version`, bin))
	if !ok || major >= opencode.MinMajor {
		s.t.Fatalf("opencode's v1 installer installed version %q, want a version 1 release", old)
	}

	s.setMachine("Turn on opencode", opencodeConfig)
	out := s.apply()
	s.wantOutput("the sync", out, fmt.Sprintf("~ upgrade opencode %s → %d (installer)", old, opencode.MinMajor))
	got, major, ok := opencode.ParseVersion(s.must(testUser, `"$1" --version`, bin))
	if !ok || major < opencode.MinMajor {
		s.t.Errorf("after the sync, opencode is version %q, want %d or newer", got, opencode.MinMajor)
	}
	s.settled()
}

// steamOff turns steam on, then off again: its packages, repos and dpkg's
// i386 architecture are left in place, and the plan says they're no
// longer used.
func steamOff(s *scene) {
	var packages, repos []string
	switch s.d.OS {
	case config.Fedora:
		packages, repos = []string{"steam"}, []string{"rpmfusion-free", "rpmfusion-nonfree"}
	default:
		packages, repos = []string{"steam-launcher", "steam-libs-amd64", "steam-libs-i386:i386"}, []string{"steam"}
	}
	s.setMachine("Turn on steam", steamOnConfig)
	s.apply()
	s.settled()

	s.setMachine("Turn off steam", steamOffConfig)
	var want []string
	for _, p := range packages {
		want = append(want, "? package "+p+" is no longer used (not removed)")
	}
	for _, r := range repos {
		want = append(want, "? repo "+r+" is no longer used (not removed)")
	}
	if s.d.OS == config.Ubuntu {
		want = append(want, "? architecture i386 is no longer used (not removed)")
	}
	out := s.sync("sync", exitOK, "--yes")
	s.wantOutput("the sync", out, want...)
	s.settled()
	for _, p := range packages {
		if !s.installed(p) {
			s.t.Errorf("package %s isn't installed any more after steam was turned off, want it left installed", p)
		}
	}
	switch s.d.OS {
	case config.Fedora:
		for _, r := range repos {
			if !s.installed(r + "-release") {
				s.t.Errorf("%s-release isn't installed any more after steam was turned off, want the repo left in place", r)
			}
		}
	default:
		if got := s.must("root", "dpkg --print-foreign-architectures"); !slices.Contains(strings.Fields(got), "i386") {
			s.t.Errorf("dpkg's foreign architectures are %q after steam was turned off, want i386 left in place", got)
		}
	}
}

// The exit codes of maisys sync.
const (
	exitOK      = 0
	exitFailed  = 1
	exitChanges = 2 // with --dry-run
)

// scene is a scenario's machine: a container started from a copy of the
// synced test machine of distro d.
type scene struct {
	*machine
	ctx context.Context
	d   fixture.Distro
	// files are the fixture's files, as the machine first synced them.
	files map[string]fixture.File
}

// maisys runs maisys with args as the test user, and returns its output
// and exit code.
func (s *scene) maisys(step string, args ...string) (string, int) {
	s.t.Helper()
	return s.output(s.ctx, step, testUser, nil, append([]string{maisysPath}, args...)...)
}

// sync runs maisys sync with args, and stops the test unless it exits
// with want. It returns the output.
func (s *scene) sync(step string, want int, args ...string) string {
	s.t.Helper()
	out, code := s.maisys(step, append([]string{"sync"}, args...)...)
	if code != want {
		s.t.Fatalf("%s: maisys sync %s exited with %d, want %d", step, strings.Join(args, " "), code, want)
	}
	return out
}

// apply syncs, which pulls the fixture, and stops the test unless the sync
// works. It returns the output.
func (s *scene) apply() string {
	s.t.Helper()
	return s.sync("sync", exitOK, "--yes")
}

// settled checks that a dry run has nothing to change.
func (s *scene) settled() {
	s.t.Helper()
	if _, code := s.maisys("dry run", "sync", "--dry-run", "--local"); code != exitOK {
		s.t.Errorf("maisys sync --dry-run exited with %d after the sync, want %d: a second sync would change something", code, exitOK)
	}
}

// refused commits the machine file data for machine name to the fixture,
// and checks that a sync with it fails with message before changing
// anything: the state file and the local settings stay as they are.
func (s *scene) refused(name, data, message string) {
	s.t.Helper()
	s.commit(fixtureDir, "Add machine "+name, map[string]string{config.MachineFile(name): data})
	files := testHome + "/.local/state/maisys/state.json " + testHome + "/.config/maisys/maisys.toml"
	before := s.must("root", "sha256sum "+files)
	out := s.sync("sync", exitFailed, "--machine", name, "--yes")
	s.wantOutput("the sync", out, message)
	s.wantNoOutput("the sync", out, "Plan for ")
	if after := s.must("root", "sha256sum "+files); after != before {
		s.t.Errorf("the refused sync changed the state file or the local settings:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if got := s.status("machine"); got != s.d.Machine() {
		s.t.Errorf("after the refused sync, maisys status shows the machine %s, want %s still", got, s.d.Machine())
	}
	s.settled()
}

// machineFile returns the path of the test machine's file in the fixture.
func (s *scene) machineFile() string {
	return config.MachineFile(s.d.Machine())
}

// machineWith returns the test machine's file with extra added to its end.
func (s *scene) machineWith(extra string) string {
	return s.files[s.machineFile()].Data + extra
}

// setMachine commits the test machine's file, with extra added to its
// end, to the fixture.
func (s *scene) setMachine(message, extra string) {
	s.t.Helper()
	s.commit(fixtureDir, message, map[string]string{s.machineFile(): s.machineWith(extra)})
}

// gitIdentity sets who git commits as.
const gitIdentity = `export GIT_AUTHOR_NAME='maisys e2e' GIT_AUTHOR_EMAIL=e2e@example.com GIT_COMMITTER_NAME='maisys e2e' GIT_COMMITTER_EMAIL=e2e@example.com`

// commit writes files, by their paths in the git repo in dir, and commits
// them, as the test user.
func (s *scene) commit(dir, message string, files map[string]string) {
	s.t.Helper()
	logStep(s.t, "commit to "+dir+": "+message)
	for name, data := range files {
		s.writeFile(testUser, dir+"/"+name, data)
	}
	s.must(testUser, gitIdentity+`; git -C "$1" add --all && git -C "$1" commit --quiet --message "$2"`, dir, message)
}

// commitPlugin commits a version of the plugin that sets MAISYS_E2E_PLUGIN
// to value, pushes it to the plugin's branch, and returns the commit. The
// first one creates the plugin's repos.
func (s *scene) commitPlugin(value string) string {
	s.t.Helper()
	logStep(s.t, "commit to the plugin's branch: "+value)
	script := gitIdentity + `
set -eu
if [ ! -d "$1" ]; then
	git init --quiet --bare --initial-branch="$3" "$1"
	git init --quiet --initial-branch="$3" "$2"
fi
printf 'export MAISYS_E2E_PLUGIN=%s\n' "$4" >"$2/$5.plugin.zsh"
git -C "$2" add --all
git -C "$2" commit --quiet --message "Set MAISYS_E2E_PLUGIN to $4"
git -C "$2" push --quiet "$1" "$3"
git -C "$2" rev-parse HEAD`
	return s.must(testUser, script, pluginRepo, pluginWork, pluginBranchName, value, pluginName)
}

// wantCheckout checks that the plugin is checked out at commit.
func (s *scene) wantCheckout(commit string) {
	s.t.Helper()
	dir := testHome + "/.oh-my-zsh/custom/plugins/" + pluginName
	if got := s.must(testUser, `git -C "$1" rev-parse HEAD`, dir); got != commit {
		s.t.Errorf("the plugin is checked out at %s, want %s, the head of its branch", got, commit)
	}
}

// writeFile writes data to file, an absolute path, as user, creating its
// folder if it's missing.
func (s *scene) writeFile(user, file, data string) {
	s.t.Helper()
	script := `mkdir -p "$(dirname "$1")" && cat >"$1"`
	if out, code := s.quiet(s.ctx, user, nil, strings.NewReader(data), "sh", "-c", script, "sh", file); code != 0 {
		s.t.Fatalf("writing %s failed with %d: %s", file, code, out)
	}
}

// sh runs a shell script as user, root or the test user, with args as $1
// and so on, and returns its output and exit code without showing them.
func (s *scene) sh(user, script string, args ...string) (string, int) {
	s.t.Helper()
	out, code := s.quiet(s.ctx, user, nil, nil, append([]string{"sh", "-c", script, "sh"}, args...)...)
	return strings.TrimSpace(out), code
}

// must runs a shell script, as sh does, and stops the test if it fails. It
// returns the script's output.
func (s *scene) must(user, script string, args ...string) string {
	s.t.Helper()
	out, code := s.sh(user, script, args...)
	if code != 0 {
		s.t.Fatalf("this script failed with %d, as %s:\n%s\noutput:\n%s", code, user, script, out)
	}
	return out
}

// exists reports whether there's a file or folder at file, which may
// start with "~/".
func (s *scene) exists(file string) bool {
	s.t.Helper()
	_, code := s.sh("root", `test -e "$1" || test -L "$1"`, expand(file))
	return code == 0
}

// installed reports whether the package name is installed.
func (s *scene) installed(name string) bool {
	s.t.Helper()
	script := `dpkg-query -W -f '${Status}' "$1" 2>/dev/null | grep -qx 'install ok installed'`
	if s.d.OS == config.Fedora {
		script = `rpm -q "$1" >/dev/null 2>&1`
	}
	_, code := s.sh("root", script, name)
	return code == 0
}

// installPackages installs packages with the distro's package manager, as
// root, the way someone might have before maisys.
func (s *scene) installPackages(names ...string) {
	s.t.Helper()
	install := append([]string{"dnf", "--assumeyes", "--quiet", "--setopt=install_weak_deps=False", "install"}, names...)
	if s.d.OS == config.Ubuntu {
		install = append([]string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "--yes", "--quiet", "--no-install-recommends", "install"}, names...)
	}
	if code := s.run(s.ctx, "install "+strings.Join(names, " "), "root", nil, install...); code != 0 {
		s.t.Fatalf("installing %s exited with %d", strings.Join(names, " "), code)
	}
}

// loginShell returns the test user's login shell.
func (s *scene) loginShell() string {
	s.t.Helper()
	return s.must("root", `getent passwd "$1" | cut -d: -f7`, testUser)
}

// status returns the value maisys status shows for key, such as
// "machine".
func (s *scene) status(key string) string {
	s.t.Helper()
	out := s.must(testUser, `"$1" status`, maisysPath)
	for line := range strings.Lines(out) {
		if value, ok := strings.CutPrefix(line, key+":"); ok {
			return strings.TrimSpace(value)
		}
	}
	s.t.Fatalf("maisys status shows no %s:\n%s", key, out)
	return ""
}

// wantTool checks that version is the tool's only version, that it's
// complete, and that ~/.local/bin/<name> runs target, a path in its
// folder.
func (s *scene) wantTool(name, version, target string) {
	s.t.Helper()
	dir := testHome + "/.local/share/maisys/tools/" + name
	if got := s.must("root", `ls -A "$1"`, dir); got != version {
		s.t.Errorf("~/.local/share/maisys/tools/%s has %s, want only %s", name, strings.ReplaceAll(got, "\n", ", "), version)
	}
	if !s.exists(dir + "/" + version + "/.maisys-complete") {
		s.t.Errorf("%s %s has no .maisys-complete marker", name, version)
	}
	if got, want := s.must("root", `readlink -f "$1"`, testHome+"/.local/bin/"+name), dir+"/"+version+"/"+target; got != want {
		s.t.Errorf("~/.local/bin/%s points at %s, want %s", name, got, want)
	}
}

// wantCommand checks that command, run as the test user from ~/.local/bin
// with args, prints want.
func (s *scene) wantCommand(command, want string, args ...string) {
	s.t.Helper()
	out := s.must(testUser, `exec "$@"`, append([]string{testHome + "/.local/bin/" + command}, args...)...)
	if !strings.Contains(out, want) {
		s.t.Errorf("%s %s printed %q, want %q in it", command, strings.Join(args, " "), out, want)
	}
}

// wantOutput checks that the output of what, such as "the dry run", says
// each of want.
func (s *scene) wantOutput(what, output string, want ...string) {
	s.t.Helper()
	for _, w := range want {
		if !strings.Contains(output, w) {
			s.t.Errorf("%s doesn't say %q", what, w)
		}
	}
}

// wantLines checks that the output of what has each of want as a line of
// its own, apart from indentation and a [sudo] tag.
func (s *scene) wantLines(what, output string, want ...string) {
	s.t.Helper()
	var lines []string
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		lines = append(lines, strings.TrimSpace(strings.TrimSuffix(line, "[sudo]")))
	}
	for _, w := range want {
		if !slices.Contains(lines, w) {
			s.t.Errorf("%s has no line %q", what, w)
		}
	}
}

// wantNoOutput checks that the output of what says none of unwanted.
func (s *scene) wantNoOutput(what, output string, unwanted ...string) {
	s.t.Helper()
	for _, u := range unwanted {
		if strings.Contains(output, u) {
			s.t.Errorf("%s says %q, but shouldn't", what, u)
		}
	}
}

// expand returns file with a leading "~" replaced by the test user's home
// folder.
func expand(file string) string {
	if rest, ok := strings.CutPrefix(file, "~"); ok {
		return testHome + rest
	}
	return file
}

// tilde returns file, in the test user's home folder, with the folder
// shown as ~.
func tilde(file string) string {
	if rest, ok := strings.CutPrefix(file, testHome); ok {
		return "~" + rest
	}
	return file
}

// title returns how maisys names an OS in messages, such as "Fedora".
func title(o config.OS) string {
	return strings.ToUpper(string(o[:1])) + string(o[1:])
}

// latestRelease returns the version of the latest release of the GitHub
// repo, without a leading "v", from where GitHub's releases/latest
// redirects to.
func latestRelease(ctx context.Context, repo string) (string, error) {
	client := &http.Client{
		Timeout:       time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	url := "https://github.com/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	_ = resp.Body.Close()
	location := resp.Header.Get("Location")
	tag, ok := strings.CutPrefix(path.Base(location), "v")
	if resp.StatusCode != http.StatusFound || !strings.Contains(location, "/releases/tag/") || !ok {
		return "", fmt.Errorf("%s answered %s, redirecting to %q, not to a release's tag", url, resp.Status, location)
	}
	return tag, nil
}

// TestScenarioConfigs checks that what the scenarios change the fixture
// to is valid for every test machine, so a mistake in one shows up
// without running them. The machine with the wrong OS and the one for the
// wrong architecture are valid too: only the live system tells they're
// wrong.
func TestScenarioConfigs(t *testing.T) {
	files, err := fixture.Files()
	if err != nil {
		t.Fatal(err)
	}
	extras := map[string]string{
		"bump":        bumpConfig,
		"latest":      latestConfig,
		"plugin":      pluginConfig,
		"login bash":  loginConfig("bash"),
		"login fish":  loginConfig("fish"),
		"take over":   takeOverConfig,
		"modules off": modulesOffConfig,
		"typo":        typoConfig,
		"interrupt":   interruptConfig,
		"other repo":  otherRepoConfig,
		"legacy":      legacyConfig,
		"opencode":    opencodeConfig,
		"steam on":    steamOnConfig,
		"steam off":   steamOffConfig,
	}
	for _, d := range fixture.Distros {
		t.Run(d.Name(), func(t *testing.T) {
			machine := config.MachineFile(d.Machine())
			check := func(what, name string, changed map[string]string) {
				t.Helper()
				fsys := fstest.MapFS{}
				for p, f := range files {
					fsys[p] = &fstest.MapFile{Data: []byte(f.Data), Mode: f.Mode}
				}
				for p, data := range changed {
					fsys[p] = &fstest.MapFile{Data: []byte(data), Mode: 0o644}
				}
				r := &config.Repo{FS: fsys, Modules: modules.All()}
				if _, err := r.Validate(name); err != nil {
					t.Errorf("%s isn't valid: %v", what, err)
				}
			}
			for what, extra := range extras {
				check("the "+what+" change", d.Machine(), map[string]string{machine: files[machine].Data + extra})
			}
			name, data := wrongOSMachine(d)
			check("the machine for the wrong OS", name, map[string]string{config.MachineFile(name): data})
			name, data = wrongArchMachine(d)
			check("the machine for the wrong architecture", name, map[string]string{config.MachineFile(name): data})
		})
	}
}
