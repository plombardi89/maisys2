package wsl

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/state"
	"github.com/plombardi89/maisys2/internal/system"
)

// DistroKind is the kind of resource a Distro is.
const DistroKind = "wsl-distro"

// The keys of a distro record's Details: what the distro was created with.
const (
	DetailImage   = "image"
	DetailUser    = "user"
	DetailMachine = "machine"
)

// SetupRule is the sudoers rule that lets a new distro's user run sudo
// without a password during its first sync, and only then.
const SetupRule = "/etc/sudoers.d/maisys-setup"

// Distro is a WSL distro the config lists. If it's registered already, its
// check says so ("= distro fedora-dev exists (synced from inside the
// distro)"), and it isn't applied: maisys never touches an existing
// distro, and the image it was created from never changes. Otherwise its
// check reports "+ create distro fedora-dev (FedoraLinux-44), user phil"
// and "+ set up fedora-dev with Linux machine workbox-camwin-fc44", and
// applying it creates the distro and sets it up:
//
//  1. wsl --install IMAGE --name NAME --no-launch, which skips the image's
//     interactive first-run setup.
//  2. As root: installs sudo if the image doesn't have it; creates User
//     with UID 1000, the image's oobe.defaultUid, so that its first-run
//     setup doesn't create another, in the wheel (Fedora) or sudo (Ubuntu)
//     group, with a random password (given to chpasswd on its input, never
//     on a command line); adds SetupRule, checked with visudo -cf; writes
//     /etc/wsl.conf; and sets credential.helper in the user's
//     ~/.config/git/config to the Windows Git Credential Manager, so the
//     first sync can clone the config repo with Windows' sign-in.
//  3. wsl --terminate NAME, so systemd and the default user take effect.
//  4. Copies the Linux maisys of the same version, checksum-verified, from
//     the Windows cache through /mnt into ~/.local/bin/maisys.
//  5. Runs maisys sync URL --machine MACHINE --yes inside the distro, as
//     the user, showing its output as it runs.
//  6. Deletes SetupRule, whether or not anything failed after it was
//     added.
//  7. Shows the user's password, once, on the terminal (never in the log
//     or the state file), with how to change it and how to reset it.
//
// A Distro is Independent: a distro that can't be set up doesn't stop the
// others. Its record says what it was created with.
type Distro struct {
	// Name is the distro's name.
	Name string
	// Image is what it's created from, by its name in wsl --list
	// --online, such as "FedoraLinux-44".
	Image string
	// User is the Linux user to create.
	User string
	// Machine is the Linux machine file the first sync applies.
	Machine string
	// OS is the Linux machine's os, which says how to install sudo, and
	// which group lets the user use it.
	OS config.OS
	// WSLConf is what /etc/wsl.conf holds: systemd on, and User as the
	// default user.
	WSLConf []byte
	// ConfigURL is the URL the first sync clones the config repo from, as
	// the distro sees it.
	ConfigURL string
	// Binary is the Linux maisys to copy in.
	Binary BinarySource
	// ReleaseURL, if set, is passed on to the first sync as
	// MAISYS_RELEASE_URL, as the distro sees it, so its self-update uses
	// the same release files as this one. It's for tests.
	ReleaseURL string
	// HelperValue returns git's credential.helper for the helper at an
	// absolute path inside the distro, as the wsl module sets it.
	HelperValue func(path string) string
	// Password returns the user's password. Nil means a random one.
	Password func() (string, error)
}

var (
	_ resource.Resource    = (*Distro)(nil)
	_ resource.Independent = (*Distro)(nil)
)

// ID returns "wsl-distro:NAME".
func (d *Distro) ID() string { return DistroKind + ":" + d.Name }

// Kind returns "wsl-distro".
func (d *Distro) Kind() string { return DistroKind }

// Targets returns nothing.
func (d *Distro) Targets() []string { return nil }

// Privileged returns false: distros are registered to your user, so
// creating one doesn't need admin rights.
func (d *Distro) Privileged() bool { return false }

// Independent returns true: one distro that can't be set up doesn't stop
// the others.
func (d *Distro) Independent() bool { return true }

// Check reports whether the distro exists.
func (d *Distro) Check(_ context.Context, rt *resource.Runtime) (resource.Change, error) {
	exists, err := d.exists(rt)
	if err != nil {
		return resource.Change{}, err
	}
	if !exists {
		return resource.Change{
			Action:  resource.Create,
			Summary: fmt.Sprintf("create distro %s (%s), user %s", d.Name, d.Image, d.User),
			More:    []string{fmt.Sprintf("set up %s with Linux machine %s", d.Name, d.Machine)},
		}, nil
	}
	c := resource.Change{Action: resource.Present, Summary: fmt.Sprintf("distro %s exists (synced from inside the distro)", d.Name)}
	if rt.Previous != nil {
		if prev, ok := rt.Previous(d.ID()); ok {
			if image := prev.Details[DetailImage]; image != "" && image != d.Image {
				c.Notes = append(c.Notes, fmt.Sprintf("%s was created from %s, and distros are never recreated, so its image stays %s, not %s", d.Name, image, image, d.Image))
			}
		}
	}
	return c, nil
}

