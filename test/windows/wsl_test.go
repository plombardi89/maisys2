//go:build windows

// The Windows integration test syncs a Windows host with real WSL 2, as
// the windows-wsl job in .github/workflows/ci.yml runs it on a hosted
// windows-2025 runner, one job for each image (MAISYS_WSL_IMAGE, such as
// FedoraLinux-44). It needs maisys.exe and the release files of a snapshot
// of the same version (dist/ from make snapshot): install.ps1 installs the
// snapshot first, and the Linux maisys handed to new distros comes from
// dist/ through MAISYS_RELEASE_URL.
//
// TestWSLHost:
//
//  1. points WSL's list of distros (the DistributionListUrl registry value)
//     at a local list with just the image, kept in MAISYS_WSL_IMAGE_DIR
//     (the Actions cache), downloading it first if it isn't there;
//  2. builds the fixture with an extra host machine, ci-windows: the
//     distro of test-windows that uses the image, plus a distro called
//     "broken", whose Linux machine asks for a package that doesn't exist,
//     so its first sync fails; .wslconfig; windows_terminal, with its
//     settings file at MAISYS_TERMINAL_SETTINGS; winget; and files;
//  3. runs the first-run maisys sync of the fixture, which must fail
//     because of the broken distro only;
//  4. checks both distros: the user, with UID 1000 and a working password;
//     /etc/wsl.conf; that the temporary sudo rule is gone; systemd and the
//     default user; the Git Credential Manager from inside; the Linux
//     maisys's first sync; and that the image's own first-run command
//     doesn't ask anything;
//  5. checks .wslconfig, files, the Windows Terminal setting and winget's
//     package on Windows;
//  6. checks that maisys sync --dry-run exits with 0 on Windows, listing
//     the distros as existing, and inside the distro.
//
// It only runs when MAISYS_WINDOWS_E2E is set: it changes the machine it
// runs on, registering distros for good, and is meant for throwaway CI
// runners.
package windows_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows/registry"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	"github.com/plombardi89/maisys2/internal/modules"
	"github.com/plombardi89/maisys2/internal/modules/winget"
	"github.com/plombardi89/maisys2/internal/modules/wslhost"
	"github.com/plombardi89/maisys2/internal/resource/terminal"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
	"github.com/plombardi89/maisys2/test/fixture"

	"github.com/pelletier/go-toml/v2"
)

// The environment variables the test reads.
const (
	// e2eEnv turns the test on.
	e2eEnv = "MAISYS_WINDOWS_E2E"
	// imageEnv is the image to create distros from, and imageURLEnv and
	// imageSumEnv where WSL's own list downloads it from and its sha256.
	imageEnv    = "MAISYS_WSL_IMAGE"
	imageURLEnv = "MAISYS_WSL_IMAGE_URL"
	imageSumEnv = "MAISYS_WSL_IMAGE_SHA256"
	// imageDirEnv is the folder images are kept in between runs.
	imageDirEnv = "MAISYS_WSL_IMAGE_DIR"
)

// The host machine the test adds to the fixture, and its broken distro.
const (
	hostMachine   = "ci-windows"
	brokenDistro  = "broken"
	brokenMachine = "ci-broken"
)

// lxssKey is where WSL's settings are, in HKEY_LOCAL_MACHINE, and
// listURLValue the setting that replaces its list of distros.
const (
	lxssKey      = `SOFTWARE\Microsoft\Windows\CurrentVersion\Lxss`
	listURLValue = "DistributionListUrl"
)

// host is the Windows host under test.
type host struct {
	t *testing.T
	// image is the image the distros are created from.
	image string
	// maisys is maisys.exe, as install.ps1 installed it.
	maisys string
	// releaseURL is the file URL of the snapshot's release files.
	releaseURL string
	// work is a folder for the test's files, which distros can reach
	// through /mnt.
	work string
	// distro is the distro of test-windows that uses the image, machine
	// its Linux machine, and user its user.
	distro, machine, user string
	// linuxOS is the os of the distro's machine.
	linuxOS config.OS
}

