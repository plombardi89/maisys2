package winget_test

import (
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/config"
	"github.com/plombardi89/maisys2/internal/facts"
	"github.com/plombardi89/maisys2/internal/modules/winget"
	"github.com/plombardi89/maisys2/internal/resource"
)

func TestResources(t *testing.T) {
	tests := []struct {
		name    string
		install []string
		want    []string
	}{
		{name: "Git only", want: []string{"winget:winget/Git.Git"}},
		{
			name:    "packages",
			install: []string{"Microsoft.WindowsTerminal", "jqlang.jq", "Microsoft.WindowsTerminal"},
			want:    []string{"winget:winget/Git.Git", "winget:winget/Microsoft.WindowsTerminal", "winget:winget/jqlang.jq"},
		},
		{
			name:    "Git listed too",
			install: []string{"jqlang.jq", "Git.Git"},
			want:    []string{"winget:winget/Git.Git", "winget:winget/jqlang.jq"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &resource.Runtime{Facts: &facts.System{OS: config.Windows}}
			rs, err := winget.Module{}.Resources(&winget.Config{Enabled: true, Install: tt.install}, rt)
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
			// Only Git is installed system-wide, which needs admin rights.
			for i, r := range rs {
				if r.Privileged() != (i == 0) {
					t.Errorf("%s: Privileged = %t", r.ID(), r.Privileged())
				}
			}
		})
	}
}
