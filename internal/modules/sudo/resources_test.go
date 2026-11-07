package sudo

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/plombardi89/maisys2/internal/resource"
	"github.com/plombardi89/maisys2/internal/resource/sudoers"
)

func TestResources(t *testing.T) {
	currentUser = func() (string, error) { return "phil", nil }
	t.Cleanup(func() { currentUser = defaultCurrentUser })

	tests := []struct {
		name string
		cfg  *Config
		ids  []string
	}{
		{name: "nopasswd", cfg: &Config{NoPasswd: true}, ids: []string{"sudoers:phil"}},
		{name: "off", cfg: &Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, err := Module{}.Resources(tt.cfg, &resource.Runtime{})
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, r := range rs {
				ids = append(ids, r.ID())
			}
			if !slices.Equal(ids, tt.ids) {
				t.Errorf("resources = %q, want %q", ids, tt.ids)
			}
			if len(rs) == 0 {
				return
			}
			if got, want := rs[0].Targets(), []string{filepath.Join("/etc/sudoers.d", "phil")}; !slices.Equal(got, want) {
				t.Errorf("Targets() = %q, want %q", got, want)
			}
			if !rs[0].Privileged() {
				t.Error("the rule isn't written as root")
			}
		})
	}
	if got, want := string(sudoers.Content("phil")), "phil ALL=(ALL) NOPASSWD: ALL\n"; got != want {
		t.Errorf("the rule is %q, want %q", got, want)
	}
	if deps := (Module{}).DependsOn(); len(deps) > 0 {
		t.Errorf("DependsOn() = %q, want nothing", deps)
	}
}
