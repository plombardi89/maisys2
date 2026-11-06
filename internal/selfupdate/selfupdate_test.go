package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// release is a fake set of release files.
type release struct {
	dir string
	url string
}

// targets are the archives every fake release has. The running platform is
// added if it's not among them.
var targets = []string{"linux_amd64", "linux_arm64", "windows_amd64"}

func binaryFor(version, target string) string {
	return fmt.Sprintf("maisys %s for %s", version, target)
}

// newRelease serves a release of version over HTTP. tweak can change the
// archives (keyed by file name) before checksums.txt is written.
func newRelease(t *testing.T, version string, tweak func(archives map[string][]byte)) *release {
	t.Helper()
	dir := t.TempDir()

	all := targets
	if self := runtime.GOOS + "_" + runtime.GOARCH; !slices.Contains(all, self) {
		all = append(append([]string(nil), all...), self)
	}

	archives := map[string][]byte{}
	for _, target := range all {
		goos, _, _ := strings.Cut(target, "_")
		name := fmt.Sprintf("maisys_%s_%s%s", version, target, archiveExt(goos))
		files := map[string]string{"LICENSE": "MIT", binaryName(goos): binaryFor(version, target)}
		if goos == "windows" {
			archives[name] = zipArchive(t, files)
		} else {
			archives[name] = tarGzArchive(t, files)
		}
	}
	if tweak != nil {
		tweak(archives)
	}

	var sums strings.Builder
	fmt.Fprintf(&sums, "%s  install.sh\n", strings.Repeat("1", 64))
	for name, data := range archives {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
		writeFile(t, filepath.Join(dir, name), data)
	}
	writeFile(t, filepath.Join(dir, "checksums.txt"), []byte(sums.String()))

	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return &release{dir: dir, url: srv.URL}
}

// newUpdater returns an Updater for the running platform whose executable is
// a temporary file containing "old".
func newUpdater(t *testing.T, releaseURL, current string) *Updater {
	t.Helper()
	exe := filepath.Join(t.TempDir(), binaryName(runtime.GOOS))
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Updater{
		ReleaseURL: releaseURL,
		Current:    current,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Executable: exe,
		Client:     &http.Client{Timeout: 30 * time.Second},
		UserAgent:  "maisys/" + current,
	}
}

