package wslhost

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/gitrepo"
	linuxwsl "github.com/plombardi89/maisys2/internal/modules/wsl"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/file"
	"github.com/plombardi89/maisys2/internal/resource/wsl"
)

var _ engine.Module = Module{}

// WSLConfigPath is where .wslconfig goes, and wslConfigName how the plan
// shows it, as Windows writes paths.
const (
	WSLConfigPath = "~/.wslconfig"
	wslConfigName = `~\.wslconfig`
)

// DependsOn returns nothing.
func (Module) DependsOn() []string { return nil }

// Resources returns, in order: WSL itself, installed and kept up to date;
// %USERPROFILE%\.wslconfig, if the config sets any of its keys; each of
// distros, by name; and default_distro as WSL's default distro, if it's
// set.
//
// Each distro is a resource of its own, which is Independent: one that
// can't be set up makes wsl_host fail, but doesn't stop the others.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the wsl_host module can't use a %T config", cfg)
	}
	rs := []resource.Resource{&wsl.Install{}}
	if data := WSLConfigFile(c, rt.Machine); data != nil {
		rs = append(rs, &wslConfig{File: &file.File{Path: WSLConfigPath, Content: file.Bytes(data), CRLF: true}})
	}
	distros, err := distroResources(c, rt)
	if err != nil {
		return nil, err
	}
	rs = append(rs, distros...)
	if c.DefaultDistro != "" {
		rs = append(rs, &wsl.Default{Name: c.DefaultDistro})
	}
	return rs, nil
}

// distroResources returns a resource for each distro, by name.
func distroResources(c *Config, rt *resource.Runtime) ([]resource.Resource, error) {
	if len(c.Distros) == 0 {
		return nil, nil
	}
	url, err := DistroURL(c, rt.RepoURL, rt.RepoDir)
	if err != nil {
		return nil, err
	}
	var binary wsl.BinarySource
	if b, err := wsl.NewLinuxBinary(rt.Paths.CacheDir, os.Getenv); err == nil {
		binary = b
	} else {
		// Only creating a distro needs it, which then fails with this.
		binary = missingBinary{err}
	}
	// The first sync inside a distro updates itself from the same release
	// files, if they're not the default ones.
	releaseURL := os.Getenv(wsl.ReleaseURLEnv)
	if wsl.IsLocal(releaseURL) {
		p, err := wsl.LinuxURL(releaseURL)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", wsl.ReleaseURLEnv, err)
		}
		releaseURL = "file://" + p
	}
	repo := &config.Repo{FS: rt.RepoFS, Modules: rt.Modules, URL: rt.RepoURL}

	var rs []resource.Resource
	for _, name := range slices.Sorted(maps.Keys(c.Distros)) {
		d := c.Distros[name]
		m, err := repo.LoadMachine(d.Machine)
		if err != nil {
			return nil, fmt.Errorf("loading machine %s, which distro %s is set up with: %w", d.Machine, name, err)
		}
		distro := &wsl.Distro{
			Name:        name,
			Image:       d.Image,
			User:        d.User,
			Machine:     d.Machine,
			OS:          m.OS,
			WSLConf:     wslConf(m, d.User),
			ConfigURL:   url,
			ReleaseURL:  releaseURL,
			Binary:      binary,
			HelperValue: linuxwsl.HelperValue,
		}
		rs = append(rs, distro)
	}
	return rs, nil
}

// missingBinary is the Linux maisys of a build that has none.
type missingBinary struct{ err error }

func (m missingBinary) Path(context.Context) (string, string, error) { return "", "", m.err }

// wslConf returns a new distro's /etc/wsl.conf, with user as its default
// user: as the wsl module of its Linux machine m writes it, or with that
// module's defaults if m doesn't turn it on, so the first sync inside the
// distro finds it as it wants it. systemd is on either way.
func wslConf(m *config.Machine, user string) []byte {
	cfg, ok := m.Modules["wsl"].(*linuxwsl.Config)
	if !ok || !cfg.IsEnabled() {
		cfg, _ = linuxwsl.Module{}.NewConfig().(*linuxwsl.Config)
	}
	c := *cfg
	c.Systemd = true
	return linuxwsl.Conf(&c, user)
}

