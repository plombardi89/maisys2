// Package install tests install.sh. Each test pipes the script into bash, the
// way "curl ... | bash -s --" does, against a fake release served over HTTP,
// with HOME pointing at a temporary directory.
package install_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const version = "1.2.3"

// fakeMaisys stands in for the real binary built for arch, and records the
// arguments it was run with.
func fakeMaisys(arch string) string {
	return "#!/bin/sh\n# linux_" + arch + "\nprintf '%s\\n' \"$*\" >\"$FAKE_MAISYS_LOG\"\n"
}

func archiveName(arch string) string {
	return fmt.Sprintf("maisys_%s_linux_%s.tar.gz", version, arch)
}

// baseTools are the commands install.sh needs besides curl or wget. Tests run
// it with only these on PATH, so a new dependency fails the tests.
var baseTools = []string{
	"cat", "chmod", "cp", "gzip", "id", "mkdir", "mktemp", "mv",
	"rm", "rmdir", "sha256sum", "tar", "uname",
}

type harness struct {
	t       *testing.T
	home    string
	release string // directory served as the release
	url     string
	tools   string // directory of the commands on PATH
	path    string // PATH the script runs with
	log     string // where the fake maisys records its arguments
	env     []string
}

func newHarness(t *testing.T, downloader string) *harness {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("install.sh is for Linux")
	}

	h := &harness{
		t:       t,
		home:    t.TempDir(),
		release: t.TempDir(),
	}
	h.log = filepath.Join(h.home, "fake-maisys.log")

	tools := baseTools
	if downloader != "" {
		tools = append(append([]string(nil), baseTools...), downloader)
	}
	h.tools = toolDir(t, tools...)
	h.path = h.tools

	srv := httptest.NewServer(http.FileServer(http.Dir(h.release)))
	t.Cleanup(srv.Close)
	h.url = srv.URL

	h.writeRelease(map[string]string{})
	return h
}

// writeRelease writes a release with an amd64 and an arm64 Linux archive, each
// containing the fake maisys for that architecture, plus checksums.txt.
// overrides replaces checksum lines by file name; an empty value drops the
// line.
func (h *harness) writeRelease(overrides map[string]string) {
	h.t.Helper()
	windows := fmt.Sprintf("maisys_%s_windows_amd64.zip", version)
	names := []string{"install.sh", archiveName("amd64"), archiveName("arm64"), windows}
	lines := map[string]string{
		"install.sh": strings.Repeat("1", 64),
		windows:      strings.Repeat("2", 64),
	}
	for _, arch := range []string{"amd64", "arm64"} {
		data := tarGz(h.t, map[string]tarFile{
			"LICENSE": {mode: 0o644, body: "MIT"},
			"maisys":  {mode: 0o755, body: fakeMaisys(arch)},
		})
		writeFile(h.t, filepath.Join(h.release, archiveName(arch)), data, 0o644)
		sum := sha256.Sum256(data)
		lines[archiveName(arch)] = hex.EncodeToString(sum[:])
	}
	maps.Copy(lines, overrides)
	var b strings.Builder
	for _, name := range names {
		if s := lines[name]; s != "" {
			fmt.Fprintf(&b, "%s  %s\n", s, name)
		}
	}
	writeFile(h.t, filepath.Join(h.release, "checksums.txt"), []byte(b.String()), 0o644)
}

// fakeUname replaces uname on the script's PATH with one that reports Linux
// on the given machine architecture (uname -m).
func (h *harness) fakeUname(machine string) {
	h.t.Helper()
	p := filepath.Join(h.tools, "uname")
	if err := os.Remove(p); err != nil {
		h.t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = -m ]; then echo %s; else echo Linux; fi\n", machine)
	writeFile(h.t, p, []byte(script), 0o755)
}

func (h *harness) binPath() string {
	return filepath.Join(h.home, ".local", "bin", "maisys")
}