func TestLatest(t *testing.T) {
	rel := newRelease(t, "0.2.0", nil)
	tests := []struct {
		goos, arch string
		archive    string
	}{
		{"linux", "amd64", "maisys_0.2.0_linux_amd64.tar.gz"},
		{"linux", "arm64", "maisys_0.2.0_linux_arm64.tar.gz"},
		{"windows", "amd64", "maisys_0.2.0_windows_amd64.zip"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.arch, func(t *testing.T) {
			u := &Updater{ReleaseURL: rel.url, OS: tt.goos, Arch: tt.arch}
			got, err := u.Latest(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != "0.2.0" || got.Archive != tt.archive || len(got.SHA256) != 64 {
				t.Errorf("Latest() = %+v, want version 0.2.0 and archive %s", got, tt.archive)
			}
		})
	}

	t.Run("no archive for the platform", func(t *testing.T) {
		u := &Updater{ReleaseURL: rel.url, OS: "linux", Arch: "riscv64"}
		_, err := u.Latest(t.Context())
		assertErrorContains(t, err, "the latest release has no archive for linux/riscv64")
	})
}

func TestUpdateInstallsNewerRelease(t *testing.T) {
	rel := newRelease(t, "0.2.0", nil)
	u := newUpdater(t, rel.url, "0.1.0")

	got, updated, err := u.Update(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !updated || got.Version != "0.2.0" {
		t.Fatalf("Update() = %+v, %v; want version 0.2.0 installed", got, updated)
	}
	assertContent(t, u.Executable, binaryFor("0.2.0", runtime.GOOS+"_"+runtime.GOARCH))
	if runtime.GOOS != "windows" {
		assertMode(t, u.Executable, 0o755)
	}

	// Only the binary is left, plus the old one Windows moved aside until
	// RemoveOld deletes it.
	want := []string{filepath.Base(u.Executable)}
	if runtime.GOOS == "windows" {
		want = append(want, filepath.Base(u.Executable)+".old")
	}
	assertDirContains(t, filepath.Dir(u.Executable), want...)
	RemoveOld(u.Executable)
	assertDirContains(t, filepath.Dir(u.Executable), filepath.Base(u.Executable))
}

func TestUpdateOnlyMovesForward(t *testing.T) {
	tests := []struct {
		current, latest string
		updated         bool
	}{
		{"0.1.0", "0.2.0", true},
		{"0.2.0-rc.1", "0.2.0", true},
		{"0.2.0", "0.2.0", false},
		{"0.3.0", "0.2.0", false},
		{"1.0.0", "0.9.9", false},
	}
	for _, tt := range tests {
		t.Run(tt.current+" to "+tt.latest, func(t *testing.T) {
			rel := newRelease(t, tt.latest, nil)
			u := newUpdater(t, rel.url, tt.current)

			_, updated, err := u.Update(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if updated != tt.updated {
				t.Errorf("updated = %v, want %v", updated, tt.updated)
			}
			if !tt.updated {
				assertContent(t, u.Executable, "old")
			}
		})
	}
}

func TestUpdateFailures(t *testing.T) {
	self := runtime.GOOS + "_" + runtime.GOARCH
	archive := fmt.Sprintf("maisys_0.2.0_%s%s", self, archiveExt(runtime.GOOS))

	tests := []struct {
		name    string
		current string
		setup   func(t *testing.T) string // returns the release URL
		want    string
	}{
		{
			name:    "checksum mismatch",
			current: "0.1.0",
			setup: func(t *testing.T) string {
				t.Helper()
				rel := newRelease(t, "0.2.0", nil)
				writeFile(t, filepath.Join(rel.dir, archive), []byte("tampered"))
				return rel.url
			},
			want: "checksum mismatch for " + archive,
		},
		{
			name:    "binary missing from the archive",
			current: "0.1.0",
			setup: func(t *testing.T) string {
				t.Helper()
				return newRelease(t, "0.2.0", func(archives map[string][]byte) {
					files := map[string]string{"LICENSE": "MIT"}
					if runtime.GOOS == "windows" {
						archives[archive] = zipArchive(t, files)
					} else {
						archives[archive] = tarGzArchive(t, files)
					}
				}).url
			},
			want: binaryName(runtime.GOOS) + " not found in the archive",
		},
		{
			name:    "corrupt archive",
			current: "0.1.0",
			setup: func(t *testing.T) string {
				t.Helper()
				return newRelease(t, "0.2.0", func(archives map[string][]byte) {
					archives[archive] = []byte("not an archive")
				}).url
			},
			want: "unpacking " + archive,
		},
		{
			name:    "no release",
			current: "0.1.0",
			setup: func(t *testing.T) string {
				t.Helper()
				return newRelease(t, "0.2.0", nil).url + "/missing"
			},
			want: "404 Not Found",
		},
		{
			name:    "invalid version in checksums.txt",
			current: "0.1.0",
			setup: func(t *testing.T) string {
				t.Helper()
				return newRelease(t, "latest", nil).url
			},
			want: "has an invalid version",
		},
		{
			name:    "running version isn't a release",
			current: "dev",
			setup: func(t *testing.T) string {
				t.Helper()
				return newRelease(t, "0.2.0", nil).url
			},
			want: `running version "dev" isn't a release version`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUpdater(t, tt.setup(t), tt.current)

			_, updated, err := u.Update(t.Context())
			assertErrorContains(t, err, tt.want)
			if updated {
				t.Error("updated = true after an error")
			}
			assertContent(t, u.Executable, "old")
			assertDirContains(t, filepath.Dir(u.Executable), filepath.Base(u.Executable))
		})
	}
}

func TestUpdateFromFileURL(t *testing.T) {
	rel := newRelease(t, "0.2.0", nil)
	fileURL := (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(rel.dir), "/")}).String()
	u := newUpdater(t, fileURL, "0.1.0")

	if _, updated, err := u.Update(t.Context()); err != nil || !updated {
		t.Fatalf("Update() = %v, %v; want the release installed", updated, err)
	}
	assertContent(t, u.Executable, binaryFor("0.2.0", runtime.GOOS+"_"+runtime.GOARCH))
}

