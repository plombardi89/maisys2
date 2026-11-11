package tool

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/download"
	"github.com/plombardi89/maisys2/internal/resource/version"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
)

// testNow is the time tests run at.
var testNow = time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC)

// needSymlinks skips tests that need Unix symlinks.
func needSymlinks(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("tools are installed with Unix symlinks, on Linux only")
	}
}

// upstream is a fake upstream publishing releases of a tool called fake on
// GitHub: for each version and architecture, a .tar.gz holding
// fake-<version>/bin/fake and fake-<version>/bin/fakectl, and a
// checksums.txt.
type upstream struct {
	*httptest.Server
	mu     sync.Mutex
	files  map[string][]byte
	hits   map[string]int
	latest string
}

func newUpstream(t *testing.T, versions ...string) *upstream {
	t.Helper()
	u := &upstream{files: map[string][]byte{}, hits: map[string]int{}}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.hits[r.URL.Path]++
		if r.URL.Path == "/fake/fake/releases/latest" && u.latest != "" {
			http.Redirect(w, r, "/fake/fake/releases/tag/v"+u.latest, http.StatusFound)
			return
		}
		data, ok := u.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(u.Close)
	for _, v := range versions {
		u.release(t, v, "bin/fake", "bin/fakectl")
	}
	return u
}

