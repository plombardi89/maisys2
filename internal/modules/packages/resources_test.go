package packages_test

import (
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/packages"
	"github.com/plombardi89/maisys2/internal/resource"
)

func TestResources(t *testing.T) {
	tests := []struct {
		name    string
		install []string
		want    []string
	}{
		{name: "packages", install: []string{"jq", "tmux", "jq"}, want: []string{"package:packages/jq", "package:packages/tmux"}},
		{name: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &resource.Runtime{Facts: &facts.System{OS: config.Fedora}}
			rs, err := packages.Module{}.Resources(&packages.Config{Enabled: true, Install: tt.install}, rt)
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
