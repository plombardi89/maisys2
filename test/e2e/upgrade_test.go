package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"

	"github.com/plombardi89/maisys2/internal/buildinfo"
	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/selfupdate"
	"github.com/plombardi89/maisys2/test/fixture"
)

// Where the upgrade test puts the draft release's files in the machine,
// for self-update to find through MAISYS_RELEASE_URL.
const (
	upgradeDraftDir = sharedDir + "/draft"
	upgradeDraftURL = "file://" + upgradeDraftDir
)

// TestUpgrade is the release workflow's upgrade test. For each distro, on a
// fresh machine, it:
//
//  1. installs the previous release, from MAISYS_UPGRADE_PREVIOUS: the URL
//     of its files, installed with its own install.sh (by default the
//     latest published release, which is never a draft), or a maisys
//     binary, installed as it is;
//  2. syncs the fixture with it, as TestSync's first sync does. The fixture
//     is the one the previous release was tested with, built from its tag,
//     so it's a config written for that release; it's this commit's if git
//     doesn't have the tag or the tag has no fixture. A release that can't
//     sync yet (v0.1.0, whose sync isn't implemented) skips this step, and
//     the test says so;
//  3. runs maisys sync --yes with MAISYS_RELEASE_URL pointing at the draft
//     release's files, from the folder MAISYS_UPGRADE_DRAFT (from gh release
//     download, or GoReleaser's dist): maisys must update itself to the
//     draft, which then syncs the machine (or sets it up, if step 2 was
//     skipped);
//  4. checks that maisys sync --dry-run --local exits with 0, so the draft
//     finds nothing more to change, and that maisys status works.
//
// It only runs when MAISYS_UPGRADE_DRAFT is set, on the distros MAISYS_E2E
// names, as for TestSync: make upgrade-test sets both.
func TestUpgrade(t *testing.T) {
	dir := os.Getenv("MAISYS_UPGRADE_DRAFT")
	if dir == "" {
		t.Skip("set MAISYS_UPGRADE_DRAFT to a folder of the draft release's files, or run make upgrade-test")
	}
	distros := selectedDistros(t)
	ctx := testContext(t)
	draft := upgradeDraft(ctx, t, dir)
	previous := upgradePrevious(t)
	t.Logf("upgrading from %s to the draft, %s", previous, draft.Version)

	for _, d := range distros {
		t.Run(d.Name(), func(t *testing.T) {
			ctx := testContext(t)
			m := &upgradeMachine{machine: startMachine(ctx, t, d), distro: d}
			m.copyDraft(ctx, dir, draft)
			m.installPrevious(ctx, previous)
			version := m.maisysVersion(ctx)
			if semver.Compare("v"+version, "v"+draft.Version) >= 0 {
				t.Fatalf("the previous release, %s, isn't older than the draft, %s, so it wouldn't update itself", version, draft.Version)
			}
			m.copyFixture(ctx, upgradeFixture(ctx, t, d, version))

			setup := []string{maisysPath, "sync", fixtureURL, "--machine", d.Machine(), "--yes"}
			out, code := m.runOutput(ctx, "sync with maisys "+version, testUser, nil, append(setup, "--local")...)
			synced := code == 0
			switch {
			case synced:
				m.deleteSetupRule(ctx)
			case strings.Contains(out, "not implemented"):
				upgradeNotice(t, "maisys %s can't sync: its sync isn't implemented yet. Skipped syncing the fixture with it; "+
					"checking that it updates itself to the draft, %s, which then sets the machine up.", version, draft.Version)
			default:
				t.Fatalf("maisys %s's sync exited with %d", version, code)
			}

			// A machine that has synced runs plain maisys sync. One that
			// hasn't runs its first sync again, which the draft carries on
			// with once maisys has updated itself.
			update := []string{maisysPath, "sync", "--yes"}
			if !synced {
				update = setup
			}
			code = m.run(ctx, "update to the draft, and sync", testUser, []string{"MAISYS_RELEASE_URL=" + upgradeDraftURL}, update...)
			if got := m.maisysVersion(ctx); got != draft.Version {
				t.Fatalf("maisys sync didn't update maisys %s to the draft: maisys --version says %s, want %s", version, got, draft.Version)
			}
			if code != 0 {
				t.Fatalf("the draft's sync exited with %d", code)
			}
			if !synced {
				m.deleteSetupRule(ctx)
			}

			if code := m.run(ctx, "dry run", testUser, nil, maisysPath, "sync", "--dry-run", "--local"); code != 0 {
				t.Errorf("maisys sync --dry-run exited with %d after the draft's sync, want 0: another sync would change something", code)
			}
			if code := m.run(ctx, "status", testUser, nil, maisysPath, "status"); code != 0 {
				t.Errorf("maisys status exited with %d", code)
			}
		})
	}
}

