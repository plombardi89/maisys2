// Package e2e_test holds the end-to-end tests: real syncs of the fixture's
// test machines (see test/fixture) in systemd containers with podman, one
// for each distro in fixture.Distros.
//
// For each distro, TestSync builds an image of a fresh machine from
// <os>.containerfile, starts it with systemd, copies in maisys (built for
// the machine's architecture, the same as this one's) and the fixture, then
// as the test user:
//
//  1. runs maisys sync file:///opt/maisys-e2e/fixture --machine
//     test-<distro> --yes --local, after which the image's temporary sudo
//     rule is deleted, so sudo works through the rule maisys wrote;
//  2. checks that maisys sync --dry-run --local exits with 0, so a second
//     sync would change nothing;
//  3. runs test/verify.sh, which checks what the sync did.
//
// The tests only run when MAISYS_E2E names the distros to test, separated
// by commas, or is "all": make e2e sets it, to DISTRO if that's given. They
// need podman and a network connection. PODMAN sets the podman command, such
// as "sudo podman" for rootful podman, as CI uses. The images are kept, as
// localhost/maisys-e2e:<distro>, so later runs build them from the cache.
// Containers are removed, but with MAISYS_E2E_KEEP=1, a test that fails
// keeps its container to look at.
package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/test/fixture"
)

// The test user, and where the tests put what they copy in.
const (
	testUser   = "tester"
	testHome   = "/home/" + testUser
	maisysPath = testHome + "/.local/bin/maisys"
	sharedDir  = "/opt/maisys-e2e"
	fixtureURL = "file://" + sharedDir + "/fixture"
	verifyPath = sharedDir + "/verify.sh"
)

// userEnv is the environment of the test user's commands, as logging in
// sets it.
var userEnv = []string{
	"HOME=" + testHome,
	"USER=" + testUser,
	"LOGNAME=" + testUser,
	"SHELL=/bin/bash",
	"PATH=/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin",
	"LANG=C.UTF-8",
	"TERM=xterm",
	"NO_COLOR=1",
}

func TestSync(t *testing.T) {
	distros := selectedDistros(t)
	ctx := testContext(t)
	maisys := buildMaisys(ctx, t)
	repo := t.TempDir()
	if err := fixture.Build(ctx, repo); err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	for _, d := range distros {
		t.Run(d.Name(), func(t *testing.T) {
			ctx := testContext(t)
			c := startMachine(ctx, t, d)
			c.copyIn(ctx, maisys, repo)

			sync := []string{maisysPath, "sync", fixtureURL, "--machine", d.Machine(), "--yes", "--local"}
			if code := c.run(ctx, "sync", testUser, nil, sync...); code != 0 {
				t.Fatalf("maisys sync exited with %d", code)
			}
			if code := c.run(ctx, "delete the image's sudo rule", "root", nil, "rm", "/etc/sudoers.d/maisys-setup"); code != 0 {
				t.Fatalf("deleting the image's sudo rule failed with %d", code)
			}

			dryRun := []string{maisysPath, "sync", "--dry-run", "--local"}
			if code := c.run(ctx, "dry run", testUser, nil, dryRun...); code != 0 {
				t.Errorf("maisys sync --dry-run exited with %d after a sync, want 0: a second sync would change something", code)
			}

			if code := c.run(ctx, "verify", testUser, expectations(d), "bash", verifyPath); code != 0 {
				t.Errorf("verify.sh exited with %d", code)
			}
		})
	}
}

// expectations are verify.sh's EXPECT_* variables for d's test machine.
func expectations(d fixture.Distro) []string {
	return []string{
		"EXPECT_MACHINE=" + d.Machine(),
		"EXPECT_CONFIG_URL=" + fixtureURL,
		"EXPECT_PACKAGES=" + strings.Join(fixture.Packages, " "),
		"EXPECT_GO_VERSION=" + fixture.GoVersion,
		"EXPECT_GO_PROGRAM=" + fixture.GoProgram,
		"EXPECT_GO_PROGRAM_VERSION=" + fixture.GoProgramVersion,
		"EXPECT_KUBECTL_VERSION=" + fixture.KubectlVersion,
		"EXPECT_YQ_VERSION=" + fixture.YqVersion,
		"EXPECT_PYTHON_TOOL=" + fixture.PythonTool,
		"EXPECT_OH_MY_ZSH_COMMIT=" + fixture.OhMyZshRef,
		"EXPECT_ZSH_PLUGIN=" + fixture.ZshPlugin,
		"EXPECT_ZSH_PLUGIN_COMMIT=" + fixture.ZshPluginCommit,
		"EXPECT_OWNER=" + fixture.Owner,
	}
}

