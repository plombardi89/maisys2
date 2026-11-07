package wsl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/gitconfig"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/repo"
	"github.com/plombardi89/maisys2/internal/resource/unit"
	"github.com/plombardi89/maisys2/internal/shells"
)

var (
	_ engine.Module      = Module{}
	_ shells.Contributor = Module{}
)

// ConfFile is the distro's WSL settings file.
const ConfFile = "/etc/wsl.conf"

// gcmDirs are the folders of Git for Windows that may hold Git Credential
// Manager, as WSL sees them, newest first: Git for Windows 2.56 moved it
// from mingw64 to ucrt64.
var gcmDirs = []string{
	"/mnt/c/Program Files/Git/ucrt64/bin",
	"/mnt/c/Program Files/Git/mingw64/bin",
}

// x410Unit is the X410 bridge: a systemd user service that forwards X11's
// display :0 to X410 on Windows over VSOCK, as the Ansible setup's
// x410-vsock service did (port 6000 is X410's display 0). It's skipped
// while WSLg links /tmp/.X11-unix to its own X server.
const x410Unit = `# Written by maisys, which replaces any changes made here.
[Unit]
Description=Bridge from X11 display :0 to X410 on Windows
ConditionPathIsSymbolicLink=!/tmp/.X11-unix

[Service]
ExecStartPre=/usr/bin/mkdir -p /tmp/.X11-unix
ExecStart=/usr/bin/socat -b65536 UNIX-LISTEN:/tmp/.X11-unix/X0,fork,mode=0777,unlink-early VSOCK-CONNECT:2:6000
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`

// wsluRepo is the repo wslu comes from on Fedora, which doesn't have it:
// the one wslu's maintainers publish on packagecloud, which the Ansible
// setup added too, in the same file.
const wsluRepo = `[whitewaterfoundry_wslu]
name=whitewaterfoundry_wslu
baseurl=https://packagecloud.io/whitewaterfoundry/wslu/fedora/$releasever/$basearch
repo_gpgcheck=1
gpgcheck=0
enabled=1
gpgkey=https://packagecloud.io/whitewaterfoundry/wslu/gpgkey
sslverify=1
metadata_expire=300
`

// Tests replace these.
var (
	// currentUser returns the name of the user maisys runs as.
	currentUser = defaultCurrentUser
	// root is where the file system starts, for finding Git for Windows
	// and whether systemd runs.
	root = string(filepath.Separator)
)

func defaultCurrentUser() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("finding your user name: %w", err)
	}
	return u.Username, nil
}

// DependsOn returns nothing.
func (Module) DependsOn() []string { return nil }

// Resources returns, in order: /etc/wsl.conf; wslu on Fedora, from its
// maintainers' repo, and socat for the X410 bridge; git's credential.helper
// set to the Windows Git Credential Manager, if git_credential_manager is
// on; and the X410 bridge, if wslg is off. It fails outside WSL, and if
// git_credential_manager is on but Git for Windows isn't installed.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the wsl module can't use a %T config", cfg)
	}
	if rt.Facts == nil || !rt.Facts.WSL {
		return nil, errors.New("the wsl module only works inside WSL, and this isn't a WSL distro")
	}
	name, err := currentUser()
	if err != nil {
		return nil, err
	}
	distro := rt.Facts.WSLDistro
	if distro == "" {
		distro = "<distro>"
	}
	rs := []resource.Resource{&restartNote{
		File:   &file.File{Path: ConfFile, Content: file.Bytes(Conf(c, name)), Mode: 0o644, Root: true},
		distro: distro,
	}}

	var repos []resource.Resource
	var pkgs []string
	if rt.Facts.OS == config.Fedora {
		repos = append(repos, &repo.DnfRepo{Name: "whitewaterfoundry_wslu", Content: wsluRepo})
		pkgs = append(pkgs, "wslu")
	}
	if !c.WSLG {
		pkgs = append(pkgs, "socat")
	}
	rs = append(rs, repos...)
	rs = append(rs, pkg.Set(rt, "wsl", pkgs, repos...)...)

	if c.GitCredentialManager {
		gcm, err := findGCM()
		if err != nil {
			return nil, err
		}
		rs = append(rs, &gitconfig.Setting{Key: "credential.helper", Values: []string{HelperValue(gcm)}})
	}
	if !c.WSLG {
		rs = append(rs, &bridge{Unit: &unit.Unit{Name: "x410.service", User: true, Content: file.Bytes([]byte(x410Unit))}, distro: distro})
	}
	return rs, nil
}

