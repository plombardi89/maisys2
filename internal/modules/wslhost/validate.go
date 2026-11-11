package wslhost

import (
	"io/fs"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/gitrepo"
)

var (
	_ config.Checker  = (*Config)(nil)
	_ config.Targeter = (*Config)(nil)
)

var (
	// imagePattern matches the images maisys can set up, capturing the
	// distro and its version.
	imagePattern = regexp.MustCompile(`^(FedoraLinux|Ubuntu)-([0-9][0-9.]*)$`)
	// userPattern matches valid Linux user names.
	userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

// imageOS maps the distro part of an image name to the os it installs.
var imageOS = map[string]config.OS{"FedoraLinux": config.Fedora, "Ubuntu": config.Ubuntu}

// UserRule describes the user names a distro's user can have.
const UserRule = "with lower-case letters, digits, '_' and '-', starting with a letter or '_', and up to 32 characters"

// ValidUser reports whether name can be a distro's user: a valid Linux user
// name other than root.
func ValidUser(name string) bool {
	return name != "root" && userPattern.MatchString(name)
}

// HTTPSURL returns the HTTPS URL of the repo at sshURL, a git SSH URL (see
// gitrepo.IsSSH), on hosts that serve repos at the same path over both, as
// GitHub, GitLab and Bitbucket do: for example
// https://github.com/owner/repo.git for git@github.com:owner/repo.git. It
// drops the user and the port. It returns "" if sshURL isn't an SSH URL
// with a host and a path.
func HTTPSURL(sshURL string) string {
	if !gitrepo.IsSSH(sshURL) {
		return ""
	}
	var host, repoPath string
	if strings.Contains(sshURL, "://") {
		u, err := url.Parse(sshURL)
		if err != nil {
			return ""
		}
		host, repoPath = u.Hostname(), u.Path
	} else {
		// [user@]host:path
		host, repoPath, _ = strings.Cut(sshURL, ":")
		host = host[strings.LastIndexByte(host, '@')+1:]
	}
	repoPath = strings.TrimPrefix(repoPath, "/")
	if host == "" || repoPath == "" {
		return ""
	}
	return "https://" + host + "/" + repoPath
}

// Check checks default_distro, config_url and every distro against the
// Linux machine it's set up from.
func (c *Config) Check(check *config.Check) {
	if c.DefaultDistro != "" {
		if _, ok := c.Distros[c.DefaultDistro]; !ok {
			check.Errorf("default_distro", "is %q, which isn't one of wsl_host.distros", c.DefaultDistro)
		}
	}
	// New distros clone config_url, or else this machine's config URL, over
	// HTTPS. That URL may not be known: then there's nothing to check.
	switch repoURL := check.Repo.URL; {
	case c.ConfigURL != "" && !gitrepo.IsHTTPS(c.ConfigURL):
		check.Errorf("config_url", "must be an HTTPS URL, not %q", gitrepo.Redact(c.ConfigURL))
	case c.ConfigURL != "" || len(c.Distros) == 0 || repoURL == "" || gitrepo.IsHTTPS(repoURL):
	case gitrepo.IsSSH(repoURL):
		check.Errorf("config_url", "is required, because this machine clones the config repo over SSH (%s), and new distros clone it over HTTPS", gitrepo.Redact(repoURL))
	default:
		check.Errorf("config_url", "is required, because this machine clones the config repo from %s, which isn't an HTTPS URL, and new distros clone it over HTTPS", gitrepo.Redact(repoURL))
	}
	for _, name := range slices.Sorted(maps.Keys(c.Distros)) {
		checkDistro(check, name, c.Distros[name])
	}
}

func checkDistro(check *config.Check, name string, d Distro) {
	key := config.JoinKey("distros", name)

	var family config.OS
	var version string
	switch m := imagePattern.FindStringSubmatch(d.Image); {
	case d.Image == "":
		check.Errorf(key, "has no image: set it to a name from wsl --list --online, such as \"FedoraLinux-44\" or \"Ubuntu-26.04\"")
	case m == nil:
		check.Errorf(key+".image", "must be a Fedora or Ubuntu image from wsl --list --online, such as \"FedoraLinux-44\" or \"Ubuntu-26.04\", not %q", d.Image)
	default:
		family, version = imageOS[m[1]], m[2]
	}

	switch {
	case d.User == "":
		check.Errorf(key, "has no user: set it to the Linux user to create")
	case d.User == "root":
		check.Errorf(key+".user", "can't be root: set it to the Linux user to create")
	case !userPattern.MatchString(d.User):
		check.Errorf(key+".user", "must be a Linux user name, "+UserRule+", not %q", d.User)
	default:
	}

	if d.Machine == "" {
		check.Errorf(key, "has no machine: set it to the Linux machine to set the distro up with")
		return
	}
	m, err := check.Repo.LoadMachine(d.Machine)
	if err != nil {
		file := config.MachineFile(d.Machine)
		if _, statErr := fs.Stat(check.Repo.FS, file); statErr != nil {
			check.Errorf(key+".machine", "is %q, but %s doesn't exist", d.Machine, file)
		} else {
			check.Errorf(key+".machine", "is %q, but %s doesn't load: maisys config validate --all lists its errors", d.Machine, file)
		}
		return
	}
	switch {
	case m.OS == "":
		check.Errorf(key+".machine", "is %q, which doesn't set os: a distro needs a fedora or ubuntu machine", d.Machine)
	case m.OS != config.Fedora && m.OS != config.Ubuntu:
		check.Errorf(key+".machine", "is %q, which is a %s machine: a distro needs a fedora or ubuntu machine", d.Machine, m.OS)
	case family != "" && m.OS != family:
		check.Errorf(key+".image", "is %q, but machine %q is %s: use a %s image", d.Image, d.Machine, m.OS, imageName(m.OS, "*"))
	case family != "" && m.OSVersion != "" && version != m.OSVersion:
		check.Errorf(key+".image", "is %q, but machine %q sets os_version = %q: use %s", d.Image, d.Machine, m.OSVersion, imageName(m.OS, m.OSVersion))
	default:
	}
	if m.Arch == config.ARM64 {
		check.Errorf(key+".machine", "is %q, which sets arch = %q, but distros on a Windows host are %s", d.Machine, m.Arch, config.AMD64)
	}
}

// imageName returns the name of the image for osName at version.
func imageName(osName config.OS, version string) string {
	for distro, imgOS := range imageOS {
		if imgOS == osName {
			return distro + "-" + version
		}
	}
	return ""
}

// Targets returns .wslconfig, which wsl_host writes.
func (c *Config) Targets(*config.Machine) []config.Target {
	return []config.Target{{Path: "~/.wslconfig", Key: "wsl_host.wslconfig"}}
}