func TestWSLHost(t *testing.T) {
	if os.Getenv(e2eEnv) == "" {
		t.Skipf("set %s to run the Windows integration test, which registers WSL distros on this machine for good", e2eEnv)
	}
	h := newHost(t)
	h.serveImage()
	repo, terminalSettings := h.buildFixture()

	// The first sync: everything but the broken distro works.
	out, code := h.sync(t, 45*time.Minute, "sync", repo, "--machine", hostMachine, "--yes")
	if code != 1 {
		t.Errorf("the first sync exited with %d, want 1, because the broken distro's first sync fails", code)
	}
	for _, want := range []string{
		fmt.Sprintf("+ create distro %s (%s), user %s", h.distro, h.image, h.user),
		fmt.Sprintf("+ set up %s with Linux machine %s", h.distro, h.machine),
		fmt.Sprintf("+ create distro %s (%s), user %s", brokenDistro, h.image, h.user),
		"+ write ~\\.wslconfig (run `wsl --shutdown` to apply)",
		"wsl_host failed: create distro " + brokenDistro,
		"the first maisys sync in " + brokenDistro + " failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the first sync's output doesn't have %q", want)
		}
	}
	if strings.Contains(out, "windows_terminal skipped") {
		t.Error("windows_terminal was skipped because the broken distro failed")
	}

	t.Cleanup(func() {
		if t.Failed() {
			h.dumpLogs()
		}
	})
	for _, name := range []string{h.distro, brokenDistro} {
		t.Run("distro "+name, func(t *testing.T) {
			h.checkDistro(t, name, password(t, out, name, h.user))
		})
	}
	t.Run("first-run setup", func(t *testing.T) { h.checkFirstRun(t) })
	t.Run("synced inside", func(t *testing.T) { h.checkSyncedInside(t) })
	t.Run("windows", func(t *testing.T) { h.checkWindows(t, terminalSettings) })

	// A second sync would change nothing, on Windows and inside the
	// distro.
	t.Run("dry runs", func(t *testing.T) {
		out, code := h.sync(t, 10*time.Minute, "sync", "--dry-run")
		if code != 0 {
			t.Errorf("maisys sync --dry-run exited with %d, want 0", code)
		}
		for _, name := range []string{h.distro, brokenDistro} {
			if want := fmt.Sprintf("= distro %s exists (synced from inside the distro)", name); !strings.Contains(out, want) {
				t.Errorf("maisys sync --dry-run doesn't say %q", want)
			}
		}
		out, code = h.wsl(t, 10*time.Minute, h.distro, h.user, "", "/home/"+h.user+"/.local/bin/maisys", "sync", "--dry-run", "--local")
		if code != 0 {
			t.Errorf("maisys sync --dry-run --local in %s exited with %d, want 0:\n%s", h.distro, code, out)
		}
	})
}

// newHost reads the test's environment.
func newHost(t *testing.T) *host {
	t.Helper()
	h := &host{t: t, image: os.Getenv(imageEnv)}
	if h.image == "" {
		t.Fatalf("%s isn't set", imageEnv)
	}
	h.maisys = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "maisys", "maisys.exe")
	if _, err := os.Stat(h.maisys); err != nil {
		t.Fatalf("maisys.exe isn't installed: %v", err)
	}
	dist, err := filepath.Abs(filepath.Join("..", "..", "dist"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, "checksums.txt")); err != nil {
		t.Fatalf("there's no snapshot in %s (make snapshot builds one): %v", dist, err)
	}
	h.releaseURL = fileURL(dist)
	// RUNNER_TEMP is a long path, which WSL can reach through /mnt; the
	// temporary folder of the runner's user is a short 8.3 one.
	h.work = filepath.Join(os.Getenv("RUNNER_TEMP"), "maisys-windows-e2e")
	if os.Getenv("RUNNER_TEMP") == "" {
		h.work = t.TempDir()
	}
	if err := os.MkdirAll(h.work, 0o755); err != nil {
		t.Fatal(err)
	}
	return h
}

// fileURL returns the file URL of the Windows path p.
func fileURL(p string) string {
	return "file:///" + filepath.ToSlash(p)
}