// release publishes version v, with the given files in its archives.
func (u *upstream) release(t *testing.T, v string, files ...string) {
	t.Helper()
	u.mu.Lock()
	defer u.mu.Unlock()
	var sums strings.Builder
	for _, arch := range []string{"x86_64", "aarch64"} {
		name := "fake-" + v + "-linux-" + arch + ".tar.gz"
		contents := map[string]string{}
		for _, f := range files {
			contents["fake-"+v+"/"+f] = fmt.Sprintf("%s %s %s", f, v, arch)
		}
		data := tarGz(t, contents)
		u.files["/fake/fake/releases/download/v"+v+"/"+name] = data
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	u.files["/fake/fake/releases/download/v"+v+"/checksums.txt"] = []byte(sums.String())
	u.latest = v
}

// set replaces the file at path.
func (u *upstream) set(path string, data []byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.files[path] = data
}

// count returns how many requests there were for path.
func (u *upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[path]
}

func (u *upstream) archive(v string) string {
	return "/fake/fake/releases/download/v" + v + "/fake-" + v + "-linux-x86_64.tar.gz"
}

// spec returns the spec of the fake tool.
func (u *upstream) spec() Spec {
	return Spec{
		Name:   "fake",
		Latest: version.GitHub{Repo: "fake/fake", TagPrefix: "v", URL: u.URL},
		Arch:   map[config.Arch]string{config.AMD64: "x86_64", config.ARM64: "aarch64"},
		Asset: func(v, arch string) download.Asset {
			name := "fake-" + v + "-linux-" + arch + ".tar.gz"
			return download.Asset{URL: u.URL + "/fake/fake/releases/download/v" + v + "/" + name, Name: name}
		},
		Checksum: func(v string, _ download.Asset) download.Checksum {
			return download.SumsFile{URL: u.URL + "/fake/fake/releases/download/v" + v + "/checksums.txt"}
		},
		Archive: download.Archive{Format: download.TarGz, Strip: 1},
		Links:   []Link{{Name: "fake", Target: "bin/fake"}, {Name: "fakectl", Target: "bin/fakectl"}},
	}
}

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))}); err != nil {
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

// machine is a test machine: a home folder, the state file's records, and
// the clock.
type machine struct {
	t     *testing.T
	home  string
	env   *Env
	rt    *resource.Runtime
	state *state.State
	now   time.Time
}

func newMachine(t *testing.T, u *upstream) *machine {
	t.Helper()
	home := t.TempDir()
	paths := settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")}
	m := &machine{t: t, home: home, state: &state.State{}, now: testNow}
	m.env = NewEnv(paths, "maisys/test")
	m.env.Client.HTTP = u.Client()
	m.env.Client.Sleep = func(context.Context, time.Duration) error { return nil }
	m.env.Latest.Now = func() time.Time { return m.now }
	m.rt = &resource.Runtime{
		Facts:    &facts.System{Arch: config.AMD64},
		Paths:    paths,
		Previous: m.state.Get,
		Now:      func() time.Time { return m.now },
	}
	return m
}

// tool returns the fake tool at version v.
func (m *machine) tool(u *upstream, v string) *Tool {
	return New(u.spec(), v, m.env)
}

// newRun starts another run of maisys, a dry run or a sync, with a Cache
// of its own for the latest versions.
func (m *machine) newRun(dryRun bool) {
	m.env.Latest = &version.Cache{Path: m.env.Latest.Path, DryRun: dryRun, Now: func() time.Time { return m.now }}
}

// check checks r, failing the test if that fails.
func (m *machine) check(r *Tool) resource.Change {
	m.t.Helper()
	c, err := r.Check(m.t.Context(), m.rt)
	if err != nil {
		m.t.Fatalf("Check() error = %v", err)
	}
	return c
}

// sync checks r and, if there's a change, applies it as the engine does,
// keeping its record. It returns the change.
func (m *machine) sync(r *Tool) resource.Change {
	m.t.Helper()
	c := m.check(r)
	if !c.Action.Applies() {
		return c
	}
	rec, err := r.Apply(m.t.Context(), m.rt)
	if err != nil {
		m.t.Fatalf("Apply() error = %v", err)
	}
	rec.ID, rec.Kind, rec.Module = r.ID(), r.Kind(), "fake"
	m.state.Put(rec)
	return c
}

// path returns a path in the home folder, given with slashes.
func (m *machine) path(rel string) string {
	return filepath.Join(m.home, filepath.FromSlash(rel))
}

// toolPath returns a path in the fake tool's folder.
func (m *machine) toolPath(rel string) string {
	return m.path(".local/share/maisys/tools/fake/" + rel)
}

// read returns a file's contents, through symlinks, or "" if it's missing.
func (m *machine) read(rel string) string {
	data, err := os.ReadFile(m.path(rel))
	if err != nil {
		return ""
	}
	return string(data)
}

// link returns where a symlink in the home folder points, or "".
func (m *machine) link(rel string) string {
	target, err := os.Readlink(m.path(rel))
	if err != nil {
		return ""
	}
	return target
}

// exists reports whether a path in the home folder exists.
func (m *machine) exists(rel string) bool {
	_, err := os.Lstat(m.path(rel))
	return err == nil
}

// entries returns the names in a folder in the home folder.
func (m *machine) entries(rel string) []string {
	list, _ := os.ReadDir(m.path(rel))
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func wantChange(t *testing.T, got resource.Change, action resource.Action, summary string) {
	t.Helper()
	if got.Action != action || got.Summary != summary {
		t.Errorf("Check() = %s %q, want %s %q", got.Action, got.Summary, action, summary)
	}
}

func TestInstall(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0")
	m := newMachine(t, u)
	tool := m.tool(u, "v1.0.0")

	wantChange(t, m.sync(tool), resource.Create, "fake 1.0.0")
	if got := m.read(".local/bin/fake"); got != "bin/fake 1.0.0 x86_64" {
		t.Errorf("~/.local/bin/fake runs %q, want fake 1.0.0 for x86_64", got)
	}
	if got, want := m.link(".local/bin/fakectl"), m.toolPath("1.0.0/bin/fakectl"); got != want {
		t.Errorf("~/.local/bin/fakectl → %s, want %s", got, want)
	}
	if !strings.HasPrefix(m.read(".local/share/maisys/tools/fake/1.0.0/"+Marker), "fake 1.0.0\n") {
		t.Errorf("the version's folder has no marker")
	}
	if got := m.entries(".cache/maisys/downloads"); len(got) != 0 {
		t.Errorf("the download cache holds %q after installing, want nothing", got)
	}

	rec, ok := m.state.Get("tool:fake")
	wantPaths := []string{m.toolPath("1.0.0"), m.path(".local/bin/fake"), m.path(".local/bin/fakectl")}
	if !ok || rec.Kind != Kind || strings.Join(rec.Paths, " ") != strings.Join(wantPaths, " ") || rec.Details["version"] != "1.0.0" {
		t.Errorf("record = %+v, want the version's folder and commands, and version 1.0.0", rec)
	}

	// A second sync changes nothing, and downloads nothing.
	wantChange(t, m.sync(m.tool(u, "1.0.0")), resource.NoChange, "")
	if n := u.count(u.archive("1.0.0")); n != 1 {
		t.Errorf("downloaded the archive %d times, want once", n)
	}
	if n := u.count("/fake/fake/releases/download/v1.0.0/checksums.txt"); n != 1 {
		t.Errorf("read checksums.txt %d times, want once", n)
	}
}

func TestInstallArm64(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0")
	m := newMachine(t, u)
	m.rt.Facts.Arch = config.ARM64
	m.sync(m.tool(u, "1.0.0"))
	if got := m.read(".local/bin/fake"); got != "bin/fake 1.0.0 aarch64" {
		t.Errorf("~/.local/bin/fake runs %q, want fake 1.0.0 for aarch64", got)
	}
}

func TestLatest(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)
	lookups := func(want int) {
		t.Helper()
		if n := u.count("/fake/fake/releases/latest"); n != want {
			t.Errorf("looked up the latest version %d times, want %d", n, want)
		}
	}

	wantChange(t, m.sync(m.tool(u, "latest")), resource.Create, "fake 1.1.0 (latest)")
	lookups(1)

	// The next run looks it up again, even minutes later.
	u.release(t, "1.2.0", "bin/fake", "bin/fakectl")
	m.newRun(true)
	m.now = m.now.Add(10 * time.Minute)
	wantChange(t, m.check(m.tool(u, "")), resource.Update, "fake 1.1.0 → 1.2.0 (latest)")
	lookups(2)

	// A sync within the hour installs what the dry run showed...
	u.release(t, "1.3.0", "bin/fake", "bin/fakectl")
	m.newRun(false)
	m.now = m.now.Add(30 * time.Minute)
	wantChange(t, m.sync(m.tool(u, "latest")), resource.Update, "fake 1.1.0 → 1.2.0 (latest)")
	lookups(2)
	// ...checking it again with the same version, as the engine does.
	wantChange(t, m.check(m.tool(u, "latest")), resource.NoChange, "")

	// ...and the next sync looks it up again.
	m.newRun(false)
	m.now = m.now.Add(time.Minute)
	wantChange(t, m.sync(m.tool(u, "latest")), resource.Update, "fake 1.2.0 → 1.3.0 (latest)")
	lookups(3)
	if got := m.entries(".local/share/maisys/tools/fake"); strings.Join(got, " ") != "1.3.0" {
		t.Errorf("the tool's folder holds %q, want only 1.3.0", got)
	}
}

func TestLookupError(t *testing.T) {
	u := newUpstream(t)
	m := newMachine(t, u)
	_, err := m.tool(u, "latest").Check(t.Context(), m.rt)
	le, ok := errors.AsType[*version.LookupError](err)
	if !ok || le.Tool != "fake" {
		t.Fatalf("Check() error = %v, want a *version.LookupError", err)
	}
	if !strings.Contains(err.Error(), "can't find the latest version of fake from "+u.URL+"/fake/fake/releases/latest: ") {
		t.Errorf("Check() error = %q", err)
	}
}

func TestSwitch(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))

	wantChange(t, m.sync(m.tool(u, "1.1.0")), resource.Update, "fake 1.0.0 → 1.1.0")
	if got := m.read(".local/bin/fakectl"); got != "bin/fakectl 1.1.0 x86_64" {
		t.Errorf("~/.local/bin/fakectl runs %q, want 1.1.0", got)
	}
	if m.exists(".local/share/maisys/tools/fake/1.0.0") {
		t.Errorf("the old version is still there after switching")
	}
	if rec, _ := m.state.Get("tool:fake"); rec.Details["version"] != "1.1.0" || rec.Paths[0] != m.toolPath("1.1.0") {
		t.Errorf("record = %+v, want version 1.1.0", rec)
	}

	// Switching back downloads the old version again.
	wantChange(t, m.sync(m.tool(u, "1.0.0")), resource.Update, "fake 1.1.0 → 1.0.0")
	if n := u.count(u.archive("1.0.0")); n != 2 {
		t.Errorf("downloaded 1.0.0 %d times, want twice", n)
	}
}