// upgradeDraft returns the draft release's archive for Linux on this
// machine's architecture, which the containers have too, from the
// checksums.txt in dir.
func upgradeDraft(ctx context.Context, t *testing.T, dir string) selfupdate.Release {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	u := &selfupdate.Updater{
		ReleaseURL: (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}).String(),
		OS:         "linux",
		Arch:       runtime.GOARCH,
	}
	rel, err := u.Latest(ctx)
	if err != nil {
		t.Fatalf("the draft release in %s: %v", dir, err)
	}
	if _, err := os.Stat(filepath.Join(abs, rel.Archive)); err != nil {
		t.Fatalf("the draft release in %s: %v", dir, err)
	}
	return rel
}

// upgradePrevious returns the previous release, from
// MAISYS_UPGRADE_PREVIOUS: the http or https URL of its files, or a maisys
// binary. It's the latest published release of the repo maisys updates
// from by default.
func upgradePrevious(t *testing.T) string {
	t.Helper()
	previous := os.Getenv("MAISYS_UPGRADE_PREVIOUS")
	switch {
	case previous == "":
		return selfupdate.DefaultReleaseURL(buildinfo.UpdateRepo())
	case strings.HasPrefix(previous, "https://"), strings.HasPrefix(previous, "http://"):
		return strings.TrimSuffix(previous, "/")
	}
	abs, err := filepath.Abs(previous)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(abs); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("MAISYS_UPGRADE_PREVIOUS is %q: it must be the URL of a release's files (http or https), or a maisys binary", previous)
	}
	return abs
}

// upgradeFixture builds the fixture that the previous release, version, was
// tested with, from its tag, which git has if it fetched the tags. If git
// doesn't have the tag, or the tag has no fixture, it builds this commit's.
func upgradeFixture(ctx context.Context, t *testing.T, d fixture.Distro, version string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "fixture")
	tag := "v" + version
	root := filepath.Join("..", "..")
	if err := exec.CommandContext(ctx, "git", "-C", root, "cat-file", "-e", tag+":test/cmd/fixture/main.go").Run(); err != nil {
		t.Logf("syncing this commit's fixture: git doesn't have the tag %s, or it has no fixture", tag)
		if err := fixture.Build(ctx, repo); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		return repo
	}

	t.Logf("syncing the fixture from %s", tag)
	src := t.TempDir()
	archive := filepath.Join(t.TempDir(), "source.tar")
	steps := []*exec.Cmd{
		exec.CommandContext(ctx, "git", "-C", root, "archive", "--format=tar", "--output="+archive, tag),
		exec.CommandContext(ctx, "tar", "-xf", archive, "-C", src),
		exec.CommandContext(ctx, "go", "run", "./test/cmd/fixture", repo),
	}
	steps[2].Dir = src
	steps[2].Env = append(os.Environ(), "GOWORK=off")
	for _, cmd := range steps {
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s's fixture: %s: %v\n%s", tag, strings.Join(cmd.Args, " "), err, out)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, filepath.FromSlash(config.MachineFile(d.Machine())))); err != nil {
		t.Fatalf("%s's fixture has no machine %s, so it can't be the previous release on %s: %v", tag, d.Machine(), d.Name(), err)
	}
	return repo
}

