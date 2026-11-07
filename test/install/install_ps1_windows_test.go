package install_test

// Tests for install.ps1, run with Windows PowerShell (powershell.exe), as a
// fresh Windows 11 has it, against a fake release served from a folder
// through a file:// MAISYS_RELEASE_URL. Each test has its own LOCALAPPDATA,
// APPDATA and TEMP, and its own registry key for the user PATH
// (MAISYS_USER_ENV_KEY), so the tests never change your own.
//
// The fake maisys.exe in the release is this test binary, which TestMain
// turns into a fake maisys when it runs as maisys.exe.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows/registry"
)

func TestMain(m *testing.M) {
	if strings.EqualFold(filepath.Base(os.Args[0]), "maisys.exe") {
		os.Exit(fakeMaisysMain())
	}
	os.Exit(m.Run())
}

// fakeMaisysMain is what this test binary does when it runs as maisys.exe:
// it records its arguments in FAKE_MAISYS_LOG, writes a line of error
// output, waits for its input to close if FAKE_MAISYS_WAIT is set, and
// exits with FAKE_MAISYS_EXIT.
func fakeMaisysMain() int {
	if log := os.Getenv("FAKE_MAISYS_LOG"); log != "" {
		_ = os.WriteFile(log, []byte(strings.Join(os.Args[1:], " ")+"\n"), 0o644)
	}
	fmt.Fprintln(os.Stderr, "fake maisys: this goes to error output")
	if os.Getenv("FAKE_MAISYS_WAIT") != "" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	code, _ := strconv.Atoi(os.Getenv("FAKE_MAISYS_EXIT"))
	return code
}

// windowsArchive is the name of the release's Windows archive.
var windowsArchive = fmt.Sprintf("maisys_%s_windows_amd64.zip", version)

// fakeExe returns the release's maisys.exe: this test binary, marked so that
// it can be told from a plain copy of it.
var fakeExe = sync.OnceValues(func() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return nil, err
	}
	// Windows ignores data after a program's last section.
	return append(data, "\nmaisys "+version+" for tests\n"...), nil
})

// fakeZip returns the release's Windows archive, laid out as GoReleaser
// makes it.
var fakeZip = sync.OnceValues(func() ([]byte, error) {
	exe, err := fakeExe()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{"LICENSE", []byte("MIT")},
		{"README.md", []byte("# maisys")},
		{"maisys.exe", exe},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
})

type psHarness struct {
	t          *testing.T
	powershell string
	// local and roaming are LOCALAPPDATA and APPDATA, and temp is TEMP.
	local, roaming, temp string
	release              string // folder served as the release
	url                  string
	// envKey is the registry key, under HKEY_CURRENT_USER, that stands in
	// for the user's environment.
	envKey string
	log    string // where the fake maisys records its arguments
	env    []string
}

func newPSHarness(t *testing.T) *psHarness {
	t.Helper()
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell isn't installed")
	}
	dir := t.TempDir()
	h := &psHarness{
		t:          t,
		powershell: powershell,
		local:      filepath.Join(dir, "Local"),
		roaming:    filepath.Join(dir, "Roaming"),
		temp:       filepath.Join(dir, "Temp"),
		release:    t.TempDir(),
		log:        filepath.Join(dir, "fake-maisys.log"),
		envKey:     `Software\maisys-tests\` + randomHex(t),
	}
	for _, d := range []string{h.local, h.roaming, h.temp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h.url = "file:///" + filepath.ToSlash(h.release)
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, h.envKey)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\maisys-tests`) // if no other test uses it
	})
	h.writeRelease(nil)
	return h
}

