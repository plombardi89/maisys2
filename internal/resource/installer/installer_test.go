package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

const testScript = "#!/bin/bash\necho installing\n"

// server serves an installer script, failing the first fails requests.
type server struct {
	*httptest.Server
	mu       sync.Mutex
	fails    int
	requests int
}

func newServer(t *testing.T, fails int) *server {
	t.Helper()
	s := &server{fails: fails}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		fail := s.requests <= s.fails
		s.mu.Unlock()
		switch {
		case r.URL.Path != "/install":
			http.NotFound(w, r)
		case fail:
			http.Error(w, "try again", http.StatusServiceUnavailable)
		default:
			_, _ = w.Write([]byte(testScript))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// fakeInstall is an install whose completeness the tests control.
type fakeInstall struct {
	mu       sync.Mutex
	complete bool
	reason   string
	err      error
}

func (f *fakeInstall) check(context.Context, *resource.Runtime) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Status{Complete: f.complete, Reason: f.reason}, f.err
}

func (f *fakeInstall) set(complete bool, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.complete, f.reason = complete, reason
}

// fixture is an installer for "sdkman" with a fake runner, server and
// install.
type fixture struct {
	in      *Installer
	runner  *systemtest.FakeRunner
	server  *server
	install *fakeInstall
	rt      *resource.Runtime
	home    string
	sleeps  []time.Duration
}

func newFixture(t *testing.T, fails int) *fixture {
	t.Helper()
	home := t.TempDir()
	f := &fixture{
		runner:  systemtest.NewFakeRunner(t),
		server:  newServer(t, fails),
		install: &fakeInstall{},
		home:    home,
	}
	f.rt = &resource.Runtime{
		Runner: f.runner,
		Paths:  settings.Paths{Home: home, CacheDir: filepath.Join(home, ".cache", "maisys")},
		Now:    time.Now,
	}
	f.in = &Installer{
		Name:      "sdkman",
		URL:       f.server.URL + "/install",
		Shell:     "bash",
		Args:      []string{"--quiet"},
		Env:       []string{"SDKMAN_DIR=" + filepath.Join(home, ".sdkman")},
		Complete:  f.install.check,
		Leftovers: []string{filepath.Join(home, ".sdkman")},
		Paths:     []string{filepath.Join(home, ".sdkman")},
		Client:    f.server.Client(),
		Sleep: func(_ context.Context, d time.Duration) error {
			f.sleeps = append(f.sleeps, d)
			return nil
		},
	}
	return f
}

// onRun answers the installer's runs with run, which gets the attempt's
// number from 1, after checking that the script was downloaded.
func (f *fixture) onRun(t *testing.T, run func(attempt int) (system.Result, error)) {
	t.Helper()
	attempt := 0
	f.runner.On("bash", func(_ context.Context, c system.Cmd) (system.Result, error) {
		attempt++
		if len(c.Args) != 2 || c.Args[1] != "--quiet" {
			t.Errorf("ran %s, want bash <script> --quiet", c)
		}
		if data, err := os.ReadFile(c.Args[0]); err != nil || string(data) != testScript {
			t.Errorf("the script = %q (%v), want the downloaded one", data, err)
		}
		if !slices.Contains(c.Env, "SDKMAN_DIR="+filepath.Join(f.home, ".sdkman")) {
			t.Errorf("the script's environment = %q, want SDKMAN_DIR", c.Env)
		}
		return run(attempt)
	})
}

func (f *fixture) leftover() string { return filepath.Join(f.home, ".sdkman") }

func TestCheck(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		reason   string
		leftover bool
		want     resource.Change
	}{
		{name: "complete", complete: true, leftover: true, want: resource.Change{}},
		{name: "missing", want: resource.Change{Action: resource.Create, Summary: "install sdkman"}},
		{
			name:   "missing, with a reason",
			reason: "opencode 1.4.2 is older than 2",
			want:   resource.Change{Action: resource.Create, Summary: "install sdkman (opencode 1.4.2 is older than 2)"},
		},
		{
			name:     "half-finished",
			leftover: true,
			want: resource.Change{
				Action:  resource.Update,
				Summary: "repair sdkman (half-finished install)",
				Notes:   []string{"~/.sdkman will be deleted first"},
			},
		},
		{
			name:     "half-finished, with a reason",
			reason:   "sdk doesn't load",
			leftover: true,
			want: resource.Change{
				Action:  resource.Update,
				Summary: "repair sdkman (sdk doesn't load)",
				Notes:   []string{"~/.sdkman will be deleted first"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, 0)
			f.install.set(tt.complete, tt.reason)
			if tt.leftover {
				writeFile(t, filepath.Join(f.leftover(), "var", "version"), "5.19.0")
			}
			got, err := f.in.Check(t.Context(), f.rt)
			if err != nil {
				t.Fatal(err)
			}
			assertChange(t, got, tt.want)
			if cmds := f.runner.Commands(); cmds != nil || f.server.count() != 0 {
				t.Errorf("Check() ran %q and downloaded %d times, want neither", cmds, f.server.count())
			}
		})
	}
}