func TestFileURLPath(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/home/me/dist", "/home/me/dist"},
		{"/C:/Users/me/dist", "C:/Users/me/dist"},
	}
	for _, tt := range tests {
		if got := fileURLPath(&url.URL{Scheme: "file", Path: tt.path}); got != filepath.FromSlash(tt.want) {
			t.Errorf("fileURLPath(%q) = %q, want %q", tt.path, got, filepath.FromSlash(tt.want))
		}
	}
}

// TestReplaceRunningBinary replaces a binary while it's running, which on
// Windows only works because the running one is moved aside first.
func TestReplaceRunningBinary(t *testing.T) {
	exe := filepath.Join(t.TempDir(), binaryName(runtime.GOOS))
	copyFile(t, os.Args[0], exe)

	cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "SELFUPDATE_HELPER=sleep")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	if err := replace(exe, []byte("new")); err != nil {
		t.Fatalf("replacing the running binary: %v", err)
	}
	assertContent(t, exe, "new")

	if runtime.GOOS == "windows" {
		if _, err := os.Stat(exe + ".old"); err != nil {
			t.Fatalf("the running binary wasn't moved aside: %v", err)
		}
		// RemoveOld leaves it while it's running, and deletes it afterwards.
		RemoveOld(exe)
		if _, err := os.Stat(exe + ".old"); err != nil {
			t.Errorf("RemoveOld deleted a binary that's still running: %v", err)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		eventually(t, func() bool {
			RemoveOld(exe)
			_, err := os.Stat(exe + ".old")
			return errors.Is(err, fs.ErrNotExist)
		})
	}
}

// TestRestart runs a helper that restarts into another helper, checking that
// the new process gets the arguments and environment, and on Windows that its
// exit code is passed back.
func TestRestart(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "SELFUPDATE_HELPER=restart")
	out, err := cmd.CombinedOutput()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("exit = %v, want exit code 3; output:\n%s", err, out)
	}
	want := "restarted: MAISYS_SELF_UPDATED=1 args=[sync --dry-run]"
	if !strings.Contains(string(out), want) {
		t.Errorf("output doesn't contain %q:\n%s", want, out)
	}
}

// TestHelperProcess isn't a real test. Other tests run the test binary with
// SELFUPDATE_HELPER set to make it act as a stand-in for maisys.
func TestHelperProcess(_ *testing.T) {
	switch os.Getenv("SELFUPDATE_HELPER") {
	case "":
		return
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "restart":
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "SELFUPDATE_HELPER=") {
				env = append(env, kv)
			}
		}
		env = append(env, "SELFUPDATE_HELPER=restarted", "MAISYS_SELF_UPDATED=1")
		args := []string{"-test.run=^TestHelperProcess$", "--", "sync", "--dry-run"}
		code, err := Restart(context.Background(), os.Args[0], args, env)
		if err != nil {
			fmt.Println("restart failed:", err)
			os.Exit(99)
		}
		os.Exit(code)
	case "restarted":
		var args []string
		for i, a := range os.Args {
			if a == "--" {
				args = os.Args[i+1:]
			}
		}
		fmt.Printf("restarted: MAISYS_SELF_UPDATED=%s args=%v\n", os.Getenv("MAISYS_SELF_UPDATED"), args)
		os.Exit(3)
	}
}

func tarGzArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
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

func zipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for range 50 {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met after 5s")
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s contains %q, want %q", path, got, want)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Errorf("%s has mode %v, want %v", path, info.Mode().Perm(), want)
	}
}

// assertDirContains checks dir contains exactly the named entries, so no
// temporary files are left behind.
func assertDirContains(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s contains %v, want %v", dir, got, want)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want one containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}