// writeRelease writes a release with the Windows archive, plus
// checksums.txt with lines for the Linux archives and install.sh too.
// overrides replaces checksum lines by file name; an empty value drops the
// line.
func (h *psHarness) writeRelease(overrides map[string]string) {
	h.t.Helper()
	data, err := fakeZip()
	if err != nil {
		h.t.Fatal(err)
	}
	writeFile(h.t, filepath.Join(h.release, windowsArchive), data, 0o644)
	sum := sha256.Sum256(data)
	names := []string{"install.ps1", "install.sh", archiveName("amd64"), archiveName("arm64"), windowsArchive}
	lines := map[string]string{
		"install.ps1":        strings.Repeat("1", 64),
		"install.sh":         strings.Repeat("2", 64),
		archiveName("amd64"): strings.Repeat("3", 64),
		archiveName("arm64"): strings.Repeat("4", 64),
		windowsArchive:       hex.EncodeToString(sum[:]),
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

func (h *psHarness) installDir() string { return filepath.Join(h.local, "Programs", "maisys") }

func (h *psHarness) exe() string { return filepath.Join(h.installDir(), "maisys.exe") }

// run runs install.ps1 as a file with args, and returns its output, error
// output and exit code.
func (h *psHarness) run(args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	return h.powershellRun(append([]string{"-File", psScriptPath(h.t)}, args...)...)
}

// runOneLiner runs install.ps1 the way its documented one-liner does, iex
// "& { $(irm ...) } ARGS", reading the script from the file instead, and
// then runs after, more PowerShell.
func (h *psHarness) runOneLiner(args, after string) (stdout, stderr string, code int) {
	h.t.Helper()
	path := strings.ReplaceAll(psScriptPath(h.t), "'", "''")
	script := fmt.Sprintf(`iex "& { $(Get-Content -Raw -LiteralPath '%s') } %s"`, path, args) + "\n" + after
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, 0, 2*len(units))
	for _, u := range units {
		encoded = append(encoded, byte(u), byte(u>>8))
	}
	return h.powershellRun("-EncodedCommand", base64.StdEncoding.EncodeToString(encoded))
}

func (h *psHarness) powershellRun(args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	cmd := exec.CommandContext(h.t.Context(), h.powershell, append([]string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"}, args...)...)
	cmd.Env = append(environWithout("LOCALAPPDATA", "APPDATA", "TEMP", "TMP", "MAISYS_RELEASE_URL", "MAISYS_USER_ENV_KEY"),
		"LOCALAPPDATA="+h.local,
		"APPDATA="+h.roaming,
		"TEMP="+h.temp,
		"TMP="+h.temp,
		"MAISYS_RELEASE_URL="+h.url,
		"MAISYS_USER_ENV_KEY="+h.envKey,
		"FAKE_MAISYS_LOG="+h.log,
	)
	cmd.Env = append(cmd.Env, h.env...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err := cmd.Run()
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

// userPath returns the Path value of the stand-in for the user's
// environment, and whether it's there.
func (h *psHarness) userPath() (string, bool) {
	h.t.Helper()
	k, err := registry.OpenKey(registry.CURRENT_USER, h.envKey, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "", false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = k.Close() }()
	v, kind, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", false
	}
	if err != nil {
		h.t.Fatal(err)
	}
	if kind != registry.EXPAND_SZ {
		h.t.Errorf("Path is of registry type %d, want REG_EXPAND_SZ", kind)
	}
	return v, true
}

// setUserPath sets the Path value of the stand-in for the user's
// environment.
func (h *psHarness) setUserPath(v string) {
	h.t.Helper()
	k, _, err := registry.CreateKey(registry.CURRENT_USER, h.envKey, registry.SET_VALUE)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = k.Close() }()
	if err := k.SetExpandStringValue("Path", v); err != nil {
		h.t.Fatal(err)
	}
}

// pathEntries returns how many entries of the user PATH are the install
// folder.
func (h *psHarness) pathEntries() int {
	h.t.Helper()
	path, _ := h.userPath()
	n := 0
	for entry := range strings.SplitSeq(path, ";") {
		if strings.EqualFold(strings.TrimRight(entry, `\`), h.installDir()) {
			n++
		}
	}
	return n
}

// assertInstalled checks that the release's maisys.exe is installed, with
// nothing left over beside it, and that the user PATH has the install
// folder once.
func (h *psHarness) assertInstalled(extra ...string) {
	h.t.Helper()
	want, err := fakeExe()
	if err != nil {
		h.t.Fatal(err)
	}
	got, err := os.ReadFile(h.exe())
	if err != nil {
		h.t.Fatalf("maisys.exe wasn't installed: %v", err)
	}
	if !bytes.Equal(got, want) {
		h.t.Errorf("%s isn't the release's maisys.exe", h.exe())
	}
	entries, err := os.ReadDir(h.installDir())
	if err != nil {
		h.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := append([]string{"maisys.exe"}, extra...); !slices.Equal(names, want) {
		h.t.Errorf("%s holds %q, want %q", h.installDir(), names, want)
	}
	if n := h.pathEntries(); n != 1 {
		path, _ := h.userPath()
		h.t.Errorf("the user PATH has %s %d times, want once: %q", h.installDir(), n, path)
	}
	h.assertNoTempLeft()
}

// assertNoTempLeft checks that install.ps1 deleted its temporary folder.
func (h *psHarness) assertNoTempLeft() {
	h.t.Helper()
	left, err := filepath.Glob(filepath.Join(h.temp, "maisys-install-*"))
	if err != nil {
		h.t.Fatal(err)
	}
	if len(left) > 0 {
		h.t.Errorf("install.ps1 left %q behind", left)
	}
}

func TestPSInstall(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)

	_, stderr, code := h.run()
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	h.assertInstalled()
	assertContains(t, stderr,
		"maisys-install: downloading maisys "+version,
		"installed maisys "+version+" to "+h.exe(),
		"added "+h.installDir()+" to your user PATH",
		"next: run 'maisys sync CONFIG-URL'",
		"or 'maisys config init'",
	)
	assertNotExist(t, h.log)
}

// TestPSInstallKeepsPath checks that the entries already in the user PATH
// are kept as they are, variables and all.
func TestPSInstallKeepsPath(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	h.setUserPath(`C:\Tools;%USERPROFILE%\bin`)

	if _, stderr, code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	path, _ := h.userPath()
	if want := `C:\Tools;%USERPROFILE%\bin;` + h.installDir(); path != want {
		t.Errorf("user PATH = %q, want %q", path, want)
	}
}

// TestPSReinstall checks that installing again replaces maisys.exe, and
// adds nothing more to the user PATH.
func TestPSReinstall(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	writeFile(t, h.exe(), []byte("old"), 0o755)
	h.setUserPath(`C:\Tools;` + h.installDir() + `\`)

	for i := range 2 {
		_, stderr, code := h.run()
		if code != 0 {
			t.Fatalf("run %d: exit code = %d, stderr:\n%s", i+1, code, stderr)
		}
		if strings.Contains(stderr, "to your user PATH") {
			t.Errorf("run %d: changed the user PATH, which already had %s:\n%s", i+1, h.installDir(), stderr)
		}
		h.assertInstalled()
	}
}

// TestPSInstallWhileRunning checks that a maisys.exe that's running is
// replaced too: moved aside to maisys.exe.old, which it can't delete yet.
func TestPSInstallWhileRunning(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, h.exe(), data, 0o755)
	running := exec.CommandContext(t.Context(), h.exe())
	running.Env = append(os.Environ(), "FAKE_MAISYS_WAIT=1")
	stdin, err := running.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = stdin.Close()
			_ = running.Wait()
		}
	}
	defer stop()

	_, stderr, code := h.run()
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	h.assertInstalled("maisys.exe.old")
	stop()

	// The next install deletes it, now that it isn't running.
	if _, stderr, code := h.run(); code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	h.assertInstalled()
}

func TestPSInstallRunsSync(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)

	args := []string{"https://example.com/config.git", "--machine", "laptop", "-y"}
	_, stderr, code := h.run(args...)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	h.assertInstalled()
	got, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatalf("maisys sync wasn't run: %v\nstderr:\n%s", err, stderr)
	}
	if want := "sync " + strings.Join(args, " ") + "\n"; string(got) != want {
		t.Errorf("maisys was run with %q, want %q", got, want)
	}
	assertContains(t, stderr, "running maisys sync "+strings.Join(args, " "), "fake maisys: this goes to error output")
}

// TestPSInstallSyncExitCode checks that install.ps1 run as a file exits
// with maisys sync's exit code, such as 3 when Windows must restart.
func TestPSInstallSyncExitCode(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	h.env = append(h.env, "FAKE_MAISYS_EXIT=3")

	_, stderr, code := h.run("https://example.com/config.git")
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stderr, "maisys-install: error") {
		t.Errorf("install.ps1 failed itself:\n%s", stderr)
	}
}

// TestPSOneLiner checks the documented one-liner, which runs the script as
// a script block, with the arguments after it.
func TestPSOneLiner(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)

	stdout, stderr, code := h.runOneLiner("https://example.com/config.git --machine laptop", "'after: ' + (Get-Command maisys).Source")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	h.assertInstalled()
	got, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatalf("maisys sync wasn't run: %v\nstderr:\n%s", err, stderr)
	}
	if want := "sync https://example.com/config.git --machine laptop\n"; string(got) != want {
		t.Errorf("maisys was run with %q, want %q", got, want)
	}
	// The PowerShell it ran in finds maisys straight away.
	assertContains(t, stdout, `after: `, `\Programs\maisys\maisys.exe`)
}

// TestPSOneLinerFailure checks that the one-liner doesn't close the
// PowerShell it runs in when it fails.
func TestPSOneLinerFailure(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	h.writeRelease(map[string]string{windowsArchive: strings.Repeat("0", 64)})

	stdout, stderr, _ := h.runOneLiner("https://example.com/config.git", "'still running'")
	assertContains(t, stderr, "maisys-install: error: checksum mismatch for "+windowsArchive)
	assertContains(t, stdout, "still running")
	assertNotExist(t, h.exe())
}

func TestPSInstallFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(h *psHarness)
		want  string
	}{
		{
			name:  "checksum mismatch",
			setup: func(h *psHarness) { h.writeRelease(map[string]string{windowsArchive: strings.Repeat("0", 64)}) },
			want:  "checksum mismatch for " + windowsArchive,
		},
		{
			name:  "no Windows archive",
			setup: func(h *psHarness) { h.writeRelease(map[string]string{windowsArchive: ""}) },
			want:  "the release has no windows_amd64 archive",
		},
		{
			name:  "no release",
			setup: func(h *psHarness) { h.url += "/missing" },
			want:  "couldn't download checksums.txt from " + "file:///",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newPSHarness(t)
			tt.setup(h)

			_, stderr, code := h.run("https://example.com/config.git")
			if code != 1 {
				t.Fatalf("exit code = %d, want 1; stderr:\n%s", code, stderr)
			}
			assertContains(t, stderr, "maisys-install: error: "+tt.want)
			assertNotExist(t, h.exe())
			assertNotExist(t, h.log)
			if path, ok := h.userPath(); ok {
				t.Errorf("the user PATH was changed to %q", path)
			}
			h.assertNoTempLeft()
		})
	}
}

func TestPSUninstall(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	if _, stderr, code := h.run(); code != 0 {
		t.Fatalf("installing: exit code = %d, stderr:\n%s", code, stderr)
	}
	path, _ := h.userPath()
	h.setUserPath(`C:\Tools;` + path + `;%USERPROFILE%\bin`)

	removed := []string{
		h.exe(),
		h.exe() + ".old",
		filepath.Join(h.roaming, "maisys", "maisys.toml"),
		filepath.Join(h.roaming, "maisys", "git-credentials"),
		filepath.Join(h.local, "maisys", "state.json"),
		filepath.Join(h.local, "maisys", "logs", "sync.log"),
		filepath.Join(h.local, "maisys", "cache", "downloads", "maisys_linux_amd64.tar.gz"),
	}
	for _, p := range removed[1:] {
		writeFile(t, p, []byte("x"), 0o644)
	}

	_, stderr, code := h.run("-Uninstall")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	for _, p := range removed {
		assertNotExist(t, p)
	}
	assertNotExist(t, h.installDir())
	assertNotExist(t, filepath.Join(h.roaming, "maisys"))
	assertNotExist(t, filepath.Join(h.local, "maisys"))
	if path, _ := h.userPath(); path != `C:\Tools;%USERPROFILE%\bin` {
		t.Errorf("user PATH = %q, want the other entries only", path)
	}
	assertContains(t, stderr,
		"removed "+h.exe(),
		"removed "+filepath.Join(h.roaming, "maisys"),
		"removed "+h.installDir()+" from your user PATH",
		"still in place",
	)
}

func TestPSUninstallNothingInstalled(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	_, stderr, code := h.run("-Uninstall")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr:\n%s", code, stderr)
	}
	assertContains(t, stderr, "nothing to remove")
}

func TestPSUninstallRejectsExtraArgs(t *testing.T) {
	t.Parallel()
	h := newPSHarness(t)
	_, stderr, code := h.run("-Uninstall", "extra")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr:\n%s", code, stderr)
	}
	assertContains(t, stderr, "-Uninstall takes no other arguments")
}

func TestPSHelp(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"-Help", "--help"} {
		h := newPSHarness(t)
		stdout, stderr, code := h.run(arg)
		if code != 0 {
			t.Fatalf("%s: exit code = %d, stderr:\n%s", arg, code, stderr)
		}
		assertContains(t, stdout, "Usage: install.ps1", "-Uninstall")
		assertNotExist(t, h.exe())
	}
}

func psScriptPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// environWithout returns this process's environment without the variables
// names.
func environWithout(names ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) }) {
			env = append(env, kv)
		}
	}
	return env
}

func randomHex(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