// upgradeNotice reports a step the upgrade test skipped, in its log, and
// as a notice on the workflow run in GitHub Actions.
func upgradeNotice(t *testing.T, format string, a ...any) {
	t.Helper()
	msg := fmt.Sprintf(format, a...)
	t.Log(msg)
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		fmt.Printf("::notice title=Upgrade test (%s)::%s\n", strings.ReplaceAll(t.Name(), ",", ""), msg)
	}
}

// upgradeMachine is a test machine with the upgrade test's steps.
type upgradeMachine struct {
	*machine
	distro fixture.Distro
}

// mustPodman runs podman commands, and fails the test if one fails.
func (m *upgradeMachine) mustPodman(ctx context.Context, commands ...[]string) {
	m.t.Helper()
	for _, args := range commands {
		if code, err := podman(ctx, m.t.Output(), args...); err != nil || code != 0 {
			m.t.Fatalf("podman %s exited with %d: %v", strings.Join(args, " "), code, err)
		}
	}
}

// copyTo copies src to dst in the machine, owned by the test user. dirs
// are the folders to make for it first, also owned by the test user.
func (m *upgradeMachine) copyTo(ctx context.Context, src, dst string, dirs ...string) {
	m.t.Helper()
	if len(dirs) > 0 {
		m.mustPodman(ctx, append([]string{"exec", m.name, "install", "-d", "-o", testUser, "-g", testUser}, dirs...))
	}
	m.mustPodman(ctx,
		[]string{"cp", src, m.name + ":" + dst},
		[]string{"exec", m.name, "chown", "-R", testUser + ":" + testUser, dst},
	)
}

// copyDraft copies the draft release's files that self-update needs, from
// dir, to upgradeDraftDir.
func (m *upgradeMachine) copyDraft(ctx context.Context, dir string, draft selfupdate.Release) {
	m.t.Helper()
	logStep(m.t, "copy in the draft release, "+draft.Version)
	m.copyTo(ctx, filepath.Join(dir, "checksums.txt"), upgradeDraftDir+"/checksums.txt", sharedDir, upgradeDraftDir)
	m.copyTo(ctx, filepath.Join(dir, draft.Archive), upgradeDraftDir+"/"+draft.Archive)
}

// copyFixture copies the fixture's repo in, where TestSync has it.
func (m *upgradeMachine) copyFixture(ctx context.Context, repo string) {
	m.t.Helper()
	logStep(m.t, "copy in the fixture")
	m.copyTo(ctx, repo, strings.TrimPrefix(fixtureURL, "file://"), sharedDir)
}

// installPrevious installs the previous release as the test user: from
// the URL of its files with its own install.sh, piped into bash as its
// instructions say, or the maisys binary it is.
func (m *upgradeMachine) installPrevious(ctx context.Context, previous string) {
	m.t.Helper()
	if !strings.Contains(previous, "://") {
		logStep(m.t, "copy in the previous maisys, "+previous)
		m.copyTo(ctx, previous, maisysPath, testHome+"/.local", path.Dir(maisysPath))
		m.mustPodman(ctx, []string{"exec", m.name, "chmod", "0755", maisysPath})
		return
	}

	m.installCurl(ctx)
	install := `set -o pipefail; curl -fsSL "$1/install.sh" | MAISYS_RELEASE_URL="$1" bash`
	if code := m.run(ctx, "install the previous release", testUser, nil, "bash", "-c", install, "bash", previous); code != 0 {
		m.t.Fatalf("installing the previous release from %s exited with %d", previous, code)
	}
}