// serveImage points WSL's list of distros at a list of its own with just
// the image, downloading the image into the cache folder first if it isn't
// there.
func (h *host) serveImage() {
	t := h.t
	url, sum, dir := os.Getenv(imageURLEnv), strings.ToLower(strings.TrimPrefix(os.Getenv(imageSumEnv), "0x")), os.Getenv(imageDirEnv)
	if url == "" || len(sum) != 64 || dir == "" {
		t.Fatalf("%s, %s and %s must be set", imageURLEnv, imageSumEnv, imageDirEnv)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, h.image+"-"+sum[:16]+".wsl")
	if got, err := hashFile(image); err != nil || got != sum {
		t.Logf("downloading %s from %s", h.image, url)
		if err := download(t.Context(), url, image, sum); err != nil {
			t.Fatal(err)
		}
	}

	flavor, _, _ := strings.Cut(h.image, "-")
	list := map[string]any{"ModernDistributions": map[string]any{flavor: []any{map[string]any{
		"Name":         h.image,
		"FriendlyName": h.image + " (maisys integration test)",
		"Default":      true,
		"Amd64Url":     map[string]string{"Url": fileURL(image), "Sha256": "0x" + sum},
	}}}}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	listFile := filepath.Join(h.work, "distributions.json")
	if err := os.WriteFile(listFile, data, 0o644); err != nil {
		t.Fatal(err)
	}

	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, lxssKey, registry.SET_VALUE)
	if err != nil {
		t.Fatalf("opening HKLM\\%s (the test needs admin rights): %v", lxssKey, err)
	}
	defer func() { _ = k.Close() }()
	if err := k.SetStringValue(listURLValue, fileURL(listFile)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, lxssKey, registry.SET_VALUE)
		if err == nil {
			_ = k.DeleteValue(listURLValue)
			_ = k.Close()
		}
	})
	t.Logf("WSL's list of distros is %s", listFile)
}

