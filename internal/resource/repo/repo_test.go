package repo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// testKey is an ASCII-armored key, shortened.
const testKey = "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmQINBGO9u+MBEADmE9i8rpt8xhRqxbzlBG06z3qe+e1DI+SyjscyVVRcGDrEfo+J\n-----END PGP PUBLIC KEY BLOCK-----\n"

// testMachine is a machine to check and apply repos on: a runtime with a
// fake runner, whose root-owned files go under root.
type testMachine struct {
	t      *testing.T
	rt     *resource.Runtime
	runner *systemtest.FakeRunner
	root   string
}

func newTestMachine(t *testing.T, os config.OS) *testMachine {
	t.Helper()
	m := &testMachine{t: t, runner: systemtest.NewFakeRunner(t), root: t.TempDir()}
	m.rt = &resource.Runtime{Runner: m.runner, Facts: &facts.System{OS: os, OSID: string(os), Arch: config.AMD64, RawArch: "amd64"}}
	return m
}

// installs lets the machine write root-owned files, as InstallFile does
// with install. Files go into folders that exist, as with the real
// install.
func (m *testMachine) installs() {
	m.t.Helper()
	if runtime.GOOS == "windows" {
		m.t.Skip("maisys only writes files as root on Linux")
	}
	m.t.Setenv("TMPDIR", m.t.TempDir())
	m.runner.On("install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if c.Args[0] == "-d" {
			return system.Result{}, os.MkdirAll(c.Args[len(c.Args)-1], 0o755)
		}
		src, dst := c.Args[6], c.Args[7]
		if !isDir(filepath.Dir(dst)) {
			return system.Result{ExitCode: 1, Stderr: fmt.Appendf(nil, "install: cannot create regular file '%s': No such file or directory\n", dst)}, nil
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return system.Result{}, err
		}
		return system.Result{}, os.WriteFile(dst, data, 0o644)
	})
}

