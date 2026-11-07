package hashicorp_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/hashicorp"
	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/repo"
)

func TestResources(t *testing.T) {
	cfg := &hashicorp.Config{Enabled: true, Tools: []string{"packer", "vagrant"}}
	wantIDs := []string{"repo:hashicorp", "package:hashicorp/packer", "package:hashicorp/vagrant"}
	tests := []struct {
		name    string
		system  facts.System
		check   func(t *testing.T, src resource.Resource)
		wantErr string
	}{
		{
			name:   "ubuntu",
			system: facts.System{OS: config.Ubuntu, OSCodename: "resolute", Arch: config.ARM64},
			check: func(t *testing.T, src resource.Resource) {
				t.Helper()
				s, ok := src.(*repo.AptSource)
				if !ok {
					t.Fatalf("the repo is a %T, want an apt source", src)
				}
				if !slices.Equal(s.URIs, []string{"https://apt.releases.hashicorp.com"}) || !slices.Equal(s.Suites, []string{"resolute"}) ||
					!slices.Equal(s.Components, []string{"main"}) || !slices.Equal(s.Architectures, []string{"arm64"}) {
					t.Errorf("source = %+v, want HashiCorp's for resolute on arm64", s)
				}
				if s.Key.URL != "https://apt.releases.hashicorp.com/gpg" || len(s.Key.SHA256) != 64 {
					t.Errorf("key = %+v, want HashiCorp's, with its sha256", s.Key)
				}
			},
		},
		{
			name:   "fedora",
			system: facts.System{OS: config.Fedora, OSVersion: "44", Arch: config.AMD64},
			check: func(t *testing.T, src resource.Resource) {
				t.Helper()
				r, ok := src.(*repo.DnfRepo)
				if !ok {
					t.Fatalf("the repo is a %T, want a dnf repo", src)
				}
				if r.Name != "hashicorp" || !strings.HasPrefix(r.Content, "[hashicorp]\n") ||
					!strings.Contains(r.Content, "baseurl=https://rpm.releases.hashicorp.com/fedora/$releasever/$basearch/stable\n") ||
					!strings.Contains(r.Content, "gpgkey=https://rpm.releases.hashicorp.com/gpg\n") {
					t.Errorf("repo = %+v, want HashiCorp's .repo file", r)
				}
			},
		},
		{
			name:    "ubuntu without a codename",
			system:  facts.System{OS: config.Ubuntu, OSVersion: "26.04"},
			wantErr: "can't add HashiCorp's apt repo: /etc/os-release doesn't give this Ubuntu release's codename (VERSION_CODENAME), which the repo is organised by",
		},
		{
			name:    "windows",
			system:  facts.System{OS: config.Windows},
			wantErr: "the hashicorp module only works on Fedora and Ubuntu",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &resource.Runtime{Facts: &tt.system}
			rs, err := hashicorp.Module{}.Resources(cfg, rt)
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
			}
			if !slices.Equal(ids, wantIDs) {
				t.Fatalf("resources = %q, want %q", ids, wantIDs)
			}
			tt.check(t, rs[0])
		})
	}
}