// selectedDistros returns the distros MAISYS_E2E names, and skips the test
// if it's not set.
func selectedDistros(t *testing.T) []fixture.Distro {
	t.Helper()
	names := os.Getenv("MAISYS_E2E")
	if names == "" {
		t.Skip("set MAISYS_E2E to the distros to test, or run make e2e")
	}
	if names == "all" {
		return fixture.Distros
	}
	var all []string
	for _, d := range fixture.Distros {
		all = append(all, d.Name())
	}
	var selected []fixture.Distro
	for name := range strings.SplitSeq(names, ",") {
		i := slices.Index(all, strings.TrimSpace(name))
		if i < 0 {
			t.Fatalf("MAISYS_E2E names %q, which isn't one of the distros: %s, or all", name, strings.Join(all, ", "))
		}
		if !slices.Contains(selected, fixture.Distros[i]) {
			selected = append(selected, fixture.Distros[i])
		}
	}
	return selected
}

// testContext returns a context that ends a minute before the test times
// out, so the test can still clean up after a command that hangs.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx := t.Context()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		t.Cleanup(cancel)
	}
	return ctx
}

// buildMaisys builds maisys for Linux on this machine's architecture, which
// the containers have too, and returns its path.
func buildMaisys(ctx context.Context, t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "maisys")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", bin, "./cmd/maisys")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building maisys: %v\n%s", err, out)
	}
	return bin
}

// podmanCommand returns the podman command: PODMAN, split into words, or
// podman.
func podmanCommand() []string {
	if words := strings.Fields(os.Getenv("PODMAN")); len(words) > 0 {
		return words
	}
	return []string{"podman"}
}

// podman runs podman with args, and returns its exit code. Its output goes
// to out. An error means podman didn't run or didn't finish.
func podman(ctx context.Context, out io.Writer, args ...string) (int, error) {
	words := podmanCommand()
	cmd := exec.CommandContext(ctx, words[0], append(words[1:], args...)...)
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.Exited() && ctx.Err() == nil {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("%s %s: %w", strings.Join(words, " "), strings.Join(args, " "), err)
	}
	return 0, nil
}

// machine is a test machine: a running container.
type machine struct {
	t    *testing.T
	name string
}

// startMachine builds the image of a fresh d machine, starts a container
// from it and waits for systemd to finish starting. The container is
// removed when the test ends, after its logs and maisys's are shown if the
// test failed.
func startMachine(ctx context.Context, t *testing.T, d fixture.Distro) *machine {
	t.Helper()
	image := "localhost/maisys-e2e:" + d.Name()
	build := []string{
		"build",
		"--tag", image,
		"--build-arg", "VERSION=" + d.Version,
		"--file", string(d.OS) + ".containerfile",
		t.TempDir(), // the build needs nothing from the context
	}
	logStep(t, "build the image")
	if code, err := podman(ctx, t.Output(), build...); err != nil || code != 0 {
		t.Fatalf("building the image exited with %d: %v", code, err)
	}

	m := &machine{t: t, name: fmt.Sprintf("maisys-e2e-%s-%s", d.Name(), strconv.FormatInt(time.Now().UnixNano(), 36))}
	logStep(t, "start "+m.name)
	t.Cleanup(m.remove)
	t.Cleanup(m.showLogs)
	run := []string{"run", "--detach", "--name", m.name, "--hostname", d.Machine(), "--systemd=always", image}
	if code, err := podman(ctx, t.Output(), run...); err != nil || code != 0 {
		t.Fatalf("starting the container exited with %d: %v", code, err)
	}
	m.waitForSystemd(ctx)
	return m
}