// isDir reports whether dir is a folder.
func isDir(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// mkdir creates the folder name under the machine's root.
func (m *testMachine) mkdir(name string) {
	m.t.Helper()
	if err := os.MkdirAll(fsPath(m.root, name), 0o755); err != nil {
		m.t.Fatal(err)
	}
}

// write writes the file name under the machine's root.
func (m *testMachine) write(name, data string) {
	m.t.Helper()
	m.mkdir(filepath.ToSlash(filepath.Dir(name)))
	if err := os.WriteFile(fsPath(m.root, name), []byte(data), 0o644); err != nil {
		m.t.Fatal(err)
	}
}

// read returns the file name under the machine's root, or "" if it's
// missing.
func (m *testMachine) read(name string) string {
	m.t.Helper()
	data, err := os.ReadFile(fsPath(m.root, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		m.t.Fatal(err)
	}
	return string(data)
}

// rootCommands returns the commands run as root since the last call,
// with the machine's root left out of paths, and InstallFile's temporary
// files called TMP.
func (m *testMachine) rootCommands(seen *int) []string {
	all := m.runner.RootCommands()
	cmds := all[*seen:]
	*seen = len(all)
	var out []string
	for _, c := range cmds {
		f := strings.Fields(strings.ReplaceAll(c, m.root, ""))
		if len(f) == 9 && f[0] == "install" && f[1] == "-m" {
			f[7] = "TMP"
		}
		out = append(out, strings.Join(f, " "))
	}
	return out
}

// check checks r, failing the test on an error, and returns the change.
func (m *testMachine) check(r resource.Resource) resource.Change {
	m.t.Helper()
	c, err := r.Check(m.t.Context(), m.rt)
	if err != nil {
		m.t.Fatalf("Check() error = %v", err)
	}
	return c
}

// apply applies r, failing the test on an error, and returns the record.
func (m *testMachine) apply(r resource.Resource) state.Record {
	m.t.Helper()
	rec, err := r.Apply(m.t.Context(), m.rt)
	if err != nil {
		m.t.Fatalf("Apply() error = %v", err)
	}
	return rec
}

// wantChange compares a change with the one wanted.
func wantChange(t *testing.T, step string, got, want resource.Change) {
	t.Helper()
	if got.Action != want.Action || got.Summary != want.Summary {
		t.Errorf("%s: change = %+v, want %+v", step, got, want)
	}
}

// wantRefresh checks that installing a package after a repo was applied
// refreshes apt's lists first: the sources changed.
func (m *testMachine) wantRefresh() {
	m.t.Helper()
	m.runner.On("apt-get update", systemtest.Respond(0, ""))
	m.runner.On("apt-get install", systemtest.Respond(0, ""))
	mgr, err := pkg.ManagerFor(m.rt)
	if err != nil {
		m.t.Fatal(err)
	}
	var seen int
	m.rootCommands(&seen)
	if err := mgr.Install(m.t.Context(), "packer"); err != nil {
		m.t.Fatal(err)
	}
	if cmds := m.rootCommands(&seen); len(cmds) == 0 || cmds[0] != "apt-get update" {
		m.t.Errorf("installing a package next ran %q, want apt-get update first: the sources changed", cmds)
	}
}

// hashicorp returns HashiCorp's apt source, with key.
func hashicorp(m *testMachine, key Key) *AptSource {
	return &AptSource{
		Name:          "hashicorp",
		URIs:          []string{"https://apt.releases.hashicorp.com"},
		Suites:        []string{"noble"},
		Components:    []string{"main"},
		Architectures: []string{"amd64"},
		Key:           key,
		root:          m.root,
	}
}

const hashicorpSource = "# Written by maisys, which replaces any changes made here.\n" +
	"Types: deb\n" +
	"URIs: https://apt.releases.hashicorp.com\n" +
	"Suites: noble\n" +
	"Components: main\n" +
	"Architectures: amd64\n" +
	"Signed-By: /etc/apt/keyrings/hashicorp.asc\n"

func TestAptSource(t *testing.T) {
	m := newTestMachine(t, config.Ubuntu)
	m.installs()
	m.mkdir(sourcesDir)
	src := hashicorp(m, Key{Data: []byte(testKey)})
	var seen int

	if got, want := src.Targets(), []string{"/etc/apt/sources.list.d/hashicorp.sources", "/etc/apt/keyrings/hashicorp.asc"}; !slices.Equal(got, want) {
		t.Errorf("Targets() = %q, want %q", got, want)
	}
	wantChange(t, "first check", m.check(src), resource.Change{Action: resource.Create, Summary: "add repo hashicorp"})
	if rec := m.apply(src); rec.Details["type"] != "apt-source" {
		t.Errorf("record = %+v, want its type", rec)
	}
	if got := m.read(src.sourceFile()); got != hashicorpSource {
		t.Errorf("source file =\n%s\nwant\n%s", got, hashicorpSource)
	}
	if got := m.read(src.keyFile()); got != testKey {
		t.Errorf("key = %q, want %q", got, testKey)
	}
	wantCmds := []string{
		"install -d -m 0755 -o root -g root /etc/apt/keyrings",
		"install -m 0644 -o root -g root TMP /etc/apt/keyrings/hashicorp.asc",
		"install -m 0644 -o root -g root TMP /etc/apt/sources.list.d/hashicorp.sources",
	}
	if got := m.rootCommands(&seen); !slices.Equal(got, wantCmds) {
		t.Errorf("commands run as root =\n%q\nwant\n%q", got, wantCmds)
	}
	wantChange(t, "after applying", m.check(src), resource.Change{})

	m.write(src.sourceFile(), strings.Replace(hashicorpSource, "noble", "jammy", 1))
	wantChange(t, "after the source file changed", m.check(src), resource.Change{Action: resource.Update, Summary: "update repo hashicorp"})
	m.apply(src)
	if got, want := m.rootCommands(&seen), []string{"install -m 0644 -o root -g root TMP /etc/apt/sources.list.d/hashicorp.sources"}; !slices.Equal(got, want) {
		t.Errorf("commands run as root = %q, want only the source file written: %q", got, want)
	}

	src.Key.Data = []byte(strings.Replace(testKey, "mQINB", "mQENB", 1))
	wantChange(t, "after the key changed", m.check(src), resource.Change{Action: resource.Update, Summary: "update repo hashicorp"})
	m.apply(src)
	if got, want := m.rootCommands(&seen), []string{"install -m 0644 -o root -g root TMP /etc/apt/keyrings/hashicorp.asc"}; !slices.Equal(got, want) {
		t.Errorf("commands run as root = %q, want only the key written: %q", got, want)
	}
	wantChange(t, "after applying the key", m.check(src), resource.Change{})
	m.wantRefresh()
}

// keyServer serves key, counting the downloads.
func keyServer(t *testing.T, key string) (url string, downloads *atomic.Int32) {
	t.Helper()
	downloads = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gpg" {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write([]byte(key))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/gpg", downloads
}

func TestAptSourceKeyURL(t *testing.T) {
	m := newTestMachine(t, config.Ubuntu)
	m.installs()
	m.mkdir(sourcesDir)
	url, downloads := keyServer(t, testKey)
	src := hashicorp(m, Key{URL: url, SHA256: strings.ToUpper(sha256Of([]byte(testKey)))})

	wantChange(t, "first check", m.check(src), resource.Change{Action: resource.Create, Summary: "add repo hashicorp"})
	if n := downloads.Load(); n != 0 {
		t.Errorf("the check downloaded the key %d times, want never", n)
	}
	m.apply(src)
	if got := m.read(src.keyFile()); got != testKey || downloads.Load() != 1 {
		t.Errorf("key = %q after %d downloads, want the key, downloaded once", got, downloads.Load())
	}
	wantChange(t, "after applying", m.check(src), resource.Change{})
	if n := downloads.Load(); n != 1 {
		t.Errorf("the key was downloaded %d times, want once: checks compare its sha256", n)
	}
}

func TestAptSourceKeyErrors(t *testing.T) {
	tests := []struct {
		name string
		key  string
		sum  string
		want string
	}{
		{
			name: "the wrong checksum",
			key:  testKey,
			sum:  strings.Repeat("ab", 32),
			want: "downloading the key of repo hashicorp from %s: its sha256 is " + sha256Of([]byte(testKey)) + ", not " + strings.Repeat("ab", 32) + " as expected",
		},
		{
			name: "not an armored key",
			key:  "\x99\x02\x0d\x04",
			sum:  sha256Of([]byte("\x99\x02\x0d\x04")),
			want: "the key of repo hashicorp from %s isn't an ASCII-armored OpenPGP public key",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestMachine(t, config.Ubuntu)
			m.installs()
			m.mkdir(sourcesDir)
			url, _ := keyServer(t, tt.key)
			src := hashicorp(m, Key{URL: url, SHA256: tt.sum})

			_, err := src.Apply(t.Context(), m.rt)
			if want := fmt.Sprintf(tt.want, url); err == nil || err.Error() != want {
				t.Errorf("Apply() error = %v, want %q", err, want)
			}
			if cmds := m.runner.RootCommands(); len(cmds) > 0 {
				t.Errorf("commands run as root = %q, want nothing written", cmds)
			}
		})
	}
}

func TestAptSourceChecks(t *testing.T) {
	tests := []struct {
		name   string
		change func(s *AptSource, m *testMachine)
		want   string
	}{
		{name: "a bad name", change: func(s *AptSource, _ *testMachine) { s.Name = "../hashicorp" }, want: `"../hashicorp" isn't a valid repo name`},
		{name: "on Fedora", change: func(_ *AptSource, m *testMachine) { m.rt.Facts.OS = config.Fedora }, want: "repo hashicorp only works on Ubuntu"},
		{name: "no URIs", change: func(s *AptSource, _ *testMachine) { s.URIs = nil }, want: "repo hashicorp needs URIs and Suites"},
		{name: "no key", change: func(s *AptSource, _ *testMachine) { s.Key = Key{} }, want: "repo hashicorp has no key"},
		{name: "a bad checksum", change: func(s *AptSource, _ *testMachine) { s.Key = Key{URL: "https://example.com/gpg", SHA256: "abc"} }, want: `"abc" isn't a sha256`},
		{name: "a binary key", change: func(s *AptSource, _ *testMachine) { s.Key = Key{Data: []byte("\x99\x02")} }, want: "the key of repo hashicorp isn't an ASCII-armored OpenPGP public key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestMachine(t, config.Ubuntu)
			src := hashicorp(m, Key{Data: []byte(testKey)})
			tt.change(src, m)
			if _, err := src.Check(t.Context(), m.rt); err == nil || err.Error() != tt.want {
				t.Errorf("Check() error = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestAptSourceConflicts checks for other entries of the same repo in
// apt's sources: with a different key, apt fails for everything.
func TestAptSourceConflicts(t *testing.T) {
	const (
		documented = "deb [arch=amd64 signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com noble main\n"
		advice     = ", which apt can't have twice with different keys: delete that entry, then run maisys sync again"
	)
	tests := []struct {
		name string
		file string
		data string
		want string
	}{
		{
			name: "the vendor's documented .list",
			file: "/etc/apt/sources.list.d/hashicorp.list",
			data: documented,
			want: "/etc/apt/sources.list.d/hashicorp.list already has https://apt.releases.hashicorp.com noble" + advice,
		},
		{
			name: "sources.list, over http, with a slash at the end and comments",
			file: "/etc/apt/sources.list",
			data: "# HashiCorp\n\tdeb  [ arch=amd64 ]  http://APT.releases.hashicorp.com/ noble main # added by hand\n",
			want: "/etc/apt/sources.list already has http://APT.releases.hashicorp.com/ noble" + advice,
		},
		{
			name: "a source package entry",
			file: "/etc/apt/sources.list.d/hashicorp-src.list",
			data: "deb-src [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] https://apt.releases.hashicorp.com noble main\n",
			want: "/etc/apt/sources.list.d/hashicorp-src.list already has https://apt.releases.hashicorp.com noble" + advice,
		},
		{
			name: "a deb822 file with several stanzas, URIs and suites",
			file: "/etc/apt/sources.list.d/vendors.sources",
			data: "Types: deb\nURIs: https://download.example.com/apt\nSuites: noble\nComponents: main\nSigned-By: /etc/apt/keyrings/example.asc\n\n" +
				"# HashiCorp\ntypes: deb deb-src\nURIs: https://download.example.org\n https://apt.releases.hashicorp.com/\n" +
				"Suites: jammy noble\nComponents: main\nEnabled: yes\nSigned-By: /usr/share/keyrings/hashicorp-archive-keyring.gpg\n",
			want: "/etc/apt/sources.list.d/vendors.sources already has https://apt.releases.hashicorp.com/ noble" + advice,
		},
		{
			name: "a disabled deb822 stanza",
			file: "/etc/apt/sources.list.d/hashicorp-old.sources",
			data: "Types: deb\nURIs: https://apt.releases.hashicorp.com\nSuites: noble\nComponents: main\nEnabled: no\n" +
				"Signed-By: /usr/share/keyrings/hashicorp-archive-keyring.gpg\n",
		},
		{
			name: "another suite",
			file: "/etc/apt/sources.list.d/hashicorp.list",
			data: strings.Replace(documented, "noble", "jammy", 1),
		},
		{
			name: "another suite, in deb822",
			file: "/etc/apt/sources.list.d/hashicorp-old.sources",
			data: "Types: deb\nURIs: https://apt.releases.hashicorp.com\nSuites: jammy\nComponents: main\n",
		},
		{
			name: "another repo on the same server",
			file: "/etc/apt/sources.list.d/hashicorp-test.list",
			data: strings.Replace(documented, ".com", ".com/test", 1),
		},
		{
			name: "a commented-out entry",
			file: "/etc/apt/sources.list.d/hashicorp.list",
			data: "# " + documented,
		},
		{
			name: "a file apt doesn't read",
			file: "/etc/apt/sources.list.d/hashicorp.list.save",
			data: documented,
		},
		{
			name: "maisys's own file",
			file: "/etc/apt/sources.list.d/hashicorp.sources",
			data: hashicorpSource,
		},
		{
			name: "Ubuntu's own sources",
			file: "/etc/apt/sources.list.d/ubuntu.sources",
			data: "Types: deb\nURIs: http://archive.ubuntu.com/ubuntu/\nSuites: noble noble-updates noble-backports\n" +
				"Components: main restricted universe multiverse\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n\n" +
				"Types: deb\nURIs: http://security.ubuntu.com/ubuntu/\nSuites: noble-security\n" +
				"Components: main restricted universe multiverse\nSigned-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestMachine(t, config.Ubuntu)
			m.write(tt.file, tt.data)
			src := hashicorp(m, Key{Data: []byte(testKey)})

			_, err := src.Check(t.Context(), m.rt)
			var got string
			if err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("Check() error = %v, want %q", err, tt.want)
			}
			if tt.want == "" {
				return
			}
			if _, err := src.Apply(t.Context(), m.rt); err == nil || err.Error() != tt.want {
				t.Errorf("Apply() error = %v, want %q", err, tt.want)
			}
			for _, c := range m.runner.Calls() {
				t.Errorf("Apply() ran %s, want nothing written", c.Cmd)
			}
		})
	}
}

const hashicorpRepo = `[hashicorp]
name=Hashicorp Stable - $basearch
baseurl=https://rpm.releases.hashicorp.com/fedora/$releasever/$basearch/stable
enabled=1
gpgcheck=1
gpgkey=https://rpm.releases.hashicorp.com/gpg
`

func TestDnfRepo(t *testing.T) {
	m := newTestMachine(t, config.Fedora)
	m.installs()
	m.mkdir(reposDir)
	r := &DnfRepo{Name: "hashicorp", Content: hashicorpRepo, root: m.root}
	var seen int

	if got, want := r.Targets(), []string{"/etc/yum.repos.d/hashicorp.repo"}; !slices.Equal(got, want) {
		t.Errorf("Targets() = %q, want %q", got, want)
	}
	wantChange(t, "first check", m.check(r), resource.Change{Action: resource.Create, Summary: "add repo hashicorp"})
	if rec := m.apply(r); rec.Details["type"] != "dnf-repo" {
		t.Errorf("record = %+v, want its type", rec)
	}
	if got := m.read(r.file()); got != hashicorpRepo {
		t.Errorf(".repo file =\n%s\nwant\n%s", got, hashicorpRepo)
	}
	if got, want := m.rootCommands(&seen), []string{"install -m 0644 -o root -g root TMP /etc/yum.repos.d/hashicorp.repo"}; !slices.Equal(got, want) {
		t.Errorf("commands run as root = %q, want %q", got, want)
	}
	wantChange(t, "after applying", m.check(r), resource.Change{})

	m.write(r.file(), strings.Replace(hashicorpRepo, "enabled=1", "enabled=0", 1))
	wantChange(t, "after the file changed", m.check(r), resource.Change{Action: resource.Update, Summary: "update repo hashicorp"})

	for _, bad := range []*DnfRepo{{Name: "hashicorp"}, {Name: "", Content: hashicorpRepo}} {
		if _, err := bad.Check(t.Context(), m.rt); err == nil {
			t.Errorf("Check() of %+v: no error", bad)
		}
	}
	m.rt.Facts.OS = config.Ubuntu
	if _, err := r.Check(t.Context(), m.rt); err == nil || err.Error() != "repo hashicorp only works on Fedora" {
		t.Errorf("Check() on Ubuntu: error = %v, want one saying it only works on Fedora", err)
	}
}

// packageServer serves data at /package, counting the downloads.
func packageServer(t *testing.T, data string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/package" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(data))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/package"
}

func TestReleasePackage(t *testing.T) {
	tests := []struct {
		name    string
		os      config.OS
		pkg     string
		install string // the command that installs it
		ext     string
		missing systemtest.Handler
	}{
		{
			name:    "fedora",
			os:      config.Fedora,
			pkg:     "rpmfusion-free-release",
			install: "dnf install -y",
			ext:     ".rpm",
			missing: systemtest.Respond(1, "package rpmfusion-free-release is not installed\n"),
		},
		{
			name:    "ubuntu",
			os:      config.Ubuntu,
			pkg:     "steam-launcher",
			install: "apt-get install",
			ext:     ".deb",
			missing: systemtest.Fail(1, "dpkg-query: no packages found matching steam-launcher\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestMachine(t, tt.os)
			t.Setenv("TMPDIR", t.TempDir())
			const data = "a package"
			r := &ReleasePackage{Name: "vendor", Package: tt.pkg, URL: packageServer(t, data), SHA256: sha256Of([]byte(data))}
			m.runner.On("rpm -q", tt.missing)
			m.runner.On("dnf -q repoquery", systemtest.Respond(0, ""))
			m.runner.On("dpkg-query -W", tt.missing)
			m.runner.On("apt-cache policy", systemtest.Respond(0, ""))
			m.runner.On("apt-get update", systemtest.Respond(0, ""))
			var installed string
			m.runner.On(tt.install, func(_ context.Context, c system.Cmd) (system.Result, error) {
				file := c.Args[len(c.Args)-1]
				got, err := os.ReadFile(file)
				if err != nil || string(got) != data || !strings.HasSuffix(file, tt.ext) {
					t.Errorf("installing %s: it has %q, %v; want the package, ending with %s", file, got, err, tt.ext)
				}
				installed = file
				return system.Result{}, nil
			})

			wantChange(t, "first check", m.check(r), resource.Change{Action: resource.Create, Summary: "add repo vendor (install " + tt.pkg + ")"})
			rec := m.apply(r)
			if rec.Details["type"] != "release-package" || rec.Details["package"] != tt.pkg {
				t.Errorf("record = %+v, want the type and the package", rec)
			}
			if installed == "" {
				t.Fatal("the package wasn't installed")
			}
			if _, err := os.Stat(installed); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the downloaded package %s is still there", installed)
			}
			if tt.os == config.Ubuntu {
				m.wantRefresh()
			}
		})
	}
}

func TestReleasePackageFind(t *testing.T) {
	m := newTestMachine(t, config.Ubuntu)
	t.Setenv("TMPDIR", t.TempDir())
	const data = "a package"
	url := packageServer(t, data)
	found := 0
	r := &ReleasePackage{Name: "steam", Package: "steam-launcher", Find: func(_ context.Context, rt *resource.Runtime) (string, string, error) {
		if rt != m.rt {
			t.Error("Find got another runtime")
		}
		found++
		return url, sha256Of([]byte(data)), nil
	}}
	m.runner.On("dpkg-query -W", systemtest.Fail(1, "dpkg-query: no packages found matching steam-launcher\n"))
	m.runner.On("apt-cache policy", systemtest.Respond(0, ""))
	m.runner.On("apt-get update", systemtest.Respond(0, ""))
	m.runner.On("apt-get install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		got, err := os.ReadFile(c.Args[len(c.Args)-1])
		if err != nil || string(got) != data {
			t.Errorf("installing: the package has %q, %v; want the one found", got, err)
		}
		return system.Result{}, nil
	})

	wantChange(t, "check", m.check(r), resource.Change{Action: resource.Create, Summary: "add repo steam (install steam-launcher)"})
	if found != 0 {
		t.Errorf("the check looked the package up %d times, want never", found)
	}
	m.apply(r)
	if found != 1 {
		t.Errorf("applying looked the package up %d times, want once", found)
	}

	r.Find = func(context.Context, *resource.Runtime) (string, string, error) {
		return "", "", errors.New("valve's index has no steam-launcher")
	}
	if _, err := r.Apply(t.Context(), m.rt); err == nil || err.Error() != "finding steam-launcher: valve's index has no steam-launcher" {
		t.Errorf("Apply() error = %v, want the lookup's", err)
	}
	r.Find = func(context.Context, *resource.Runtime) (string, string, error) { return url, "abc", nil }
	if _, err := r.Apply(t.Context(), m.rt); err == nil || err.Error() != `finding steam-launcher: "abc" isn't a sha256` {
		t.Errorf("Apply() error = %v, want the bad sha256", err)
	}
}

func TestReleasePackageErrors(t *testing.T) {
	m := newTestMachine(t, config.Fedora)
	url := packageServer(t, "a package")
	r := &ReleasePackage{Name: "rpmfusion-free", Package: "rpmfusion-free-release", URL: url, SHA256: strings.Repeat("0", 64)}
	_, err := r.Apply(t.Context(), m.rt)
	if want := "downloading rpmfusion-free-release from " + url + ": its sha256 is "; err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("Apply() error = %v, want it to start with %q", err, want)
	}
	if len(m.runner.Calls()) > 0 {
		t.Errorf("ran %q, want nothing installed", m.runner.Commands())
	}

	r.URL += "/missing"
	r.SHA256 = sha256Of([]byte("a package"))
	if _, err := r.Apply(t.Context(), m.rt); err == nil || !strings.HasSuffix(err.Error(), ": the server said 404 Not Found") {
		t.Errorf("Apply() error = %v, want the server's", err)
	}

	for _, bad := range []*ReleasePackage{
		{Name: "rpmfusion-free", URL: url, SHA256: r.SHA256},
		{Name: "rpmfusion-free", Package: "rpmfusion-free-release", URL: url},
	} {
		if _, err := bad.Check(t.Context(), m.rt); err == nil {
			t.Errorf("Check() of %+v: no error", bad)
		}
	}
}

func TestArchitecture(t *testing.T) {
	m := newTestMachine(t, config.Ubuntu)
	foreign := ""
	m.runner.On("dpkg --print-foreign-architectures", func(context.Context, system.Cmd) (system.Result, error) {
		return system.Result{Stdout: []byte(foreign)}, nil
	})
	m.runner.On("dpkg --add-architecture", func(context.Context, system.Cmd) (system.Result, error) {
		foreign = "i386\n"
		return system.Result{}, nil
	})
	a := &Architecture{Name: "i386"}
	var seen int

	wantChange(t, "first check", m.check(a), resource.Change{Action: resource.Create, Summary: "add architecture i386"})
	if rec := m.apply(a); rec.Details["architecture"] != "i386" {
		t.Errorf("record = %+v, want the architecture", rec)
	}
	if got, want := m.rootCommands(&seen), []string{"dpkg --add-architecture i386"}; !slices.Equal(got, want) {
		t.Errorf("commands run as root = %q, want %q", got, want)
	}
	wantChange(t, "after applying", m.check(a), resource.Change{})
	m.wantRefresh()

	calls := len(m.runner.Calls())
	wantChange(t, "the machine's own", m.check(&Architecture{Name: "amd64"}), resource.Change{})
	if len(m.runner.Calls()) != calls {
		t.Error("checking the machine's own architecture ran a command")
	}

	if _, err := (&Architecture{Name: "i 386"}).Check(t.Context(), m.rt); err == nil {
		t.Error("Check() of a bad name: no error")
	}
	m.rt.Facts.OS = config.Fedora
	if _, err := a.Check(t.Context(), m.rt); err == nil || err.Error() != "extra package architectures only work on Ubuntu" {
		t.Errorf("Check() on Fedora: error = %v, want one saying it only works on Ubuntu", err)
	}
}

func TestRemovers(t *testing.T) {
	sourcePaths := []string{"/etc/apt/sources.list.d/hashicorp.sources", "/etc/apt/keyrings/hashicorp.asc"}
	tests := []struct {
		name    string
		os      config.OS
		remover resource.Remover
		rec     state.Record
		setup   func(m *testMachine)
		want    resource.Change
	}{
		{
			name: "an apt source",
			os:   config.Ubuntu,
			rec:  state.Record{ID: "repo:hashicorp", Kind: Kind, Paths: sourcePaths, Details: map[string]string{"type": "apt-source"}},
			setup: func(m *testMachine) {
				m.write(sourcePaths[0], hashicorpSource)
			},
			want: resource.Change{Action: resource.Orphan, Summary: "repo hashicorp"},
		},
		{
			name:  "an apt source that's gone",
			os:    config.Ubuntu,
			rec:   state.Record{ID: "repo:hashicorp", Kind: Kind, Paths: sourcePaths, Details: map[string]string{"type": "apt-source"}},
			setup: func(*testMachine) {},
		},
		{
			name: "a dnf repo",
			os:   config.Fedora,
			rec:  state.Record{ID: "repo:hashicorp", Kind: Kind, Paths: []string{"/etc/yum.repos.d/hashicorp.repo"}, Details: map[string]string{"type": "dnf-repo"}},
			setup: func(m *testMachine) {
				m.write("/etc/yum.repos.d/hashicorp.repo", hashicorpRepo)
			},
			want: resource.Change{Action: resource.Orphan, Summary: "repo hashicorp"},
		},
		{
			name: "a release package",
			os:   config.Fedora,
			rec:  state.Record{ID: "repo:rpmfusion-free", Kind: Kind, Details: map[string]string{"type": "release-package", "package": "rpmfusion-free-release"}},
			setup: func(m *testMachine) {
				m.runner.On("rpm -q", systemtest.Respond(0, "rpmfusion-free-release\tnoarch\t44-1\n"))
				m.runner.On("dnf -q check-update", systemtest.Respond(0, ""))
			},
			want: resource.Change{Action: resource.Orphan, Summary: "repo rpmfusion-free"},
		},
		{
			name: "a release package that's gone",
			os:   config.Fedora,
			rec:  state.Record{ID: "repo:rpmfusion-free", Kind: Kind, Details: map[string]string{"type": "release-package", "package": "rpmfusion-free-release"}},
			setup: func(m *testMachine) {
				m.runner.On("rpm -q", systemtest.Respond(1, "package rpmfusion-free-release is not installed\n"))
				m.runner.On("dnf -q repoquery", systemtest.Respond(0, ""))
			},
		},
		{
			name:    "an architecture",
			os:      config.Ubuntu,
			remover: ArchRemover{},
			rec:     state.Record{ID: "dpkg-arch:i386", Kind: ArchKind, Details: map[string]string{"architecture": "i386"}},
			setup: func(m *testMachine) {
				m.runner.On("dpkg --print-foreign-architectures", systemtest.Respond(0, "i386\n"))
			},
			want: resource.Change{Action: resource.Orphan, Summary: "architecture i386"},
		},
		{
			name:    "an architecture that's gone",
			os:      config.Ubuntu,
			remover: ArchRemover{},
			rec:     state.Record{ID: "dpkg-arch:i386", Kind: ArchKind},
			setup: func(m *testMachine) {
				m.runner.On("dpkg --print-foreign-architectures", systemtest.Respond(0, ""))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestMachine(t, tt.os)
			tt.setup(m)
			remover := tt.remover
			if remover == nil {
				remover = Remover{root: m.root}
			}
			c, err := remover.CheckRemove(t.Context(), m.rt, tt.rec)
			if err != nil {
				t.Fatal(err)
			}
			wantChange(t, "CheckRemove()", c, tt.want)
			if err := remover.Remove(t.Context(), m.rt, tt.rec); err == nil {
				t.Error("Remove() worked, want an error: maisys never removes repos")
			}
		})
	}
	if (Remover{}).Kind() != Kind || (ArchRemover{}).Kind() != ArchKind {
		t.Error("the removers' kinds are wrong")
	}
}