// DistroURL returns the URL new distros clone the config repo from, as
// they see it: config_url if it's set; otherwise this machine's own config
// URL, repoURL, if it's an HTTPS URL; and otherwise, for a config repo
// cloned from a folder, or without a remote, that folder or this machine's
// clone, repoDir, inside WSL. An SSH URL needs config_url (validation
// checks that).
func DistroURL(c *Config, repoURL, repoDir string) (string, error) {
	switch {
	case c.ConfigURL != "":
		return c.ConfigURL, nil
	case repoURL == "":
		if repoDir == "" {
			return "", errors.New("new distros have no config repo to clone: this machine's config repo has no remote and no folder")
		}
		return wsl.LinuxPath(repoDir)
	case gitrepo.IsSSH(repoURL):
		return "", fmt.Errorf("new distros can't clone the config repo over SSH (%s): set wsl_host.config_url to its HTTPS URL", repoURL)
	default:
		return wsl.LinuxURL(repoURL)
	}
}

// WSLConfigFile returns the .wslconfig for c, with only the keys the
// config files of the machine m set, or nil if they set none: WSL's
// defaults apply to the rest.
func WSLConfigFile(c *Config, m *config.Machine) []byte {
	set := func(key string) bool {
		if m == nil {
			return false
		}
		_, ok := m.Origins["wsl_host.wslconfig."+key]
		return ok
	}
	w := c.WSLConfig
	var wsl2, experimental []string
	add := func(lines *[]string, key, name, value string) {
		if set(key) && value != "" {
			*lines = append(*lines, name+"="+value)
		}
	}
	add(&wsl2, "memory", "memory", w.Memory)
	if w.Processors > 0 {
		add(&wsl2, "processors", "processors", strconv.Itoa(w.Processors))
	}
	add(&wsl2, "swap", "swap", w.Swap)
	add(&wsl2, "networking_mode", "networkingMode", w.NetworkingMode)
	add(&wsl2, "dns_tunneling", "dnsTunneling", strconv.FormatBool(w.DNSTunneling))
	add(&experimental, "auto_memory_reclaim", "autoMemoryReclaim", reclaimValues[w.AutoMemoryReclaim])
	if len(wsl2) == 0 && len(experimental) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("# Written by maisys from wsl_host.wslconfig, which replaces any changes made here.\n")
	b.WriteString("# Changes take effect once WSL restarts: run `wsl --shutdown`.\n")
	for _, section := range []struct {
		name  string
		lines []string
	}{{"wsl2", wsl2}, {"experimental", experimental}} {
		if len(section.lines) == 0 {
			continue
		}
		b.WriteString("\n[" + section.name + "]\n")
		for _, line := range section.lines {
			b.WriteString(line + "\n")
		}
	}
	return []byte(b.String())
}

// reclaimValues maps auto_memory_reclaim's values to .wslconfig's.
var reclaimValues = map[string]string{"disabled": "disabled", "gradual": "gradual", "dropcache": "dropCache"}

// wslConfig is .wslconfig, which only takes effect once WSL restarts:
// the plan says to run wsl --shutdown, which maisys leaves to you, so open
// sessions aren't closed.
type wslConfig struct {
	*file.File
}

// Check checks the file, saying how to apply a change.
func (w *wslConfig) Check(ctx context.Context, rt *resource.Runtime) (resource.Change, error) {
	c, err := w.File.Check(ctx, rt)
	if err != nil || !c.Action.Applies() {
		return c, err
	}
	switch c.Action {
	case resource.Create, resource.Update:
		c.Summary = "write " + wslConfigName + " (run `wsl --shutdown` to apply)"
	default:
		c.Notes = append(c.Notes, wslConfigName+" takes effect once WSL restarts: run `wsl --shutdown` to apply it")
	}
	return c, nil
}