// waitForSystemd waits until systemd has started everything it starts at
// boot. A unit that failed is reported, but doesn't fail the test.
func (m *machine) waitForSystemd(ctx context.Context) {
	m.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var state string
	for ctx.Err() == nil {
		var out bytes.Buffer
		_, err := podman(ctx, &out, "exec", m.name, "systemctl", "is-system-running", "--wait")
		if err != nil {
			break
		}
		state = strings.TrimSpace(out.String())
		switch state {
		case "running":
			return
		case "degraded":
			m.t.Logf("systemd started, but some units failed:")
			_, _ = podman(ctx, m.t.Output(), "exec", m.name, "systemctl", "--failed", "--no-pager")
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	m.t.Fatalf("systemd didn't finish starting: %s", state)
}

// copyIn copies maisys to the test user's ~/.local/bin, where install.sh
// installs it, and the fixture and verify.sh to sharedDir, all owned by the
// test user.
func (m *machine) copyIn(ctx context.Context, maisys, repo string) {
	m.t.Helper()
	logStep(m.t, "copy in maisys, the fixture and verify.sh")
	steps := [][]string{
		{"exec", m.name, "install", "-d", "-o", testUser, "-g", testUser, testHome + "/.local", filepath.Dir(maisysPath), sharedDir},
		{"cp", maisys, m.name + ":" + maisysPath},
		{"cp", repo, m.name + ":" + sharedDir + "/fixture"},
		{"cp", filepath.Join("..", "verify.sh"), m.name + ":" + verifyPath},
		{"exec", m.name, "chown", "-R", testUser + ":" + testUser, maisysPath, sharedDir},
		{"exec", m.name, "chmod", "0755", maisysPath, verifyPath},
	}
	for _, args := range steps {
		if code, err := podman(ctx, m.t.Output(), args...); err != nil || code != 0 {
			m.t.Fatalf("podman %s exited with %d: %v", strings.Join(args, " "), code, err)
		}
	}
}

// run runs a command in the machine as user, root or the test user, and
// returns its exit code. The test user's commands run from their home
// folder, in a clean environment like a login's, plus env. Its output goes
// to the test's.
func (m *machine) run(ctx context.Context, step, user string, env []string, command ...string) int {
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
	code, err := podman(ctx, m.t.Output(), args...)
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Logf("%s: exit code %d after %s", step, code, time.Since(start).Round(time.Second))
	return code
}

// showLogs shows the container's own output, the warnings and errors in
// its journal, and every log maisys wrote, if the test failed.
func (m *machine) showLogs() {
	if !m.t.Failed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	logStep(m.t, "the container's output")
	_, _ = podman(ctx, m.t.Output(), "logs", m.name)
	logStep(m.t, "the journal's warnings and errors")
	_, _ = podman(ctx, m.t.Output(), "exec", m.name, "journalctl", "--no-pager", "--boot", "--priority=warning")
	logStep(m.t, "maisys's logs")
	script := `for f in "$1"/*.log; do [ -e "$f" ] && printf '\n==> %s\n' "$f" && cat "$f"; done`
	_, _ = podman(ctx, m.t.Output(), "exec", m.name, "sh", "-c", script, "sh", testHome+"/.local/state/maisys/logs")
}

// remove removes the container, unless the test failed and MAISYS_E2E_KEEP
// is set.
func (m *machine) remove() {
	if m.t.Failed() && os.Getenv("MAISYS_E2E_KEEP") != "" {
		p := strings.Join(podmanCommand(), " ")
		m.t.Logf("keeping the container: look inside with\n  %s exec -it --user %s --workdir %s %s bash -l\nand remove it with\n  %s rm --force %s",
			p, testUser, testHome, m.name, p, m.name)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	if code, err := podman(ctx, &out, "rm", "--force", "--ignore", "--time", "0", m.name); err != nil || code != 0 {
		m.t.Errorf("removing the container %s exited with %d: %v\n%s", m.name, code, err, out.String())
	}
}

// logStep starts a step in the test's output.
func logStep(t *testing.T, step string) {
	t.Helper()
	_, _ = fmt.Fprintf(t.Output(), "\n==> %s\n", step)
}