// exists reports whether the distro is registered.
func (d *Distro) exists(rt *resource.Runtime) (bool, error) {
	reg, err := RegistryFor(rt)
	if err != nil {
		return false, err
	}
	distros, err := reg.Distros()
	if err != nil {
		return false, err
	}
	_, ok := Find(distros, d.Name)
	return ok, nil
}

// Apply creates the distro and sets it up.
func (d *Distro) Apply(ctx context.Context, rt *resource.Runtime) (rec state.Record, err error) {
	if exists, err := d.exists(rt); err != nil || exists {
		if err == nil {
			err = fmt.Errorf("distro %s exists already, and maisys never changes a distro it didn't just create", d.Name)
		}
		return state.Record{}, err
	}
	// Everything that can fail before the distro exists is done first.
	s, err := d.prepare(ctx, rt)
	if err != nil {
		return state.Record{}, err
	}

	if _, err := rt.Runner.Run(ctx, command("creating distro "+d.Name, "--install", d.Image, "--name", d.Name, "--no-launch")); err != nil {
		return state.Record{}, err
	}
	created := true
	defer func() {
		if err != nil && created {
			err = fmt.Errorf("%w\n%s was created, but not set up: maisys never deletes a distro, so finish setting it up in it, or delete it with `wsl --unregister %s` and run maisys sync again", err, d.Name, d.Name)
		}
	}()

	var passwordSet bool
	defer func() {
		if passwordSet {
			d.showPassword(rt.Out, s.password)
		}
	}()
	defer func() {
		// The rule goes whatever happened, even if the step in progress
		// was stopped.
		if rmErr := d.removeRule(context.WithoutCancel(ctx), rt); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
	}()

	if _, err := rt.Runner.Run(ctx, d.root("setting up "+d.Name+" as root", setupScript, d.User, d.group(), string(d.OS), string(d.WSLConf), s.gitconfig, SetupRule)); err != nil {
		return state.Record{}, err
	}
	chpasswd := inDistro(d.Name, "root", "setting the password of "+d.User, "chpasswd")
	chpasswd.Stdin = strings.NewReader(d.User + ":" + s.password + "\n")
	if _, err := rt.Runner.Run(ctx, chpasswd); err != nil {
		return state.Record{}, err
	}
	passwordSet = true

	if _, err := rt.Runner.Run(ctx, command("restarting "+d.Name, "--terminate", d.Name)); err != nil {
		return state.Record{}, err
	}

	res, err := rt.Runner.Run(ctx, d.root("copying maisys into "+d.Name, copyScript, d.User, s.binary, s.sum))
	if err != nil {
		return state.Record{}, err
	}
	home := lastLine(res.Stdout)
	if !strings.HasPrefix(home, "/") {
		return state.Record{}, fmt.Errorf("copying maisys into %s: can't tell where %s's home folder is (got %q)", d.Name, d.User, home)
	}

	created = false // From here on, the distro is set up but for its config.
	if err := d.firstSync(ctx, rt, home); err != nil {
		return state.Record{}, err
	}
	return state.Record{Details: map[string]string{DetailImage: d.Image, DetailUser: d.User, DetailMachine: d.Machine}}, nil
}

// setup is what setting a distro up needs, found before it's created.
type setup struct {
	// binary is the Linux maisys inside WSL, and sum its sha256.
	binary, sum string
	// gitconfig is the user's ~/.config/git/config, which sets
	// credential.helper to the Windows Git Credential Manager, or "" to
	// leave it alone.
	gitconfig string
	// password is the user's password.
	password string
}

// prepare finds what setting the distro up needs: the Linux maisys, Git
// Credential Manager, and the user's password.
func (d *Distro) prepare(ctx context.Context, rt *resource.Runtime) (*setup, error) {
	if d.Binary == nil {
		return nil, errors.New("there's no Linux maisys to copy into new distros")
	}
	path, sum, err := d.Binary.Path(ctx)
	if err != nil {
		return nil, err
	}
	s := &setup{sum: sum}
	if s.binary, err = LinuxPath(path); err != nil {
		return nil, err
	}
	gcm, err := d.findGCM(rt)
	if err != nil {
		return nil, err
	}
	if gcm != "" {
		helper := gcm
		if d.HelperValue != nil {
			helper = d.HelperValue(gcm)
		}
		s.gitconfig = GitConfig(helper)
	}
	if d.Password != nil {
		s.password, err = d.Password()
	} else {
		s.password = rand.Text()
	}
	if err != nil {
		return nil, fmt.Errorf("making a password for %s: %w", d.User, err)
	}
	return s, nil
}

