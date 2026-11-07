package hashicorp

import (
	"errors"
	"fmt"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/engine"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/pkg"
	"github.com/plombardi89/maisys2/internal/resource/repo"
)

var _ engine.Module = Module{}

// The key HashiCorp signs its apt repo with, and its sha256, as
// https://apt.releases.hashicorp.com/gpg served it on 2026-11-06. If
// HashiCorp changes the key, adding the repo fails until this is updated.
const (
	aptKeyURL    = "https://apt.releases.hashicorp.com/gpg"
	aptKeySHA256 = "1df7d66b79352bab45388511079f74987568672a8ed6395053a050b61f6c5430"
)

// dnfRepo is HashiCorp's dnf repo file, as
// https://rpm.releases.hashicorp.com/fedora/hashicorp.repo served it on
// 2026-11-06. dnf imports the key gpgkey names the first time it installs
// from the repo.
const dnfRepo = `[hashicorp]
name=Hashicorp Stable - $basearch
baseurl=https://rpm.releases.hashicorp.com/fedora/$releasever/$basearch/stable
enabled=1
gpgcheck=1
gpgkey=https://rpm.releases.hashicorp.com/gpg

[hashicorp-test]
name=Hashicorp Test - $basearch
baseurl=https://rpm.releases.hashicorp.com/fedora/$releasever/$basearch/test
enabled=0
gpgcheck=1
gpgkey=https://rpm.releases.hashicorp.com/gpg
`

// DependsOn returns nothing: the module adds HashiCorp's repo itself,
// before its packages.
func (Module) DependsOn() []string { return nil }

// Resources returns HashiCorp's repo, then a package for each of tools,
// which come from it. The repo goes in /etc/apt/sources.list.d/hashicorp.sources
// on Ubuntu, for the release's codename, and /etc/yum.repos.d/hashicorp.repo
// on Fedora: the files the Ansible setup wrote, so they're taken over.
func (Module) Resources(cfg config.ModuleConfig, rt *resource.Runtime) ([]resource.Resource, error) {
	c, ok := cfg.(*Config)
	if !ok {
		return nil, fmt.Errorf("the hashicorp module can't use a %T config", cfg)
	}
	if rt.Facts == nil {
		return nil, errors.New("the hashicorp module needs to know the system")
	}
	var src resource.Resource
	switch rt.Facts.OS {
	case config.Ubuntu:
		if rt.Facts.OSCodename == "" {
			return nil, errors.New("can't add HashiCorp's apt repo: /etc/os-release doesn't give this Ubuntu release's codename (VERSION_CODENAME), which the repo is organised by")
		}
		s := &repo.AptSource{
			Name:       "hashicorp",
			URIs:       []string{"https://apt.releases.hashicorp.com"},
			Suites:     []string{rt.Facts.OSCodename},
			Components: []string{"main"},
			Key:        repo.Key{URL: aptKeyURL, SHA256: aptKeySHA256},
		}
		// HashiCorp only builds for the machine's own architecture, not
		// for extra ones such as Steam's i386.
		if rt.Facts.Arch != "" {
			s.Architectures = []string{string(rt.Facts.Arch)}
		}
		src = s
	case config.Fedora:
		src = &repo.DnfRepo{Name: "hashicorp", Content: dnfRepo}
	default:
		return nil, errors.New("the hashicorp module only works on Fedora and Ubuntu")
	}
	return append([]resource.Resource{src}, pkg.Set(rt, "hashicorp", c.Tools, src)...), nil
}
