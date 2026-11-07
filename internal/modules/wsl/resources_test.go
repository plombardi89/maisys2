package wsl

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/gitconfig"
	"github.com/plombardi89/maisys2/internal/settings"
	"github.com/plombardi89/maisys2/internal/shells"
)

// fakeRoot makes a folder the module finds Git for Windows and systemd
// in, with the files rel.
func fakeRoot(t *testing.T, rel ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, r := range rel {
		p := filepath.Join(dir, filepath.FromSlash(r))
		if strings.HasSuffix(r, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("gcm"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldRoot, oldUser := root, currentUser
	root, currentUser = dir, func() (string, error) { return "phil", nil }
	t.Cleanup(func() { root, currentUser = oldRoot, oldUser })
}

const (
	ucrt64  = "mnt/c/Program Files/Git/ucrt64/bin/git-credential-manager.exe"
	mingw64 = "mnt/c/Program Files/Git/mingw64/bin/git-credential-manager.exe"
	systemd = "run/systemd/system/"
)

func TestResources(t *testing.T) {
	conf := "file:" + file.Display(ConfFile)
	tests := []struct {
		name    string
		system  facts.System
		cfg     Config
		files   []string
		want    []string
		helper  string
		wantErr string
	}{
		{
			name:   "fedora with everything",
			system: facts.System{OS: config.Fedora, WSL: true, WSLDistro: "fedora-dev"},
			cfg:    Config{Enabled: true, Systemd: true, GitCredentialManager: true},
			files:  []string{ucrt64, mingw64, systemd},
			want: []string{
				conf, "repo:whitewaterfoundry_wslu", "package:wsl/wslu", "package:wsl/socat",
				"git-setting:credential.helper", "systemd-unit:user/x410.service",
			},
			helper: `/mnt/c/Program\ Files/Git/ucrt64/bin/git-credential-manager.exe`,
		},
		{
			name:   "ubuntu with WSLg and an older Git for Windows",
			system: facts.System{OS: config.Ubuntu, WSL: true},
			cfg:    Config{Enabled: true, Systemd: true, WSLG: true, GitCredentialManager: true},
			files:  []string{mingw64},
			want:   []string{conf, "git-setting:credential.helper"},
			helper: `/mnt/c/Program\ Files/Git/mingw64/bin/git-credential-manager.exe`,
		},
		{
			name:   "ubuntu with WSLg",
			system: facts.System{OS: config.Ubuntu, WSL: true},
			cfg:    Config{Enabled: true, Systemd: true, WSLG: true},
			want:   []string{conf},
		},
		{
			name:   "no Git for Windows",
			system: facts.System{OS: config.Fedora, WSL: true},
			cfg:    Config{Enabled: true, Systemd: true, WSLG: true, GitCredentialManager: true},
			wantErr: "can't find the Windows Git Credential Manager that wsl.git_credential_manager uses: " +
				"neither /" + ucrt64 + " nor /" + mingw64 + " exists. Install Git for Windows, which includes it, or turn git_credential_manager off",
		},
		{
			name:    "outside WSL",
			system:  facts.System{OS: config.Fedora},
			cfg:     Config{Enabled: true, Systemd: true, WSLG: true},
			wantErr: "the wsl module only works inside WSL, and this isn't a WSL distro",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeRoot(t, tt.files...)
			rs, err := Module{}.Resources(&tt.cfg, &resource.Runtime{Facts: &tt.system})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Resources() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range rs {
				ids = append(ids, r.ID())
				if s, ok := r.(*gitconfig.Setting); ok && !slices.Equal(s.Values, []string{tt.helper}) {
					t.Errorf("credential.helper = %q, want %q", s.Values, tt.helper)
				}
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("resources =\n%q\nwant\n%q", ids, tt.want)
			}
			if !rs[0].Privileged() {
				t.Errorf("%s isn't written as root", ConfFile)
			}
		})
	}
}

func TestConf(t *testing.T) {
	tests := []struct {
		cfg  Config
		want string
	}{
		{
			cfg: Config{Systemd: true, AppendWindowsPath: false},
			want: "# Written by maisys, which replaces any changes made here.\n" +
				"[boot]\nsystemd=true\n\n[user]\ndefault=phil\n\n[interop]\nappendWindowsPath=false\n",
		},
		{
			cfg: Config{Systemd: false, AppendWindowsPath: true},
			want: "# Written by maisys, which replaces any changes made here.\n" +
				"[boot]\nsystemd=false\n\n[user]\ndefault=phil\n\n[interop]\nappendWindowsPath=true\n",
		},
	}
	for _, tt := range tests {
		if got := string(Conf(&tt.cfg, "phil")); got != tt.want {
			t.Errorf("Conf(%+v) =\n%s\nwant\n%s", tt.cfg, got, tt.want)
		}
	}
}

func TestHelperValue(t *testing.T) {
	tests := []struct{ path, want string }{
		{"/mnt/c/Program Files/Git/ucrt64/bin/git-credential-manager.exe", `/mnt/c/Program\ Files/Git/ucrt64/bin/git-credential-manager.exe`},
		{"/mnt/c/Program Files (x86)/Git/bin/gcm.exe", `/mnt/c/Program\ Files\ \(x86\)/Git/bin/gcm.exe`},
		{"/usr/bin/git-credential-libsecret", "/usr/bin/git-credential-libsecret"},
	}
	for _, tt := range tests {
		if got := HelperValue(tt.path); got != tt.want {
			t.Errorf("HelperValue(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestRestartNote(t *testing.T) {
	home := t.TempDir()
	rt := &resource.Runtime{Paths: settings.Paths{Home: home}}
	r := &restartNote{File: &file.File{Path: "~/wsl.conf", Content: file.Bytes([]byte("[boot]\n"))}, distro: "fedora-dev"}
	c, err := r.Check(t.Context(), rt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/etc/wsl.conf takes effect once the distro restarts: run `wsl --terminate fedora-dev` in Windows"}
	if c.Action != resource.Create || !slices.Equal(c.Notes, want) {
		t.Errorf("Check() = %+v, want it created with the note %q", c, want)
	}
	if _, err := r.Apply(t.Context(), rt); err != nil {
		t.Fatal(err)
	}
	if c, err := r.Check(t.Context(), rt); err != nil || c.Action != resource.NoChange || len(c.Notes) > 0 {
		t.Errorf("Check() after applying = %+v, %v, want no change and no note", c, err)
	}
}

func TestBridge(t *testing.T) {
	cfg := &Config{Enabled: true, Systemd: true}
	sys := &facts.System{OS: config.Ubuntu, WSL: true, WSLDistro: "ubuntu"}
	for _, running := range []bool{false, true} {
		var files []string
		if running {
			files = append(files, systemd)
		}
		fakeRoot(t, files...)
		rt := &resource.Runtime{Facts: sys, Paths: settings.Paths{Home: t.TempDir()}}
		rs, err := Module{}.Resources(cfg, rt)
		if err != nil {
			t.Fatal(err)
		}
		b, ok := rs[len(rs)-1].(*bridge)
		if !ok {
			t.Fatalf("the last resource is %s, want the X410 bridge", rs[len(rs)-1].ID())
		}
		if b.Privileged() || !slices.Equal(b.Targets(), []string{"~/.config/systemd/user/x410.service"}) {
			t.Errorf("Privileged, Targets = %v, %q, want a user unit", b.Privileged(), b.Targets())
		}
		c, err := b.Check(t.Context(), rt)
		if err != nil {
			t.Fatal(err)
		}
		if !running {
			want := []string{"the X410 bridge needs systemd, which isn't running yet: run `wsl --terminate ubuntu` in Windows, then maisys sync again"}
			if c.Action != resource.NoChange || !slices.Equal(c.Notes, want) {
				t.Errorf("Check() without systemd = %+v, want no change and the note %q", c, want)
			}
			continue
		}
		if c.Action != resource.Create || c.Summary != "systemd user unit x410.service" {
			t.Errorf("Check() = %+v, want the unit created", c)
		}
	}
	for _, want := range []string{
		"ConditionPathIsSymbolicLink=!/tmp/.X11-unix\n",
		"ExecStart=/usr/bin/socat -b65536 UNIX-LISTEN:/tmp/.X11-unix/X0,fork,mode=0777,unlink-early VSOCK-CONNECT:2:6000\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(x410Unit, want) {
			t.Errorf("the unit doesn't have %q:\n%s", want, x410Unit)
		}
	}
}

func TestShellEnv(t *testing.T) {
	tests := []struct {
		name string
		wsl  bool
		wslg bool
		want []shells.Variable
	}{
		{name: "WSLg", wsl: true, wslg: true, want: []shells.Variable{{Name: "BROWSER", Value: "wslview"}}},
		{name: "X410", wsl: true, want: []shells.Variable{{Name: "BROWSER", Value: "wslview"}, {Name: "DISPLAY", Value: ":0"}}},
		{name: "outside WSL", wslg: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &resource.Runtime{Facts: &facts.System{OS: config.Fedora, WSL: tt.wsl}}
			env, err := Module{}.ShellEnv(&Config{Enabled: true, Systemd: true, WSLG: tt.wslg}, rt)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(env.Variables, tt.want) || len(env.Path) > 0 || len(env.Init) > 0 {
				t.Errorf("ShellEnv() = %+v, want the variables %+v", env, tt.want)
			}
		})
	}
}