func TestCheckErrors(t *testing.T) {
	broken := errors.New("can't tell")
	tests := []struct {
		name   string
		change func(in *Installer, install *fakeInstall)
		want   string
	}{
		{"no name", func(in *Installer, _ *fakeInstall) { in.Name = "" }, "an installer has no name"},
		{"http", func(in *Installer, _ *fakeInstall) { in.URL = "http://example.com/install" }, `sdkman: the installer's URL "http://example.com/install" isn't an HTTPS URL`},
		{"bad sha256", func(in *Installer, _ *fakeInstall) { in.SHA256 = "abc" }, `sdkman: "abc" isn't a sha256`},
		{"no check", func(in *Installer, _ *fakeInstall) { in.Complete = nil }, "sdkman: the installer has no completeness check"},
		{"relative leftover", func(in *Installer, _ *fakeInstall) { in.Leftovers = []string{".sdkman"} }, `sdkman: ".sdkman" isn't an absolute path`},
		{"check fails", func(_ *Installer, install *fakeInstall) { install.err = broken }, "can't tell"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, 0)
			tt.change(f.in, f.install)
			_, err := f.in.Check(t.Context(), f.rt)
			if err == nil || err.Error() != tt.want {
				t.Errorf("Check() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestApply(t *testing.T) {
	f := newFixture(t, 0)
	f.in.SHA256 = strings.ToUpper(sha(testScript))
	writeFile(t, filepath.Join(f.leftover(), "half"), "")
	f.onRun(t, func(int) (system.Result, error) {
		if _, err := os.Stat(filepath.Join(f.leftover(), "half")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the half-finished install wasn't deleted before the script ran (%v)", err)
		}
		f.install.set(true, "")
		return system.Result{Stdout: []byte("installed\n")}, nil
	})

	rec, err := f.in.Apply(t.Context(), f.rt)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"name": "sdkman", "url": f.in.URL, "sha256": sha(testScript)}
	if !maps.Equal(rec.Details, want) || !slices.Equal(rec.Paths, f.in.Paths) {
		t.Errorf("Apply() = %+v, want details %v and paths %q", rec, want, f.in.Paths)
	}
	if f.sleeps != nil || f.server.count() != 1 {
		t.Errorf("Apply() slept %v and downloaded %d times, want one download and no sleep", f.sleeps, f.server.count())
	}
	assertNoScripts(t, f.rt.Paths.CacheDir)
}

func TestApplyRetries(t *testing.T) {
	f := newFixture(t, 1)
	f.onRun(t, func(attempt int) (system.Result, error) {
		if _, err := os.Stat(filepath.Join(f.leftover(), "half")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("attempt %d: the half-finished install wasn't deleted (%v)", attempt, err)
		}
		writeFile(t, filepath.Join(f.leftover(), "half"), "")
		switch attempt {
		case 1:
			return system.Result{ExitCode: 1, Stderr: []byte("curl: (56) connection reset\n")}, nil
		case 2:
			return system.Result{Stdout: []byte("done, maybe\n")}, nil
		default:
			f.install.set(true, "")
			return system.Result{}, nil
		}
	})

	// The first download fails, so the first attempt doesn't run the
	// script: it runs on attempts 2, 3 and 4.
	f.in.Attempts = 4
	if _, err := f.in.Apply(t.Context(), f.rt); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}; !slices.Equal(f.sleeps, want) {
		t.Errorf("Apply() slept %v, want %v", f.sleeps, want)
	}
	if got := f.server.count(); got != 2 {
		t.Errorf("Apply() downloaded %d times, want 2", got)
	}
	if _, err := os.Stat(filepath.Join(f.leftover(), "half")); err != nil {
		t.Errorf("Apply() deleted the complete install: %v", err)
	}
}

func TestApplyNeverCompletes(t *testing.T) {
	f := newFixture(t, 0)
	f.in.Attempts = 2
	f.install.set(false, "sdk doesn't load")
	f.onRun(t, func(attempt int) (system.Result, error) {
		writeFile(t, filepath.Join(f.leftover(), "half"), "")
		out := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\n\nattempt " + string(rune('0'+attempt)) + "\n"
		return system.Result{Stdout: []byte(out)}, nil
	})

	_, err := f.in.Apply(t.Context(), f.rt)
	want := "installing sdkman failed after 2 attempts: the sdkman installer finished, but the install isn't complete (sdk doesn't load). " +
		"Its output ended with:\n  line 3\n  line 4\n  line 5\n  line 6\n  attempt 2"
	if err == nil || err.Error() != want {
		t.Errorf("Apply() error = %v, want %q", err, want)
	}
	if _, err := os.Stat(f.leftover()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Apply() left the half-finished install behind (%v)", err)
	}
	if want := []time.Duration{5 * time.Second}; !slices.Equal(f.sleeps, want) {
		t.Errorf("Apply() slept %v, want %v", f.sleeps, want)
	}
	assertNoScripts(t, f.rt.Paths.CacheDir)
}

func TestApplyScriptFails(t *testing.T) {
	f := newFixture(t, 0)
	f.in.Attempts = 1
	f.onRun(t, func(int) (system.Result, error) {
		return system.Result{ExitCode: 1, Stderr: []byte("Looking for unzip...\nNot found.\n")}, nil
	})

	_, err := f.in.Apply(t.Context(), f.rt)
	want := "installing sdkman failed: the sdkman installer failed with exit code 1:\n  Looking for unzip...\n  Not found."
	if err == nil || err.Error() != want {
		t.Errorf("Apply() error = %v, want %q", err, want)
	}
	if ee, ok := errors.AsType[*system.ExitError](err); !ok || ee.Code != 1 {
		t.Errorf("Apply() error = %#v, want one wrapping the *system.ExitError", err)
	}
}

func TestApplyWrongChecksum(t *testing.T) {
	f := newFixture(t, 0)
	f.in.SHA256 = sha("another script")
	writeFile(t, filepath.Join(f.leftover(), "candidates", "java", "current"), "21")
	_, err := f.in.Apply(t.Context(), f.rt)
	if !errors.Is(err, errChecksum) || !strings.Contains(err.Error(), "has sha256 "+sha(testScript)+", not "+sha("another script")) {
		t.Errorf("Apply() error = %v, want a checksum error", err)
	}
	if f.sleeps != nil || f.server.count() != 1 {
		t.Errorf("Apply() slept %v and downloaded %d times, want no retries", f.sleeps, f.server.count())
	}
	if cmds := f.runner.Commands(); cmds != nil {
		t.Errorf("Apply() ran %q, want nothing", cmds)
	}
	assertContent(t, filepath.Join(f.leftover(), "candidates", "java", "current"), "21")
}

func TestApplyDownloadFails(t *testing.T) {
	f := newFixture(t, 0)
	f.in.URL = f.server.URL + "/missing"
	writeFile(t, filepath.Join(f.leftover(), "candidates", "java", "current"), "21")
	_, err := f.in.Apply(t.Context(), f.rt)
	want := "installing sdkman failed after 3 attempts: couldn't download the sdkman installer from " + f.in.URL + ": 404 Not Found"
	if err == nil || err.Error() != want {
		t.Errorf("Apply() error = %v, want %q", err, want)
	}
	if got := f.server.count(); got != 3 {
		t.Errorf("Apply() downloaded %d times, want 3", got)
	}
	// The script never ran, so the install it would have repaired stays.
	assertContent(t, filepath.Join(f.leftover(), "candidates", "java", "current"), "21")
}

func TestApplyCancelled(t *testing.T) {
	f := newFixture(t, 0)
	f.in.Sleep = nil
	ctx, cancel := context.WithCancel(t.Context())
	f.onRun(t, func(int) (system.Result, error) {
		cancel()
		return system.Result{ExitCode: 1}, nil
	})
	start := time.Now()
	_, err := f.in.Apply(ctx, f.rt)
	if err == nil || !strings.HasPrefix(err.Error(), "installing sdkman failed: ") {
		t.Errorf("Apply() error = %v, want one after a single attempt", err)
	}
	if time.Since(start) > 4*time.Second {
		t.Error("Apply() waited to try again after its context was cancelled")
	}
}

func TestDefaultBackoff(t *testing.T) {
	want := map[int]time.Duration{2: 5 * time.Second, 3: 10 * time.Second, 4: 20 * time.Second, 5: 40 * time.Second, 6: time.Minute, 20: time.Minute}
	for attempt, d := range want {
		if got := DefaultBackoff(attempt); got != d {
			t.Errorf("DefaultBackoff(%d) = %s, want %s", attempt, got, d)
		}
	}
}

func TestRemover(t *testing.T) {
	r := Remover{}
	if r.Kind() != Kind {
		t.Errorf("Kind() = %q, want %q", r.Kind(), Kind)
	}
	home := t.TempDir()
	there, gone := filepath.Join(home, ".sdkman"), filepath.Join(home, ".gone")
	writeFile(t, filepath.Join(there, "var", "version"), "5.19.0")
	orphan := resource.Change{Action: resource.Orphan, Summary: "sdkman"}
	tests := []struct {
		name string
		rec  state.Record
		want resource.Change
	}{
		{"there", state.Record{Details: map[string]string{"name": "sdkman"}, Paths: []string{there}}, orphan},
		{"there, without a name", state.Record{Paths: []string{there}}, orphan},
		{"partly there", state.Record{Paths: []string{gone, there}}, orphan},
		{"no paths", state.Record{}, orphan},
		{"a relative path", state.Record{Paths: []string{".gone"}}, orphan},
		{"gone", state.Record{Details: map[string]string{"name": "sdkman"}, Paths: []string{gone}}, resource.Change{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tt.rec
			rec.ID, rec.Kind = "installer:sdkman", Kind
			got, err := r.CheckRemove(t.Context(), &resource.Runtime{}, rec)
			if err != nil {
				t.Fatal(err)
			}
			assertChange(t, got, tt.want)
			if err := r.Remove(t.Context(), &resource.Runtime{}, rec); err != nil {
				t.Errorf("Remove() error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(there, "var", "version")); err != nil {
				t.Errorf("Remove() deleted the install: %v", err)
			}
		})
	}
}

func TestResource(t *testing.T) {
	in := &Installer{Name: "opencode", Paths: []string{"/home/u/.opencode"}}
	if in.ID() != "installer:opencode" || in.Kind() != Kind || in.Privileged() || !slices.Equal(in.Targets(), in.Paths) {
		t.Errorf("ID, Kind, Privileged, Targets = %q, %q, %t, %q", in.ID(), in.Kind(), in.Privileged(), in.Targets())
	}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertChange(t *testing.T, got, want resource.Change) {
	t.Helper()
	if got.Action != want.Action || got.Summary != want.Summary || !slices.Equal(got.Notes, want.Notes) {
		t.Errorf("change = %s %q %q, want %s %q %q", got.Action, got.Summary, got.Notes, want.Action, want.Summary, want.Notes)
	}
}

func assertContent(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil || string(got) != want {
		t.Errorf("%s holds %q (%v), want %q", name, got, err, want)
	}
}

// assertNoScripts checks that no downloaded script was left in dir.
func assertNoScripts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("%s was left behind", filepath.Join(dir, e.Name()))
	}
}