func TestInterruptedInstall(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)

	// An install that stopped before writing the marker, and one that
	// stopped while unpacking.
	for _, dir := range []string{"1.0.0/bin", ".1.1.0.tmp-123/bin"} {
		if err := os.MkdirAll(m.toolPath(dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(m.toolPath("1.0.0/bin/fake"), []byte("half"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantChange(t, m.sync(m.tool(u, "1.0.0")), resource.Create, "fake 1.0.0")
	if got := m.read(".local/bin/fake"); got != "bin/fake 1.0.0 x86_64" {
		t.Errorf("~/.local/bin/fake runs %q, want a fresh 1.0.0", got)
	}
	if got := m.entries(".local/share/maisys/tools/fake"); strings.Join(got, " ") != "1.0.0" {
		t.Errorf("the tool's folder holds %q, want only 1.0.0", got)
	}
	wantChange(t, m.check(m.tool(u, "1.0.0")), resource.NoChange, "")
}

func TestVersionMissingCommands(t *testing.T) {
	needSymlinks(t)
	tests := []struct {
		name  string
		setup func(t *testing.T, m *machine, u *upstream)
	}{
		{
			// A later maisys fixes the tool's spec, as when an archive's
			// folder is stripped differently: the version an earlier one
			// installed has its marker, but not the files the commands
			// now run.
			name: "installed with an earlier spec",
			setup: func(t *testing.T, m *machine, u *upstream) {
				t.Helper()
				old := m.tool(u, "1.0.0")
				old.Spec.Archive.Strip = 0
				old.Spec.Links = []Link{{Name: "fake", Target: "fake-1.0.0/bin/fake"}, {Name: "fakectl", Target: "fake-1.0.0/bin/fakectl"}}
				m.sync(old)
			},
		},
		{
			name: "a command's file deleted",
			setup: func(t *testing.T, m *machine, u *upstream) {
				t.Helper()
				m.sync(m.tool(u, "1.0.0"))
				if err := os.Remove(m.toolPath("1.0.0/bin/fakectl")); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUpstream(t, "1.0.0")
			m := newMachine(t, u)
			tt.setup(t, m, u)

			wantChange(t, m.sync(m.tool(u, "1.0.0")), resource.Create, "fake 1.0.0")
			for _, cmd := range []string{"fake", "fakectl"} {
				if got, want := m.read(".local/bin/"+cmd), "bin/"+cmd+" 1.0.0 x86_64"; got != want {
					t.Errorf("~/.local/bin/%s runs %q, want %q", cmd, got, want)
				}
			}
			if got := m.entries(".local/share/maisys/tools/fake/1.0.0"); strings.Join(got, " ") != Marker+" bin" {
				t.Errorf("1.0.0's folder holds %q, want a fresh install", got)
			}
			if n := u.count(u.archive("1.0.0")); n != 2 {
				t.Errorf("downloaded 1.0.0 %d times, want twice", n)
			}
			wantChange(t, m.check(m.tool(u, "1.0.0")), resource.NoChange, "")
		})
	}
}

func TestInterruptedSwitch(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))
	// A sync stopped after installing 1.1.0, before pointing the commands
	// at it.
	if err := (download.Archive{Format: download.TarGz, Strip: 1}).Unpack(writeArchive(t, u, "1.1.0"), m.toolPath("1.1.0")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.toolPath("1.1.0/"+Marker), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	wantChange(t, m.sync(m.tool(u, "1.1.0")), resource.Update, "fake 1.0.0 → 1.1.0")
	if n := u.count(u.archive("1.1.0")); n != 0 {
		t.Errorf("downloaded 1.1.0, though it was installed")
	}
	if got := m.link(".local/bin/fake"); got != m.toolPath("1.1.0/bin/fake") {
		t.Errorf("~/.local/bin/fake → %s, want 1.1.0's", got)
	}
	if got := m.entries(".local/share/maisys/tools/fake"); strings.Join(got, " ") != "1.1.0" {
		t.Errorf("the tool's folder holds %q, want only 1.1.0", got)
	}
}

// writeArchive writes the x86_64 archive of version v to a temporary file.
func writeArchive(t *testing.T, u *upstream, v string) string {
	t.Helper()
	u.mu.Lock()
	data := u.files[u.archive(v)]
	u.mu.Unlock()
	path := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLeftovers(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.1.0"))

	// A version left behind by a sync that stopped after switching.
	if err := os.MkdirAll(m.toolPath("1.0.0/bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantChange(t, m.sync(m.tool(u, "latest")), resource.Update, "fake 1.1.0 (delete 1.0.0)")
	if m.exists(".local/share/maisys/tools/fake/1.0.0") {
		t.Errorf("the old version is still there")
	}

	if err := os.MkdirAll(m.toolPath(".1.2.0.tmp-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	wantChange(t, m.sync(m.tool(u, "1.1.0")), resource.Update, "fake 1.1.0 (delete an unfinished install)")
	wantChange(t, m.check(m.tool(u, "1.1.0")), resource.NoChange, "")
}

func TestCommands(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))

	// A command deleted by hand is restored.
	if err := os.Remove(m.path(".local/bin/fakectl")); err != nil {
		t.Fatal(err)
	}
	wantChange(t, m.sync(m.tool(u, "1.0.0")), resource.Update, "fake 1.0.0 (restore commands)")
	if got := m.link(".local/bin/fakectl"); got != m.toolPath("1.0.0/bin/fakectl") {
		t.Errorf("~/.local/bin/fakectl → %q after restoring it", got)
	}

	// Something else at a command's path is backed up and replaced.
	if err := os.Remove(m.path(".local/bin/fakectl")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.path(".local/bin/fakectl"), []byte("mine"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(m.path(".local/bin/fake")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/bin/true", m.path(".local/bin/fake")); err != nil {
		t.Fatal(err)
	}
	c := m.sync(m.tool(u, "1.0.0"))
	wantChange(t, c, resource.Update, "fake 1.0.0 (restore commands)")
	wantNotes := []string{
		"~/.local/bin/fake isn't a link to a maisys tool (will back up and replace)",
		"~/.local/bin/fakectl isn't a link to a maisys tool (will back up and replace)",
	}
	if strings.Join(c.Notes, "\n") != strings.Join(wantNotes, "\n") {
		t.Errorf("Check() notes = %q, want %q", c.Notes, wantNotes)
	}
	if got := m.read(".local/bin/fakectl.maisys-bak.20261106-093000"); got != "mine" {
		t.Errorf("the backup of ~/.local/bin/fakectl holds %q, want mine", got)
	}
	if got := m.link(".local/bin/fake.maisys-bak.20261106-093000"); got != "/usr/bin/true" {
		t.Errorf("the backup of ~/.local/bin/fake → %q, want /usr/bin/true", got)
	}
	wantChange(t, m.check(m.tool(u, "1.0.0")), resource.NoChange, "")
	if got := m.entries(".local/bin"); strings.Join(got, " ") != "fake fake.maisys-bak.20261106-093000 fakectl fakectl.maisys-bak.20261106-093000" {
		t.Errorf("~/.local/bin holds %q", got)
	}
}

func TestDroppedCommands(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))

	// A later maisys drops the fakectl command, and the user points an
	// old command of theirs elsewhere.
	rec, _ := m.state.Get("tool:fake")
	rec.Paths = append(rec.Paths, m.path(".local/bin/oldcmd"))
	m.state.Put(rec)
	if err := os.Symlink("/usr/bin/true", m.path(".local/bin/oldcmd")); err != nil {
		t.Fatal(err)
	}
	tool := m.tool(u, "1.1.0")
	tool.Spec.Links = tool.Spec.Links[:1]
	m.sync(tool)
	if m.exists(".local/bin/fakectl") {
		t.Errorf("the dropped command ~/.local/bin/fakectl is still there")
	}
	if got := m.link(".local/bin/oldcmd"); got != "/usr/bin/true" {
		t.Errorf("~/.local/bin/oldcmd, which isn't the tool's, → %q, want it left alone", got)
	}
	if rec, _ := m.state.Get("tool:fake"); len(rec.Paths) != 2 {
		t.Errorf("record paths = %q, want the version's folder and one command", rec.Paths)
	}
}

func TestChecksumMismatch(t *testing.T) {
	needSymlinks(t)
	u := newUpstream(t, "1.0.0", "1.1.0")
	m := newMachine(t, u)
	m.sync(m.tool(u, "1.0.0"))
	u.set(u.archive("1.1.0"), []byte("tampered"))

	tool := m.tool(u, "1.1.0")
	wantChange(t, m.check(tool), resource.Update, "fake 1.0.0 → 1.1.0")
	_, err := tool.Apply(t.Context(), m.rt)
	if _, ok := errors.AsType[*download.ChecksumError](err); !ok {
		t.Fatalf("Apply() error = %v, want a *download.ChecksumError", err)
	}
	if m.exists(".local/share/maisys/tools/fake/1.1.0") {
		t.Errorf("a download that failed its checksum was installed")
	}
	if got := m.link(".local/bin/fake"); got != m.toolPath("1.0.0/bin/fake") {
		t.Errorf("~/.local/bin/fake → %s, want it still at 1.0.0", got)
	}
}

func TestNoChecksum(t *testing.T) {
	u := newUpstream(t, "0.9.0")
	m := newMachine(t, u)
	u.set("/repos/fake/fake/releases/tags/v0.9.0", []byte(`{"assets": [{"name": "fake-0.9.0-linux-x86_64.tar.gz", "digest": null}]}`))
	tool := m.tool(u, "0.9.0")
	tool.Spec.Checksum = func(v string, _ download.Asset) download.Checksum {
		return download.GitHubDigest{Repo: "fake/fake", Tag: "v" + v, API: u.URL}
	}
	_, err := tool.Check(t.Context(), m.rt)
	want := "fake 0.9.0 has no checksum: its release has no checksums file and predates GitHub's asset digests (June 2025); pin a newer version"
	if err == nil || err.Error() != want {
		t.Errorf("Check() error = %v, want %q", err, want)
	}
}

func TestBrokenDownloads(t *testing.T) {
	needSymlinks(t)
	tests := []struct {
		name    string
		files   []string
		wantErr string
	}{
		{name: "a command missing", files: []string{"bin/fake"}, wantErr: "fake 1.0.0's download has no bin/fakectl"},
		{name: "not unpackable", wantErr: "installing fake 1.0.0: unpacking "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newUpstream(t)
			if tt.files != nil {
				u.release(t, "1.0.0", tt.files...)
			} else {
				u.release(t, "1.0.0")
				data := []byte("not a tar.gz")
				u.set(u.archive("1.0.0"), data)
				sum := sha256.Sum256(data)
				u.set("/fake/fake/releases/download/v1.0.0/checksums.txt", []byte(hex.EncodeToString(sum[:])+"  fake-1.0.0-linux-x86_64.tar.gz\n"))
			}
			m := newMachine(t, u)
			tool := m.tool(u, "1.0.0")
			m.check(tool)
			if _, err := tool.Apply(t.Context(), m.rt); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Apply() error = %v, want one containing %q", err, tt.wantErr)
			}
			if got := m.entries(".local/share/maisys/tools/fake"); len(got) != 0 {
				t.Errorf("the tool's folder holds %q after a failed install, want nothing", got)
			}
			if m.exists(".local/bin/fake") {
				t.Errorf("a failed install made a command")
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	u := newUpstream(t, "1.0.0")
	tests := []struct {
		name    string
		tweak   func(s *Spec, m *machine)
		version string
		wantErr string
	}{
		{name: "unsupported architecture", tweak: func(s *Spec, m *machine) { m.rt.Facts.Arch = config.ARM64; delete(s.Arch, config.ARM64) }, wantErr: "fake has no download for arm64"},
		{name: "invalid version", version: "1.0/../x", wantErr: `"1.0/../x" isn't a version of fake`},
		{name: "no latest", tweak: func(s *Spec, _ *machine) { s.Latest = nil }, version: "latest", wantErr: "fake has no latest version: pin one"},
		{name: "no name", tweak: func(s *Spec, _ *machine) { s.Name = "" }, wantErr: `a tool has an invalid name, ""`},
		{name: "bad name", tweak: func(s *Spec, _ *machine) { s.Name = "../x" }, wantErr: `a tool has an invalid name, "../x"`},
		{name: "no download", tweak: func(s *Spec, _ *machine) { s.Asset = nil }, wantErr: "tool fake has no download or no checksum source"},
		{name: "no commands", tweak: func(s *Spec, _ *machine) { s.Links = nil }, wantErr: "tool fake has no commands"},
		{name: "bad command", tweak: func(s *Spec, _ *machine) { s.Links = []Link{{Name: "x", Target: "../../x"}} }, wantErr: "tool fake has an invalid command, x → ../../x"},
		{name: "hidden command", tweak: func(s *Spec, _ *machine) { s.Links = []Link{{Name: ".x", Target: "x"}} }, wantErr: "tool fake has an invalid command, .x → x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMachine(t, u)
			tool := m.tool(u, cmp.Or(tt.version, "1.0.0"))
			if tt.tweak != nil {
				tt.tweak(&tool.Spec, m)
			}
			if _, err := tool.Check(t.Context(), m.rt); err == nil || err.Error() != tt.wantErr {
				t.Errorf("Check() error = %v, want %q", err, tt.wantErr)
			}
		})
	}

	if _, err := New(u.spec(), "1.0.0", nil).Check(t.Context(), &resource.Runtime{}); err == nil {
		t.Error("Check() without an Env worked")
	}
}

func TestResource(t *testing.T) {
	u := newUpstream(t)
	m := newMachine(t, u)
	tool := m.tool(u, "1.0.0")
	if tool.ID() != "tool:fake" || tool.Kind() != "tool" || tool.Privileged() {
		t.Errorf("ID, Kind, Privileged = %s, %s, %t", tool.ID(), tool.Kind(), tool.Privileged())
	}
	want := []string{m.path(".local/share/maisys/tools/fake"), m.path(".local/bin/fake"), m.path(".local/bin/fakectl")}
	if got := tool.Targets(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("Targets() = %q, want %q", got, want)
	}
}

func TestNewEnv(t *testing.T) {
	paths := settings.Paths{Home: filepath.FromSlash("/home/me"), CacheDir: filepath.FromSlash("/home/me/.cache/maisys")}
	env := NewEnv(paths, "maisys/1.2.3")
	checks := []struct{ got, want string }{
		{env.ToolsDir, "/home/me/.local/share/maisys/tools"},
		{env.BinDir, "/home/me/.local/bin"},
		{env.Client.Dir, "/home/me/.cache/maisys/downloads"},
		{env.Client.Digests.Path, "/home/me/.cache/maisys/digests.json"},
		{env.Latest.Path, "/home/me/.cache/maisys/latest.json"},
	}
	for _, c := range checks {
		if c.got != filepath.FromSlash(c.want) {
			t.Errorf("got %s, want %s", c.got, filepath.FromSlash(c.want))
		}
	}
	if env.Client.UserAgent != "maisys/1.2.3" {
		t.Errorf("UserAgent = %q", env.Client.UserAgent)
	}
	if err := env.Load(); err != nil {
		t.Errorf("Load() without cache files: %v", err)
	}
}