// installCurl installs curl, which install.sh and its instructions need,
// unless the machine has it: Ubuntu's images don't. The package lists are
// deleted again, as the image has none.
func (m *upgradeMachine) installCurl(ctx context.Context) {
	m.t.Helper()
	install := map[config.OS]string{
		config.Fedora: "dnf -y install --setopt=install_weak_deps=False curl",
		config.Ubuntu: "apt-get -qq update && DEBIAN_FRONTEND=noninteractive apt-get -qq -y install --no-install-recommends curl && rm -rf /var/lib/apt/lists/*",
	}[m.distro.OS]
	script := "command -v curl >/dev/null || { " + install + "; }"
	if code := m.run(ctx, "install curl", "root", nil, "sh", "-c", script); code != 0 {
		m.t.Fatalf("installing curl exited with %d", code)
	}
}

// maisysVersion returns the version of the test user's maisys.
func (m *upgradeMachine) maisysVersion(ctx context.Context) string {
	m.t.Helper()
	out, code := m.runOutput(ctx, "version", testUser, nil, maisysPath, "--version")
	fields := strings.Fields(out)
	if code != 0 || len(fields) < 2 || fields[0] != "maisys" {
		m.t.Fatalf("maisys --version exited with %d, and said %q", code, out)
	}
	return fields[1]
}

// deleteSetupRule deletes the image's temporary sudo rule once maisys has
// written its own, as TestSync does.
func (m *upgradeMachine) deleteSetupRule(ctx context.Context) {
	m.t.Helper()
	if code := m.run(ctx, "delete the image's sudo rule", "root", nil, "rm", "/etc/sudoers.d/maisys-setup"); code != 0 {
		m.t.Fatalf("deleting the image's sudo rule failed with %d", code)
	}
}

// runOutput is run, but also returns the command's output.
func (m *upgradeMachine) runOutput(ctx context.Context, step, user string, env []string, command ...string) (string, int) {
	m.t.Helper()
	args := []string{"exec", "--user", user}
	if user == testUser {
		args = append(args, "--workdir", testHome, m.name, "env", "-i")
		args = append(args, userEnv...)
		args = append(args, env...)
	} else {
		args = append(args, m.name)
	}
	args = append(args, command...)

	logStep(m.t, step+": "+strings.Join(command, " "))
	start := time.Now()
	var out bytes.Buffer
	code, err := podman(ctx, io.MultiWriter(m.t.Output(), &out), args...)
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Logf("%s: exit code %d after %s", step, code, time.Since(start).Round(time.Second))
	return out.String(), code
}

// TestUpgradeMatchesCI checks that the release workflow's upgrade job runs
// on distros in fixture.Distros, and that the release is only published
// once it passes.
func TestUpgradeMatchesCI(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	job, err := section(string(data), "  upgrade:\n")
	if err != nil {
		t.Fatal(err)
	}
	matrix := regexp.MustCompile(`(?m)^\s*distro: \[(.*)\]\s*$`).FindStringSubmatch(job)
	if matrix == nil {
		t.Fatal("can't find the distros in release.yml's upgrade matrix")
	}
	var names []string
	for _, d := range fixture.Distros {
		names = append(names, d.Name())
	}
	for distro := range strings.SplitSeq(matrix[1], ",") {
		if distro = strings.Trim(strings.TrimSpace(distro), `"`); !slices.Contains(names, distro) {
			t.Errorf("release.yml's upgrade job runs on %s, which isn't one of fixture.Distros: %s", distro, strings.Join(names, ", "))
		}
	}

	publish, err := section(string(data), "  publish:\n")
	if err != nil {
		t.Fatal(err)
	}
	needs := regexp.MustCompile(`(?m)^\s*needs: \[(.*)\]\s*$`).FindStringSubmatch(publish)
	if needs == nil || !slices.Contains(strings.Split(strings.ReplaceAll(needs[1], " ", ""), ","), "upgrade") {
		t.Error("release.yml's publish job doesn't wait for the upgrade job")
	}
}
