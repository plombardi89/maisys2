package podman_test

import (
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/podman"
	"github.com/plombardi89/maisys2/internal/resource"
)

func TestResources(t *testing.T) {
	tests := []struct {
		os   config.OS
		want []string
	}{
		{os: config.Fedora, want: []string{"package:podman/podman", "package:podman/iptables-nft"}},
		{os: config.Ubuntu, want: []string{"package:podman/podman"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.os), func(t *testing.T) {
			rt := &resource.Runtime{Facts: &facts.System{OS: tt.os}}
			rs, err := podman.Module{}.Resources(&podman.Config{Enabled: true}, rt)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range rs {
				ids = append(ids, r.ID())
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("resources = %q, want %q", ids, tt.want)
			}
		})
	}
}
