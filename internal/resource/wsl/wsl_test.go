package wsl

import (
	"archive/tar"
	"bytes"
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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
	"github.com/plombardi89/maisys2/internal/system/systemtest"
)

// fakeRegistry is a Registry tests set up.
type fakeRegistry struct {
	mu        sync.Mutex
	distros   []Registered
	defaultID string
	version   string
	reboot    bool
	gitDir    string
}

func (r *fakeRegistry) Distros() ([]Registered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.distros), nil
}

func (r *fakeRegistry) DefaultDistro() (string, error) { return r.defaultID, nil }
func (r *fakeRegistry) Version() (string, error)       { return r.version, nil }
func (r *fakeRegistry) RebootPending() (bool, error)   { return r.reboot, nil }
func (r *fakeRegistry) GitDir() (string, error)        { return r.gitDir, nil }

// register adds a distro, as wsl --install does.
func (r *fakeRegistry) register(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.distros = append(r.distros, Registered{ID: fmt.Sprintf("{00000000-0000-4000-8000-%012d}", len(r.distros)+1), Name: name, Modern: true})
}

// newRuntime returns a runtime with reg and runner, whose terminal is out.
func newRuntime(reg Registry, runner system.Runner, out *bytes.Buffer) *resource.Runtime {
	rt := &resource.Runtime{Runner: runner, Out: out, Log: &bytes.Buffer{}}
	rt.SetShared(RegistryKey{}, reg)
	return rt
}

func TestLinuxPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{`C:\Users\phil\AppData\Local\maisys\cache`, "/mnt/c/Users/phil/AppData/Local/maisys/cache"},
		{`D:/a/maisys2/dist/`, "/mnt/d/a/maisys2/dist"},
		{`c:\Program Files\Git`, "/mnt/c/Program Files/Git"},
		{`E:\`, "/mnt/e"},
		{`\\server\share`, ""},
		{`relative\path`, ""},
		{"/home/phil", ""},
	}
	for _, tt := range tests {
		got, err := LinuxPath(tt.in)
		if (err != nil) != (tt.want == "") || got != tt.want {
			t.Errorf("LinuxPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestLinuxURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/owner/config.git", "https://github.com/owner/config.git"},
		{`C:\Users\phil\maisys-config`, "/mnt/c/Users/phil/maisys-config"},
		{"file:///D:/a/_temp/fixture", "/mnt/d/a/_temp/fixture"},
		{"file:///D:/a/maisys2/dist", "/mnt/d/a/maisys2/dist"},
	}
	for _, tt := range tests {
		if got, err := LinuxURL(tt.in); err != nil || got != tt.want {
			t.Errorf("LinuxURL(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
	if _, err := LinuxURL("file:///srv/config.git"); err == nil {
		t.Error("LinuxURL of a file URL that isn't on a drive worked")
	}
}

func TestFindGCM(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		want    string
		wantErr string
	}{
		{
			name:  "ucrt64",
			files: []string{`C:\Program Files\Git\ucrt64\bin\git-credential-manager.exe`, `C:\Program Files\Git\mingw64\bin\git-credential-manager.exe`},
			want:  "/mnt/c/Program Files/Git/ucrt64/bin/git-credential-manager.exe",
		},
		{
			name:  "mingw64",
			files: []string{`C:\Program Files\Git\mingw64\bin\git-credential-manager.exe`},
			want:  "/mnt/c/Program Files/Git/mingw64/bin/git-credential-manager.exe",
		},
		{name: "missing", wantErr: `neither C:\Program Files\Git\ucrt64\bin\git-credential-manager.exe nor`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setFiles(t, tt.files...)
			got, err := FindGCM(`C:\Program Files\Git\`)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("FindGCM() error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("FindGCM() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
	if _, err := FindGCM(""); err == nil {
		t.Error("FindGCM without Git for Windows worked")
	}
}

// setFiles makes fileExists find only files, until the test ends.
func setFiles(t *testing.T, files ...string) {
	t.Helper()
	old := fileExists
	fileExists = func(p string) bool { return slices.Contains(files, p) }
	t.Cleanup(func() { fileExists = old })
}

func TestGitConfig(t *testing.T) {
	got := GitConfig(`/mnt/c/Program\ Files/Git/ucrt64/bin/git-credential-manager.exe`)
	want := "[credential]\n\thelper = \"/mnt/c/Program\\\\ Files/Git/ucrt64/bin/git-credential-manager.exe\"\n"
	if got != want {
		t.Errorf("GitConfig() =\n%s\nwant\n%s", got, want)
	}
}

func TestNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"2.6.1", "2.5.10.0", true},
		{"2.6.1", "2.6.1.0", false},
		{"2.6.1.0", "2.6.1", false},
		{"2.5.10", "2.6.1.0", false},
		{"2.10.0", "2.9.9", true},
	}
	for _, tt := range tests {
		if got := newer(tt.a, tt.b); got != tt.want {
			t.Errorf("newer(%q, %q) = %t, want %t", tt.a, tt.b, got, tt.want)
		}
	}
	if got := short("2.5.10.0"); got != "2.5.10" {
		t.Errorf("short() = %q, want 2.5.10", got)
	}
}

func TestInstall(t *testing.T) {
	latest := func(v string, err error) func(context.Context, *resource.Runtime) (string, error) {
		return func(context.Context, *resource.Runtime) (string, error) { return v, err }
	}
	tests := []struct {
		name       string
		installed  string
		latest     string
		lookupErr  error
		reboot     bool
		want       resource.Change
		wantCmd    string
		wantReboot bool
	}{
		{
			name: "missing", latest: "2.6.1",
			want:    resource.Change{Action: resource.Create, Summary: "install WSL"},
			wantCmd: "wsl.exe --install --no-distribution",
		},
		{
			name: "missing, with a reboot", latest: "2.6.1", reboot: true,
			want:    resource.Change{Action: resource.Create, Summary: "install WSL"},
			wantCmd: "wsl.exe --install --no-distribution", wantReboot: true,
		},
		{
			name: "old", installed: "2.5.10.0", latest: "2.6.1",
			want: resource.Change{
				Action: resource.Update, Summary: "update WSL 2.5.10 → 2.6.1",
				Notes: []string{"the WSL update takes effect once WSL restarts: run `wsl --shutdown` to apply"},
			},
			wantCmd: "wsl.exe --update --web-download",
		},
		{name: "latest", installed: "2.6.1.0", latest: "2.6.1"},
		{
			name: "lookup fails", installed: "2.6.1.0", lookupErr: errors.New("503 Service Unavailable"),
			want: resource.Change{Notes: []string{"couldn't check for a newer WSL: 503 Service Unavailable"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := &fakeRegistry{version: tt.installed, reboot: tt.reboot}
			runner := systemtest.NewFakeRunner(t)
			runner.On("wsl.exe --install --no-distribution", func(context.Context, system.Cmd) (system.Result, error) {
				reg.version = "2.6.1.0"
				return system.Result{}, nil
			})
			runner.On("wsl.exe --update --web-download", systemtest.Respond(0, ""))
			rt := newRuntime(reg, runner, nil)
			i := &Install{Latest: latest(tt.latest, tt.lookupErr)}
			if !i.Privileged() {
				t.Error("installing WSL doesn't need admin rights")
			}

			c, err := i.Check(t.Context(), rt)
			if err != nil {
				t.Fatal(err)
			}
			if c.Action != tt.want.Action || c.Summary != tt.want.Summary || !slices.Equal(c.Notes, tt.want.Notes) {
				t.Errorf("Check() = %+v, want %+v", c, tt.want)
			}
			if !c.Action.Applies() {
				return
			}
			_, err = i.Apply(t.Context(), rt)
			if got := runner.Commands(); !slices.Equal(got, []string{tt.wantCmd}) {
				t.Errorf("commands = %q, want %q", got, tt.wantCmd)
			}
			var reboot interface{ RebootRequired() bool }
			if got := errors.As(err, &reboot) && reboot.RebootRequired(); got != tt.wantReboot {
				t.Errorf("Apply() error = %v, want a reboot: %t", err, tt.wantReboot)
			}
			if !tt.wantReboot && err != nil {
				t.Errorf("Apply() error = %v", err)
			}
			for _, call := range runner.Calls() {
				if !slices.Contains(call.Cmd.Env, "WSL_UTF8=1") {
					t.Errorf("%s runs without WSL_UTF8=1", call.Cmd)
				}
			}
		})
	}
}

func TestDefault(t *testing.T) {
	reg := &fakeRegistry{}
	reg.register("ubuntu")
	reg.register("fedora-dev")
	reg.defaultID = reg.distros[0].ID
	runner := systemtest.NewFakeRunner(t)
	runner.On("wsl.exe --set-default fedora-dev", func(context.Context, system.Cmd) (system.Result, error) {
		reg.defaultID = reg.distros[1].ID
		return system.Result{}, nil
	})
	rt := newRuntime(reg, runner, nil)

	d := &Default{Name: "fedora-dev"}
	c, err := d.Check(t.Context(), rt)
	if err != nil || c.Action != resource.Update || c.Summary != "default distro → fedora-dev" {
		t.Fatalf("Check() = %+v, %v; want ~ default distro → fedora-dev", c, err)
	}
	if _, err := d.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	if c, err := d.Check(t.Context(), rt); err != nil || c.Action != resource.NoChange {
		t.Errorf("Check() after Apply() = %+v, %v; want no change", c, err)
	}

	missing := &Default{Name: "scratch"}
	if _, err := missing.Apply(t.Context(), rt); err == nil || !strings.Contains(err.Error(), "there's no distro scratch") {
		t.Errorf("Apply() of a distro that doesn't exist: error = %v", err)
	}
}

// fakeBinary is a Linux maisys in the Windows cache.
type fakeBinary struct{ err error }

const (
	binaryPath = `C:\Users\phil\AppData\Local\maisys\cache\linux\maisys-0.6.0`
	binarySum  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func (b fakeBinary) Path(context.Context) (string, string, error) {
	return binaryPath, binarySum, b.err
}

// distroHost is a Windows host whose runner answers wsl.exe as a new
// distro would.
type distroHost struct {
	reg    *fakeRegistry
	runner *systemtest.FakeRunner
	rt     *resource.Runtime
	out    bytes.Buffer
	// syncErr, setupErr and createErr make those steps fail.
	syncCode, setupCode, createCode int
	// onSync runs during the first sync.
	onSync func()
	// passwordInput is what chpasswd read.
	passwordInput string
}

func newDistroHost(t *testing.T) *distroHost {
	t.Helper()
	setFiles(t, `C:\Program Files\Git\ucrt64\bin\git-credential-manager.exe`)
	h := &distroHost{reg: &fakeRegistry{gitDir: `C:\Program Files\Git`}, runner: systemtest.NewFakeRunner(t)}
	h.runner.On("wsl.exe --install", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if h.createCode != 0 {
			return system.Result{ExitCode: h.createCode, Stderr: []byte("There is no distribution with the supplied name.\n")}, nil
		}
		h.reg.register(c.Args[3])
		return system.Result{}, nil
	})
	root := "wsl.exe -d fedora-dev -u root --cd ~ --exec "
	h.runner.On(root+"sh -c", func(_ context.Context, c system.Cmd) (system.Result, error) {
		switch c.Args[9] {
		case setupScript:
			return system.Result{ExitCode: h.setupCode, Stderr: []byte("useradd: group 'wheel' does not exist\n")}, nil
		case copyScript:
			return system.Result{Stdout: []byte("/home/phil\n")}, nil
		}
		return system.Result{ExitCode: 1}, errors.New("unexpected script")
	})
	h.runner.On(root+"chpasswd", func(_ context.Context, c system.Cmd) (system.Result, error) {
		data, err := readAll(c)
		h.passwordInput = data
		return system.Result{}, err
	})
	h.runner.On(root+"rm -f "+SetupRule, systemtest.Respond(0, ""))
	h.runner.On("wsl.exe --terminate fedora-dev", systemtest.Respond(0, ""))
	h.runner.On("wsl.exe -d fedora-dev -u phil --cd ~ --exec", func(_ context.Context, c system.Cmd) (system.Result, error) {
		if c.Stream != nil {
			_, _ = c.Stream.Write([]byte("Plan for workbox-camwin-fc44 (fedora 44, amd64)\n"))
		}
		if h.onSync != nil {
			h.onSync()
		}
		return system.Result{ExitCode: h.syncCode, Stderr: []byte("packages failed\n")}, nil
	})
	h.rt = newRuntime(h.reg, h.runner, &h.out)
	return h
}

func readAll(c system.Cmd) (string, error) {
	if c.Stdin == nil {
		return "", errors.New("no input")
	}
	var b bytes.Buffer
	_, err := b.ReadFrom(c.Stdin)
	return b.String(), err
}

// newDistro returns the fedora-dev distro of the design's example.
func newDistro() *Distro {
	return &Distro{
		Name:        "fedora-dev",
		Image:       "FedoraLinux-44",
		User:        "phil",
		Machine:     "workbox-camwin-fc44",
		OS:          config.Fedora,
		WSLConf:     []byte("[boot]\nsystemd=true\n\n[user]\ndefault=phil\n"),
		ConfigURL:   "https://github.com/plombardi89/maisys-config.git",
		Binary:      fakeBinary{},
		HelperValue: func(p string) string { return strings.ReplaceAll(p, " ", `\ `) },
		Password:    func() (string, error) { return "S3CRETPASSWORD", nil },
	}
}

func TestDistroCheck(t *testing.T) {
	reg := &fakeRegistry{}
	rt := newRuntime(reg, systemtest.NewFakeRunner(t), nil)
	d := newDistro()
	if !resource.IsIndependent(d) || d.Privileged() {
		t.Error("a distro must be Independent, and not need admin rights")
	}

	c, err := d.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	want := resource.Change{
		Action:  resource.Create,
		Summary: "create distro fedora-dev (FedoraLinux-44), user phil",
		More:    []string{"set up fedora-dev with Linux machine workbox-camwin-fc44"},
	}
	if c.Action != want.Action || c.Summary != want.Summary || !slices.Equal(c.More, want.More) {
		t.Errorf("Check() = %+v, want %+v", c, want)
	}

	reg.register("Fedora-Dev")
	c, err = d.Check(t.Context(), rt)
	if err != nil || c.Action != resource.Present || c.Summary != "distro fedora-dev exists (synced from inside the distro)" || len(c.Notes) > 0 {
		t.Errorf("Check() of a distro that exists = %+v, %v", c, err)
	}

	rt.Previous = func(id string) (state.Record, bool) {
		return state.Record{ID: id, Details: map[string]string{DetailImage: "FedoraLinux-43"}}, id == d.ID()
	}
	c, err = d.Check(t.Context(), rt)
	wantNote := "fedora-dev was created from FedoraLinux-43, and distros are never recreated, so its image stays FedoraLinux-43, not FedoraLinux-44"
	if err != nil || c.Action != resource.Present || !slices.Equal(c.Notes, []string{wantNote}) {
		t.Errorf("Check() of a distro whose image changed = %+v, %v; want the note %q", c, err, wantNote)
	}
	if _, err := d.Apply(t.Context(), rt); err == nil {
		t.Error("Apply() of a distro that exists worked")
	}
}

func TestDistroApply(t *testing.T) {
	h := newDistroHost(t)
	d := newDistro()
	d.ReleaseURL = "file:///mnt/d/a/maisys2/dist"
	rec, err := d.Apply(t.Context(), h.rt)
	if err != nil {
		t.Fatal(err)
	}

	root := "wsl.exe -d fedora-dev -u root --cd '~' --exec "
	wsl := "wsl.exe -d fedora-dev -u phil --cd '~' --exec "
	gitconfig := "[credential]\n\thelper = \"/mnt/c/Program\\\\ Files/Git/ucrt64/bin/git-credential-manager.exe\"\n"
	want := []string{
		"wsl.exe --install FedoraLinux-44 --name fedora-dev --no-launch",
		root + system.Cmd{Name: "sh", Args: []string{"-c", setupScript, "sh", "phil", "wheel", "fedora", string(d.WSLConf), gitconfig, SetupRule}}.String(),
		root + "chpasswd",
		"wsl.exe --terminate fedora-dev",
		root + system.Cmd{Name: "sh", Args: []string{"-c", copyScript, "sh", "phil", "/mnt/c/Users/phil/AppData/Local/maisys/cache/linux/maisys-0.6.0", binarySum}}.String(),
		wsl + "env MAISYS_RELEASE_URL=file:///mnt/d/a/maisys2/dist /home/phil/.local/bin/maisys sync https://github.com/plombardi89/maisys-config.git --machine workbox-camwin-fc44 --yes",
		root + "rm -f " + SetupRule,
	}
	if got := h.runner.Commands(); !slices.Equal(got, want) {
		t.Errorf("commands =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	if h.passwordInput != "phil:S3CRETPASSWORD\n" {
		t.Errorf("chpasswd read %q, want the user and password", h.passwordInput)
	}
	for _, call := range h.runner.Calls() {
		if strings.Contains(call.Cmd.String(), "S3CRETPASSWORD") || strings.Contains(strings.Join(call.Cmd.Env, " "), "S3CRETPASSWORD") {
			t.Errorf("the password is on the command line or in the environment of %s", call.Cmd)
		}
		if call.Cmd.Root {
			t.Errorf("%s runs with sudo", call.Cmd)
		}
		if !slices.Contains(call.Cmd.Env, "WSL_UTF8=1") {
			t.Errorf("%s runs without WSL_UTF8=1", call.Cmd)
		}
	}
	if log, ok := h.rt.Log.(*bytes.Buffer); !ok || strings.Contains(log.String(), "S3CRETPASSWORD") {
		t.Errorf("the password is in the log:\n%s", log)
	}

	out := h.out.String()
	if !strings.Contains(out, "Plan for workbox-camwin-fc44") {
		t.Errorf("the first sync's output wasn't shown:\n%s", out)
	}
	for _, line := range []string{
		"The password of phil in fedora-dev is: S3CRETPASSWORD\n",
		"Change it with: wsl -d fedora-dev passwd\n",
		"If you lose it, reset it with: wsl -d fedora-dev -u root passwd phil\n",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("the terminal doesn't show %q:\n%s", line, out)
		}
	}
	if n := strings.Count(out, "S3CRETPASSWORD"); n != 1 {
		t.Errorf("the password is shown %d times, want once", n)
	}

	wantRec := map[string]string{DetailImage: "FedoraLinux-44", DetailUser: "phil", DetailMachine: "workbox-camwin-fc44"}
	if fmt.Sprint(rec.Details) != fmt.Sprint(wantRec) {
		t.Errorf("record details = %v, want %v", rec.Details, wantRec)
	}
	for _, v := range rec.Details {
		if strings.Contains(v, "S3CRETPASSWORD") {
			t.Error("the password is in the record")
		}
	}
}

// TestDistroUbuntu checks that an Ubuntu distro's user is in the sudo
// group, and that a config repo in a folder needs no Git Credential
// Manager.
func TestDistroUbuntu(t *testing.T) {
	h := newDistroHost(t)
	setFiles(t)
	d := newDistro()
	d.Name, d.OS, d.ConfigURL = "fedora-dev", config.Ubuntu, "/mnt/c/Users/phil/maisys-config"
	if _, err := d.Apply(t.Context(), h.rt); err != nil {
		t.Fatal(err)
	}
	setup := h.runner.Calls()[1].Cmd
	if got := setup.Args[11:14]; !slices.Equal(got, []string{"phil", "sudo", "ubuntu"}) {
		t.Errorf("the setup script's user, group and os are %q", got)
	}
	if gitconfig := setup.Args[15]; gitconfig != "" {
		t.Errorf("the setup script sets credential.helper without Git Credential Manager: %q", gitconfig)
	}

	h = newDistroHost(t)
	setFiles(t)
	d.ConfigURL = "https://github.com/plombardi89/maisys-config.git"
	if _, err := d.Apply(t.Context(), h.rt); err == nil || !strings.Contains(err.Error(), "can't find the Git Credential Manager") {
		t.Errorf("Apply() without Git Credential Manager: error = %v", err)
	}
	if got := h.runner.Commands(); len(got) > 0 {
		t.Errorf("a distro was created that can't clone its config: %q", got)
	}
}

func TestDistroFailures(t *testing.T) {
	root := "wsl.exe -d fedora-dev -u root --cd '~' --exec "
	tests := []struct {
		name     string
		setup    func(h *distroHost)
		binErr   error
		wantErr  []string
		wantCmds int
		// wantRule reports whether the setup rule is deleted at the end.
		wantRule bool
		// wantPassword reports whether the password is shown.
		wantPassword bool
	}{
		{
			name:     "no Linux maisys",
			binErr:   errors.New("this development build of maisys has no release"),
			wantErr:  []string{"this development build of maisys has no release"},
			wantCmds: 0,
		},
		{
			name:     "create fails",
			setup:    func(h *distroHost) { h.createCode = 1 },
			wantErr:  []string{"creating distro fedora-dev failed with exit code 1: There is no distribution with the supplied name."},
			wantCmds: 1,
		},
		{
			name:     "setup fails",
			setup:    func(h *distroHost) { h.setupCode = 1 },
			wantErr:  []string{"setting up fedora-dev as root failed", "fedora-dev was created, but not set up", "wsl --unregister fedora-dev"},
			wantCmds: 3,
			wantRule: true,
		},
		{
			name:         "first sync fails",
			setup:        func(h *distroHost) { h.syncCode = 1 },
			wantErr:      []string{"the first maisys sync in fedora-dev failed with exit code 1: packages failed", "finish setting it up by running maisys sync in it"},
			wantCmds:     7,
			wantRule:     true,
			wantPassword: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newDistroHost(t)
			if tt.setup != nil {
				tt.setup(h)
			}
			d := newDistro()
			d.Binary = fakeBinary{err: tt.binErr}
			_, err := d.Apply(t.Context(), h.rt)
			if err == nil {
				t.Fatal("Apply() worked")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Apply() error =\n%v\nwant it to contain %q", err, want)
				}
			}
			cmds := h.runner.Commands()
			if len(cmds) != tt.wantCmds {
				t.Errorf("commands =\n%s\nwant %d", strings.Join(cmds, "\n"), tt.wantCmds)
			}
			if got := len(cmds) > 0 && cmds[len(cmds)-1] == root+"rm -f "+SetupRule; got != tt.wantRule {
				t.Errorf("the setup rule is deleted at the end: %t, want %t", got, tt.wantRule)
			}
			if got := strings.Contains(h.out.String(), "S3CRETPASSWORD"); got != tt.wantPassword {
				t.Errorf("the password is shown: %t, want %t", got, tt.wantPassword)
			}
		})
	}
}

// TestDistroAborted stops the first sync, as a second Ctrl-C does: the
// setup rule is deleted all the same.
func TestDistroAborted(t *testing.T) {
	h := newDistroHost(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h.onSync = cancel
	h.syncCode = -1
	_, err := newDistro().Apply(ctx, h.rt)
	if err == nil {
		t.Fatal("Apply() worked")
	}
	cmds := h.runner.Commands()
	if last := cmds[len(cmds)-1]; last != "wsl.exe -d fedora-dev -u root --cd '~' --exec rm -f "+SetupRule {
		t.Errorf("the last command is %q, want the setup rule deleted", last)
	}
}

func TestDistroRemover(t *testing.T) {
	reg := &fakeRegistry{}
	reg.register("fedora-dev")
	rt := newRuntime(reg, systemtest.NewFakeRunner(t), nil)
	r := DistroRemover{}
	c, err := r.CheckRemove(t.Context(), rt, state.Record{ID: "wsl-distro:fedora-dev", Kind: DistroKind})
	if err != nil || c.Action != resource.Orphan || c.Summary != "distro fedora-dev" {
		t.Errorf("CheckRemove() = %+v, %v; want an orphan", c, err)
	}
	c, err = r.CheckRemove(t.Context(), rt, state.Record{ID: "wsl-distro:gone", Kind: DistroKind})
	if err != nil || c.Action != resource.NoChange {
		t.Errorf("CheckRemove() of a distro that's gone = %+v, %v; want no change", c, err)
	}
}

func TestLinuxBinary(t *testing.T) {
	bin := []byte("#!/bin/sh\necho maisys 0.6.0\n")
	archive := tarGz(t, map[string][]byte{"maisys": bin, "LICENSE": []byte("MIT")})
	sum := sha256.Sum256(archive)
	dir := t.TempDir()
	name := "maisys_0.6.0_linux_amd64.tar.gz"
	write(t, filepath.Join(dir, name), archive)
	write(t, filepath.Join(dir, "checksums.txt"), fmt.Appendf(nil, "%s  %s\n%s  maisys_0.6.0_windows_amd64.zip\n", hex.EncodeToString(sum[:]), name, strings.Repeat("1", 64)))
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()

	cache := t.TempDir()
	b := &LinuxBinary{ReleaseURL: srv.URL, Version: "0.6.0", CacheDir: cache}
	path, gotSum, err := b.Path(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, bin) {
		t.Errorf("the binary at %s = %q, %v; want %q", path, got, err, bin)
	}
	binSum := sha256.Sum256(bin)
	if gotSum != hex.EncodeToString(binSum[:]) {
		t.Errorf("sum = %s, want the binary's", gotSum)
	}

	// The archive is in the cache now.
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	if again, _, err := b.Path(t.Context()); err != nil || again != path {
		t.Errorf("Path() from the cache = %q, %v; want %q", again, err, path)
	}

	other := &LinuxBinary{ReleaseURL: srv.URL, Version: "0.7.0", CacheDir: cache}
	if _, _, err := other.Path(t.Context()); err == nil || !strings.Contains(err.Error(), "no archive of maisys 0.7.0 for linux/amd64") {
		t.Errorf("Path() of a version the release doesn't have: error = %v", err)
	}

	// A broken download fails its checksum.
	write(t, filepath.Join(dir, "checksums.txt"), fmt.Appendf(nil, "%s  maisys_0.6.1_linux_amd64.tar.gz\n", strings.Repeat("2", 64)))
	write(t, filepath.Join(dir, "maisys_0.6.1_linux_amd64.tar.gz"), archive)
	broken := &LinuxBinary{ReleaseURL: srv.URL, Version: "0.6.1", CacheDir: cache}
	if _, _, err := broken.Path(t.Context()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("Path() of a download with the wrong checksum: error = %v", err)
	}
}

func TestNewLinuxBinary(t *testing.T) {
	// Tests run a development build, which needs MAISYS_RELEASE_URL.
	if _, err := NewLinuxBinary(t.TempDir(), func(string) string { return "" }); err == nil {
		t.Error("NewLinuxBinary() worked for a development build without a release URL")
	}
	b, err := NewLinuxBinary(t.TempDir(), func(name string) string {
		if name == ReleaseURLEnv {
			return "file:///D:/a/maisys2/dist"
		}
		return ""
	})
	if err != nil || b.ReleaseURL != "file:///D:/a/maisys2/dist" || b.Version != "" {
		t.Errorf("NewLinuxBinary() = %+v, %v; want the release URL, and any version", b, err)
	}
}

func tarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
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

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