// run pipes install.sh into bash with args and returns stdout, stderr and the
// exit code.
func (h *harness) run(args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	script, err := os.ReadFile(scriptPath(h.t))
	if err != nil {
		h.t.Fatal(err)
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		h.t.Skip("bash not installed")
	}
	cmd := exec.CommandContext(h.t.Context(), bash, append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = bytes.NewReader(script) // a pipe, like curl | bash
	cmd.Env = append([]string{
		"HOME=" + h.home,
		"PATH=" + h.path,
		"MAISYS_RELEASE_URL=" + h.url,
		"FAKE_MAISYS_LOG=" + h.log,
	}, h.env...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		h.t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func TestInstall(t *testing.T) {
	for _, downloader := range []string{"curl", "wget"} {
		t.Run(downloader, func(t *testing.T) {
			skipUnlessInstallable(t)
			h := newHarness(t, downloader)

			_, stderr, code := h.run()
			if code != 0 {
				t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
			}
			assertFile(t, h.binPath(), fakeMaisys(runtime.GOARCH), 0o755)
			assertContains(t, stderr,
				"installed maisys "+version+" to "+h.binPath(),
				"isn't on your PATH yet",
				"next: run '"+h.binPath()+" sync CONFIG-URL'",
				"or '"+h.binPath()+" config init'",
			)
			assertNotExist(t, h.log)
		})
	}
}

func TestInstallRunsSync(t *testing.T) {
	skipUnlessInstallable(t)
	h := newHarness(t, "curl")

	_, stderr, code := h.run("https://example.com/config.git", "--machine", "laptop")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	got, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatalf("maisys sync wasn't run: %v\nstderr:\n%s", err, stderr)
	}
	if want := "sync https://example.com/config.git --machine laptop\n"; string(got) != want {
		t.Errorf("maisys was run with %q, want %q", got, want)
	}
}

func TestInstallReplacesExistingBinary(t *testing.T) {
	skipUnlessInstallable(t)
	h := newHarness(t, "curl")
	writeFile(t, h.binPath(), []byte("old"), 0o755)
	h.path += ":" + filepath.Dir(h.binPath())

	_, stderr, code := h.run()
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	assertFile(t, h.binPath(), fakeMaisys(runtime.GOARCH), 0o755)
	assertNotExist(t, filepath.Join(filepath.Dir(h.binPath()), ".maisys.new"))
	if strings.Contains(stderr, "isn't on your PATH") {
		t.Errorf("warned about PATH although %s is on it:\n%s", filepath.Dir(h.binPath()), stderr)
	}
	assertContains(t, stderr, "next: run 'maisys sync CONFIG-URL'")
}

// TestInstallArchitectures checks that install.sh picks the archive for the
// machine's architecture, whatever the tests run on.
func TestInstallArchitectures(t *testing.T) {
	tests := []struct {
		machine string // what uname -m reports
		arch    string // the archive install.sh should install
	}{
		{"x86_64", "amd64"},
		{"aarch64", "arm64"},
	}
	for _, tt := range tests {
		t.Run(tt.machine, func(t *testing.T) {
			skipUnlessInstallable(t)
			h := newHarness(t, "curl")
			h.fakeUname(tt.machine)

			_, stderr, code := h.run()
			if code != 0 {
				t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
			}
			assertFile(t, h.binPath(), fakeMaisys(tt.arch), 0o755)
		})
	}
}

func TestInstallFailures(t *testing.T) {
	archive := archiveName(runtime.GOARCH)
	tests := []struct {
		name       string
		downloader string
		setup      func(h *harness)
		want       string
	}{
		{
			name:       "checksum mismatch",
			downloader: "curl",
			setup:      func(h *harness) { h.writeRelease(map[string]string{archive: strings.Repeat("0", 64)}) },
			want:       "checksum mismatch for " + archive,
		},
		{
			name:       "no Linux archive",
			downloader: "curl",
			setup:      func(h *harness) { h.writeRelease(map[string]string{archive: ""}) },
			want:       "the release has no linux_" + runtime.GOARCH + " archive",
		},
		{
			name:       "unsupported architecture",
			downloader: "curl",
			setup:      func(h *harness) { h.fakeUname("riscv64") },
			want:       "unsupported architecture: riscv64",
		},
		{
			name:       "no release",
			downloader: "curl",
			setup:      func(h *harness) { h.url += "/missing" },
			want:       "couldn't download checksums.txt",
		},
		{
			name:       "no curl or wget",
			downloader: "",
			want:       "curl or wget is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skipUnlessInstallable(t)
			h := newHarness(t, tt.downloader)
			if tt.setup != nil {
				tt.setup(h)
			}

			_, stderr, code := h.run("https://example.com/config.git")
			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stderr:\n%s", stderr)
			}
			assertContains(t, stderr, tt.want)
			assertNotExist(t, h.binPath())
			assertNotExist(t, h.log)
		})
	}
}

func TestRefusesRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only runs as root")
	}
	h := newHarness(t, "curl")

	_, stderr, code := h.run()
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stderr:\n%s", stderr)
	}
	assertContains(t, stderr, "not root")
	assertNotExist(t, h.binPath())
}

func TestUninstall(t *testing.T) {
	tests := []struct {
		name string
		xdg  bool
	}{
		{"default directories", false},
		{"XDG directories", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, "")
			config := filepath.Join(h.home, ".config")
			state := filepath.Join(h.home, ".local", "state")
			cache := filepath.Join(h.home, ".cache")
			if tt.xdg {
				config = filepath.Join(h.home, "xdg", "config")
				state = filepath.Join(h.home, "xdg", "state")
				cache = filepath.Join(h.home, "xdg", "cache")
				h.env = append(h.env, "XDG_CONFIG_HOME="+config, "XDG_STATE_HOME="+state, "XDG_CACHE_HOME="+cache)
			}

			removed := []string{
				h.binPath(),
				filepath.Join(config, "maisys", "maisys.toml"),
				filepath.Join(config, "maisys", "git-credentials"),
				filepath.Join(state, "maisys", "state.json"),
				filepath.Join(cache, "maisys", "latest.json"),
			}
			kept := filepath.Join(config, "maisys", "shell", "env.zsh")
			for _, p := range append(removed, kept) {
				writeFile(t, p, []byte("x"), 0o644)
			}

			_, stderr, code := h.run("--uninstall")
			if code != 0 {
				t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
			}
			for _, p := range removed {
				assertNotExist(t, p)
			}
			assertNotExist(t, filepath.Join(state, "maisys"))
			assertNotExist(t, filepath.Join(cache, "maisys"))
			assertFile(t, kept, "x", 0o644)
			assertContains(t, stderr, "removed "+h.binPath(), "still in place")
		})
	}
}

func TestUninstallRemovesEmptyConfigDir(t *testing.T) {
	h := newHarness(t, "")
	settings := filepath.Join(h.home, ".config", "maisys", "maisys.toml")
	writeFile(t, settings, []byte("x"), 0o644)

	if _, stderr, code := h.run("--uninstall"); code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	assertNotExist(t, filepath.Dir(settings))
}

func TestUninstallRemovesBrokenSymlink(t *testing.T) {
	h := newHarness(t, "")
	if err := os.MkdirAll(filepath.Dir(h.binPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.home, "deleted-build", "maisys"), h.binPath()); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := h.run("--uninstall")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	assertNotExist(t, h.binPath())
	assertContains(t, stderr, "removed "+h.binPath())
}

func TestUninstallNothingInstalled(t *testing.T) {
	h := newHarness(t, "")
	_, stderr, code := h.run("--uninstall")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	assertContains(t, stderr, "nothing to remove")
}

func TestUninstallRejectsExtraArgs(t *testing.T) {
	h := newHarness(t, "")
	_, stderr, code := h.run("--uninstall", "extra")
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stderr:\n%s", stderr)
	}
	assertContains(t, stderr, "--uninstall takes no other arguments")
}

func TestHelp(t *testing.T) {
	h := newHarness(t, "")
	stdout, stderr, code := h.run("--help")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	assertContains(t, stdout, "Usage: install.sh", "--uninstall")
}

type tarFile struct {
	mode int64
	body string
}

func tarGz(t *testing.T, files map[string]tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, f := range files {
		hdr := &tar.Header{Name: name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// toolDir returns a directory containing symlinks to the named commands, for
// use as PATH.
func toolDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		p, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s not installed", name)
		}
		if err := os.Symlink(p, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func scriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// skipUnlessInstallable skips tests that expect install.sh to install
// maisys, where it would refuse to: as root (see TestRefusesRoot), or on an
// architecture it doesn't support.
func skipUnlessInstallable(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("install.sh refuses to run as root (see TestRefusesRoot)")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("install.sh doesn't support %s", runtime.GOARCH)
	}
}

func writeFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if string(got) != want {
		t.Errorf("%s contains %q, want %q", path, got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), mode)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists, want it gone (err: %v)", path, err)
	}
}

func assertContains(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(s, want) {
			t.Errorf("output doesn't contain %q:\n%s", want, s)
		}
	}
}