// findGCM returns where the Windows Git Credential Manager is inside WSL,
// so git in the distro can clone the config repo with Windows' sign-in. A
// config repo cloned from a folder needs no sign-in, so then it's "" if
// there's no Git Credential Manager.
func (d *Distro) findGCM(rt *resource.Runtime) (string, error) {
	reg, err := RegistryFor(rt)
	if err != nil {
		return "", err
	}
	dir, err := reg.GitDir()
	if err != nil {
		return "", err
	}
	gcm, err := FindGCM(dir)
	if err != nil && strings.HasPrefix(d.ConfigURL, "/") {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("%s can't clone %s with Windows' sign-in: %w", d.Name, d.ConfigURL, err)
	}
	return gcm, nil
}

// gcmDirs are the folders of Git for Windows that may hold Git Credential
// Manager, newest first: Git for Windows 2.56 moved it from mingw64 to
// ucrt64.
var gcmDirs = []string{`ucrt64\bin`, `mingw64\bin`}

// fileExists reports whether there's a file at the Windows path p. Tests
// replace it.
var fileExists = func(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// FindGCM returns where Git Credential Manager is inside WSL, given the
// folder Git for Windows is installed in.
func FindGCM(gitDir string) (string, error) {
	if gitDir == "" {
		return "", errors.New("can't find the Git Credential Manager: Git for Windows, which has it, isn't installed (winget install Git.Git installs it)")
	}
	var tried []string
	for _, dir := range gcmDirs {
		p := strings.TrimRight(gitDir, `\/`) + `\` + dir + `\git-credential-manager.exe`
		if fileExists(p) {
			return LinuxPath(p)
		}
		tried = append(tried, p)
	}
	return "", fmt.Errorf("can't find the Git Credential Manager in Git for Windows: neither %s exists", strings.Join(tried, " nor "))
}

// GitConfig returns a git config file that sets credential.helper to
// helper.
func GitConfig(helper string) string {
	value := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(helper)
	return "[credential]\n\thelper = \"" + value + "\"\n"
}

// lastLine returns the last line of out that isn't blank, trimmed.
func lastLine(out []byte) string {
	var last string
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimSpace(line); line != "" {
			last = line
		}
	}
	return last
}

// group returns the group that lets the user use sudo.
func (d *Distro) group() string {
	if d.OS == config.Fedora {
		return "wheel"
	}
	return "sudo"
}

// root returns a command that runs script with sh as root in the distro,
// with args as its arguments.
func (d *Distro) root(label, script string, args ...string) system.Cmd {
	return inDistro(d.Name, "root", label, append([]string{"sh", "-c", script, "sh"}, args...)...)
}

// firstSync runs the first maisys sync inside the distro, as the user
// whose home folder is home, showing its output as it runs.
func (d *Distro) firstSync(ctx context.Context, rt *resource.Runtime, home string) error {
	argv := []string{home + "/.local/bin/maisys", "sync", d.ConfigURL, "--machine", d.Machine, "--yes"}
	if d.ReleaseURL != "" {
		argv = append([]string{"env", ReleaseURLEnv + "=" + d.ReleaseURL}, argv...)
	}
	c := inDistro(d.Name, d.User, "the first maisys sync in "+d.Name, argv...)
	if rt.Out != nil {
		_, _ = fmt.Fprintf(rt.Out, "\nmaisys sync in %s, with Linux machine %s:\n", d.Name, d.Machine)
		c.Stream = rt.Out
	}
	if _, err := rt.Runner.Run(ctx, c); err != nil {
		return fmt.Errorf("%w\n%s is created, but its first sync failed: finish setting it up by running maisys sync in it", err, d.Name)
	}
	return nil
}

// removeRule deletes SetupRule.
func (d *Distro) removeRule(ctx context.Context, rt *resource.Runtime) error {
	_, err := rt.Runner.Run(ctx, inDistro(d.Name, "root", "deleting "+SetupRule+" in "+d.Name, "rm", "-f", SetupRule))
	if err != nil {
		return fmt.Errorf("%w\n%s still lets %s use sudo without a password: delete it with `wsl -d %s -u root rm %s`", err, SetupRule, d.User, d.Name, SetupRule)
	}
	return nil
}

// showPassword shows the user's password on out, with how to change it and
// how to reset it.
func (d *Distro) showPassword(out io.Writer, password string) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, `
The password of %[2]s in %[1]s is: %[3]s
It isn't shown again or saved anywhere. Change it with: wsl -d %[1]s passwd
If you lose it, reset it with: wsl -d %[1]s -u root passwd %[2]s

`, d.Name, d.User, password)
}

// DistroRemover handles the records of distros taken out of the config.
// Distros are never deleted, so the plan says the distro is no longer used
// ("? distro fedora-dev is no longer used (not removed)"), and its record
// stays. Once the distro is gone, unregistered by hand, the record is
// dropped.
type DistroRemover struct{}

var _ resource.Remover = DistroRemover{}

// Kind returns "wsl-distro".
func (DistroRemover) Kind() string { return DistroKind }

// CheckRemove reports the distro as no longer used, if it still exists.
func (DistroRemover) CheckRemove(_ context.Context, rt *resource.Runtime, rec state.Record) (resource.Change, error) {
	name := strings.TrimPrefix(rec.ID, DistroKind+":")
	d := &Distro{Name: name}
	exists, err := d.exists(rt)
	if err != nil || !exists {
		return resource.Change{}, err
	}
	return resource.Change{Action: resource.Orphan, Summary: "distro " + name}, nil
}

// Remove does nothing: distros are never deleted.
func (DistroRemover) Remove(context.Context, *resource.Runtime, state.Record) error { return nil }

// setupScript sets a new distro up as root. Its arguments are the user,
// the group that may use sudo, the os (fedora or ubuntu), the content of
// /etc/wsl.conf, the content of the user's ~/.config/git/config ("" for
// none), and the path of the temporary sudoers rule.
const setupScript = `set -eu
user=$1 group=$2 os=$3 wslconf=$4 gitconfig=$5 rule=$6

# sudo, and the commands that create the user, if the image doesn't have
# them.
missing=
command -v sudo >/dev/null 2>&1 || missing="$missing sudo"
case $os in
fedora)
	command -v useradd >/dev/null 2>&1 && command -v chpasswd >/dev/null 2>&1 || missing="$missing shadow-utils"
	command -v passwd >/dev/null 2>&1 || missing="$missing passwd"
	if [ -n "$missing" ]; then dnf install -y $missing </dev/null; fi
	;;
ubuntu)
	command -v useradd >/dev/null 2>&1 && command -v chpasswd >/dev/null 2>&1 && command -v passwd >/dev/null 2>&1 || missing="$missing passwd"
	if [ -n "$missing" ]; then
		apt-get update </dev/null
		DEBIAN_FRONTEND=noninteractive apt-get install -y $missing </dev/null
	fi
	;;
esac

# The user, with UID 1000: the image's first-run setup only creates a user
# if there isn't one with that UID.
owner=$(getent passwd 1000 | cut -d: -f1) || true
if [ -n "$owner" ] && [ "$owner" != "$user" ]; then
	echo "UID 1000 belongs to $owner already, so $user can't have it" >&2
	exit 1
fi
if [ -z "$owner" ]; then
	useradd --create-home --uid 1000 --user-group --groups "$group" --shell /bin/bash "$user"
else
	usermod --append --groups "$group" "$user"
fi
home=$(getent passwd "$user" | cut -d: -f6)
primary=$(id -gn "$user")

# sudo without a password, for the first sync only.
tmp=$(mktemp /etc/sudoers.d/.maisys-setup.XXXXXX)
printf '%s ALL=(ALL) NOPASSWD: ALL\n' "$user" >"$tmp"
chmod 0440 "$tmp"
if ! visudo -cf "$tmp" >/dev/null; then
	rm -f "$tmp"
	echo "visudo rejected the rule for $rule" >&2
	exit 1
fi
mv -f "$tmp" "$rule"

printf '%s' "$wslconf" >/etc/wsl.conf
chmod 0644 /etc/wsl.conf

# git signs in with the Windows Git Credential Manager.
if [ -n "$gitconfig" ]; then
	install -d -o "$user" -g "$primary" "$home/.config" "$home/.config/git"
	printf '%s' "$gitconfig" >"$home/.config/git/config"
	chown "$user:$primary" "$home/.config/git/config"
fi
`

// copyScript copies the Linux maisys into the user's ~/.local/bin as root,
// checks its sha256, and prints the user's home folder. Its arguments are
// the user, the binary's path inside WSL, and its sha256.
const copyScript = `set -eu
user=$1 src=$2 sum=$3
home=$(getent passwd "$user" | cut -d: -f6)
primary=$(id -gn "$user")
install -d -o "$user" -g "$primary" "$home/.local" "$home/.local/bin"
install -m 0755 -o "$user" -g "$primary" "$src" "$home/.local/bin/maisys"
echo "$sum  $home/.local/bin/maisys" | sha256sum -c --quiet - >&2
printf '%s\n' "$home"
`