// download downloads url to path, checking that its sha256 is sum.
func download(ctx context.Context, url, path, sum string) (err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), resp.Body); err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != sum {
		return fmt.Errorf("%s has sha256 %s, want %s", url, got, sum)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// buildFixture builds the fixture with the test's host machine, and
// returns its folder and the Windows Terminal settings file the host edits.
func (h *host) buildFixture() (repo, terminalSettings string) {
	t := h.t
	repo = filepath.Join(h.work, "fixture")
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Build(t.Context(), repo); err != nil {
		t.Fatal(err)
	}

	// The distro of test-windows that uses the image.
	m, err := (&config.Repo{FS: os.DirFS(repo), Modules: modules.All()}).LoadMachine(fixture.WindowsMachine)
	if err != nil {
		t.Fatal(err)
	}
	hostCfg, ok := m.Modules["wsl_host"].(*wslhost.Config)
	if !ok {
		t.Fatalf("wsl_host's config is a %T", m.Modules["wsl_host"])
	}
	for name, d := range hostCfg.Distros {
		if d.Image == h.image {
			h.distro, h.machine, h.user = name, d.Machine, d.User
		}
	}
	if h.distro == "" {
		t.Fatalf("%s has no distro with the image %s", fixture.WindowsMachine, h.image)
	}
	linux, err := (&config.Repo{FS: os.DirFS(repo), Modules: modules.All()}).LoadMachine(h.machine)
	if err != nil {
		t.Fatal(err)
	}
	h.linuxOS = linux.OS

	files := map[string]string{
		config.MachineFile(hostMachine): fmt.Sprintf(`# The Windows integration test's host: the %[1]s distro of
# test-windows, and a distro whose first sync fails.
os = "windows"
profiles = ["windows"]

[wsl_host]
enabled = true
default_distro = %[1]q

[wsl_host.wslconfig]
memory = "6GB"

[wsl_host.distros.%[1]s]
image = %[2]q
machine = %[3]q
user = %[4]q

[wsl_host.distros.%[5]s]
image = %[2]q
machine = %[6]q
user = %[4]q

[windows_terminal]
enabled = true

[winget]
enabled = true
install = ["jqlang.jq"]

[files]
enabled = true

[files.items.plain]
source = "files/e2e/plain.txt"
target = "~/maisys-e2e/plain.txt"
`, h.distro, h.image, h.machine, h.user, brokenDistro, brokenMachine),
		config.MachineFile(brokenMachine): fmt.Sprintf(`# A distro whose first sync fails: there's no such package.
os = %q

[packages]
enabled = true
install = ["maisys-no-such-package"]
`, h.linuxOS),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(name)), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git, err := gitrepo.Find()
	if err != nil {
		t.Fatal(err)
	}
	clone, err := gitrepo.Open(t.Context(), repo, gitrepo.Options{Git: git, Env: []string{
		"GIT_AUTHOR_NAME=maisys e2e", "GIT_AUTHOR_EMAIL=e2e@example.com",
		"GIT_COMMITTER_NAME=maisys e2e", "GIT_COMMITTER_EMAIL=e2e@example.com",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.CommitAll(t.Context(), "Add the Windows integration test's host"); err != nil {
		t.Fatal(err)
	}

	// Windows Terminal isn't installed on the runner, so the host edits a
	// settings file of its own, with comments that must be kept.
	terminalSettings = filepath.Join(h.work, "terminal-settings.json")
	const settings = `// Windows Terminal's settings, as the integration test writes them.
{
    "$schema": "https://aka.ms/terminal-profiles-schema",
    // Windows PowerShell.
    "defaultProfile": "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}",
    "profiles": { "list": [] },
}
`
	if err := os.WriteFile(terminalSettings, []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	return repo, terminalSettings
}

// sync runs maisys.exe with args, as the user would after installing it,
// pointed at the snapshot's release files and the test's Windows Terminal
// settings, showing its output as it runs. It returns the output and the
// exit code.
func (h *host) sync(t *testing.T, timeout time.Duration, args ...string) (string, int) {
	t.Helper()
	env := append(os.Environ(),
		wsl.ReleaseURLEnv+"="+h.releaseURL,
		terminal.SettingsEnv+"="+filepath.Join(h.work, "terminal-settings.json"),
	)
	return h.run(t, timeout, env, "", true, h.maisys, args...)
}

// wsl runs argv in distro as user (the default user if it's ""), with
// stdin as its input, and returns its output and exit code.
func (h *host) wsl(t *testing.T, timeout time.Duration, distro, user, stdin string, argv ...string) (string, int) {
	t.Helper()
	args := []string{"-d", distro}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(append(args, "--cd", "~", "--exec"), argv...)
	return h.run(t, timeout, append(os.Environ(), "WSL_UTF8=1"), stdin, false, wsl.Exe, args...)
}

// run runs name with args and env, giving it stdin, and returns its
// output and error output, and its exit code. With show, the output is
// shown as it runs, too.
func (h *host) run(t *testing.T, timeout time.Duration, env []string, stdin string, show bool, name string, args ...string) (string, int) {
	t.Helper()
	// Not t.Context(), which is done before cleanups, which show logs.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	if show {
		cmd.Stdout = io.MultiWriter(&out, os.Stdout)
	} else {
		cmd.Stdout = &out
	}
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return out.String(), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("running %s %s: %v", name, strings.Join(args, " "), err)
	}
	return out.String(), 0
}

// password returns the password maisys showed for user in distro.
func password(t *testing.T, out, distro, user string) string {
	t.Helper()
	m := regexp.MustCompile(fmt.Sprintf(`The password of %s in %s is: (\S+)`, regexp.QuoteMeta(user), regexp.QuoteMeta(distro))).FindAllStringSubmatch(out, -1)
	if len(m) != 1 {
		t.Fatalf("the first sync showed the password of %s in %s %d times, want once", user, distro, len(m))
	}
	return m[0][1]
}

// checkDistro checks what setting up distro did, with password the
// password maisys showed.
func (h *host) checkDistro(t *testing.T, distro, password string) {
	t.Helper()
	const minute = time.Minute
	user := h.user
	expect := func(what string, ok bool, out string) {
		t.Helper()
		if !ok {
			t.Errorf("%s:\n%s", what, out)
		}
	}

	out, _ := h.wsl(t, minute, distro, "root", "", "getent", "passwd", "1000")
	expect("UID 1000 isn't "+user, strings.HasPrefix(out, user+":"), out)
	out, _ = h.wsl(t, minute, distro, "root", "", "getent", "shadow", user)
	fields := strings.Split(out, ":")
	expect(user+" has no password", len(fields) > 1 && strings.HasPrefix(fields[1], "$"), out)
	out, code := h.wsl(t, minute, distro, user, "", "sudo", "-n", "true")
	expect(user+" can still use sudo without a password: the temporary rule wasn't deleted", code != 0, out)
	out, code = h.wsl(t, minute, distro, user, password+"\n", "sudo", "-S", "-k", "-p", "", "true")
	expect(user+" can't use sudo with the password maisys showed", code == 0, out)
	out, code = h.wsl(t, minute, distro, "root", "", "test", "!", "-e", wsl.SetupRule)
	expect(wsl.SetupRule+" is still there", code == 0, out)

	out, _ = h.wsl(t, minute, distro, "root", "", "cat", "/etc/wsl.conf")
	expect("/etc/wsl.conf doesn't turn systemd on", strings.Contains(out, "systemd=true"), out)
	expect("/etc/wsl.conf doesn't make "+user+" the default user", strings.Contains(out, "default="+user), out)
	out, _ = h.wsl(t, minute, distro, "", "", "id", "-un")
	expect("the default user isn't "+user, strings.TrimSpace(out) == user, out)
	out, _ = h.wsl(t, minute, distro, "root", "", "cat", "/proc/1/comm")
	expect("systemd isn't running", strings.TrimSpace(out) == "systemd", out)
	out, _ = h.wsl(t, 2*minute, distro, "root", "", "systemctl", "is-system-running", "--wait")
	t.Logf("systemctl is-system-running in %s: %s", distro, strings.TrimSpace(out))

	// The first sync inside the distro ran, as the user, and set
	// maisys up for the Linux machine.
	machine := h.machine
	if distro == brokenDistro {
		machine = brokenMachine
	}
	out, _ = h.wsl(t, minute, distro, user, "", "cat", "/home/"+user+"/.config/maisys/maisys.toml")
	var saved struct {
		Machine string `toml:"machine"`
	}
	_ = toml.Unmarshal([]byte(out), &saved)
	expect("maisys isn't set up inside for "+machine, saved.Machine == machine, out)
}

// checkFirstRun runs the image's own first-run command, which WSL runs the
// first time someone opens the distro, and checks that it finds the user
// maisys created and asks nothing.
func (h *host) checkFirstRun(t *testing.T) {
	t.Helper()
	conf, code := h.wsl(t, time.Minute, h.distro, "root", "", "cat", "/etc/wsl-distribution.conf")
	if code != 0 {
		t.Skipf("%s has no /etc/wsl-distribution.conf: %s", h.image, conf)
	}
	command := oobeCommand(conf)
	if command == "" {
		t.Skipf("%s has no first-run command:\n%s", h.image, conf)
	}
	// Its input is empty, so a question it asks fails it.
	out, code := h.wsl(t, 5*time.Minute, h.distro, "root", "", "timeout", "120", "sh", "-c", command)
	t.Logf("%s:\n%s", command, out)
	if code != 0 {
		t.Errorf("the first-run command %s exited with %d", command, code)
	}
	if strings.Contains(strings.ToLower(out), "username") {
		t.Errorf("the first-run command %s asked for a user name", command)
	}
}

// oobeCommand returns the command of the [oobe] section of a
// wsl-distribution.conf.
func oobeCommand(conf string) string {
	section := ""
	for line := range strings.Lines(conf) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && section == "oobe" && strings.EqualFold(strings.TrimSpace(key), "command") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// checkSyncedInside checks what the Linux maisys's first sync did in the
// distro that works: git signs in with the Windows Git Credential Manager,
// which runs from inside.
func (h *host) checkSyncedInside(t *testing.T) {
	t.Helper()
	user := h.user
	out, code := h.wsl(t, time.Minute, h.distro, user, "", "git", "config", "--global", "--get", "credential.helper")
	if code != 0 || !strings.Contains(out, "git-credential-manager.exe") {
		t.Errorf("git's credential.helper isn't the Windows Git Credential Manager:\n%s", out)
	}
	out, code = h.wsl(t, 2*time.Minute, h.distro, user, "", "sh", "-c", `eval "$(git config --global --get credential.helper) --version"`)
	if code != 0 {
		t.Errorf("the Git Credential Manager doesn't run from inside %s:\n%s", h.distro, out)
	}
	out, code = h.wsl(t, time.Minute, h.distro, user, "", "test", "-f", "/home/"+user+"/.local/state/maisys/state.json")
	if code != 0 {
		t.Errorf("the first sync inside %s left no state file:\n%s", h.distro, out)
	}
}

// checkWindows checks what the sync did on Windows.
func (h *host) checkWindows(t *testing.T, terminalSettings string) {
	t.Helper()
	home := os.Getenv("USERPROFILE")
	data, err := os.ReadFile(filepath.Join(home, ".wslconfig"))
	if err != nil || !strings.Contains(string(data), "[wsl2]\r\nmemory=6GB\r\n") {
		t.Errorf(".wslconfig = %q, %v; want memory=6GB under [wsl2], with Windows line endings", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, "maisys-e2e", "plain.txt")); err != nil {
		t.Errorf("files didn't copy plain.txt: %v", err)
	}

	distros, err := wsl.Live().Distros()
	if err != nil {
		t.Fatal(err)
	}
	d, ok := wsl.Find(distros, h.distro)
	if !ok {
		t.Fatalf("%s isn't registered", h.distro)
	}
	data, err = os.ReadFile(terminalSettings)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := terminal.DefaultOf(data); err != nil || got != terminal.ProfileGUID(d) {
		t.Errorf("Windows Terminal's default profile = %q, %v; want %s's, %s", got, err, h.distro, terminal.ProfileGUID(d))
	}
	if !strings.Contains(string(data), "// Windows PowerShell.") {
		t.Errorf("editing Windows Terminal's settings lost a comment:\n%s", data)
	}
	// The profile GUID maisys works out for a distro WSL hasn't written a
	// fragment for yet is the one WSL writes.
	if d.Modern && d.TerminalProfile != "" && terminal.ProfileGUID(d) != terminal.ModernGUID(d.ID) {
		t.Errorf("WSL's fragment %s gives %s the profile %s, but maisys works out %s", d.TerminalProfile, h.distro, terminal.ProfileGUID(d), terminal.ModernGUID(d.ID))
	}
	t.Logf("%s: ID %s, modern %t, fragment %q", h.distro, d.ID, d.Modern, d.TerminalProfile)
	defaultID, err := wsl.Live().DefaultDistro()
	if err != nil || !strings.EqualFold(defaultID, d.ID) {
		t.Errorf("WSL's default distro is %s, %v; want %s (%s)", defaultID, err, h.distro, d.ID)
	}

	// The winget module installs jq once it has resources.
	if _, ok := any(winget.Module{}).(engine.Module); !ok {
		t.Log("the winget module has no resources yet, so jqlang.jq isn't checked")
		return
	}
	out, code := h.run(t, 2*time.Minute, os.Environ(), "", false, "winget", "list", "--id", "jqlang.jq", "--exact", "--accept-source-agreements", "--disable-interactivity")
	if code != 0 {
		t.Errorf("winget didn't install jqlang.jq:\n%s", out)
	}
}

// dumpLogs shows the logs of the last sync inside each distro.
func (h *host) dumpLogs() {
	for _, distro := range []string{h.distro, brokenDistro} {
		out, _ := h.wsl(h.t, time.Minute, distro, "root", "", "sh", "-c", `tail -n 200 "$(ls -t /home/*/.local/state/maisys/logs/* | head -n 1)"`)
		h.t.Logf("the last maisys log in %s:\n%s", distro, out)
	}
}