// ShellEnv sets BROWSER to wslview, so links open in Windows' browser, and
// with wslg off, DISPLAY to the X410 bridge's display. It adds nothing
// outside WSL.
func (Module) ShellEnv(cfg config.ModuleConfig, rt *resource.Runtime) (shells.Env, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return shells.Env{}, fmt.Errorf("the wsl module can't use a %T config", cfg)
	}
	if rt.Facts == nil || !rt.Facts.WSL {
		return shells.Env{}, nil
	}
	env := shells.Env{Variables: []shells.Variable{{Name: "BROWSER", Value: "wslview"}}}
	if !c.WSLG {
		env.Variables = append(env.Variables, shells.Variable{Name: "DISPLAY", Value: ":0"})
	}
	return env, nil
}

// Conf returns /etc/wsl.conf for cfg, with user as the distro's default
// user.
func Conf(cfg *Config, user string) []byte {
	return fmt.Appendf(nil, `# Written by maisys, which replaces any changes made here.
[boot]
systemd=%t

[user]
default=%s

[interop]
appendWindowsPath=%t
`, cfg.Systemd, user, cfg.AppendWindowsPath)
}

// findGCM returns the path of Git Credential Manager in Git for Windows,
// as WSL sees it.
func findGCM() (string, error) {
	var tried []string
	for _, dir := range gcmDirs {
		p := dir + "/git-credential-manager.exe"
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err == nil && info.Mode().IsRegular() {
			return p, nil
		}
		tried = append(tried, p)
	}
	return "", fmt.Errorf("can't find the Windows Git Credential Manager that wsl.git_credential_manager uses: neither %s exists. Install Git for Windows, which includes it, or turn git_credential_manager off",
		strings.Join(tried, " nor "))
}

// HelperValue returns git's credential.helper value for the helper at the
// absolute path p. git runs an absolute path through the shell, so the
// characters the shell would treat specially, such as the space in
// "Program Files", are escaped with a backslash.
func HelperValue(p string) string {
	var b strings.Builder
	for _, r := range p {
		if !shellSafe(r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// shellSafe reports whether the shell takes r literally.
func shellSafe(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("/._-+,:=@%", r)
}

// restartNote is a file whose changes take effect once the distro
// restarts, which the plan says.
type restartNote struct {
	*file.File
	distro string
}

// Check checks the file, noting how to restart the distro if it changes.
func (r *restartNote) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	c, err := r.File.Check(ctx, rt)
	if err == nil && c.Action.Applies() {
		c.Notes = append(c.Notes, fmt.Sprintf("%s takes effect once the distro restarts: run `wsl --terminate %s` in Windows", ConfFile, r.distro))
	}
	return c, err
}

// bridge is the X410 bridge's unit, which waits for systemd to run in the
// distro: until then, the plan says how to restart it.
type bridge struct {
	*unit.Unit
	distro string
}

// Check checks the unit, if systemd runs.
func (b *bridge) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	if !systemdRuns() {
		return resource.Change{Notes: []string{fmt.Sprintf("the X410 bridge needs systemd, which isn't running yet: run `wsl --terminate %s` in Windows, then maisys sync again", b.distro)}}, nil
	}
	return b.Unit.Check(ctx, rt)
}

// systemdRuns reports whether systemd runs the system, as sd_booted(3)
// tells.
func systemdRuns() bool {
	info, err := os.Stat(filepath.Join(root, "run", "systemd", "system"))
	return err == nil && info.IsDir()
}
